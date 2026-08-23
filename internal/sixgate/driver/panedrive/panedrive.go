// Package panedrive is SIXGATE's Driver B: it builds the shipped binary, runs
// it in a real terminal, presses the keys the G0 script names, and records what
// the terminal actually painted.
//
// WHY IT EXISTS ALONGSIDE DRIVER A. Driver A boots the real Bubble Tea model in
// process and reads View(), which for a full-screen program is the string the
// terminal paints. It is faster, deterministic, and spawns nothing. What it
// cannot see is stated in its own package comment and is exactly what this
// driver covers: that the shipped binary boots at all, that a real terminal
// wraps and clips these frames the way a real terminal does, that the
// alt-screen is entered and released, that a cold machine with no configuration
// gets past whatever it asks first, and that the path from a row in SQLite to a
// session on the screen exists. Where both drivers run the same script on the
// same journey, a frame that differs is free evidence nobody had to think to
// look for.
//
// WHY IT IS THE MINORITY DRIVER. This host has lost its entire live session
// fleet twice to software that spawned tmux — once to leaked test servers that
// took the pseudo-terminal pool to 507 of 511 so that nothing on the machine
// could attach, and once to a reaper that identified tmux processes by argv. A
// gate framework that leaks a pseudo-terminal is a fleet-killer wearing a QA
// badge. So tmux is touched by as few rows as possible, through exactly one
// chokepoint (tmuxctl.go), and every run has to PROVE it cleaned up: after
// teardown, `ls` on this run's socket must fail, no socket may remain under the
// isolated directory, and the pseudo-terminal census must not have grown. Those
// three facts are written into run.json whether the gate passed or not.
//
// WHAT IS REAL HERE. Everything. The binary is built from the checkout under
// test and executed; the terminal is a real pty; the keys go through tmux
// send-keys; the frames come from capture-pane, both stripped and with escape
// sequences intact. There is no seam.
package panedrive

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/script"
)

// Name identifies this driver in artifacts.
const Name = "panedrive"

// RunSchema is the run.json schema version for this driver.
const RunSchema = 1

// Tunables. Constants rather than flags: a gate whose timing can be argued with
// is a gate that gets argued with.
const (
	// pollInterval is how often the pane is re-captured while waiting.
	pollInterval = 150 * time.Millisecond
	// settleFrames is how many consecutive identical captures count as
	// quiescent. A real binary starts workers, polls status and repaints, so
	// the driver waits for the screen to stop moving rather than guessing.
	settleFrames = 3
	// settleTimeout bounds the wait for quiescence after an action.
	settleTimeout = 8 * time.Second
	// waitForTimeout bounds an explicit wait_for.
	waitForTimeout = 25 * time.Second
	// bootTimeout bounds the first paint. A binary that has not painted
	// anything by then did not boot, which is a finding, not a flake.
	bootTimeout = 30 * time.Second
	// modalRounds caps how many first-run questions will be dismissed before
	// the driver decides the binary is stuck asking.
	modalRounds = 4
)

// paneSession is the tmux session name inside our dedicated socket.
//
// Deliberately NOT of the form agentdeck_*: the binary treats a session with
// that name as one of its own and would consider itself nested. It also keeps
// any human reading `tmux -L ctxgate-… ls` from mistaking this for fleet state.
const paneSession = "gate"

// Options configure one pane run.
type Options struct {
	// Script is the validated G0 journey.
	Script *script.Script
	// World is the sandboxed machine to run on.
	World *World
	// OutDir receives the frames, transcript.md and run.json.
	OutDir string
	// Repo is the checkout to build from and to record.
	Repo string
	// Tool is the sixgate version string.
	Tool string
	// Binary, when set, is used instead of building one. It exists for a
	// matrix that drives many rows against one build; the path is recorded
	// either way so an artifact never hides what it drove.
	Binary string
}

// StepRun is one beat of the journey as it actually happened in the pane.
type StepRun struct {
	ID         string `json:"id"`
	Note       string `json:"note,omitempty"`
	Verb       string `json:"verb"`
	Detail     string `json:"detail,omitempty"`
	Capture    string `json:"capture,omitempty"`
	ScreenFile string `json:"screen_file,omitempty"`
	ANSIFile   string `json:"ansi_file,omitempty"`
	SettleMS   int    `json:"settle_ms"`
	Polls      int    `json:"polls"`
	Settled    bool   `json:"settled"`
	FrameLines int    `json:"frame_lines,omitempty"`
	FrameBytes int    `json:"frame_bytes,omitempty"`
	Error      string `json:"error,omitempty"`
	// Skipped names a step the row declared out of scope, with the reason. A
	// skipped step is never counted as passing: it is recorded, and G2 will
	// mark its frame missing if this drive is ever asserted against the whole
	// journey.
	Skipped string `json:"skipped,omitempty"`
}

// PTYCensus records the host's pseudo-terminal population around the run.
type PTYCensus struct {
	Before int `json:"before"`
	After  int `json:"after"`
	Delta  int `json:"delta"`
}

// Run is run.json: the machine-readable record of one pane drive.
type Run struct {
	Schema int  `json:"schema"`
	Pass   bool `json:"pass"`

	Slug        string            `json:"slug"`
	Fixture     string            `json:"fixture"`
	FixtureNote string            `json:"fixture_note,omitempty"`
	Notes       []string          `json:"fixture_notes,omitempty"`
	FixtureEnv  map[string]string `json:"fixture_env,omitempty"`
	FixtureRoot string            `json:"fixture_root,omitempty"`

	Driver     string `json:"driver"`
	DriverKind string `json:"driver_kind"`
	Artifact   string `json:"artifact"`
	BinaryPath string `json:"binary_path"`
	BinaryInfo string `json:"binary_build_info,omitempty"`
	Tool       string `json:"tool"`
	GoVersion  string `json:"go_version"`
	GitSHA     string `json:"git_sha"`
	GitDirty   bool   `json:"git_dirty"`

	TermWidth  int `json:"term_width"`
	TermHeight int `json:"term_height"`

	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	DurationMS int    `json:"duration_ms"`

	// FirstRunQuestions are the things a never-configured machine asked before
	// the deck appeared: each captured as a frame, then answered with the key
	// the product itself printed as the way to decline.
	FirstRunQuestions []FirstRunQuestion `json:"first_run_questions,omitempty"`
	// StoppedAfter and StopReason scope a row that does not run the whole
	// journey. Both are set or neither is.
	StoppedAfter string `json:"stopped_after,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`

	Steps []StepRun `json:"steps"`

	// Isolation is the refuse-to-start evidence: which guards were checked,
	// and what the socket address resolved to.
	Isolation isolationReport `json:"isolation"`
	// Teardown is the proof of cleanup. Teardown.Verified false fails the gate
	// no matter how well the journey went.
	Teardown teardownReport `json:"teardown"`
	// TmuxServersSpawned is how many sockets this run created, all of them
	// inside its own isolated directory.
	TmuxServersSpawned int       `json:"tmux_servers_spawned"`
	TmuxVersion        string    `json:"tmux_version"`
	PTY                PTYCensus `json:"pty"`

	Failures []string `json:"failures,omitempty"`
}

// Drive builds the binary, runs the journey in a pane and writes G1's artifacts.
//
// It returns the run record even when the drive failed, because a failed gate
// must still leave evidence: the artifact IS the report. Teardown runs on every
// path out of this function, including a panic, and its result is recorded.
func Drive(opts Options) (run *Run, err error) {
	if opts.Script == nil {
		return nil, fmt.Errorf("panedrive: no script")
	}
	if opts.World == nil {
		return nil, fmt.Errorf("panedrive: no world")
	}
	if opts.World.StopAfter != "" && strings.TrimSpace(opts.World.StopReason) == "" {
		return nil, fmt.Errorf("panedrive: world %q stops after %q without a recorded reason; "+
			"a row that runs less of the journey must say why in the artifact", opts.World.Name, opts.World.StopAfter)
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, err
	}

	runID, err := newRunID()
	if err != nil {
		return nil, err
	}
	sha, dirty := gitState(opts.Repo)
	start := time.Now()
	run = &Run{
		Schema:      RunSchema,
		Slug:        opts.Script.Slug,
		Fixture:     opts.World.Name,
		FixtureNote: opts.World.Description,
		Notes:       opts.World.Notes,
		FixtureEnv:  envPairs(opts.World.Env),
		FixtureRoot: opts.World.Root,
		Driver:      Name,
		DriverKind: "real binary in a real tmux pane on a dedicated socket: proves the shipped artifact " +
			"boots, wraps and clips like a terminal, and releases the alt-screen; costs a pty, so it is the minority driver",
		Tool:         opts.Tool,
		GoVersion:    goVersion(),
		GitSHA:       sha,
		GitDirty:     dirty,
		TermWidth:    opts.Script.Term.Width,
		TermHeight:   opts.Script.Term.Height,
		StartedAt:    start.UTC().Format(time.RFC3339Nano),
		StoppedAfter: opts.World.StopAfter,
		StopReason:   opts.World.StopReason,
	}
	run.PTY.Before = ptyCount()

	binary := opts.Binary
	if binary == "" {
		binary, err = buildBinary(opts.Repo)
		if err != nil {
			return finish(opts, run, start, fmt.Sprintf("the shipped binary did not build: %v", err))
		}
	}
	run.BinaryPath = binary
	run.BinaryInfo = binaryBuildInfo(binary)
	run.Artifact = "the shipped binary, built from this checkout: " + binary

	c, isol, err := newCtl(runID)
	run.Isolation = isol
	if err != nil {
		// Refusing to start is a PASS for the safety rule and a FAIL for the
		// gate. Both facts belong in the artifact.
		return finish(opts, run, start, err.Error())
	}
	run.TmuxVersion = isol.TmuxVersion

	// Teardown on every path out, including a panic. The one thing that must
	// never happen is this function returning with a server still alive.
	defer func() {
		run.Teardown = c.teardown()
		if !run.Teardown.Verified {
			run.Failures = append(run.Failures, teardownFailure(run.Teardown))
		}
		run.PTY.After = ptyCount()
		run.PTY.Delta = run.PTY.After - run.PTY.Before
		if run.PTY.Delta > 0 {
			run.Failures = append(run.Failures, fmt.Sprintf(
				"the pseudo-terminal census GREW by %d across this run (%d → %d). This driver opens exactly one pty "+
					"and must return it; growth is the 2026-07-18 leak signature.",
				run.PTY.Delta, run.PTY.Before, run.PTY.After))
		}
		if _, werr := finish(opts, run, start, ""); werr != nil && err == nil {
			err = werr
		}
	}()

	if err := c.newSession(paneSession, opts.Script.Term.Width, opts.Script.Term.Height,
		opts.World.Env, binary); err != nil {
		run.Failures = append(run.Failures, err.Error())
		return run, nil
	}
	run.TmuxServersSpawned = 1

	if !waitForFirstPaint(c, bootTimeout) {
		run.Failures = append(run.Failures, fmt.Sprintf(
			"the binary painted nothing within %s. Whatever else this run proves, it does not prove the shipped artifact boots.", bootTimeout))
		return run, nil
	}

	run.FirstRunQuestions = dismissFirstRunQuestions(c, opts, run)

	for _, st := range opts.Script.Steps {
		if run.StoppedAfter != "" && st.ID > run.StoppedAfter {
			run.Steps = append(run.Steps, StepRun{
				ID: st.ID, Note: st.Note, Capture: st.Capture,
				Skipped: run.StopReason,
			})
			continue
		}
		rec := execStep(c, st, opts.OutDir)
		run.Steps = append(run.Steps, rec)
		if rec.Error != "" {
			run.Failures = append(run.Failures, fmt.Sprintf("step %s: %s", st.ID, rec.Error))
		}
		if st.Capture != "" && rec.FrameBytes == 0 && rec.Error == "" {
			run.Failures = append(run.Failures,
				fmt.Sprintf("step %s captured an empty frame: there is nothing for G2 to assert on", st.ID))
		}
		if !rec.Settled && rec.Error == "" && rec.Skipped == "" {
			run.Failures = append(run.Failures,
				fmt.Sprintf("step %s never stopped repainting within %s; its frame is a moving target", st.ID, settleTimeout))
		}
	}
	return run, nil
}

// finish stamps the run, decides Pass and writes the artifacts.
func finish(opts Options, run *Run, start time.Time, extraFailure string) (*Run, error) {
	if extraFailure != "" {
		run.Failures = append(run.Failures, extraFailure)
	}
	if run.FinishedAt != "" {
		return run, nil
	}
	if run.PTY.After == 0 {
		// An abort before the driver reached its teardown defer (the binary did
		// not build, or isolation refused to start). The census still belongs in
		// the artifact: "we never got far enough to leak" is a claim, and the
		// number is the check on it.
		run.PTY.After = ptyCount()
		run.PTY.Delta = run.PTY.After - run.PTY.Before
	}
	end := time.Now()
	run.FinishedAt = end.UTC().Format(time.RFC3339Nano)
	run.DurationMS = int(end.Sub(start) / time.Millisecond)
	run.Pass = len(run.Failures) == 0
	if err := writeRun(opts.OutDir, run); err != nil {
		return run, err
	}
	if err := writeTranscript(opts, run); err != nil {
		return run, err
	}
	return run, nil
}

// teardownFailure explains, in the artifact, exactly which half of the
// postcondition did not hold.
func teardownFailure(t teardownReport) string {
	var parts []string
	if !t.PostconditionFailed {
		parts = append(parts, fmt.Sprintf(
			"the postcondition `%s` SUCCEEDED, which means a tmux server is still alive on this run's socket. "+
				"A leaked server keeps a pty per pane and cannot be reaped once its socket is gone — this is how the "+
				"host reached 507 of 511 ptys on 2026-07-18 and nothing could attach", t.PostconditionCmd))
	}
	if len(t.LiveSockets) > 0 {
		parts = append(parts, "a tmux server is STILL ANSWERING on "+strings.Join(t.LiveSockets, ", ")+
			" after the sweep; every one of its panes is holding a pty this run will never get back")
	}
	if t.KillError != "" && t.ServerWasSpawned && !t.PostconditionFailed {
		parts = append(parts, "terminating the server on our socket reported: "+t.KillError)
	}
	if len(parts) == 0 {
		return "teardown could not be verified"
	}
	return strings.Join(parts, "; ")
}

// waitForFirstPaint blocks until the pane holds anything at all.
func waitForFirstPaint(c *ctl, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frame, err := c.capture(paneSession, false)
		if err == nil && strings.TrimSpace(frame) != "" {
			return true
		}
		time.Sleep(pollInterval)
	}
	return false
}

// firstRunMarkers are the questions a never-configured agent-deck asks before
// it shows the deck.
//
// TWO RULES, BOTH LEARNED FROM THE FIRST RECORDED FRAME. This table originally
// held one entry — the Claude Code hooks modal, dismissed with "n" — because
// that is what the repository's own capability-verification notes say a first
// run shows. The first pane drive recorded something else entirely: a genuinely
// cold machine opens a SETUP WIZARD saying "Welcome to Agent Deck!" and offering
// "Press Enter to continue or Esc to use defaults". No amount of reading source
// or prior notes produced that correction; one capture did. Hence:
//
//  1. A question is recognised by text on the SCREEN, never by internal state.
//     A driver that knows which modal the product will show is a driver that
//     stops noticing when the product shows a different one.
//  2. The dismissal key is not chosen by the driver. Each entry names the hint
//     the product itself prints, and the key is only sent when that hint is on
//     screen. If the product asks something and does not say how to decline it,
//     the run FAILS with the frame attached rather than the driver guessing —
//     because a harness that presses keys the product never offered writes a
//     confident transcript of a journey no user could take.
var firstRunMarkers = []struct {
	label  string
	needle string
	// hint must appear on the frame: it is the product's own statement that
	// the key below is offered.
	hint string
	// key is a G0 script key name, translated by the same table the journey
	// uses.
	key string
}{
	{
		label:  "setup-wizard",
		needle: "Welcome to Agent Deck!",
		hint:   "Esc to use defaults",
		key:    "esc",
	},
	{
		label:  "claude-code-hooks",
		needle: "Claude Code Hooks",
		hint:   "n skip",
		key:    "n",
	},
	{
		label:  "update-prompt",
		needle: "Update now?",
		hint:   "[Y/n]",
		key:    "n",
	},
}

// FirstRunQuestion records one thing a cold machine asked, and how it was
// answered, so a reader can check the driver did not invent the answer.
type FirstRunQuestion struct {
	Label string `json:"label"`
	Frame string `json:"frame"`
	// Hint is the sentence the product printed offering the key below.
	Hint string `json:"hint"`
	Key  string `json:"key"`
}

// dismissFirstRunQuestions captures whatever a cold machine asks first, answers
// it with the key the product offered, and returns what it saw.
//
// The capture is the point. A first-run modal is the literal first thing a new
// user sees, so a harness that dismisses it silently deletes the most important
// frame in the run. These frames are written into the drive directory and
// reproduced in the transcript; G2 ignores them, because the G0 journey does not
// name them, and G3 — which owns the cold row — does not.
func dismissFirstRunQuestions(c *ctl, opts Options, run *Run) []FirstRunQuestion {
	var seen []FirstRunQuestion
	for round := 0; round < modalRounds; round++ {
		settle(c, settleTimeout)
		frame, err := c.capture(paneSession, false)
		if err != nil {
			return seen
		}
		idx := -1
		for i, m := range firstRunMarkers {
			if strings.Contains(frame, m.needle) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return seen
		}
		m := firstRunMarkers[idx]
		name := fmt.Sprintf("00%c-first-run-%s", 'a'+rune(round), m.label)
		writeFrame(c, opts.OutDir, name)
		seen = append(seen, FirstRunQuestion{Label: m.label, Frame: name + ".screen.txt", Hint: m.hint, Key: m.key})

		if !strings.Contains(frame, m.hint) {
			run.Failures = append(run.Failures, fmt.Sprintf(
				"the %s question is on screen but does not offer %q, so this driver will not press %q. "+
					"Either the product stopped telling users how to decline, or this row's expectation is stale — "+
					"see %s.screen.txt", m.label, m.hint, m.key, name))
			return seen
		}
		// Declining is the choice a gate must make: accepting would write into
		// the config directory and make the next run a different experiment.
		if err := sendKey(c, m.key); err != nil {
			run.Failures = append(run.Failures, "answering the first-run question: "+err.Error())
			return seen
		}
	}
	run.Failures = append(run.Failures, fmt.Sprintf(
		"the binary was still asking a first-run question after %d answers; a user who cannot get to the deck has a broken product", modalRounds))
	return seen
}

// writeFrame captures and persists one frame pair outside the script's steps.
func writeFrame(c *ctl, dir, name string) {
	plain, err := c.capture(paneSession, false)
	if err != nil {
		return
	}
	raw, err := c.capture(paneSession, true)
	if err != nil {
		raw = plain
	}
	_ = os.WriteFile(filepath.Join(dir, name+".screen.txt"), []byte(normalizeFrame(ansi.Strip(plain))), 0o644) //nolint:gosec // committed artifact
	_ = os.WriteFile(filepath.Join(dir, name+".screen.ansi"), []byte(raw), 0o644)                              //nolint:gosec // committed artifact
}

// execStep performs one script step against the pane and captures its frame.
func execStep(c *ctl, st script.Step, outDir string) StepRun {
	rec := StepRun{ID: st.ID, Note: st.Note, Capture: st.Capture}
	kinds := st.Do.Kinds()
	if len(kinds) != 1 {
		rec.Error = "step does not carry exactly one verb; the script should not have validated"
		return rec
	}
	rec.Verb = string(kinds[0])

	begin := time.Now()
	switch kinds[0] {
	case script.ActKey:
		rec.Detail = *st.Do.Key
		if err := sendKey(c, *st.Do.Key); err != nil {
			rec.Error = err.Error()
			return rec
		}
	case script.ActKeys:
		rec.Detail = strings.Join(st.Do.Keys, " ")
		for _, k := range st.Do.Keys {
			if err := sendKey(c, k); err != nil {
				rec.Error = err.Error()
				return rec
			}
			settle(c, settleTimeout)
		}
	case script.ActType:
		rec.Detail = *st.Do.Type
		if err := c.sendLiteral(paneSession, *st.Do.Type); err != nil {
			rec.Error = err.Error()
			return rec
		}
	case script.ActWait:
		rec.Detail = *st.Do.Wait
		d, err := time.ParseDuration(*st.Do.Wait)
		if err != nil {
			rec.Error = err.Error()
			return rec
		}
		time.Sleep(d)
	case script.ActWaitFor:
		rec.Detail = *st.Do.WaitFor
		if !waitForText(c, *st.Do.WaitFor, waitForTimeout) {
			rec.Error = fmt.Sprintf("waited %s for %q to appear on the pane and it never did", waitForTimeout, *st.Do.WaitFor)
			// Fall through and capture anyway: a gate that discards its
			// evidence when it fails is a gate nobody can debug.
		}
	case script.ActRun:
		rec.Detail = *st.Do.Run
		rec.Error = "the run verb executes a shell command; this driver owns a TUI pane and will not open a second one"
		return rec
	}

	settled, polls := settle(c, settleTimeout)
	rec.Settled = settled
	rec.Polls = polls
	rec.SettleMS = int(time.Since(begin) / time.Millisecond)

	if st.Capture == "" {
		return rec
	}

	plain, err := c.capture(paneSession, false)
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	raw, err := c.capture(paneSession, true)
	if err != nil {
		raw = plain
	}
	stripped := ansi.Strip(plain)
	rec.ScreenFile = st.ID + ".screen.txt"
	rec.ANSIFile = st.ID + ".screen.ansi"
	rec.FrameBytes = len(strings.TrimSpace(stripped))
	rec.FrameLines = len(strings.Split(strings.TrimRight(stripped, "\n"), "\n"))

	if err := os.WriteFile(filepath.Join(outDir, rec.ScreenFile), []byte(normalizeFrame(stripped)), 0o644); err != nil { //nolint:gosec // committed artifact
		rec.Error = err.Error()
		return rec
	}
	if err := os.WriteFile(filepath.Join(outDir, rec.ANSIFile), []byte(raw), 0o644); err != nil { //nolint:gosec // committed artifact
		rec.Error = err.Error()
	}
	return rec
}

// tmuxKeyNames maps the script's driver-agnostic key names onto tmux's.
//
// The table is the same journey vocabulary Driver A translates into Bubble Tea
// key types, which is what lets one script drive both. Anything not named here
// and longer than one character is an error rather than a best-effort literal:
// silently typing the letters of a misspelt key name into a search box is how a
// harness produces a confident transcript of something that never happened.
var tmuxKeyNames = map[string]string{
	"enter":     "Enter",
	"esc":       "Escape",
	"escape":    "Escape",
	"tab":       "Tab",
	"shift+tab": "BTab",
	"space":     "Space",
	"backspace": "BSpace",
	"delete":    "DC",
	"up":        "Up",
	"down":      "Down",
	"left":      "Left",
	"right":     "Right",
	"home":      "Home",
	"end":       "End",
	"pgup":      "PageUp",
	"pgdown":    "PageDown",
}

// sendKey translates one script key name into a tmux keystroke.
func sendKey(c *ctl, name string) error {
	key := strings.TrimSpace(name)
	if key == "" {
		return fmt.Errorf("empty key")
	}
	if tk, ok := tmuxKeyNames[strings.ToLower(key)]; ok {
		return c.sendNamed(paneSession, tk)
	}
	if strings.HasPrefix(strings.ToLower(key), "ctrl+") {
		return fmt.Errorf("key %q is not in this driver's key table; add it rather than approximating it", key)
	}
	if len([]rune(key)) != 1 {
		return fmt.Errorf("key %q is neither a named key nor a single character", key)
	}
	// Literal, always: without -l tmux resolves the argument as a key NAME
	// first, so a script asking for a letter is one lookup away from sending
	// something else entirely.
	return c.sendLiteral(paneSession, key)
}

// settle polls the pane until it stops changing, and reports whether it did.
func settle(c *ctl, timeout time.Duration) (settled bool, polls int) {
	deadline := time.Now().Add(timeout)
	last := ""
	stable := 0
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		polls++
		cur, err := c.capture(paneSession, false)
		if err != nil {
			// The pane is gone. That is quiescent in the only sense that
			// matters, and the caller's capture will report the truth.
			return true, polls
		}
		if cur == last {
			stable++
			if stable >= settleFrames {
				return true, polls
			}
			continue
		}
		last = cur
		stable = 0
	}
	return false, polls
}

// waitForText polls until the pane's stripped content contains substr.
func waitForText(c *ctl, substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		frame, err := c.capture(paneSession, false)
		if err == nil && strings.Contains(ansi.Strip(frame), substr) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// normalizeFrame trims trailing whitespace per line and guarantees a final
// newline, so a committed frame diffs cleanly without changing what it says.
func normalizeFrame(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// BuildForRows compiles the shipped command once for a caller driving several
// rows against one build. Each run.json records the binary path regardless, so
// sharing a build costs no honesty.
func BuildForRows(repo string) (string, error) { return buildBinary(repo) }

// buildBinary compiles the shipped command from the checkout under test.
func buildBinary(repo string) (string, error) {
	dir, err := os.MkdirTemp("", "sixgate-bin-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "agent-deck")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/agent-deck")
	cmd.Dir = repo
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(combined)))
	}
	return out, nil
}

// binaryBuildInfo records what was actually built, so an artifact cannot be
// mistaken for one produced by a different toolchain or a clean tree.
func binaryBuildInfo(binary string) string {
	out, err := exec.Command("go", "version", "-m", binary).Output()
	if err != nil {
		return ""
	}
	var keep []string
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "build\tvcs") || strings.HasPrefix(t, "path") || strings.Contains(t, ": go1.") {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, "; ")
}

// ptyCount counts the pseudo-terminal device nodes present on the host.
//
// It is a read-only directory listing. It identifies nothing by process name
// and terminates nothing: on macOS a process keeps its original argv, so argv
// is not identity, and a reaper that believed otherwise destroyed this
// machine's whole session fleet on 2026-07-26. The census is evidence, never an
// actuator.
//
// Only GROWTH is this driver's fault. A shared host releases pseudo-terminals
// while a run is in progress, so a negative delta is ordinary and failing on it
// would make the one safety signal that matters flaky — and a flaky safety gate
// is a gate that gets switched off, which is how this machine lost its fleet.
// The precise, non-flaky leak check is the teardown postcondition: `ls` on this
// run's own socket must fail and no socket may remain. The census corroborates.
func ptyCount() int {
	entries, err := os.ReadDir("/dev")
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "ttys") || strings.HasPrefix(name, "pts") {
			n++
		}
	}
	return n
}

func newRunID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func goVersion() string {
	out, err := exec.Command("go", "version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func gitState(repo string) (sha string, dirty bool) {
	sha = "unknown"
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output(); err == nil {
		sha = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output(); err == nil {
		dirty = strings.TrimSpace(string(out)) != ""
	}
	return sha, dirty
}

func writeRun(dir string, run *Run) error {
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, "run.json"), raw, 0o644) //nolint:gosec // committed artifact
}
