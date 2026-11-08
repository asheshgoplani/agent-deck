package orders

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
)

// Files next to ORDERS.md in the conductor's directory.
const (
	StateFileName = "orders-state.json"
	LockFileName  = ".orders.lock"
	LogFileName   = "orders.log"
)

// WakePrefix starts every message the tick sends. The hook handler reads it
// to mark the turn as order-started (no new orders, permission prompts
// denied) and the conductor template tells the model what it means.
const WakePrefix = "[ORDER "

// NoReply is the exact reply a conductor gives a wake that needs nothing.
const NoReply = "NO_REPLY"

// Tick outcomes, one per order per tick.
const (
	ResultPaused        = "paused"
	ResultExpired       = "expired"
	ResultOutsideHours  = "outside-active-hours"
	ResultNotDue        = "not-due"
	ResultBaseline      = "baseline"
	ResultSilent        = "silent"
	ResultWoke          = "woke"
	ResultCapped        = "capped"
	ResultSendFailed    = "send-failed"
	ResultNotDelivered  = "not-delivered"
	ResultInvalid       = "invalid"
	maxStoredOutput     = 2000 // runes kept of the last output (state + "Was:")
	maxCapturedOutput   = 64 << 10
	maxLogBytes         = 1 << 20
	checkKillWaitDelay  = 5 * time.Second
	wakeOutputMaxRunes  = 1500
	wakePrevMaxRunes    = 600
	logLineOutputLength = 200
)

// dueGrace lets a tick that starts a little early still count as on time:
// the timer fires every 60 s but each tick reads the clock a few hundred ms
// after it starts, so without it an "every: 1m" order would run every 2 min.
const dueGrace = 15 * time.Second

// maxSendBackoff caps the retry delay after failed sends.
const maxSendBackoff = 30 * time.Minute

// ErrLocked is returned when another tick (or an edit) holds the lock.
var ErrLocked = errors.New("another orders tick holds the lock")

// OrderState is what the tick remembers per order (orders-state.json).
type OrderState struct {
	NextCheck    time.Time `json:"next_check,omitempty"`
	LastCheck    time.Time `json:"last_check,omitempty"`
	Fingerprint  string    `json:"fingerprint,omitempty"`
	LastOutput   string    `json:"last_output,omitempty"`
	SilentChecks int       `json:"silent_checks,omitempty"`
	WakeDay      string    `json:"wake_day,omitempty"`
	WakesToday   int       `json:"wakes_today,omitempty"`
	CapNoticed   bool      `json:"cap_noticed,omitempty"`
	LastWake     time.Time `json:"last_wake,omitempty"`
	SendFailing  bool      `json:"send_failing,omitempty"`
	SendFails    int       `json:"send_fails,omitempty"` // consecutive failed sends (retry backoff)
}

// State is the whole orders-state.json.
type State map[string]*OrderState

// SendResult is what a send reports: Delivered false with a nil error means
// the receiving end accepted but did not deliver (a muted trigger, a skipped
// send); the change is not marked as seen.
type SendResult struct {
	Delivered bool
	Detail    string
}

// Env is everything one tick of one conductor needs. Run and Send are the
// seams tests replace.
type Env struct {
	Dir  string         // conductor directory holding ORDERS.md
	Now  time.Time      // tick time
	Loc  *time.Location // zone the daily wake cap counts days in (nil = Local)
	Run  func(ctx context.Context, command, dir string) string
	Send func(order, text string) (SendResult, error)
}

// Outcome is one order's result for one tick.
type Outcome struct {
	Order  string `json:"order"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// Lock takes the conductor's orders lock. wait=false fails fast with
// ErrLocked; wait=true blocks (edits wait for a running tick).
func Lock(dir string, wait bool) (release func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening orders lock: %w", err)
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("locking orders: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// LoadState reads orders-state.json; a missing or unreadable file is an
// empty state (every order starts with a silent baseline).
func LoadState(dir string) State {
	st := State{}
	data, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	if st == nil {
		st = State{}
	}
	return st
}

// SaveState writes orders-state.json atomically through a unique temp file,
// so two writers can never share a temp name.
func SaveState(dir string, st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding orders state: %w", err)
	}
	return writeAtomic(filepath.Join(dir, StateFileName), data, 0o600)
}

// WriteFile replaces ORDERS.md atomically.
func WriteFile(dir, text string) error {
	return writeAtomic(filepath.Join(dir, FileName), []byte(text), 0o644)
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", filepath.Base(path), err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, perm)
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing %s: %w", filepath.Base(path), werr)
	}
	return nil
}

// Tick runs every due order of one conductor once. It holds the lock for the
// whole tick; a second tick at the same moment returns ErrLocked and does
// nothing. A missing ORDERS.md is not an error: there is nothing to do.
func Tick(env Env) ([]Outcome, error) {
	if _, err := os.Stat(filepath.Join(env.Dir, FileName)); os.IsNotExist(err) {
		return nil, nil
	}
	release, err := Lock(env.Dir, false)
	if err != nil {
		return nil, err
	}
	defer release()
	data, err := os.ReadFile(filepath.Join(env.Dir, FileName))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", FileName, err)
	}

	loc := env.Loc
	if loc == nil {
		loc = time.Local
	}
	now := env.Now
	list, errs := Parse(string(data))
	var out []Outcome
	for _, e := range errs {
		out = append(out, Outcome{Result: ResultInvalid, Detail: e.Error()})
	}
	st := LoadState(env.Dir)
	save := func() error { return SaveState(env.Dir, st) }
	today := now.In(loc).Format("2006-01-02")

	for _, o := range list {
		s := st[o.Name]
		if s == nil {
			s = &OrderState{}
			st[o.Name] = s
		}
		res := Outcome{Order: o.Name}
		switch {
		case o.Status == StatusPaused:
			res.Result = ResultPaused
		case o.Expired(now, loc):
			res.Result = ResultExpired
		case !o.InActiveHours(now):
			res.Result = ResultOutsideHours
		case !s.NextCheck.IsZero() && now.Before(s.NextCheck.Add(-dueGrace)):
			res.Result = ResultNotDue
		}
		if res.Result != "" {
			out = append(out, res)
			continue
		}

		// One run per slot: the next check moves forward BEFORE the check
		// runs, so a crash or a slow check never runs the same slot twice.
		s.NextCheck = now.Add(o.Every)
		if err := save(); err != nil {
			return out, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), CheckTimeout)
		output := env.Run(ctx, o.Check, env.Dir)
		cancel()
		fp := fingerprint(output)
		s.LastCheck = now

		switch {
		case s.Fingerprint == "":
			// First sight records a baseline; it never wakes anyone.
			s.Fingerprint, s.LastOutput = fp, keep(output)
			res.Result = ResultBaseline
		case fp == s.Fingerprint:
			s.SilentChecks++
			res.Result = ResultSilent
		default:
			res = wake(env, o, s, output, fp, today)
		}
		if err := save(); err != nil {
			return out, err
		}
		out = append(out, res)
	}
	// Forget state of orders that are gone from the file.
	names := map[string]bool{}
	for _, o := range list {
		names[o.Name] = true
	}
	changed := false
	for name := range st {
		if !names[name] && !hasInvalid(errs, name) {
			delete(st, name)
			changed = true
		}
	}
	if changed {
		if err := save(); err != nil {
			return out, err
		}
	}
	appendLog(env.Dir, now, out)
	return out, nil
}

func hasInvalid(errs []error, name string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), fmt.Sprintf("%q", name)) {
			return true
		}
	}
	return false
}

// wake handles a changed check: one message to the conductor, or one cap
// note once the day's cap is spent. The fingerprint is saved only after a
// delivered send, so a failed send is retried on the next tick.
func wake(env Env, o Order, s *OrderState, output, fp, today string) Outcome {
	res := Outcome{Order: o.Name}
	if s.WakeDay != today {
		s.WakeDay, s.WakesToday, s.CapNoticed = today, 0, false
	}
	if s.WakesToday >= o.MaxWakesPerDay {
		res.Result = ResultCapped
		res.Detail = fmt.Sprintf("cap %d reached, change held until tomorrow", o.MaxWakesPerDay)
		if !s.CapNoticed {
			r, err := env.Send(o.Name, CapText(o))
			if err == nil && r.Delivered {
				s.CapNoticed = true
				res.Detail += "; cap note sent"
			}
		}
		return res
	}
	r, err := env.Send(o.Name, WakeText(o, s.LastOutput, output))
	switch {
	case err != nil:
		// Retry while the change is still unseen: after 1 minute, then 2, 4,
		// ... up to the order's own interval (at most 30 min), so a stopped
		// conductor does not run the check every minute forever.
		s.SendFails++
		s.NextCheck = env.Now.Add(sendBackoff(s.SendFails, o.Every))
		res.Result = ResultSendFailed
		res.Detail = oneLine(err.Error())
		if s.SendFailing {
			res.Detail = "" // logged once per failing streak
		}
		s.SendFailing = true
	case !r.Delivered:
		res.Result = ResultNotDelivered
		res.Detail = r.Detail
	default:
		s.Fingerprint, s.LastOutput = fp, keep(output)
		s.WakesToday++
		s.LastWake = env.Now
		s.SendFailing, s.SendFails = false, 0
		res.Result = ResultWoke
		res.Detail = fmt.Sprintf("%d/%d today", s.WakesToday, o.MaxWakesPerDay)
	}
	return res
}

func sendBackoff(fails int, every time.Duration) time.Duration {
	d := time.Minute
	for i := 1; i < fails && d < maxSendBackoff; i++ {
		d *= 2
	}
	if d > every {
		d = every
	}
	if d > maxSendBackoff {
		d = maxSendBackoff
	}
	if d < time.Minute {
		d = time.Minute
	}
	return d
}

// WakeText is the message a changed check sends to the conductor.
func WakeText(o Order, was, now string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s] its check changed.\n", WakePrefix, o.Name)
	fmt.Fprintf(&b, "Was: %s\n", runewidth.Truncate(orNone(was), wakePrevMaxRunes, "…"))
	fmt.Fprintf(&b, "Now: %s\n", runewidth.Truncate(orNone(now), wakeOutputMaxRunes, "…"))
	do := o.Do
	if do == "" {
		do = "tell the owner in one line what changed"
	}
	fmt.Fprintf(&b, "Do: %s\n", do)
	if o.AskFirst != "" {
		fmt.Fprintf(&b, "Ask the owner first (NEED: line or agent-deck ask) before: %s.\n", o.AskFirst)
	}
	b.WriteString("Nobody is watching this turn: a permission prompt is denied, so ask with NEED: instead. ")
	fmt.Fprintf(&b, "If this needs nothing, reply exactly %s. Do not add or change orders from this run.", NoReply)
	return b.String()
}

// CapText is the one note sent when an order's daily wake cap is reached.
func CapText(o Order) string {
	return fmt.Sprintf("%s%s] daily cap of %d wakes reached; further changes are held until tomorrow. Reply %s unless the owner should raise max_wakes_per_day.",
		WakePrefix, o.Name, o.MaxWakesPerDay, NoReply)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return s
}

func keep(s string) string { return runewidth.Truncate(s, maxStoredOutput, "…") }

func fingerprint(s string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))[:16]
}

// RunCheck runs a check with bash -lc in its own process group, bounded by
// ctx (CheckTimeout) and by a capped output buffer. The watched value is the
// trimmed stdout plus the exit code when it is not zero; stderr is not
// watched (it often carries timestamps and progress noise).
func RunCheck(ctx context.Context, command, dir string) string {
	// The command is the owner's own ORDERS.md entry, run on the owner's
	// machine like a crontab line.
	cmd := exec.CommandContext(ctx, bashPath(), "-lc", command) //nolint:gosec // G204: owner-authored check, like a crontab entry
	cmd.Dir = dir
	cmd.Env = childenv.ForLaunch("")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = checkKillWaitDelay
	var stdout capped
	cmd.Stdout = &stdout
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // no orphan outlives its slot
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("[check timed out after %s]", CheckTimeout)
	}
	text := strings.TrimSpace(stdout.buf.String())
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		text += fmt.Sprintf("\n[exit %d]", exitErr.ExitCode())
	case err != nil:
		text += "\n[check failed: " + oneLine(err.Error()) + "]"
	}
	return strings.TrimSpace(text)
}

type capped struct{ buf bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxCapturedOutput - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func bashPath() string {
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "/bin/bash"
}

// appendLog writes the outcomes worth a line to orders.log: everything but
// the quiet ones (not due, silent, paused, outside hours).
func appendLog(dir string, now time.Time, out []Outcome) {
	var lines []string
	for _, o := range out {
		switch o.Result {
		case ResultNotDue, ResultSilent, ResultPaused, ResultOutsideHours:
			continue
		case ResultSendFailed:
			if o.Detail == "" {
				continue
			}
		}
		line := fmt.Sprintf("%s %s: %s", now.Format(time.RFC3339), orDash(o.Order), o.Result)
		if o.Detail != "" {
			line += " (" + runewidth.Truncate(oneLine(o.Detail), logLineOutputLength, "…") + ")"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return
	}
	path := filepath.Join(dir, LogFileName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(strings.Join(lines, "\n") + "\n")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
