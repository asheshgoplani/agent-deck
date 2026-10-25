// Wait timer (ASK-119): a session that has waited on a human longer than its
// wait timeout takes the recommended option of the question on screen.
//
// Design:
//
//   - Opt-in. [sessions] wait_timeout sets the default (empty or "0" is off,
//     the default); `session set <id> wait-timeout <dur|off>` overrides it per
//     session. One watcher per agent-deck process ticks every session, like
//     the idle-timeout watcher; the TUI runs it only while it is the primary
//     instance, so two processes never answer the same prompt.
//   - Only a numbered Claude question picker whose one "(Recommended)" row
//     carries the cursor has a safe default. The pane must first pass the
//     hands-free switch guard (ClassifySettlePane, switch_settle.go) as an
//     unknown menu: any permission, approval, trust, bypass, login or
//     first-start screen is never one. On top of that the picker must be the
//     only menu and the last thing on screen, single-select, a single
//     question, and its text must not touch permissions, credentials or a
//     destructive action. Anything else: nothing is pressed.
//   - The wait is measured from the first tick that saw this exact prompt
//     (question, rows and cursor). The session leaving waiting, the prompt
//     changing, a client attaching or a failed capture resets it.
//   - At the timeout the pane is read again and the key goes out only when it
//     shows the same prompt and a dialog still owns the keyboard, so a bare
//     Enter never reaches a composer (#2651). The key is the one Enter, on the
//     row the cursor is already on; no arrows are sent. A prompt is answered
//     at most once.
//   - Every choice is logged (session-lifecycle.jsonl and the debug log):
//     session id, prompt kind, option chosen and how long it waited.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ReasonWaitTimeoutDefaultChosen is the session-lifecycle action recorded when
// the wait timer answers a prompt.
const ReasonWaitTimeoutDefaultChosen = "wait-timeout-default-chosen"

// WaitPromptRecommended is the only prompt kind the wait timer answers: a
// numbered question picker with one row marked "(Recommended)".
const WaitPromptRecommended = "recommended-option"

// MinWaitTimeout is the shortest wait timeout accepted: a shorter one would
// answer for a person who is still reading the question.
const MinWaitTimeout = time.Minute

var waitTimerLog = logging.ForComponent(logging.CompSession)

const (
	waitRecommendedMark = "(Recommended)"
	// waitQuestionLookback bounds how far above the option rows the
	// question is read when no rule line marks the dialog's top.
	waitQuestionLookback = 12
)

var (
	// waitOptionRow is a numbered option row after the frame is stripped:
	// an optional cursor, the number, the label.
	waitOptionRow = regexp.MustCompile(`^(❯\s*)?(\d+)\.\s+(\S.*)$`)
	// waitCheckboxLabel marks a multi-select row, where Enter toggles
	// rather than answers.
	waitCheckboxLabel = regexp.MustCompile(`^(\[.?\]|[☐☑☒◯◉●○])`)
	// waitSensitive are words a question must not carry to be answered
	// unattended: permissions and trust, credentials, and destructive or
	// outward-facing actions. Matched case-insensitively on whole words
	// anywhere in the dialog. Stricter than the switch guard, never instead
	// of it.
	waitSensitive = regexp.MustCompile(`(?i)\b(permissions?|trust\w*|allow\w*|approv\w*|authori[sz]\w*|passwords?|passphrases?|credentials?|secrets?|tokens?|api[ -]?keys?|log ?in|sign ?in|sudo|security|bypass\w*|dangerous\w*|delet\w*|remov\w*|destroy\w*|drop|wipe|overwrit\w*|forc\w*|rm|reset|push\w*|deploy\w*|publish\w*|releas\w*|merg\w*|pay|payments?|purchas\w*|billing|commit\w*|install\w*|uninstall\w*|kill\w*|truncat\w*|migrat\w*|prod|production|revert\w*|rollback\w*|roll ?back|send\w*|e-?mail\w*|upload\w*|shut ?down\w*|disabl\w*|revok\w*|rotat\w*|chmod|chown|terminat\w*|format\w*|wip\w*|erase\w*|purg\w*)\b`)
)

// WaitDefault is the safe default the wait timer found on a pane.
type WaitDefault struct {
	Kind     string // WaitPromptRecommended
	Question string // first question line, for the log
	Option   string // the chosen row as shown: "1. SQLite (Recommended)"
	// Fingerprint identifies the prompt (question, rows and cursor); the
	// timer measures the wait of one fingerprint and re-checks it before the
	// key.
	Fingerprint uint64
}

// ClassifyWaitDefault reports the safe default on a pane capture (ANSI
// allowed), or ok=false and why there is none.
func ClassifyWaitDefault(raw string) (d WaitDefault, ok bool, reason string) {
	// The shared hands-free guard first: only a menu it does not know (no
	// permission, approval, trust, bypass, login or first-start screen) goes
	// on.
	if pane := ClassifySettlePane(raw, nil); pane.State != SettlePaneUnknownMenu {
		return WaitDefault{}, false, "pane is " + string(pane.State)
	}
	content := tmux.StripANSI(raw)
	if !send.DialogOpen(content) {
		return WaitDefault{}, false, "no dialog owns the keyboard"
	}
	lines := liveDialogRegion(strings.Split(strings.TrimRight(content, "\n"), "\n"))
	norm := make([]string, len(lines))
	for i, line := range lines {
		norm[i] = strings.Join(strings.Fields(strings.Trim(line, settleBoxChars)), " ")
	}

	cursor := -1
	for i, line := range lines {
		if m := waitOptionRow.FindStringSubmatch(norm[i]); m != nil && m[1] != "" {
			if cursor >= 0 {
				return WaitDefault{}, false, "more than one cursor row"
			}
			cursor = i
		} else if strings.HasPrefix(norm[i], "❯") && strings.TrimSpace(strings.TrimPrefix(norm[i], "❯")) != "" {
			return WaitDefault{}, false, "cursor on a row that is not numbered: " + strings.TrimSpace(line)
		}
	}
	if cursor < 0 {
		return WaitDefault{}, false, "no numbered cursor row"
	}
	edge := func(i int) bool {
		return norm[i] == "" || settleHintLine(norm[i]) || send.IsComposerBorderLine(lines[i])
	}
	// The option block: the contiguous rows around the cursor.
	start, end := cursor, cursor
	for start-1 >= 0 && !edge(start-1) {
		start--
	}
	for end+1 < len(lines) && !edge(end+1) {
		end++
	}

	var options []waitOption
	question := []string{}
	for i := start; i <= end; i++ {
		m := waitOptionRow.FindStringSubmatch(norm[i])
		if m == nil {
			if len(options) == 0 {
				// The question drawn right above its rows.
				question = append(question, norm[i])
				continue
			}
			// A description under a row: prose, never a row of another menu.
			if settleBodyMenuLike.MatchString(norm[i]) {
				return WaitDefault{}, false, "menu-like line inside the options: " + norm[i]
			}
			continue
		}
		var num int
		_, _ = fmt.Sscanf(m[2], "%d", &num)
		options = append(options, waitOption{num: num, label: m[3], cursor: m[1] != ""})
	}
	for k, opt := range options {
		if opt.num != k+1 {
			return WaitDefault{}, false, "option numbers are not 1..n"
		}
	}

	// After the block: only blank, hint and rule lines, and rows that
	// continue the numbering ("4. Chat about this"). Anything else means the
	// menu is not the live thing at the bottom of the screen.
	var tail []string
	next := len(options) + 1
	for i := end + 1; i < len(lines); i++ {
		if norm[i] == "" || send.IsComposerBorderLine(lines[i]) {
			continue
		}
		tail = append(tail, norm[i])
		if settleHintLine(norm[i]) {
			continue
		}
		if m := waitOptionRow.FindStringSubmatch(norm[i]); m != nil && m[1] == "" && m[2] == fmt.Sprint(next) &&
			!strings.Contains(m[3], waitRecommendedMark) {
			next++
			continue
		}
		return WaitDefault{}, false, "the menu is not the last thing on screen"
	}

	// Above the block, up to the dialog's top rule (or a bounded window):
	// the question and its header, never a second menu.
	top := start - 1
	for ; top >= 0 && start-top <= waitQuestionLookback; top-- {
		if send.IsComposerBorderLine(lines[top]) {
			break
		}
	}
	var header []string
	for i := top + 1; i < start; i++ {
		if i < 0 || norm[i] == "" {
			continue
		}
		if settleBodyMenuLike.MatchString(norm[i]) || waitOptionRow.MatchString(norm[i]) {
			return WaitDefault{}, false, "a second menu above the question: " + norm[i]
		}
		header = append(header, norm[i])
	}
	// A row anywhere else in the live region (history above the dialog
	// included) that carries a cursor or a Recommended mark is another menu.
	for i := 0; i <= top && i < len(lines); i++ {
		if m := waitOptionRow.FindStringSubmatch(norm[i]); m != nil && (m[1] != "" || strings.Contains(m[3], waitRecommendedMark)) {
			return WaitDefault{}, false, "a second menu on screen: " + norm[i]
		}
	}
	// The whole dialog: header, question, rows with their descriptions, and
	// what follows them. It is what the sensitive-word check reads and what
	// identifies the prompt.
	dialog := append(append(append([]string{}, header...), norm[start:end+1]...), tail...)
	for _, line := range dialog {
		// A multi-question form ("☐ Storage ☐ Eviction ✔ Submit"): Enter
		// answers one question and moves to the next.
		if strings.Contains(line, "Submit") {
			return WaitDefault{}, false, "multi-question form"
		}
	}
	if m := waitSensitive.FindString(strings.Join(dialog, "\n")); m != "" {
		return WaitDefault{}, false, fmt.Sprintf("prompt mentions %q", m)
	}

	chosen := -1
	for k, opt := range options {
		if waitCheckboxLabel.MatchString(opt.label) {
			return WaitDefault{}, false, "multi-select menu"
		}
		if strings.HasSuffix(opt.label, waitRecommendedMark) {
			if chosen >= 0 {
				return WaitDefault{}, false, "more than one recommended option"
			}
			chosen = k
		}
	}
	if chosen < 0 {
		return WaitDefault{}, false, "no option is marked recommended"
	}
	if !options[chosen].cursor {
		return WaitDefault{}, false, "the cursor is not on the recommended option"
	}
	q := strings.Join(header, " ")
	if len(question) > 0 {
		q = strings.Join(question, " ")
	} else if len(header) > 0 {
		q = header[len(header)-1]
	}
	return WaitDefault{
		Kind:        WaitPromptRecommended,
		Question:    q,
		Option:      fmt.Sprintf("%d. %s", options[chosen].num, options[chosen].label),
		Fingerprint: hashPaneContent(strings.Join(append(dialog, fmt.Sprintf("cursor=%d", chosen)), "\n")),
	}, true, ""
}

// waitOption is one numbered row of a question picker.
type waitOption struct {
	num    int
	label  string
	cursor bool
}

// ParseWaitTimeout parses a wait timeout: a Go duration of at least
// MinWaitTimeout ("5m", "1h"), or "", "0" or "off" for off (0).
func ParseWaitTimeout(value string) (time.Duration, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "0" || v == "off" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid wait timeout %q: use a Go duration like 5m or 1h, or off", value)
	}
	if d == 0 {
		return 0, nil
	}
	if d < MinWaitTimeout {
		return 0, fmt.Errorf("invalid wait timeout %q: the minimum is %s (use off to disable)", value, MinWaitTimeout)
	}
	return d, nil
}

// normalizeWaitTimeout is the stored form of a per-session value: "" (use
// the config), "off", or the duration as given ("5m").
func normalizeWaitTimeout(value string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return "", nil
	}
	d, err := ParseWaitTimeout(v)
	if err != nil {
		return "", err
	}
	if d == 0 {
		return "off", nil
	}
	return v, nil
}

// GetWaitTimeout returns [sessions] wait_timeout, 0 (off) when unset or
// invalid.
func (c *UserConfig) GetWaitTimeout() time.Duration {
	if c == nil {
		return 0
	}
	d, err := ParseWaitTimeout(c.Sessions.WaitTimeout)
	if err != nil {
		return 0
	}
	return d
}

// EffectiveWaitTimeout is the session's wait timeout: its own value when set
// ("off" disables it), else global. 0 means off.
func (i *Instance) EffectiveWaitTimeout(global time.Duration) time.Duration {
	if strings.TrimSpace(i.WaitTimeout) == "" {
		return global
	}
	d, err := ParseWaitTimeout(i.WaitTimeout)
	if err != nil {
		return 0
	}
	return d
}

const toolDataWaitTimeoutKey = "wait_timeout"

// WriteWaitTimeoutToToolData merges the per-session wait timeout into the
// tool_data blob, removing the key when value is empty. Sibling keys survive.
func WriteWaitTimeoutToToolData(td json.RawMessage, value string) json.RawMessage {
	return writeToolDataString(td, toolDataWaitTimeoutKey, value)
}

// ReadWaitTimeoutFromToolData returns the per-session wait timeout, or ""
// (unset) for missing, malformed or legacy rows.
func ReadWaitTimeoutFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		WaitTimeout string `json:"wait_timeout"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.WaitTimeout
}

// WaitTimerConfig wires the wait timer to its environment. Every field is
// optional and defaults to production behaviour; tests inject a fake clock,
// pane and key sender.
type WaitTimerConfig struct {
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
	// Capture reads the session's agent pane with ANSI intact. Defaults to
	// the primary window's fresh capture.
	Capture func(*Instance) (string, error)
	// Attached reports whether a client is attached to the session: a
	// person looking at the prompt. Defaults to the tmux client check.
	Attached func(*Instance) (bool, error)
	// SendKey sends one tmux key to the agent pane. Defaults to the primary
	// window.
	SendKey func(inst *Instance, key string) error
	// LogEvent records the choice. Defaults to WriteSessionLifecycleEvent.
	LogEvent func(SessionLifecycleEvent) error
	// DefaultTimeout is [sessions] wait_timeout. Defaults to reading the
	// user config.
	DefaultTimeout func() time.Duration
}

// WaitTimer answers prompts that have waited past their session's wait
// timeout with their recommended option. Tick is the unit of work; it is
// safe for concurrent use but meant for one ticking goroutine.
type WaitTimer struct {
	cfg WaitTimerConfig

	mu      sync.Mutex
	waiting map[string]*waitEntry
}

// waitEntry is one prompt being timed.
type waitEntry struct {
	fingerprint uint64
	since       time.Time
	answered    bool
}

// NewWaitTimer builds a wait timer with production defaults for any nil
// callback.
func NewWaitTimer(cfg WaitTimerConfig) *WaitTimer {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Capture == nil {
		cfg.Capture = waitTimerCapture
	}
	if cfg.Attached == nil {
		cfg.Attached = waitTimerAttached
	}
	if cfg.SendKey == nil {
		cfg.SendKey = waitTimerSendKey
	}
	if cfg.LogEvent == nil {
		cfg.LogEvent = WriteSessionLifecycleEvent
	}
	if cfg.DefaultTimeout == nil {
		cfg.DefaultTimeout = func() time.Duration {
			c, err := LoadUserConfig()
			if err != nil {
				return 0 // an unreadable config never turns the timer on
			}
			return c.GetWaitTimeout()
		}
	}
	return &WaitTimer{cfg: cfg, waiting: map[string]*waitEntry{}}
}

// Armed reports whether any of instances has a wait timeout, so a caller can
// skip work (an election, a tick) when none does.
func (w *WaitTimer) Armed(instances []*Instance) bool {
	global := w.cfg.DefaultTimeout()
	for _, inst := range instances {
		if inst != nil && inst.EffectiveWaitTimeout(global) > 0 {
			return true
		}
	}
	return false
}

// Reset forgets every prompt being timed.
func (w *WaitTimer) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	clear(w.waiting)
}

// Tick checks every instance once. A session is timed only while its wait
// timeout is on, it is a Claude session, its status is waiting, no client is
// attached and its pane shows a safe default; anything else drops its timer.
func (w *WaitTimer) Tick(instances []*Instance) {
	w.mu.Lock()
	defer w.mu.Unlock()

	global := w.cfg.DefaultTimeout()
	seen := make(map[string]bool, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		seen[inst.ID] = true
		timeout := inst.EffectiveWaitTimeout(global)
		// The cheap gates first: the pane is read only for a session that
		// opted in and is waiting.
		if timeout <= 0 || !IsClaudeCompatible(inst.Tool) || inst.GetStatusThreadSafe() != StatusWaiting {
			delete(w.waiting, inst.ID)
			continue
		}
		if attached, err := w.cfg.Attached(inst); err != nil || attached {
			// A person is at the prompt (or it is unknown whether one is).
			delete(w.waiting, inst.ID)
			continue
		}
		raw, err := w.cfg.Capture(inst)
		if err != nil {
			delete(w.waiting, inst.ID)
			continue
		}
		offer, ok, _ := ClassifyWaitDefault(raw)
		if !ok {
			delete(w.waiting, inst.ID)
			continue
		}
		entry := w.waiting[inst.ID]
		if entry == nil || entry.fingerprint != offer.Fingerprint {
			w.waiting[inst.ID] = &waitEntry{fingerprint: offer.Fingerprint, since: w.cfg.Now()}
			continue
		}
		if entry.answered {
			continue
		}
		waited := w.cfg.Now().Sub(entry.since)
		if waited < timeout {
			continue
		}
		if err := w.answer(inst, entry, timeout, waited); err != nil {
			waitTimerLog.Warn("wait_timer_not_answered",
				slog.String("instance_id", inst.ID),
				slog.String("reason", err.Error()))
		}
	}
	for id := range w.waiting {
		if !seen[id] {
			delete(w.waiting, id)
		}
	}
}

// errWaitPromptChanged is returned when the prompt changed between the
// timer's check and the key.
var errWaitPromptChanged = errors.New("prompt changed before the key")

// answer re-reads the pane, and only when it still shows the same prompt
// with a dialog owning the keyboard (never a composer: #2651) sends the one
// Enter that takes the recommended row under the cursor.
func (w *WaitTimer) answer(inst *Instance, entry *waitEntry, timeout, waited time.Duration) error {
	raw, err := w.cfg.Capture(inst)
	if err != nil {
		delete(w.waiting, inst.ID)
		return fmt.Errorf("re-read: %w", err)
	}
	offer, ok, reason := ClassifyWaitDefault(raw)
	if !ok || offer.Fingerprint != entry.fingerprint {
		delete(w.waiting, inst.ID)
		if !ok {
			return fmt.Errorf("%w: %s", errWaitPromptChanged, reason)
		}
		return errWaitPromptChanged
	}
	// ClassifyWaitDefault already required an open dialog (send.DialogOpen,
	// the check every automated keystroke into a Claude pane makes); a
	// composer showing a suggestion is refused here as well, belt and
	// braces, since Enter there would submit it (#2651).
	if send.ComposerBodyIsSuggestion(raw) {
		delete(w.waiting, inst.ID)
		return fmt.Errorf("%w: composer shows a suggestion", errWaitPromptChanged)
	}
	// A person may have attached since this tick's check: they win.
	if attached, err := w.cfg.Attached(inst); err != nil || attached {
		delete(w.waiting, inst.ID)
		return errors.New("a client attached before the key")
	}
	// Marked before the key: a failed or half-delivered send is never
	// retried at the same prompt.
	entry.answered = true
	if err := w.cfg.SendKey(inst, "Enter"); err != nil {
		return fmt.Errorf("send Enter: %w", err)
	}
	waitTimerLog.Info("wait_timer_chose_default",
		slog.String("instance_id", inst.ID),
		slog.String("title", inst.Title),
		slog.String("prompt_kind", offer.Kind),
		slog.String("question", offer.Question),
		slog.String("option", offer.Option),
		slog.Duration("waited", waited),
		slog.Duration("timeout", timeout))
	if logErr := w.cfg.LogEvent(SessionLifecycleEvent{
		InstanceID: inst.ID,
		Action:     ReasonWaitTimeoutDefaultChosen,
		Reason: fmt.Sprintf("kind=%s option=%q question=%q waited=%s timeout=%s",
			offer.Kind, offer.Option, offer.Question, waited.Round(time.Second), timeout),
	}); logErr != nil {
		waitTimerLog.Warn("wait_timer_log_failed",
			slog.String("instance_id", inst.ID),
			slog.String("error", logErr.Error()))
	}
	return nil
}

func waitTimerCapture(inst *Instance) (string, error) {
	tm := inst.GetTmuxSession()
	if tm == nil {
		return "", errors.New("no tmux session bound")
	}
	return tm.CapturePrimaryPaneFresh()
}

func waitTimerAttached(inst *Instance) (bool, error) {
	tm := inst.GetTmuxSession()
	if tm == nil {
		return false, errors.New("no tmux session bound")
	}
	// Control-mode clients (agent-deck's own live pipes and key senders)
	// are not a person; session_attached counts them.
	return tm.HumanClientAttached()
}

func waitTimerSendKey(inst *Instance, key string) error {
	tm := inst.GetTmuxSession()
	if tm == nil {
		return errors.New("no tmux session bound")
	}
	return tm.SendNamedKeyToPrimaryWindow(key)
}
