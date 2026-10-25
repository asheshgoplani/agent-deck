package session

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Wait timer (ASK-119): a session waiting on a human longer than its wait
// timeout takes the recommended option of a numbered menu, and nothing else.

// waitMenuPane draws a Claude question picker: a rule, a header chip, the
// question, the option rows and the key hints, under some conversation.
func waitMenuPane(question string, rows ...string) string {
	lines := []string{
		"⏺ I need one decision before I continue.",
		"",
		"────────────────────────────────────────────────────────────",
		" ☐ Storage",
		"",
		question,
		"",
	}
	lines = append(lines, rows...)
	lines = append(lines, "", "Enter to select · ↑/↓ to navigate · Esc to cancel")
	return strings.Join(lines, "\n")
}

var waitRecommendedRows = []string{
	"❯ 1. SQLite (Recommended)",
	"     Embedded, no extra service; matches the rest of the repo.",
	"  2. Redis",
	"     Faster with many writers, but adds a service to run.",
	"  3. Type something.",
}

func waitRecommendedPane() string {
	return waitMenuPane("Which storage backend should the cache use?", waitRecommendedRows...)
}

// waitFake is the environment of one wait-timer test: a fake clock, a pane
// per session and a record of every key and log row.
type waitFake struct {
	now      time.Time
	panes    map[string]string
	attached map[string]bool
	captures int
	keys     []string
	events   []SessionLifecycleEvent
	global   time.Duration
	// onCapture, when set, runs before each capture returns (to change the
	// pane between the timer's two reads).
	onCapture func(n int)
}

func newWaitFake() *waitFake {
	return &waitFake{
		now:      time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		panes:    map[string]string{},
		attached: map[string]bool{},
	}
}

func (f *waitFake) timer() *WaitTimer {
	return NewWaitTimer(WaitTimerConfig{
		Now: func() time.Time { return f.now },
		Capture: func(inst *Instance) (string, error) {
			f.captures++
			if f.onCapture != nil {
				f.onCapture(f.captures)
			}
			pane, ok := f.panes[inst.ID]
			if !ok {
				return "", errors.New("no pane")
			}
			return pane, nil
		},
		Attached: func(inst *Instance) (bool, error) { return f.attached[inst.ID], nil },
		SendKey: func(inst *Instance, key string) error {
			f.keys = append(f.keys, inst.ID+":"+key)
			return nil
		},
		LogEvent: func(ev SessionLifecycleEvent) error {
			f.events = append(f.events, ev)
			return nil
		},
		DefaultTimeout: func() time.Duration { return f.global },
	})
}

func waitInstance(id, timeout string) *Instance {
	inst := NewInstanceWithTool("wait-"+id, "/tmp/wait-"+id, "claude")
	inst.ID = id
	inst.Status = StatusWaiting
	inst.WaitTimeout = timeout
	return inst
}

// Off by default: no config and no per-session value never reads the pane,
// let alone sends a key, however long the session waits.
func TestWaitTimer_OffByDefault(t *testing.T) {
	f := newWaitFake()
	inst := waitInstance("s1", "")
	f.panes["s1"] = waitRecommendedPane()
	w := f.timer()
	for i := 0; i < 10; i++ {
		w.Tick([]*Instance{inst})
		f.now = f.now.Add(time.Hour)
	}
	if len(f.keys) != 0 || f.captures != 0 || len(f.events) != 0 {
		t.Fatalf("off by default: keys=%v captures=%d events=%v", f.keys, f.captures, f.events)
	}
	var cfg UserConfig
	if got := cfg.GetWaitTimeout(); got != 0 {
		t.Fatalf("an empty [sessions] wait_timeout must be off, got %v", got)
	}
}

// Fires only once the prompt has waited the full timeout, not a tick before,
// and presses Enter exactly once on the recommended row.
func TestWaitTimer_FiresOnlyAfterTimeout(t *testing.T) {
	f := newWaitFake()
	inst := waitInstance("s1", "5m")
	f.panes["s1"] = waitRecommendedPane()
	w := f.timer()

	w.Tick([]*Instance{inst}) // first sighting arms the timer
	f.now = f.now.Add(4*time.Minute + 59*time.Second)
	w.Tick([]*Instance{inst})
	if len(f.keys) != 0 {
		t.Fatalf("fired before the timeout: %v", f.keys)
	}
	f.now = f.now.Add(time.Second)
	w.Tick([]*Instance{inst})
	if strings.Join(f.keys, ",") != "s1:Enter" {
		t.Fatalf("keys at the timeout = %v, want one Enter", f.keys)
	}
	// The same prompt still on screen (a slow repaint) is never answered
	// twice.
	f.now = f.now.Add(10 * time.Minute)
	w.Tick([]*Instance{inst})
	if len(f.keys) != 1 {
		t.Fatalf("answered the same prompt twice: %v", f.keys)
	}
}

// The global [sessions] wait_timeout applies to a session without its own
// value, and a per-session "off" wins over it.
func TestWaitTimer_GlobalDefaultAndSessionOff(t *testing.T) {
	f := newWaitFake()
	f.global = 5 * time.Minute
	inherit := waitInstance("inherit", "")
	off := waitInstance("off", "off")
	f.panes["inherit"] = waitRecommendedPane()
	f.panes["off"] = waitRecommendedPane()
	w := f.timer()
	w.Tick([]*Instance{inherit, off})
	f.now = f.now.Add(5 * time.Minute)
	w.Tick([]*Instance{inherit, off})
	if strings.Join(f.keys, ",") != "inherit:Enter" {
		t.Fatalf("keys = %v, want only the inheriting session answered", f.keys)
	}
}

// Logs what it chose: session id, prompt kind, option and how long it waited.
func TestWaitTimer_LogsChoice(t *testing.T) {
	f := newWaitFake()
	inst := waitInstance("s1", "5m")
	f.panes["s1"] = waitRecommendedPane()
	w := f.timer()
	w.Tick([]*Instance{inst})
	f.now = f.now.Add(6 * time.Minute)
	w.Tick([]*Instance{inst})
	if len(f.events) != 1 {
		t.Fatalf("events = %+v, want one", f.events)
	}
	ev := f.events[0]
	if ev.InstanceID != "s1" || ev.Action != ReasonWaitTimeoutDefaultChosen {
		t.Fatalf("event = %+v", ev)
	}
	for _, want := range []string{"kind=recommended-option", `option="1. SQLite (Recommended)"`, "waited=6m0s", "timeout=5m0s"} {
		if !strings.Contains(ev.Reason, want) {
			t.Errorf("reason %q lacks %q", ev.Reason, want)
		}
	}
}

// Never on a permission, trust, bypass, login or security prompt, even one
// that carries a "(Recommended)" row, and never on a menu whose question is
// about something destructive or sensitive.
func TestWaitTimer_NeverOnPermissionTrustOrSecurityPrompts(t *testing.T) {
	cases := map[string]string{
		"tool permission": strings.Join([]string{
			"╭──────────────────────────────────────────────╮",
			"│ Bash command                                 │",
			"│   rm -rf build                               │",
			"│ Do you want to proceed?                      │",
			"│ ❯ 1. Yes (Recommended)                       │",
			"│   2. Yes, and don't ask again for rm         │",
			"│   3. No, and tell Claude what to do differently │",
			"╰──────────────────────────────────────────────╯",
		}, "\n"),
		"folder trust": strings.Join([]string{
			"Accessing workspace:",
			"",
			"/work/alpha",
			"",
			"Quick safety check: Is this a project you created or one you trust?",
			"",
			"❯ 1. Yes, I trust this folder (Recommended)",
			"  2. No, exit",
			"",
			"Enter to confirm · Esc to cancel",
		}, "\n"),
		"bypass ack": strings.Join([]string{
			"WARNING: Claude Code running in Bypass Permissions mode",
			"",
			"❯ 1. Yes, I accept (Recommended)",
			"  2. No, exit",
			"",
			"Enter to confirm · Esc to cancel",
		}, "\n"),
		// Only the shared switch guard knows this one: no sensitive word.
		"edit approval": strings.Join([]string{
			"╭──────────────────────────────────────────────╮",
			"│ Edit file notes.md                           │",
			"│ Do you want to make this edit to notes.md?   │",
			"│ ❯ 1. Yes (Recommended)                       │",
			"│   2. No                                      │",
			"╰──────────────────────────────────────────────╯",
			"",
			"Enter to confirm · Esc to cancel",
		}, "\n"),
		"mcp approval": waitMenuPane("Allow this MCP server to run?", "❯ 1. Allow once (Recommended)", "  2. Deny"),
		"login":        waitMenuPane("Select login method:", "❯ 1. Claude account (Recommended)", "  2. API key"),
		"destructive":  waitMenuPane("Delete the old migrations before continuing?", "❯ 1. Delete them (Recommended)", "  2. Keep them"),
		"push":         waitMenuPane("Which branch should I push to?", "❯ 1. main (Recommended)", "  2. a new branch"),
		"credentials":  waitMenuPane("Where should the token come from?", "❯ 1. The env file (Recommended)", "  2. Ask me"),
		"permissions":  waitMenuPane("Which permissions mode for the worker?", "❯ 1. acceptEdits (Recommended)", "  2. default"),
		"destructive description": waitMenuPane("Which cleanup approach?",
			"❯ 1. Fast (Recommended)", "     Deletes the build cache first.", "  2. Slow"),
	}
	// Destructive or outward-facing verbs a model-written question can
	// carry, one per family (review follow-up, ASK-119).
	for _, q := range []string{
		"Should I commit the generated files now?",
		"Install the new toolchain globally?",
		"Uninstall the old CLI first?",
		"Kill the stuck worker?",
		"Truncate the events table?",
		"Run the migrations now?",
		"Use the prod database for this check?",
		"Apply this to production?",
		"Revert the last change?",
		"Rollback the release?",
		"Send the summary to the team?",
		"Email the report to the owner?",
		"Upload the bundle to the bucket?",
		"Shutdown the staging host?",
		"Disable the failing check?",
		"Revoke the old key?",
		"Rotate the signing key now?",
		"chmod the scripts to 777?",
		"Terminate the idle instances?",
		"Format the disk before copying?",
	} {
		cases["verb: "+q] = waitMenuPane(q, "❯ 1. Yes (Recommended)", "  2. No")
	}
	for name, pane := range cases {
		t.Run(name, func(t *testing.T) {
			if d, ok, reason := ClassifyWaitDefault(pane); ok {
				t.Fatalf("offered %+v (%s) on a %s prompt", d, reason, name)
			}
			f := newWaitFake()
			inst := waitInstance("s1", "1m")
			f.panes["s1"] = pane
			w := f.timer()
			for i := 0; i < 5; i++ {
				w.Tick([]*Instance{inst})
				f.now = f.now.Add(time.Hour)
			}
			if len(f.keys) != 0 || len(f.events) != 0 {
				t.Fatalf("answered a %s prompt: keys=%v events=%v", name, f.keys, f.events)
			}
		})
	}
}

// No safe default, no key: a menu without a marked recommendation, with two
// of them, with the cursor elsewhere, a multi-select or multi-question form,
// a second menu on screen, or an idle composer.
func TestWaitTimer_NoSafeDefaultDoesNothing(t *testing.T) {
	cases := map[string]string{
		"no recommendation": waitMenuPane("Which storage backend?", "❯ 1. SQLite", "  2. Redis"),
		"two recommendations": waitMenuPane("Which storage backend?",
			"  1. SQLite (Recommended)", "❯ 2. Redis (Recommended)"),
		"cursor not on recommended": waitMenuPane("Which storage backend?",
			"  1. SQLite (Recommended)", "❯ 2. Redis"),
		"multi-select": waitMenuPane("Which caches should I add?",
			"❯ 1. [ ] SQLite (Recommended)", "  2. [ ] Redis"),
		"multi-question form":     strings.Replace(waitRecommendedPane(), " ☐ Storage", "←  ☐ Storage  ☐ Eviction  ✔ Submit  →", 1),
		"menu not last on screen": waitRecommendedPane() + "\n⏺ Still thinking about the rest.",
		"second menu above": strings.Join([]string{
			"❯ 1. Postgres (Recommended)",
			"  2. MySQL",
			"",
		}, "\n") + waitRecommendedPane(),
		// A recommended row in the conversation above the dialog: a copy
		// of a menu, so the screen is not one clear question.
		"recommended row in history": strings.Replace(waitRecommendedPane(),
			"⏺ I need one decision before I continue.", "⏺ Last time you chose:\n  1. Postgres (Recommended)", 1),
		"idle composer": strings.Join([]string{
			"⏺ Done. 1. SQLite (Recommended) is what I used.",
			"",
			"────────────────────────────────────────────────",
			"❯ ",
			"────────────────────────────────────────────────",
			"  ? for shortcuts",
		}, "\n"),
		// The cursor row scrolled far above the bottom with no key hints:
		// nothing shows a dialog owns the keyboard.
		"no dialog at the bottom": strings.Join(append([]string{
			"Which storage backend should the cache use?",
			"",
			"❯ 1. SQLite (Recommended)",
		}, append(strings.Split(strings.Repeat("     Embedded and simple.\n", 11), "\n")[:11], "  2. Redis")...), "\n"),
		"numbering skips": waitMenuPane("Which storage backend?", "❯ 1. SQLite (Recommended)", "  3. Redis"),
	}
	for name, pane := range cases {
		t.Run(name, func(t *testing.T) {
			if d, ok, _ := ClassifyWaitDefault(pane); ok {
				t.Fatalf("offered %+v on %s", d, name)
			}
		})
	}
	if d, ok, reason := ClassifyWaitDefault(waitRecommendedPane()); !ok || d.Option != "1. SQLite (Recommended)" || d.Kind != WaitPromptRecommended {
		t.Fatalf("the plain recommended menu must be offered: %+v ok=%v (%s)", d, ok, reason)
	}
	withChat := waitMenuPane("Which storage backend should the cache use?", append(append([]string{}, waitRecommendedRows...),
		"────────────────────────────────────────────────────────────", "  4. Chat about this")...)
	if _, ok, reason := ClassifyWaitDefault(withChat); !ok {
		t.Fatalf("a trailing numbered row that continues the menu must be allowed: %s", reason)
	}
}

// Cancels when the human answers: the session leaving waiting, or the prompt
// changing (a different question, the cursor moved), resets the timer, and
// nothing is sent for the old prompt.
func TestWaitTimer_CancelsWhenHumanAnswers(t *testing.T) {
	t.Run("status leaves waiting", func(t *testing.T) {
		f := newWaitFake()
		inst := waitInstance("s1", "5m")
		f.panes["s1"] = waitRecommendedPane()
		w := f.timer()
		w.Tick([]*Instance{inst})
		f.now = f.now.Add(3 * time.Minute)
		inst.Status = StatusRunning // the human answered
		w.Tick([]*Instance{inst})
		f.now = f.now.Add(time.Minute)
		inst.Status = StatusWaiting // the same question again, later
		w.Tick([]*Instance{inst})
		f.now = f.now.Add(4 * time.Minute) // 8m after the first sighting
		w.Tick([]*Instance{inst})
		if len(f.keys) != 0 {
			t.Fatalf("timer did not reset when the session left waiting: %v", f.keys)
		}
		f.now = f.now.Add(time.Minute)
		w.Tick([]*Instance{inst})
		if len(f.keys) != 1 {
			t.Fatalf("the new prompt's own timeout should fire: %v", f.keys)
		}
	})
	t.Run("prompt changes", func(t *testing.T) {
		f := newWaitFake()
		inst := waitInstance("s1", "5m")
		f.panes["s1"] = waitRecommendedPane()
		w := f.timer()
		w.Tick([]*Instance{inst})
		f.now = f.now.Add(4 * time.Minute)
		f.panes["s1"] = waitMenuPane("Which eviction policy?", "❯ 1. LRU (Recommended)", "  2. LFU")
		w.Tick([]*Instance{inst})
		f.now = f.now.Add(4 * time.Minute)
		w.Tick([]*Instance{inst})
		if len(f.keys) != 0 {
			t.Fatalf("a new prompt inherited the old one's wait: %v", f.keys)
		}
		// The new prompt's own five minutes (counted from when it appeared).
		f.now = f.now.Add(time.Minute)
		w.Tick([]*Instance{inst})
		if strings.Join(f.keys, ",") != "s1:Enter" {
			t.Fatalf("the new prompt did not fire after its own timeout: %v", f.keys)
		}
		if ev := f.events[0]; !strings.Contains(ev.Reason, `option="1. LRU (Recommended)"`) || !strings.Contains(ev.Reason, "waited=5m0s") {
			t.Fatalf("logged %q, want the new prompt after 5m", ev.Reason)
		}
	})
	t.Run("cursor moved by the human", func(t *testing.T) {
		f := newWaitFake()
		inst := waitInstance("s1", "5m")
		f.panes["s1"] = waitRecommendedPane()
		w := f.timer()
		w.Tick([]*Instance{inst})
		f.now = f.now.Add(4 * time.Minute)
		f.panes["s1"] = waitMenuPane("Which storage backend should the cache use?",
			"  1. SQLite (Recommended)", "❯ 2. Redis", "  3. Type something.")
		w.Tick([]*Instance{inst})
		f.now = f.now.Add(time.Hour)
		w.Tick([]*Instance{inst})
		if len(f.keys) != 0 {
			t.Fatalf("pressed Enter on a row the human chose to look at: %v", f.keys)
		}
	})
	t.Run("human attached", func(t *testing.T) {
		f := newWaitFake()
		inst := waitInstance("s1", "5m")
		f.panes["s1"] = waitRecommendedPane()
		w := f.timer()
		w.Tick([]*Instance{inst})
		f.now = f.now.Add(4 * time.Minute)
		f.attached["s1"] = true
		w.Tick([]*Instance{inst})
		f.attached["s1"] = false
		f.now = f.now.Add(2 * time.Minute)
		w.Tick([]*Instance{inst})
		if len(f.keys) != 0 {
			t.Fatalf("an attached client must reset the timer: %v", f.keys)
		}
	})
}

// The prompt is re-read immediately before the key: a pane that changed
// between the timer's check and the key gets nothing, and a composer
// (where Enter would submit a suggestion, #2651) never gets a bare Enter.
func TestWaitTimer_ReverifiesBeforeSending(t *testing.T) {
	changed := map[string]string{
		// A new question replaced the old one: it has its own wait.
		"different prompt": waitMenuPane("Which eviction policy?", "❯ 1. LRU (Recommended)", "  2. LFU"),
		// The human answered and the composer came back with a suggestion.
		"composer with suggestion": strings.Join([]string{
			"⏺ Picked SQLite.",
			"",
			"────────────────────────────────────────────────",
			"❯ \x1b[2mTry \"add a cache test\"\x1b[0m",
			"────────────────────────────────────────────────",
		}, "\n"),
	}
	for name, pane := range changed {
		t.Run(name, func(t *testing.T) {
			f := newWaitFake()
			inst := waitInstance("s1", "5m")
			f.panes["s1"] = waitRecommendedPane()
			w := f.timer()
			w.Tick([]*Instance{inst})
			f.now = f.now.Add(5 * time.Minute)
			first := f.captures
			f.onCapture = func(n int) {
				if n == first+2 { // the re-read right before the key
					f.panes["s1"] = pane
				}
			}
			w.Tick([]*Instance{inst})
			if f.captures != first+2 {
				t.Fatalf("captures = %d, want the check and a re-read (%d)", f.captures, first+2)
			}
			if len(f.keys) != 0 {
				t.Fatalf("sent %v to a pane that changed before the key", f.keys)
			}
		})
	}
}

// A person who attaches between the timer's check and the key wins: the
// attach state is read again right before the Enter.
func TestWaitTimer_HumanAttachesJustBeforeKey(t *testing.T) {
	f := newWaitFake()
	inst := waitInstance("s1", "5m")
	f.panes["s1"] = waitRecommendedPane()
	w := f.timer()
	w.Tick([]*Instance{inst})
	f.now = f.now.Add(5 * time.Minute)
	first := f.captures
	f.onCapture = func(n int) {
		if n == first+2 { // the re-read right before the key
			f.attached["s1"] = true
		}
	}
	w.Tick([]*Instance{inst})
	if len(f.keys) != 0 || len(f.events) != 0 {
		t.Fatalf("answered while a person attached: keys=%v events=%v", f.keys, f.events)
	}
	// Detached again: the wait starts over, nothing goes out at once.
	f.attached["s1"] = false
	f.onCapture = nil
	w.Tick([]*Instance{inst})
	f.now = f.now.Add(4 * time.Minute)
	w.Tick([]*Instance{inst})
	if len(f.keys) != 0 {
		t.Fatalf("answered before a fresh full wait: %v", f.keys)
	}
}

// Non-Claude sessions are never touched: the menu layouts are Claude's.
func TestWaitTimer_NonClaudeIgnored(t *testing.T) {
	f := newWaitFake()
	inst := waitInstance("s1", "5m")
	inst.Tool = "codex"
	f.panes["s1"] = waitRecommendedPane()
	w := f.timer()
	w.Tick([]*Instance{inst})
	f.now = f.now.Add(time.Hour)
	w.Tick([]*Instance{inst})
	if len(f.keys) != 0 || f.captures != 0 {
		t.Fatalf("codex session touched: keys=%v captures=%d", f.keys, f.captures)
	}
}

func TestParseWaitTimeout(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"off", 0, false},
		{"OFF", 0, false},
		{"5m", 5 * time.Minute, false},
		{" 1h ", time.Hour, false},
		{"30s", 0, true}, // under the one-minute floor
		{"-5m", 0, true},
		{"soon", 0, true},
	}
	for _, c := range cases {
		got, err := ParseWaitTimeout(c.in)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ParseWaitTimeout(%q) = %v, %v; want %v, err=%v", c.in, got, err, c.want, c.wantErr)
		}
	}
	cfg := UserConfig{Sessions: SessionsSettings{WaitTimeout: "5m"}}
	if got := cfg.GetWaitTimeout(); got != 5*time.Minute {
		t.Errorf("[sessions] wait_timeout = 5m read as %v", got)
	}
	cfg.Sessions.WaitTimeout = "10s"
	if got := cfg.GetWaitTimeout(); got != 0 {
		t.Errorf("an invalid [sessions] wait_timeout must read as off, got %v", got)
	}
}

func TestSetField_WaitTimeout(t *testing.T) {
	identityTestEnv(t)
	inst := waitInstance("s1", "")
	if _, _, err := SetField(inst, FieldWaitTimeout, "soon", nil); err == nil {
		t.Fatal("an invalid duration must be rejected")
	}
	if _, _, err := SetField(inst, FieldWaitTimeout, "20s", nil); err == nil {
		t.Fatal("a value under one minute must be rejected")
	}
	if old, _, err := SetField(inst, FieldWaitTimeout, "5m", nil); err != nil || old != "" || inst.WaitTimeout != "5m" {
		t.Fatalf("set 5m: old=%q err=%v stored=%q", old, err, inst.WaitTimeout)
	}
	if old, _, err := SetField(inst, FieldWaitTimeout, "0", nil); err != nil || old != "5m" || inst.WaitTimeout != "off" {
		t.Fatalf("set 0: old=%q err=%v stored=%q", old, err, inst.WaitTimeout)
	}
	if _, _, err := SetField(inst, FieldWaitTimeout, "", nil); err != nil || inst.WaitTimeout != "" {
		t.Fatalf("clear: err=%v stored=%q", err, inst.WaitTimeout)
	}
	if RestartPolicyFor(FieldWaitTimeout) != FieldLive {
		t.Error("wait-timeout must apply live")
	}
	found := false
	for _, f := range ValidMutableFields {
		found = found || f == FieldWaitTimeout
	}
	if !found {
		t.Error("wait-timeout missing from ValidMutableFields")
	}
}

// The per-session value survives a save/load and a clear is not resurrected
// by MergeToolDataExtras.
func TestWaitTimeout_PersistsOnInstance(t *testing.T) {
	td := WriteWaitTimeoutToToolData(json.RawMessage(`{"context_level":"primer"}`), "5m")
	if got := ReadWaitTimeoutFromToolData(td); got != "5m" || !strings.Contains(string(td), `"context_level":"primer"`) {
		t.Fatalf("round trip: %q from %s", got, td)
	}

	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	inst := NewInstance("wait-timeout-roundtrip", "/tmp")
	inst.Tool = "shell"
	inst.WaitTimeout = "5m"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil || len(loaded) != 1 || loaded[0].WaitTimeout != "5m" {
		t.Fatalf("override not preserved: err=%v", err)
	}
	loaded[0].WaitTimeout = ""
	if err := storage.SaveWithGroups(loaded, NewGroupTreeWithGroups(loaded, nil)); err != nil {
		t.Fatalf("SaveWithGroups after clear: %v", err)
	}
	reloaded, _, err := storage.LoadWithGroups()
	if err != nil || len(reloaded) != 1 || reloaded[0].WaitTimeout != "" {
		t.Fatalf("clear did not survive save/reload: err=%v", err)
	}
}
