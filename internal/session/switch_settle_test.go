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

func trustPane(path string) string {
	return strings.Join([]string{
		"╭──────────────────────────────────────────────────────────╮",
		"│ Accessing workspace:                                     │",
		"│                                                          │",
		"│ " + path + " │",
		"│                                                          │",
		"│ Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first. │",
		"│                                                          │",
		"│ ❯ 1. Yes, I trust this folder                            │",
		"│   2. No, exit                                            │",
		"╰──────────────────────────────────────────────────────────╯",
		"  Enter to confirm · Esc to cancel",
	}, "\n")
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
		{"old trust wording", "Do you trust the files in this folder?\n\n" + settleProject + "\n\n❯ 1. Yes, proceed\n  2. No, exit\n", SettlePaneTrustOwn},
		{"wrapped path", "Accessing workspace:\n\n/work/al\npha\n\nQuick safety check: Is this a project you created or one you trust?\n❯ 1. Yes, I trust this folder\n  2. No, exit\n", SettlePaneTrustOwn},
		{"question wrapped by a narrow pane", "│ " + settleProject + " │\n│ Quick safety check: Is this a project you created │\n│ or one you trust? (Like your own code) │\n│ ❯ 1. Yes, I trust this folder │\n│   2. No, exit │\n", SettlePaneTrustOwn},
		{"theme", themePane, SettlePaneTheme},
		{"bypass", bypassPane(false), SettlePaneBypass},
		{"onboarding security notes", "Security notes:\n\n1. Claude can make mistakes\n\nPress Enter to continue…\n", SettlePaneOnboarding},
		{"login", "Select login method:\n\n❯ 1. Claude account with subscription\n", SettlePaneLoginNeeded},
		{"unknown menu", "Resume a previous conversation?\n\n❯ 1. Resume\n  2. Start fresh\n\nEnter to select\n", SettlePaneUnknownMenu},
		{"ready composer", readyPane, SettlePaneReady},
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
	f.onKey = func(f *fakeSettle, key string) {
		if key == "Enter" {
			f.screens = []string{readyPane}
		}
	}
	report, err := SettleSwitch(f.deps(), settleOpts(cfgDir))
	if err != nil {
		t.Fatalf("settle: %v (%s)", err, report.Receipt())
	}
	if strings.Join(f.keys, ",") != "Enter" {
		t.Fatalf("keys = %v, want exactly one Enter", f.keys)
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
	f.onKey = func(f *fakeSettle, key string) {
		if len(f.keys) == 1 {
			f.screens = []string{trustPane(settleProject)}
		} else {
			f.screens = []string{readyPane}
		}
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
	procs.cmds[90] = procowner.ProcCommand{PPID: 1, Args: []string{"bun", "run", "telegram/server.ts"}}
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
