package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// SWFB1018: `session set <s> account <a>` (the self-switch path) must seed
// the new account's folder trust for the session's own project, or the
// restart that follows sits at "Quick safety check".
func TestSessionSetAccount_SeedsTargetFolderTrust(t *testing.T) {
	home := intendedSwitchHome(t)
	proj := filepath.Join(home, "proj")
	id := addTestSession(t, home, proj, "worker")
	runSetOK(t, home, id, "account", "personal")
	runSetOK(t, home, id, "account", "work")

	data, err := os.ReadFile(filepath.Join(home, ".claude-work", ".claude.json"))
	if err != nil {
		t.Fatalf("target account has no .claude.json after the switch: %v", err)
	}
	var cfg struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Projects[proj]["hasTrustDialogAccepted"] != true {
		t.Fatalf("trust for %s not seeded in the target account:\n%s", proj, data)
	}
}

func TestSessionSwitchSettle_RefusesWrongAccountAndUnknownSession(t *testing.T) {
	home := intendedSwitchHome(t)
	id := addTestSession(t, home, filepath.Join(home, "proj"), "worker")
	runSetOK(t, home, id, "account", "personal")

	stdout, stderr, code := runAgentDeck(t, home, "session", "switch-settle", id, "--account", "work", "--no-kick")
	if code == 0 || !strings.Contains(stdout+stderr, `not "work"`) {
		t.Fatalf("wrong account not refused (exit %d):\n%s%s", code, stdout, stderr)
	}
	if _, _, code := runAgentDeck(t, home, "session", "switch-settle", "no-such-session"); code != 2 {
		t.Fatalf("unknown session exit = %d, want 2", code)
	}
	stdout, _, code = runAgentDeck(t, home, "session", "switch-settle", "--help")
	if code != 0 || !strings.Contains(stdout, "folder trust for the") || !strings.Contains(stdout, "nothing pressed") {
		t.Fatalf("help (exit %d) does not describe the prompt contract:\n%s", code, stdout)
	}
}

func TestSettleChannelPlugins(t *testing.T) {
	inst := &session.Instance{Channels: []string{"plugin:telegram@claude-plugins-official", "plugin:discord@x", "weird"}}
	if got := strings.Join(settleChannelPlugins(inst), ","); got != "telegram,discord" {
		t.Fatalf("got %q", got)
	}
}

func TestSettleExitCode(t *testing.T) {
	healthy := &session.SettleReport{Ready: true, AccountOK: true, Kick: "sent"}
	if c := settleExitCode(healthy, nil); c != 0 {
		t.Fatalf("healthy = %d", c)
	}
	if c := settleExitCode(&session.SettleReport{StoppedAt: "x"}, session.ErrSettleStopped); c != settleExitStopped {
		t.Fatalf("stopped = %d", c)
	}
	if c := settleExitCode(&session.SettleReport{Ready: true, Kick: "sent"}, nil); c != settleExitFailed {
		t.Fatalf("unverified = %d", c)
	}
	if c := settleExitCode(nil, errors.New("x")); c != settleExitFailed {
		t.Fatalf("nil = %d", c)
	}
}

// settleFakeClaudeScript is a stand-in Claude on a real tmux pane. Mode "trust"
// draws the folder-trust dialog for $1 and waits for one key; mode
// "closed" draws a ready composer with a CONNECTION_CLOSED plugin the first
// time and a clean one after a restart. Every key it reads is logged.
const settleFakeClaudeScript = `#!/bin/bash
mode=$1; shown=$2; log=$3; launches=$4
n=$(( $(cat "$launches" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$launches"
composer() { printf '\n● resumed\n\n────────────────────────────────────────\n❯ \n────────────────────────────────────────\n  ? for shortcuts\n'; }
if [ "$mode" = trust ]; then
  printf ' Accessing workspace:\n\n %s\n\n Quick safety check: Is this a project you created or one\n you trust? (Like your own code, a well-known open source project)\n\n ❯ 1. Yes, I trust this folder\n   2. No, exit\n\n Enter to confirm · Esc to cancel\n' "$shown"
  IFS= read -r key; echo "key:[$key]" >> "$log"
  clear; composer
elif [ "$mode" = closed ] && [ "$n" = 1 ]; then
  composer; printf '  plugin:telegram:telegram (CONNECTION_CLOSED) · /mcp\n'
else
  composer
fi
while IFS= read -r key; do echo "key:[$key]" >> "$log"; done
`

type settleFakePane struct {
	sess     *tmux.Session
	dir      string
	keysLog  string
	launches string
	command  string
}

func startSettleFakePane(t *testing.T, name, mode, shownPath string) *settleFakePane {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(script, []byte(settleFakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &settleFakePane{dir: dir, keysLog: filepath.Join(dir, "keys.log"), launches: filepath.Join(dir, "launches")}
	p.command = fmt.Sprintf("bash %s %s %s %s %s", script, mode, shownPath, p.keysLog, p.launches)
	p.sess = tmux.NewSession(name, dir)
	if err := p.sess.Start(p.command); err != nil {
		t.Fatalf("start fake claude: %v", err)
	}
	t.Cleanup(func() { _ = p.sess.Kill() })
	return p
}

func (p *settleFakePane) keys() string {
	data, _ := os.ReadFile(p.keysLog)
	return strings.TrimSpace(string(data))
}

func (p *settleFakePane) deps(t *testing.T, kicks *[]string) session.SettleDeps {
	return session.SettleDeps{
		Capture: p.sess.CapturePaneFresh,
		SendKey: p.sess.SendNamedKey,
		Restart: func() error {
			if err := p.sess.Kill(); err != nil {
				return err
			}
			return p.sess.Start(p.command)
		},
		PanePID: p.sess.PanePID,
		Kick:    func(m string) error { *kicks = append(*kicks, m); return nil },
	}
}

func fakePaneOpts(project string) session.SettleOptions {
	return session.SettleOptions{InstanceID: "e2e", Title: "alpha", Account: "work", ProjectPath: project,
		Kick: session.DefaultSettleKick("work"), ReadyTimeout: 20 * time.Second, PollEvery: 100 * time.Millisecond}
}

// End to end on a real pane: the trust dialog for the session's own project
// is answered with exactly one Enter, then the kick goes out.
func TestSwitchSettle_FakeClaudePane_OwnTrustPromptAnswered(t *testing.T) {
	project := t.TempDir()
	p := startSettleFakePane(t, "swfb-trust-own", "trust", project)
	var kicks []string
	report, err := session.SettleSwitch(p.deps(t, &kicks), fakePaneOpts(project))
	if err != nil {
		pane, _ := p.sess.CapturePaneFresh()
		t.Fatalf("settle: %v\nreceipt: %s\npane:\n%s", err, report.Receipt(), pane)
	}
	if !report.Ready || strings.Join(report.Answered, ",") != "trust-own-path" {
		t.Fatalf("receipt: %s", report.Receipt())
	}
	if got := p.keys(); got != "key:[]" {
		t.Fatalf("keys read by the fake claude = %q, want one Enter", got)
	}
	if len(kicks) != 1 {
		t.Fatalf("kicks = %d", len(kicks))
	}
}

// A trust dialog for any other path is never answered.
func TestSwitchSettle_FakeClaudePane_ForeignTrustPromptUntouched(t *testing.T) {
	project := t.TempDir()
	p := startSettleFakePane(t, "swfb-trust-other", "trust", filepath.Dir(project))
	var kicks []string
	report, err := session.SettleSwitch(p.deps(t, &kicks), fakePaneOpts(project))
	if !errors.Is(err, session.ErrSettleStopped) {
		t.Fatalf("err = %v, receipt %s", err, report.Receipt())
	}
	time.Sleep(300 * time.Millisecond)
	if got := p.keys(); got != "" || len(kicks) != 0 {
		t.Fatalf("pressed %q / kicked %d on a foreign trust dialog", got, len(kicks))
	}
}

// A CONNECTION_CLOSED plugin on first start is retried once by restart.
func TestSwitchSettle_FakeClaudePane_ConnectionClosedRetriedOnce(t *testing.T) {
	project := t.TempDir()
	p := startSettleFakePane(t, "swfb-closed", "closed", project)
	var kicks []string
	report, err := session.SettleSwitch(p.deps(t, &kicks), fakePaneOpts(project))
	if err != nil {
		t.Fatalf("settle: %v (%s)", err, report.Receipt())
	}
	launches, _ := os.ReadFile(p.launches)
	if strings.TrimSpace(string(launches)) != "2" || !report.PluginRetried || len(report.PluginFailures) != 0 {
		pane, _ := p.sess.CapturePaneFresh()
		t.Fatalf("launches=%q receipt=%s\npane:\n%s", launches, report.Receipt(), pane)
	}
}
