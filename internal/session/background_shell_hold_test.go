package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// [status] background_shell_hold: how long a run_in_background shell keeps a
// finished Claude turn running after its launch (A5). Default 3m; 0 is the
// rule before A5 (the footer counter alone holds running).

// writeStatusConfig writes config.toml with body for this test's isolated
// config dir and loads it, so the hold comes from the real config path. The
// default hold is restored afterwards.
func writeStatusConfig(t *testing.T, body string) {
	t.Helper()
	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	if !strings.HasPrefix(path, os.TempDir()) {
		t.Skipf("config path %s is not under the test temp dir", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	if _, err := LoadUserConfig(); err != nil {
		t.Fatalf("load config: %v", err)
	}
	t.Cleanup(func() {
		ClearUserConfigCache()
		setBackgroundShellHold(DefaultBackgroundShellHold)
	})
}

func TestBackgroundShellHold_ConfigValues(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    time.Duration
		invalid bool
	}{
		{"", DefaultBackgroundShellHold, false},
		{"30m", 30 * time.Minute, false},
		{"0", 0, false},
		{"0s", 0, false},
		{"soon", DefaultBackgroundShellHold, true},
		{"-1m", DefaultBackgroundShellHold, true},
	} {
		got, err := StatusSettings{BackgroundShellHold: tc.raw}.GetBackgroundShellHold()
		if got != tc.want || (err != nil) != tc.invalid {
			t.Errorf("background_shell_hold %q = %v, %v; want %v, invalid=%v", tc.raw, got, err, tc.want, tc.invalid)
		}
	}

	t.Setenv("HOME", t.TempDir())
	writeStatusConfig(t, "[status]\nbackground_shell_hold = \"30m\"\n")
	if got := backgroundShellHold(); got != 30*time.Minute {
		t.Fatalf("hold from config.toml = %v, want 30m", got)
	}
	writeStatusConfig(t, "[status]\nbackground_shell_hold = \"soon\"\n")
	if got := backgroundShellHold(); got != DefaultBackgroundShellHold {
		t.Fatalf("invalid hold in config.toml = %v, want the default %v", got, DefaultBackgroundShellHold)
	}
}

// The merge rule under the default, a raised and a zero hold, with an
// injected clock.
func TestBackgroundShellHold_MergeRule(t *testing.T) {
	t.Cleanup(func() { setBackgroundShellHold(DefaultBackgroundShellHold) })
	now := time.Now()
	pane := tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: "1 shell", Source: "pane"}
	at := func(ago time.Duration) transcriptBackgroundScan {
		return scanTranscriptBackground(a5BashLaunch(now.Add(-ago)))
	}

	// Default: 3 minutes from the launch.
	setBackgroundShellHold(DefaultBackgroundShellHold)
	if got, _ := mergeBackgroundWork(pane, at(2*time.Minute), true, time.Time{}, now); !got.Running() {
		t.Fatalf("default hold, launch 2m ago = %+v, want running", got)
	}
	if got, _ := mergeBackgroundWork(pane, at(5*time.Minute), true, time.Time{}, now); got.Running() {
		t.Fatalf("default hold, launch 5m ago = %+v, want nothing running", got)
	}

	// Raised to 10 minutes: a 5 minute old launch still holds running, on
	// the counter and transcript-only; past 10 minutes it does not.
	setBackgroundShellHold(10 * time.Minute)
	if got, _ := mergeBackgroundWork(pane, at(5*time.Minute), true, time.Time{}, now); !got.Running() || got.Kind != tmux.BackgroundKindBash {
		t.Fatalf("10m hold, launch 5m ago = %+v, want bash running", got)
	}
	if got, _ := mergeBackgroundWork(tmux.BackgroundWork{}, at(5*time.Minute), true, time.Time{}, now); !got.Running() {
		t.Fatalf("10m hold, transcript only, launch 5m ago = %+v, want running", got)
	}
	if got, _ := mergeBackgroundWork(pane, at(11*time.Minute), true, time.Time{}, now); got.Running() {
		t.Fatalf("10m hold, launch 11m ago = %+v, want nothing running", got)
	}

	// 0: the rule before A5. The counter alone holds running, with no
	// transcript and with a launch hours old, and the pane frame alone is
	// work again for the tmux layer.
	setBackgroundShellHold(0)
	if got, shown := mergeBackgroundWork(pane, transcriptBackgroundScan{}, false, time.Time{}, now); !got.Running() || !shown {
		t.Fatalf("hold 0, pane only = %+v shown=%v, want running, proven by the pane", got, shown)
	}
	if got, _ := mergeBackgroundWork(pane, at(2*time.Hour), true, time.Time{}, now); !got.Running() {
		t.Fatalf("hold 0, launch 2h ago = %+v, want running", got)
	}
	if !tmux.ParseClaudeBackgroundWork(a5PaneGhostSuggestion).PaneProven().Running() {
		t.Fatal("hold 0: the tmux layer must count the shell counter as work")
	}
	setBackgroundShellHold(DefaultBackgroundShellHold)
	if tmux.ParseClaudeBackgroundWork(a5PaneGhostSuggestion).PaneProven().Running() {
		t.Fatal("default hold: the tmux layer must not count the shell counter alone")
	}
}

// End to end on the Stop hook path, the hold read from config.toml: a
// launch 5 minutes old reads waiting under the default and running /
// background-work with the hold raised to 10m; with 0 a pane-only shell
// counter reads running as before A5.
func TestBackgroundShellHold_FromConfigEndToEnd(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	inst, cleanup := startHookLagInstance(t, "shell-hold", a5PaneGhostSuggestion)
	defer cleanup()
	inst.ClaudeSessionID = "sess-610"
	pass := func(launchedAt time.Time) (Status, Substate) {
		t.Helper()
		turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
		writeInstanceTranscript(t, inst, a5BashLaunch(launchedAt))
		writeHookLagStopFile(t, inst.ID)
		return cliPass(t, inst)
	}
	fiveMinutes := time.Now().Add(-5 * time.Minute)

	writeStatusConfig(t, "")
	if status, sub := pass(fiveMinutes); status != StatusWaiting || sub != SubstateIdleAtEmptyPrompt {
		t.Fatalf("default hold, launch 5m ago = %q/%q, want waiting/%q", status, sub, SubstateIdleAtEmptyPrompt)
	}
	writeStatusConfig(t, "[status]\nbackground_shell_hold = \"10m\"\n")
	if status, sub := pass(fiveMinutes); status != StatusRunning || sub != SubstateBackgroundWork {
		t.Fatalf("10m hold, launch 5m ago = %q/%q, want running/%q", status, sub, SubstateBackgroundWork)
	}
	writeStatusConfig(t, "[status]\nbackground_shell_hold = \"0\"\n")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	writeInstanceTranscript(t, inst, nil)
	writeHookLagStopFile(t, inst.ID)
	if status, sub := cliPass(t, inst); status != StatusRunning || sub != SubstateBackgroundWork {
		t.Fatalf("hold 0, shell counter without a launch = %q/%q, want running/%q (the rule before A5)", status, sub, SubstateBackgroundWork)
	}
}

// a5PaneEmptyPrompt is the A5 frame with an empty composer: a completed turn
// the missing-Stop-hook rule recognises.
const a5PaneEmptyPrompt = "⏺ Done. The dev server is still up on :5173 if you want to look.\n" +
	"\n" +
	"✻ Crunched for 45s · done 10:20 PM · 1 shell still running\n" +
	"\n" +
	"────────────────────────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on · 1 shell · ← for agents\n"

// A lost Stop hook (the hook file still says running over the finished
// frame) and a delivered one must agree on a background shell: both running
// / background-work while the launch is inside the hold, both waiting past
// it.
func TestBackgroundShellHold_LostAndDeliveredStopHookAgree(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, tc := range []struct {
		name    string
		ago     time.Duration
		running bool
	}{
		{"within the hold", 20 * time.Second, true},
		{"past the hold", 2 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launch := a5BashLaunch(time.Now().Add(-tc.ago))
			want := StatusWaiting
			if tc.running {
				want = StatusRunning
			}

			// Lost Stop hook: two completed-turn samples under the running
			// hook confirm the lag.
			lost, cleanup := startHookLagInstance(t, "shell-lost-hook", a5PaneEmptyPrompt)
			defer cleanup()
			lost.ClaudeSessionID = "sess-610"
			turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
			writeInstanceTranscript(t, lost, launch)
			cliPass(t, lost)
			time.Sleep(tmux.CompletedTurnSampleInterval + 200*time.Millisecond)
			lostStatus, lostSub := cliPass(t, lost)

			// Delivered Stop hook on the same frame and transcript.
			delivered, cleanup2 := startHookLagInstance(t, "shell-stop-hook", a5PaneEmptyPrompt)
			defer cleanup2()
			delivered.ClaudeSessionID = "sess-610"
			turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
			writeInstanceTranscript(t, delivered, launch)
			writeHookLagStopFile(t, delivered.ID)
			deliveredStatus, deliveredSub := cliPass(t, delivered)

			if lostStatus != want || deliveredStatus != want {
				t.Fatalf("lost hook = %q/%q, delivered hook = %q/%q, want both %q", lostStatus, lostSub, deliveredStatus, deliveredSub, want)
			}
			if tc.running && (lostSub != SubstateBackgroundWork || deliveredSub != SubstateBackgroundWork) {
				t.Fatalf("lost hook substate %q, delivered %q, want both %q", lostSub, deliveredSub, SubstateBackgroundWork)
			}
		})
	}
}
