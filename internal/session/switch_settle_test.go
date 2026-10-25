package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/procowner"
)

// Hands-free switch (SWFB1018): fake Claude panes stand in for the real
// first-start prompts seen in the field.

const settleProject = "/work/alpha"

// trustScreen is the folder-trust dialog as a live capture draws it: one
// rule on top, the workspace path, the question, the cursor on "No, exit"
// first and the rows unnumbered. cursor 0 = "No, exit", 1 = "Yes".
func trustScreen(path string, cursor int) string {
	no, yes := "   No, exit", "   Yes, I trust this folder"
	if cursor == 0 {
		no = " ❯ No, exit"
	} else {
		yes = " ❯ Yes, I trust this folder"
	}
	return strings.Join([]string{
		strings.Repeat("─", 80),
		" Accessing workspace:",
		"",
		" " + path,
		"",
		" Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known",
		" open source project, or work from your team). If not, take a moment to review what's in this folder first.",
		"",
		" Claude Code'll be able to read, edit, and execute files here.",
		"",
		" Security guide",
		"",
		no,
		yes,
		"",
		" Enter to confirm · Esc to cancel",
	}, "\n")
}

func trustPane(path string) string { return trustScreen(path, 0) }

const exitedPane = "ashesh@host:~$ "

// trustDialogKeys drives a fake trust dialog: arrows move the cursor, Enter
// picks the row under it (No exits the session).
func trustDialogKeys(path string, next string) func(f *fakeSettle, key string) {
	cursor := 0
	return func(f *fakeSettle, key string) {
		switch key {
		case "Down":
			cursor = 1
			f.screens = []string{trustScreen(path, cursor)}
		case "Up":
			cursor = 0
			f.screens = []string{trustScreen(path, cursor)}
		case "Enter":
			if cursor == 1 {
				f.screens = []string{next}
			} else {
				f.screens = []string{exitedPane}
			}
		}
	}
}

const readyPane = "\n● Resumed.\n\n────────────────────────────────────────\n❯ \n────────────────────────────────────────\n  ? for shortcuts\n"

const themePane = "Let's get started.\n\nChoose the text style that looks best with your terminal\n\n❯ 1. Dark mode ✔\n  2. Light mode\n"

func bypassPane(cursorOnYes bool) string {
	no, yes := "❯ 1. No, exit", "  2. Yes, I accept"
	if cursorOnYes {
		no, yes = "  1. No, exit", "❯ 2. Yes, I accept"
	}
	return "WARNING: Claude Code running in Bypass Permissions mode\n\n" + no + "\n" + yes + "\n\nEnter to confirm · Esc to exit\n"
}

func TestClassifySettlePane(t *testing.T) {
	paths := []string{settleProject}
	cases := []struct {
		name string
		pane string
		want SettlePaneState
	}{
		{"trust own path", trustPane(settleProject), SettlePaneTrustOwn},
		{"trust own path trailing slash in config", trustPane(settleProject), SettlePaneTrustOwn},
		{"trust other path", trustPane("/work/beta"), SettlePaneTrustOther},
		{"trust parent of own path", trustPane("/work"), SettlePaneTrustOther},
		{"trust longer path sharing a prefix", trustPane(settleProject + "-evil"), SettlePaneTrustOther},
		{"old trust wording, numbered rows", "Do you trust the files in this folder?\n\n" + settleProject + "\n\n❯ 1. Yes, proceed\n  2. No, exit\n", SettlePaneTrustOwn},
		{"wrapped path", "Accessing workspace:\n\n/work/al\npha\n\nQuick safety check: Is this a project you created or one you trust?\n❯ No, exit\n  Yes, I trust this folder\n", SettlePaneTrustOwn},
		{"trust question quoted in history above the composer", trustScreen(settleProject, 1) + "\n" + readyPane, SettlePaneReady},
		{"trust question without option rows is not a live dialog", "Quick safety check: Is this a project you created or one you trust?\n" + settleProject + "\n", SettlePaneTrustOther},
		{"question wrapped by a narrow pane", "│ Accessing workspace: │\n│ " + settleProject + " │\n│ Quick safety check: Is this a project you created │\n│ or one you trust? (Like your own code) │\n│ │\n│ ❯ No, exit │\n│   Yes, I trust this folder │\n", SettlePaneTrustOwn},
		{"own path elsewhere on screen, dialog for another folder", " " + settleProject + "\n" + trustScreen("/work/beta", 0), SettlePaneTrustOther},
		{"wrapped path that only starts with the own path", strings.Replace(trustScreen(settleProject+"-evil", 0), settleProject+"-evil", settleProject+"\n -evil", 1), SettlePaneTrustOther},
		{"wrapped subdirectory of the own path", strings.Replace(trustScreen(settleProject+"/sub", 0), settleProject+"/sub", settleProject+"\n /sub", 1), SettlePaneTrustOther},
		{"yes row without a no row is not a live dialog", "Accessing workspace:\n\n" + settleProject + "\n\nQuick safety check: Is this a project you created or one you trust?\n❯ Yes, I trust this folder\n", SettlePaneTrustOther},
		{"trust dialog framed by two rules", strings.Repeat("─", 40) + "\n" + trustScreen(settleProject, 0) + "\n" + strings.Repeat("─", 40), SettlePaneTrustOwn},
		{"leftover own-path dialog above a live one for another folder", trustScreen(settleProject, 0) + "\n" + trustScreen("/work/beta", 0), SettlePaneTrustOther},
		{"theme", themePane, SettlePaneTheme},
		{"bypass", bypassPane(false), SettlePaneBypass},
		{"onboarding security notes", "Security notes:\n\n1. Claude can make mistakes\n\nPress Enter to continue…\n", SettlePaneOnboarding},
		{"login", "Select login method:\n\n❯ 1. Claude account with subscription\n", SettlePaneLoginNeeded},
		{"unknown menu", "Resume a previous conversation?\n\n❯ 1. Resume\n  2. Start fresh\n\nEnter to select\n", SettlePaneUnknownMenu},
		{"ready composer", readyPane, SettlePaneReady},
		{"draft that looks like a menu row", "● earlier reply\n  ❯ 1. Yes, old picker in history\n────────────────────────────────────────\n❯ 1. fix the bug\n────────────────────────────────────────\n  ? for shortcuts\n", SettlePaneReady},
		{"booting", "Claude Code v2.3.1\nloading…\n", SettlePaneStarting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifySettlePane(tc.pane, paths)
			if got.State != tc.want {
				t.Fatalf("state = %s (%s), want %s", got.State, got.Detail, tc.want)
			}
		})
	}
}

// fakeSettle scripts a pane: each Capture returns the current screen, and
// keys or a restart move it on.
type fakeSettle struct {
	mu       sync.Mutex
	screens  []string
	keys     []string
	kicks    []string
	restarts int
	onKey    func(f *fakeSettle, key string)
	onRetry  func(f *fakeSettle)
	procs    *fakeProcs
	now      time.Time
}

func (f *fakeSettle) deps() SettleDeps {
	return SettleDeps{
		Capture: func() (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.screens) == 0 {
				return "", errors.New("no pane")
			}
			screen := f.screens[0]
			if len(f.screens) > 1 {
				f.screens = f.screens[1:]
			}
			return screen, nil
		},
		SendKey: func(key string) error {
			f.mu.Lock()
			f.keys = append(f.keys, key)
			cb := f.onKey
			f.mu.Unlock()
			if cb != nil {
				cb(f, key)
			}
			return nil
		},
		Restart: func() error {
			f.restarts++
			if f.onRetry != nil {
				f.onRetry(f)
			}
			return nil
		},
		PanePID:  func() (int, error) { return 100, nil },
		Procs:    f.procs,
		Kick:     func(m string) error { f.kicks = append(f.kicks, m); return nil },
		AuthInfo: func(string) (string, error) { return "ops@example.com", nil },
		Sleep:    func(d time.Duration) { f.now = f.now.Add(d) },
		Now:      func() time.Time { return f.now },
	}
}

func (f *fakeSettle) set(screens ...string) {
	f.mu.Lock()
	f.screens = screens
	f.mu.Unlock()
}

type fakeProcs struct {
	mu   sync.Mutex
	cmds map[int]procowner.ProcCommand
	env  map[int]map[string]string
	// gone[pid] = polls left before the process exits
	gone map[int]int
}

func (p *fakeProcs) Commands() (map[int]procowner.ProcCommand, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[int]procowner.ProcCommand{}
	for pid, c := range p.cmds {
		out[pid] = c
	}
	for pid, left := range p.gone {
		if left <= 0 {
			delete(p.cmds, pid)
			delete(p.gone, pid)
		} else {
			p.gone[pid] = left - 1
		}
	}
	return out, nil
}

func (p *fakeProcs) Env(pid int, key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.env[pid][key]
}

// liveProcs is a healthy restarted pane: shell 100 -> claude 101 on the
// target config dir -> telegram poller 102.
func liveProcs(id, configDir string) *fakeProcs {
	return &fakeProcs{
		cmds: map[int]procowner.ProcCommand{
			100: {PPID: 1, Args: []string{"bash", "-c", "claude"}},
			101: {PPID: 100, Args: []string{"node", "/usr/lib/claude/cli.js"}},
			102: {PPID: 101, Args: []string{"bun", "run", "telegram/server.ts"}},
		},
		env: map[int]map[string]string{
			100: {"AGENTDECK_INSTANCE_ID": id},
			101: {"AGENTDECK_INSTANCE_ID": id, "CLAUDE_CONFIG_DIR": configDir},
			102: {"AGENTDECK_INSTANCE_ID": id, "CLAUDE_CONFIG_DIR": configDir},
		},
		gone: map[int]int{},
	}
}

func settleOpts(configDir string) SettleOptions {
	return SettleOptions{InstanceID: "inst-1", Title: "alpha", Account: "work", ConfigDir: configDir,
		ProjectPath: settleProject, Kick: DefaultSettleKick("work"), ReadyTimeout: time.Minute,
		PollerWait: 5 * time.Second, PollEvery: 100 * time.Millisecond}
}

func TestSettleSwitch_AnswersOwnTrustPromptOnceThenKicksAndVerifies(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(trustPane(settleProject))
	f.onKey = trustDialogKeys(settleProject, readyPane)
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil {
		t.Fatalf("settle: %v (%s)", err, report.Receipt())
	}
	// The cursor starts on "No, exit": move to Yes, then one Enter.
	if strings.Join(f.keys, ",") != "Down,Enter" {
		t.Fatalf("keys = %v, want Down,Enter (never Enter on No, exit)", f.keys)
	}
	if len(f.kicks) != 1 || !strings.Contains(f.kicks[0], "re-check your background work") {
		t.Fatalf("kicks = %q", f.kicks)
	}
	if !report.OK() || !report.AccountOK || report.AuthIdentity != "ops@example.com" {
		t.Fatalf("report not healthy: %s", report.Receipt())
	}
	receipt := report.Receipt()
	for _, want := range []string{"settle alpha: ok", "account=work", "verified", "answered=trust-own-path", "plugins=ok", "kick=sent"} {
		if !strings.Contains(receipt, want) {
			t.Errorf("receipt %q lacks %q", receipt, want)
		}
	}
	if strings.Contains(receipt, "\n") {
		t.Errorf("receipt is not one line: %q", receipt)
	}
}

func TestSettleSwitch_TrustPromptForOtherPathStopsWithNothingPressed(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(trustPane("/work/beta"))
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if !errors.Is(err, ErrSettleStopped) {
		t.Fatalf("err = %v, want ErrSettleStopped", err)
	}
	if len(f.keys) != 0 || len(f.kicks) != 0 {
		t.Fatalf("pressed %v, kicked %v; want nothing", f.keys, f.kicks)
	}
	if !strings.Contains(report.Receipt(), "STOPPED") || !strings.Contains(report.StoppedAt, "/work/beta") {
		t.Fatalf("receipt %q", report.Receipt())
	}
}

func TestSettleSwitch_NumberedTrustWithCursorOnYesIsOneEnter(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set("Do you trust the files in this folder?\n\n" + settleProject + "\n\n❯ 1. Yes, proceed\n  2. No, exit\n\nEnter to confirm\n")
	f.onKey = func(f *fakeSettle, key string) { f.screens = []string{readyPane} }
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || !report.OK() || strings.Join(f.keys, ",") != "Enter" {
		t.Fatalf("err=%v keys=%v receipt=%s", err, f.keys, report.Receipt())
	}
}

func TestSettleSwitch_TrustCursorThatWillNotMoveStops(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(trustPane(settleProject)) // arrows ignored: cursor stays on No
	_, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if !errors.Is(err, ErrSettleStopped) {
		t.Fatalf("err = %v", err)
	}
	for _, k := range f.keys {
		if k == "Enter" {
			t.Fatalf("Enter pressed with the cursor on No, exit: keys=%v", f.keys)
		}
	}
}

func TestSettleSwitch_UnknownMenuStopsWithNothingPressed(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set("Pick a model\n\n❯ 1. Opus\n  2. Sonnet\n\nEnter to select\n")
	_, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if !errors.Is(err, ErrSettleStopped) || len(f.keys) != 0 {
		t.Fatalf("err=%v keys=%v", err, f.keys)
	}
}

func TestSettleSwitch_ThemeThenTrust(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(themePane)
	trust := trustDialogKeys(settleProject, readyPane)
	f.onKey = func(f *fakeSettle, key string) {
		if len(f.keys) == 1 {
			f.screens = []string{trustPane(settleProject)}
			return
		}
		trust(f, key)
	}
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || !report.OK() {
		t.Fatalf("err=%v receipt=%s", err, report.Receipt())
	}
	if strings.Join(report.Answered, ",") != "theme-picker,trust-own-path" {
		t.Fatalf("answered = %v", report.Answered)
	}
}

func TestSettleSwitch_BypassOnlyWhenSessionRunsWithIt(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(bypassPane(false))
	if _, err := SettleSwitch(f.deps(), settleOpts(cfgDir)); !errors.Is(err, ErrSettleStopped) || len(f.keys) != 0 {
		t.Fatalf("without bypass: err=%v keys=%v", err, f.keys)
	}

	f = &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(bypassPane(false))
	f.onKey = func(f *fakeSettle, key string) {
		switch key {
		case "Down":
			f.screens = []string{bypassPane(true)}
		case "Enter":
			f.screens = []string{readyPane}
		}
	}
	opts := settleOpts(cfgDir)
	opts.Bypass = true
	report, err := SettleSwitch(f.deps(), opts)
	if err != nil || !report.OK() {
		t.Fatalf("with bypass: err=%v receipt=%s", err, report.Receipt())
	}
	if strings.Join(f.keys, ",") != "Down,Enter" {
		t.Fatalf("keys = %v, want Down,Enter", f.keys)
	}
}

func TestSettleSwitch_ConnectionClosedPluginRetriesOnceThenHealthy(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	closed := readyPane + "\n  plugin:telegram:telegram (CONNECTION_CLOSED) · /mcp\n"
	f.set(closed)
	f.onRetry = func(f *fakeSettle) { f.screens = []string{readyPane} }
	opts := settleOpts(cfgDir)
	opts.Plugins = []string{"telegram"}
	report, err := SettleSwitch(f.deps(), opts)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if f.restarts != 1 || !report.PluginRetried {
		t.Fatalf("restarts = %d retried=%t, want exactly one retry", f.restarts, report.PluginRetried)
	}
	if !report.OK() {
		t.Fatalf("not healthy after retry: %s", report.Receipt())
	}
}

func TestSettleSwitch_PersistentPluginFailureIsNamed(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	closed := readyPane + "\n  plugin:telegram:telegram (CONNECTION_CLOSED)\n"
	f.set(closed)
	f.onRetry = func(f *fakeSettle) { f.screens = []string{closed} }
	report, _ := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if f.restarts != 1 {
		t.Fatalf("restarts = %d, want 1 (retry once, not a loop)", f.restarts)
	}
	if report.OK() || !strings.Contains(report.Receipt(), "plugins=FAILED:plugin:telegram:telegram") {
		t.Fatalf("receipt %q", report.Receipt())
	}
	if len(f.kicks) != 1 {
		t.Fatalf("a named plugin failure must not withhold the kick: kicks=%d", len(f.kicks))
	}
}

func TestSettleSwitch_MissingChannelPollerIsAFailure(t *testing.T) {
	cfgDir := t.TempDir()
	procs := liveProcs("inst-1", cfgDir)
	delete(procs.cmds, 102)
	f := &fakeSettle{procs: procs, now: time.Unix(0, 0)}
	f.set(readyPane)
	opts := settleOpts(cfgDir)
	opts.Plugins = []string{"telegram"}
	report, _ := SettleSwitch(f.deps(), opts)
	if report.OK() || !strings.Contains(strings.Join(report.PluginFailures, ","), "telegram(no process)") {
		t.Fatalf("receipt %q", report.Receipt())
	}
}

func TestSettleSwitch_WaitsForOrphanedOldPoller(t *testing.T) {
	cfgDir := t.TempDir()
	procs := liveProcs("inst-1", cfgDir)
	// The replaced Claude's telegram poller, reparented to init, still
	// carrying this instance's id; it exits after a few polls.
	// Reparented to a subreaper (systemd --user), not to pid 1.
	procs.cmds[50] = procowner.ProcCommand{PPID: 1, Args: []string{"systemd", "--user"}}
	procs.cmds[90] = procowner.ProcCommand{PPID: 50, Args: []string{"bun", "run", "telegram/server.ts"}}
	procs.env[90] = map[string]string{"AGENTDECK_INSTANCE_ID": "inst-1"}
	procs.gone[90] = 4
	// An unrelated orphan with another id is not ours.
	procs.cmds[91] = procowner.ProcCommand{PPID: 1, Args: []string{"bun", "run", "telegram/server.ts"}}
	procs.env[91] = map[string]string{"AGENTDECK_INSTANCE_ID": "other"}
	f := &fakeSettle{procs: procs, now: time.Unix(0, 0)}
	f.set(readyPane)
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || !report.OK() || len(report.StalePollers) != 0 {
		t.Fatalf("err=%v stale=%v receipt=%s", err, report.StalePollers, report.Receipt())
	}
	if _, still := procs.cmds[90]; still {
		t.Fatalf("settle finished before the old poller exited")
	}
}

func TestSettleSwitch_OldPollerThatNeverExitsIsReportedNotKilled(t *testing.T) {
	cfgDir := t.TempDir()
	procs := liveProcs("inst-1", cfgDir)
	procs.cmds[90] = procowner.ProcCommand{PPID: 1, Args: []string{"bun", "telegram"}}
	procs.env[90] = map[string]string{"AGENTDECK_INSTANCE_ID": "inst-1"}
	f := &fakeSettle{procs: procs, now: time.Unix(0, 0)}
	f.set(readyPane)
	report, _ := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if report.OK() || fmt.Sprint(report.StalePollers) != "[90]" {
		t.Fatalf("stale=%v receipt=%s", report.StalePollers, report.Receipt())
	}
}

func TestSettleSwitch_WrongLiveConfigDirIsUnverified(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", t.TempDir()), now: time.Unix(0, 0)}
	f.set(readyPane)
	report, _ := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if report.AccountOK || report.OK() || !strings.Contains(report.Receipt(), "unverified") {
		t.Fatalf("receipt %q", report.Receipt())
	}
}

func TestSettleSwitch_NeverReadyReportsTimeout(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set("Claude Code starting…")
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || report.Ready || report.OK() || len(f.kicks) != 0 {
		t.Fatalf("err=%v receipt=%s kicks=%d", err, report.Receipt(), len(f.kicks))
	}
}

func TestPaneMCPFailures(t *testing.T) {
	cases := map[string]string{
		"plugin:telegram:telegram (CONNECTION_CLOSED)":       "plugin:telegram:telegram",
		"plugin:discord:discord (CONNECTION_CLOSED): closed": "plugin:discord:discord",
		"  2 MCP servers failed · /mcp":                      "mcp(2)",
		`MCP server "github" failed to start`:                "github",
		"all good\n❯ ":                                       "",
	}
	for pane, want := range cases {
		if got := strings.Join(PaneMCPFailures(pane), ","); got != want {
			t.Errorf("PaneMCPFailures(%q) = %q, want %q", pane, got, want)
		}
	}
}

func TestPsEnvValue(t *testing.T) {
	line := "node /opt/claude/cli.js --resume abc PATH=/usr/bin AGENTDECK_INSTANCE_ID=inst-1 CLAUDE_CONFIG_DIR=/Users/o/.claude-work TERM=xterm"
	if got := psEnvValue(line, "CLAUDE_CONFIG_DIR"); got != "/Users/o/.claude-work" {
		t.Fatalf("got %q", got)
	}
	if got := psEnvValue(line, "AGENTDECK_INSTANCE_ID"); got != "inst-1" {
		t.Fatalf("got %q", got)
	}
	if got := psEnvValue(line, "MISSING"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// Seven parallel switches to one account each pre-seeded trust with an
// unlocked read-modify-write and lost each other's entries (field evidence:
// several of seven sessions sat at the trust prompt).
func TestPreAcceptClaudeTrust_ParallelWritersKeepEveryEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte(`{"projects":{"/keep":{"hasTrustDialogAccepted":true,"allowedTools":["x"]}},"oauthAccount":{"emailAddress":"a@b.c"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const writers = 7
	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if err := PreAcceptClaudeTrust(path, fmt.Sprintf("/work/r%d-s%d", round, i)); err != nil {
					t.Errorf("seed: %v", err)
				}
			}(i)
		}
		wg.Wait()
	}
	var cfg struct {
		Projects     map[string]map[string]any `json:"projects"`
		OauthAccount map[string]any            `json:"oauthAccount"`
	}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	var missing []string
	for round := 0; round < 5; round++ {
		for i := 0; i < writers; i++ {
			p := fmt.Sprintf("/work/r%d-s%d", round, i)
			if cfg.Projects[p]["hasTrustDialogAccepted"] != true {
				missing = append(missing, p)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("lost %d trust entries to concurrent writers: %v", len(missing), missing)
	}
	if cfg.Projects["/keep"]["allowedTools"] == nil || cfg.OauthAccount == nil {
		t.Fatalf("unrelated keys were dropped: %s", data)
	}
}

func TestPreseedClaudeFirstStart_SeedsEverySpellingAndCopiesOnlyWhatSourceHas(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real-proj")
	link := filepath.Join(root, "link-proj")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(src, ".claude.json"), []byte(`{"theme":"dark","hasCompletedOnboarding":true,"lastOnboardingVersion":"2.0.0","bypassPermissionsModeAccepted":true}`), 0o600)
	_ = os.WriteFile(filepath.Join(dst, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"t@x.y"}}`), 0o600)

	notes, err := PreseedClaudeFirstStart(src, dst, link, false)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	data, _ := os.ReadFile(filepath.Join(dst, ".claude.json"))
	_ = json.Unmarshal(data, &cfg)
	projects := cfg["projects"].(map[string]any)
	resolvedReal, _ := filepath.EvalSymlinks(real)
	for _, p := range []string{link, resolvedReal} {
		entry, _ := projects[p].(map[string]any)
		if entry["hasTrustDialogAccepted"] != true {
			t.Errorf("trust for %s not seeded: %s", p, data)
		}
	}
	if cfg["theme"] != "dark" || cfg["hasCompletedOnboarding"] != true {
		t.Errorf("theme/onboarding not copied: %s", data)
	}
	if _, has := cfg["bypassPermissionsModeAccepted"]; has {
		t.Errorf("bypass acknowledged for a session that does not run with it: %s", data)
	}
	if !strings.Contains(strings.Join(notes, ","), "trust:") {
		t.Errorf("notes = %v", notes)
	}

	// A target that is not logged in keeps its onboarding (login is part of it).
	dst2 := filepath.Join(root, "dst2")
	_ = os.MkdirAll(dst2, 0o700)
	if _, err := PreseedClaudeFirstStart(src, dst2, real, true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dst2, ".claude.json"))
	cfg = nil
	_ = json.Unmarshal(data, &cfg)
	if _, has := cfg["hasCompletedOnboarding"]; has {
		t.Errorf("onboarding marked complete on a logged-out target: %s", data)
	}
	if cfg["bypassPermissionsModeAccepted"] != true {
		t.Errorf("bypass ack not carried for a bypass session: %s", data)
	}
}

func TestPreseedClaudeFirstStart_MalformedTargetIsLeftAlone(t *testing.T) {
	dst := t.TempDir()
	path := filepath.Join(dst, ".claude.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	if _, err := PreseedClaudeFirstStart("", dst, "/work/alpha", false); err == nil {
		t.Fatal("want an error for a malformed target")
	}
	if data, _ := os.ReadFile(path); string(data) != "{not json" {
		t.Fatalf("malformed file was rewritten: %q", data)
	}
	if _, err := os.Stat(path + ".lock"); err == nil {
		t.Fatalf("agent-deck must not create Claude's own %s.lock", path)
	}
}

func TestSessionRunsBypass(t *testing.T) {
	no := false
	cfgOff := &UserConfig{}
	cfgOff.Claude.DangerousMode = &no
	inst := &Instance{Tool: "claude"}
	if SessionRunsBypass(inst, cfgOff) {
		t.Fatal("bypass with dangerous_mode off and no options")
	}
	if !SessionRunsBypass(inst, &UserConfig{}) {
		t.Fatal("dangerous_mode defaults on")
	}
	_ = inst.SetClaudeOptions(&ClaudeOptions{SkipPermissions: false})
	if SessionRunsBypass(inst, &UserConfig{}) {
		t.Fatal("explicit options override the config default")
	}
}

// A slow repaint: each capture shows the screen as it was before the last
// key. The cursor must be seen on Yes in two captures before Enter, and no
// arrow is sent before the previous one shows.
func TestSettleSwitch_StaleFramesNeverEnterOnNo(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	cursor, shown := 0, 0 // shown lags cursor by one capture
	f.onKey = func(f *fakeSettle, key string) {
		switch key {
		case "Down":
			cursor = (cursor + 1) % 2 // a selector that wraps
		case "Up":
			cursor = (cursor + 1) % 2
		case "Enter":
			if cursor != 1 {
				t.Errorf("Enter with the cursor on No, exit (keys %v)", f.keys)
			}
			cursor = 2
		}
	}
	deps := f.deps()
	deps.Capture = func() (string, error) {
		screen := trustScreen(settleProject, shown)
		if shown == 2 {
			screen = readyPane
		}
		shown = cursor
		return screen, nil
	}
	report, err := SettleSwitch(deps, settleOpts(cfgDir))
	if err != nil || !report.OK() {
		t.Fatalf("err=%v receipt=%s", err, report.Receipt())
	}
	if strings.Join(f.keys, ",") != "Down,Enter" {
		t.Fatalf("keys = %v, want one Down then Enter", f.keys)
	}
}

func TestSettleSwitch_YesAboveCursorMovesUp(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	screen := func(onYes bool) string {
		yes, no := "   Yes, proceed", " ❯ No, exit"
		if onYes {
			yes, no = " ❯ Yes, proceed", "   No, exit"
		}
		return "Do you trust the files in this folder?\n\n" + settleProject + "\n\n" + yes + "\n" + no + "\n\nEnter to confirm\n"
	}
	f.set(screen(false))
	f.onKey = func(f *fakeSettle, key string) {
		switch key {
		case "Up":
			f.screens = []string{screen(true)}
		case "Down":
			f.screens = []string{screen(false)}
		case "Enter":
			f.screens = []string{readyPane}
		}
	}
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || !report.OK() || strings.Join(f.keys, ",") != "Up,Enter" {
		t.Fatalf("err=%v keys=%v receipt=%s", err, f.keys, report.Receipt())
	}
}

// A composer painted for one frame before the trust dialog is not ready.
func TestSettleSwitch_TransientComposerIsNotReady(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(readyPane, trustPane(settleProject))
	f.onKey = trustDialogKeys(settleProject, readyPane)
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || strings.Join(report.Answered, ",") != "trust-own-path" {
		t.Fatalf("err=%v receipt=%s", err, report.Receipt())
	}
}

func TestSettleSwitch_PluginTalkInTheConversationIsNotAFailure(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set("● Earlier the plugin:discord:discord server failed and ECONNRESET (CONNECTION_CLOSED) showed up.\n" + readyPane)
	report, _ := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if f.restarts != 0 || len(report.PluginFailures) != 0 || !report.OK() {
		t.Fatalf("restarts=%d receipt=%s", f.restarts, report.Receipt())
	}
}

func TestSettleSwitch_WorkerScratchDirVerifiesAgainstItsAccount(t *testing.T) {
	account, other := t.TempDir(), t.TempDir()
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, scratchSourceMarker), []byte(account+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeSettle{procs: liveProcs("inst-1", scratch), now: time.Unix(0, 0)}
	f.set(readyPane)
	report, _ := SettleSwitch(f.deps(), settleOpts(account))
	if !report.AccountOK {
		t.Fatalf("scratch mirror of the account not verified: %s", report.Receipt())
	}
	f = &fakeSettle{procs: liveProcs("inst-1", scratch), now: time.Unix(0, 0)}
	f.set(readyPane)
	report, _ = SettleSwitch(f.deps(), settleOpts(other))
	if report.AccountOK {
		t.Fatalf("scratch mirror of another account verified: %s", report.Receipt())
	}
}

// The shell wrapper carries the id but no config dir; the Claude process
// under it is what must be on the account.
func TestSettleSwitch_ClaudeProcessNotShellIsVerified(t *testing.T) {
	cfgDir := t.TempDir()
	procs := liveProcs("inst-1", t.TempDir()) // claude 101 on the wrong dir
	f := &fakeSettle{procs: procs, now: time.Unix(0, 0)}
	f.set(readyPane)
	home, _ := os.UserHomeDir()
	opts := settleOpts(filepath.Join(home, ".claude"))
	report, _ := SettleSwitch(f.deps(), opts)
	if report.AccountOK {
		t.Fatalf("verified by the shell wrapper: %s", report.Receipt())
	}
	_ = cfgDir
}

// The plugin retry waits for every process of the pane it replaces, not
// only orphans: the old poller here is still parented to the old pane.
func TestSettleSwitch_PluginRetryWaitsForReplacedPaneProcesses(t *testing.T) {
	cfgDir := t.TempDir()
	procs := liveProcs("inst-1", cfgDir)
	f := &fakeSettle{procs: procs, now: time.Unix(0, 0)}
	closed := readyPane + "\n  plugin:telegram:telegram (CONNECTION_CLOSED)\n"
	f.set(closed)
	pane := 100
	deps := f.deps()
	deps.PanePID = func() (int, error) { return pane, nil }
	var pollerAtKick bool
	deps.Kick = func(string) error {
		procs.mu.Lock()
		_, pollerAtKick = procs.cmds[102]
		procs.mu.Unlock()
		return nil
	}
	f.onRetry = func(f *fakeSettle) {
		// New pane 200 with its own claude; the old poller 102 (still under
		// the old claude 101) takes a few polls to exit.
		procs.mu.Lock()
		procs.cmds[200] = procowner.ProcCommand{PPID: 1, Args: []string{"bash"}}
		procs.cmds[201] = procowner.ProcCommand{PPID: 200, Args: []string{"claude"}}
		procs.env[201] = map[string]string{"AGENTDECK_INSTANCE_ID": "inst-1", "CLAUDE_CONFIG_DIR": cfgDir}
		procs.gone[100], procs.gone[101], procs.gone[102] = 0, 0, 5
		// Its environment is unreadable (another uid, or ps -E refused):
		// only the pane snapshot taken before the restart knows it.
		delete(procs.env, 102)
		procs.mu.Unlock()
		pane = 200
		f.screens = []string{readyPane}
	}
	report, _ := SettleSwitch(deps, settleOpts(cfgDir))
	if pollerAtKick {
		t.Fatalf("kick sent while the replaced pane's poller still ran: %s", report.Receipt())
	}
}

// A Claude process of the account rewrote .claude.json from memory right
// after the seed; the locked writer notices and seeds again.
func TestPreAcceptClaudeTrust_RedoesAfterConcurrentRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	rewrites := 0
	claudeJSONAfterWrite = func(p string) {
		if rewrites == 0 {
			rewrites++
			_ = os.WriteFile(p, []byte(`{"projects":{}}`), 0o600)
		}
	}
	t.Cleanup(func() { claudeJSONAfterWrite = nil })
	if err := PreAcceptClaudeTrust(path, "/work/alpha"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"/work/alpha"`) {
		t.Fatalf("seed lost to a concurrent rewrite: %s", data)
	}
}

// After Enter, a stale frame can still show the dialog with the cursor on
// Yes; it is not answered a second time.
func TestSettleSwitch_StaleDialogAfterEnterIsNotAnsweredAgain(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(trustScreen(settleProject, 1))
	f.onKey = func(f *fakeSettle, key string) {
		if key == "Enter" {
			f.screens = []string{trustScreen(settleProject, 1), trustScreen(settleProject, 1), readyPane}
		}
	}
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || !report.OK() || strings.Join(f.keys, ",") != "Enter" {
		t.Fatalf("err=%v keys=%v receipt=%s", err, f.keys, report.Receipt())
	}
}

// The cursor shows on Yes for one frame and then back on No (a queued key,
// a wrapping selector): no Enter unless two captures agree on Yes.
func TestSettleSwitch_CursorBouncingOffYesIsNotConfirmed(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(trustPane(settleProject))
	f.onKey = func(f *fakeSettle, key string) {
		if key == "Down" {
			f.screens = []string{trustScreen(settleProject, 1), trustScreen(settleProject, 0)}
		}
	}
	_, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if !errors.Is(err, ErrSettleStopped) {
		t.Fatalf("err = %v", err)
	}
	for _, k := range f.keys {
		if k == "Enter" {
			t.Fatalf("Enter pressed although the cursor went back to No, exit: keys=%v", f.keys)
		}
	}
}

// Processes in other windows of the session carry its id too (tmux sets it
// session-wide); they are live, not old pollers.
func TestSettleSwitch_OtherWindowsOfTheSessionAreNotOldPollers(t *testing.T) {
	cfgDir := t.TempDir()
	procs := liveProcs("inst-1", cfgDir)
	procs.cmds[300] = procowner.ProcCommand{PPID: 1, Args: []string{"zsh"}}
	procs.cmds[301] = procowner.ProcCommand{PPID: 300, Args: []string{"bun", "run", "mcp-server.ts"}}
	procs.env[301] = map[string]string{"AGENTDECK_INSTANCE_ID": "inst-1"}
	procs.cmds[302] = procowner.ProcCommand{PPID: 300, Args: []string{"vim", "mcp.json"}}
	procs.env[302] = map[string]string{"AGENTDECK_INSTANCE_ID": "inst-1"}
	f := &fakeSettle{procs: procs, now: time.Unix(0, 0)}
	f.set(readyPane)
	deps := f.deps()
	deps.SessionPanePIDs = func() ([]int, error) { return []int{100, 300}, nil }
	report, _ := SettleSwitch(deps, settleOpts(cfgDir))
	if !report.OK() || len(report.StalePollers) != 0 {
		t.Fatalf("stale=%v receipt=%s", report.StalePollers, report.Receipt())
	}
}

// One torn frame while the cursor moves is read again, not a stop.
func TestSettleSwitch_TornFrameDuringMoveIsRetried(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(trustPane(settleProject))
	f.onKey = func(f *fakeSettle, key string) {
		switch key {
		case "Down":
			f.screens = []string{"Accessing workspa", trustScreen(settleProject, 1)}
		case "Enter":
			f.screens = []string{readyPane}
		}
	}
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || !report.OK() || strings.Join(f.keys, ",") != "Down,Enter" {
		t.Fatalf("err=%v keys=%v receipt=%s", err, f.keys, report.Receipt())
	}
}

// A Claude worker started inside the session on another config dir is not
// the session's own Claude.
func TestSettleSwitch_ClaudeWorkerChildDoesNotFailVerification(t *testing.T) {
	cfgDir := t.TempDir()
	procs := liveProcs("inst-1", cfgDir)
	procs.cmds[110] = procowner.ProcCommand{PPID: 101, Args: []string{"claude", "-p", "x"}}
	procs.env[110] = map[string]string{"AGENTDECK_INSTANCE_ID": "inst-1", "CLAUDE_CONFIG_DIR": t.TempDir()}
	f := &fakeSettle{procs: procs, now: time.Unix(0, 0)}
	f.set(readyPane)
	report, _ := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if !report.AccountOK {
		t.Fatalf("receipt=%s", report.Receipt())
	}
}

// The default account is seeded in ~/.claude.json, and no stub
// ~/.claude/.claude.json is created (other readers take it as login state).
func TestPreseedClaudeFirstStart_DefaultAccountSeedsHomeClaudeJSONWithoutStub(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PreseedClaudeFirstStart("", filepath.Join(home, ".claude"), "/work/alpha", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil || !strings.Contains(string(data), `"/work/alpha"`) {
		t.Fatalf("~/.claude.json not seeded: %v %s", err, data)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".claude.json")); err == nil {
		t.Fatal("stub ~/.claude/.claude.json created")
	}
}

// Claude repaints the closed dialog for longer than the grace period after
// Enter; settle waits for it to close instead of answering it again.
func TestSettleSwitch_SlowRepaintAfterEnterIsNotAnsweredAgain(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(trustScreen(settleProject, 1))
	f.onKey = func(f *fakeSettle, key string) {
		if key == "Enter" {
			stale := make([]string, 0, 41)
			for i := 0; i < 40; i++ {
				stale = append(stale, trustScreen(settleProject, 1))
			}
			f.screens = append(stale, readyPane)
		}
	}
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || strings.Join(f.keys, ",") != "Enter" {
		t.Fatalf("err=%v keys=%v receipt=%s", err, f.keys, report.Receipt())
	}
}

// ASK-73: tool-permission prompts, laid out as Claude draws them, whose
// content (the command, the diff) carries a first-start screen's words.
// None of them may be answered: settle stops with nothing pressed.
var permissionPanesWithScreenWords = map[string]string{
	"bash prompt printing the theme picker": strings.Join([]string{
		"╭──────────────────────────────────────────────────────────────────────╮",
		"│ Bash command                                                         │",
		"│                                                                      │",
		"│   printf 'Choose the text style that looks best with your terminal\\n'  │",
		"│   Show the theme picker text                                         │",
		"│                                                                      │",
		"│ Do you want to proceed?                                              │",
		"│ ❯ 1. Yes                                                             │",
		"│   2. Yes, and don't ask again for printf commands in /work/alpha     │",
		"│   3. No, and tell Claude what to do differently (esc)                │",
		"╰──────────────────────────────────────────────────────────────────────╯",
	}, "\n"),
	"bash heredoc whose lines are the whole theme picker": strings.Join([]string{
		" Bash command",
		"",
		"   cat <<'EOF'",
		"Choose the text style that looks best with your terminal",
		"",
		"❯ 1. Dark mode ✔",
		"  2. Light mode",
		"EOF",
		"",
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. No, and tell Claude what to do differently (esc)",
	}, "\n"),
	"edit prompt adding the onboarding notes": strings.Join([]string{
		" Edit file",
		" notes.md",
		" ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
		"  1 +Security notes:",
		"  2 +",
		"  3 +1. Claude can make mistakes",
		"  4 +Press Enter to continue…",
		" ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
		"",
		" Do you want to make this edit to notes.md?",
		" ❯ 1. Yes",
		"   2. Yes, allow all edits during this session (shift+tab)",
		"   3. No, and tell Claude what to do differently (esc)",
	}, "\n"),
	"onboarding words in a prompt without a known marker": strings.Join([]string{
		"Security notes:",
		"Press Enter to continue",
		"",
		"Approve this tool call?",
		"❯ 1. Approve",
		"  2. Deny",
	}, "\n"),
	"theme title over a foreign option block": strings.Join([]string{
		"Choose the text style that looks best with your terminal",
		"",
		"❯ 1. Approve",
		"  2. Deny",
	}, "\n"),
	"mcp approval quoting the bypass warning": strings.Join([]string{
		" New MCP server found in .mcp.json: demo",
		" WARNING: Claude Code running in Bypass Permissions mode",
		"",
		" ❯ 1. No, exit",
		"   2. Yes, I accept",
		"   3. Use this and all future MCP servers in this project",
		"",
		" Allow this MCP server? Enter to confirm · Esc to reject",
	}, "\n"),
	"bash prompt quoting the trust dialog for the own path": strings.Join([]string{
		" Bash command",
		"",
		"   echo 'Accessing workspace:'",
		"Accessing workspace:",
		"",
		settleProject,
		"",
		"Quick safety check: Is this a project you created or one you trust?",
		"",
		"❯ No, exit",
		"  Yes, I trust this folder",
		"",
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. No, and tell Claude what to do differently (esc)",
	}, "\n"),
}

func TestClassifySettlePane_PermissionPromptsNeverLookLikeFirstStartScreens(t *testing.T) {
	for name, pane := range permissionPanesWithScreenWords {
		t.Run(name, func(t *testing.T) {
			got := ClassifySettlePane(pane, []string{settleProject})
			switch got.State {
			case SettlePanePermission, SettlePaneUnknownMenu:
			default:
				t.Fatalf("state = %s (%s); a permission prompt must be permission-prompt or unknown-prompt", got.State, got.Detail)
			}
		})
	}
}

func TestSettleSwitch_PermissionPromptWithScreenWordsPressesNothing(t *testing.T) {
	for name, pane := range permissionPanesWithScreenWords {
		t.Run(name, func(t *testing.T) {
			cfgDir := t.TempDir()
			f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
			f.set(pane)
			opts := settleOpts(cfgDir)
			opts.Bypass = true
			report, err := SettleSwitch(f.deps(), opts)
			if !errors.Is(err, ErrSettleStopped) {
				t.Fatalf("err = %v, receipt %s", err, report.Receipt())
			}
			if len(f.keys) != 0 || len(f.kicks) != 0 {
				t.Fatalf("pressed %v, kicked %d on a permission prompt", f.keys, len(f.kicks))
			}
		})
	}
}

// Enter on the theme picker only when a fresh capture still shows it: a
// screen that changed into a permission prompt in between gets no key.
func TestSettleSwitch_ThemeChangedBeforeEnterPressesNothing(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(themePane, permissionPanesWithScreenWords["bash prompt printing the theme picker"])
	_, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if !errors.Is(err, ErrSettleStopped) || len(f.keys) != 0 {
		t.Fatalf("err=%v keys=%v", err, f.keys)
	}
}

// The same words on foreign layouts with no permission marker at all: each
// layout check holds on its own (title at line start, exact option block).
var foreignLayoutsWithScreenWords = map[string]string{
	"theme title not at the start of its line":              "Run: Choose the text style that looks best with your terminal\n\n❯ 1. Dark mode\n  2. Light mode\n",
	"bypass title not at the start of its line":             "Note: WARNING: Claude Code running in Bypass Permissions mode\n\n❯ 1. No, exit\n  2. Yes, I accept\n\nEnter to confirm\n",
	"bypass dialog with a third row":                        "WARNING: Claude Code running in Bypass Permissions mode\n\n❯ 1. No, exit\n  2. Yes, I accept\n  3. Accept for this project\n\nEnter to confirm\n",
	"onboarding notes with a menu before the continue line": "Security notes:\n\n❯ 1. Approve\n  2. Deny\n\nPress Enter to continue\n",
	"trust dialog with a third row":                         strings.Replace(trustScreen(settleProject, 0), "   Yes, I trust this folder", "   Yes, I trust this folder\n   Trust every folder under it", 1),
}

func TestSettleSwitch_ForeignLayoutWithScreenWordsPressesNothing(t *testing.T) {
	for name, pane := range foreignLayoutsWithScreenWords {
		t.Run(name, func(t *testing.T) {
			got := ClassifySettlePane(pane, []string{settleProject})
			switch got.State {
			case SettlePaneTheme, SettlePaneBypass, SettlePaneOnboarding, SettlePaneTrustOwn, SettlePaneReady:
				t.Fatalf("state = %s (%s) on a foreign layout", got.State, got.Detail)
			}
			cfgDir := t.TempDir()
			f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
			f.set(pane)
			opts := settleOpts(cfgDir)
			opts.Bypass = true
			if _, err := SettleSwitch(f.deps(), opts); !errors.Is(err, ErrSettleStopped) || len(f.keys) != 0 {
				t.Fatalf("err=%v keys=%v", err, f.keys)
			}
		})
	}
}

// #2893: under load a capture can land while Claude is still drawing the
// trust dialog (title up, only the first row drawn). That frame is not the
// whole dialog, so it is not answered, but settle must look again rather
// than stop for good: the next frames show the full dialog.
func TestSettleSwitch_HalfDrawnTrustPromptIsSampledAgain(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	full := trustPane(settleProject)
	half := full[:strings.Index(full, " ❯ No, exit")+len(" ❯ No, exit")]
	if got := ClassifySettlePane(half, SettleProjectPaths(settleProject)); got.State != SettlePaneTrustOther {
		t.Fatalf("half-drawn frame = %s (%s), want it refused", got.State, got.Detail)
	}
	f.set(half, half, half, full)
	f.onKey = trustDialogKeys(settleProject, readyPane)
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil || !report.OK() || strings.Join(f.keys, ",") != "Down,Enter" {
		t.Fatalf("err=%v keys=%v receipt=%s", err, f.keys, report.Receipt())
	}
}

// The re-sampling is bounded: an own-path trust block that stays beside
// another menu still stops settle, with nothing pressed.
func TestSettleSwitch_TrustPromptNeverAloneStillStops(t *testing.T) {
	cfgDir := t.TempDir()
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(decoyPanes["own-path trust block above the MCP dialog"])
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if !errors.Is(err, ErrSettleStopped) || len(f.keys) != 0 || len(f.kicks) != 0 {
		t.Fatalf("err=%v keys=%v kicks=%d receipt=%s", err, f.keys, len(f.kicks), report.Receipt())
	}
	if !strings.Contains(report.StoppedAt, "not the only menu on screen") {
		t.Fatalf("stopped at %q", report.StoppedAt)
	}
	if elapsed := f.now.Sub(time.Unix(0, 0)); elapsed > 10*time.Second {
		t.Fatalf("took %s of fake time to stop; the re-sample must be bounded", elapsed)
	}
}
