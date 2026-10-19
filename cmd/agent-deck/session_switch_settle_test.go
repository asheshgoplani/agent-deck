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
trust() { # $1 = cursor row: 0 "No, exit" (the default), 1 "Yes"
  clear
  printf '%s\n Accessing workspace:\n\n %s\n\n Quick safety check: Is this a project you created or one\n you trust? (Like your own code, a well-known open source project)\n\n' "────────────────────────────────────────" "$shown"
  if [ "$1" = 0 ]; then printf ' ❯ No, exit\n   Yes, I trust this folder\n'; else printf '   No, exit\n ❯ Yes, I trust this folder\n'; fi
  printf '\n Enter to confirm · Esc to cancel\n'
}
readkey() { IFS= read -rsn1 k; if [ "$k" = $'\e' ]; then IFS= read -rsn2 rest; k="ESC$rest"; fi; echo "key:[$k]" >> "$log"; }
if [ "$mode" = trust ]; then
  cur=0; trust $cur
  while readkey; do
    case "$k" in
      "ESC[B") cur=1; trust $cur ;;
      "ESC[A") cur=0; trust $cur ;;
      "") [ $cur = 1 ] && break; echo "exited: No, exit chosen"; exit 1 ;;
    esac
  done
  clear; composer
elif [ "$mode" = permission ]; then
  # A tool-permission prompt whose command prints the theme picker text.
  printf ' Bash command\n\n   cat <<EOF\nChoose the text style that looks best with your terminal\n\n❯ 1. Dark mode ✔\n  2. Light mode\nSecurity notes:\nPress Enter to continue\nEOF\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)\n'
elif [ "$mode" = decoy ]; then
  # Earlier tool output holding the theme picker block, above a live
  # project MCP-server dialog that carries no permission marker.
  printf '⏺ Bash(cat notes.txt)\n  ⎿  hello\n     Choose the text style that looks best with your terminal\n     ❯ 1. Dark mode\n       2. Light mode\n\n New MCP server found in .mcp.json: demo\n\n ❯ 1. Use this and all future MCP servers in this project\n   2. Use this MCP server\n   3. Continue without using this MCP server\n\n   Enter to confirm · Esc to reject\n'
elif [ "$mode" = closed ] && [ "$n" = 1 ]; then
  composer; printf '  plugin:telegram:telegram (CONNECTION_CLOSED) · /mcp\n'
else
  composer
fi
while readkey; do :; done
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
	// The production tmux wiring, so the window targeting is under test.
	deps := settleTmuxDeps(func() *tmux.Session { return p.sess })
	deps.Restart = func() error {
		if err := p.sess.Kill(); err != nil {
			return err
		}
		return p.sess.Start(p.command)
	}
	deps.Kick = func(m string) error { *kicks = append(*kicks, m); return nil }
	return deps
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
	// The cursor starts on "No, exit": one Down, then one Enter on Yes.
	if got := p.keys(); got != "key:[ESC[B]\nkey:[]" {
		t.Fatalf("keys read by the fake claude = %q, want Down then Enter", got)
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

// A completed switch exits 0 unless --settle-strict asks for the settle's
// verdict; existing callers that test $? keep working.
func TestSwitchSettleExit(t *testing.T) {
	unhealthy := &session.SettleReport{Ready: true, Kick: "sent"}
	if c := switchSettleExit(false, unhealthy, nil); c != 0 {
		t.Fatalf("default = %d, want 0", c)
	}
	if c := switchSettleExit(true, unhealthy, nil); c != switchExitSettle {
		t.Fatalf("strict unhealthy = %d", c)
	}
	if c := switchSettleExit(true, &session.SettleReport{StoppedAt: "x"}, session.ErrSettleStopped); c != settleExitStopped {
		t.Fatalf("strict stopped = %d", c)
	}
	if c := switchSettleExit(true, nil, nil); c != 0 {
		t.Fatalf("no settle = %d", c)
	}
}

func TestParsePIDList(t *testing.T) {
	if got := fmt.Sprint(parsePIDList("12, 13,x,1,0,-4,99")); got != "[12 13 99]" {
		t.Fatalf("got %s", got)
	}
}

// An operator window focused in the session does not divert settle: it
// reads and answers the agent's own window.
func TestSwitchSettle_FakeClaudePane_OtherWindowFocused(t *testing.T) {
	project := t.TempDir()
	p := startSettleFakePane(t, "swfb-two-windows", "trust", project)
	if out, err := exec.Command("tmux", "new-window", "-t", p.sess.Name, "cat").CombinedOutput(); err != nil {
		t.Skipf("new-window: %v %s", err, out)
	}
	var kicks []string
	deps := p.deps(t, &kicks)
	// The pane pid (process checks, verification) is the agent window's too.
	first, err := exec.Command("tmux", "list-panes", "-t", p.sess.Name+":^", "-F", "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	if pid, err := deps.PanePID(); err != nil || fmt.Sprint(pid) != strings.TrimSpace(string(first)) {
		t.Fatalf("pane pid = %d (%v), want the agent window's %s", pid, err, first)
	}
	report, err := session.SettleSwitch(deps, fakePaneOpts(project))
	if err != nil || strings.Join(report.Answered, ",") != "trust-own-path" {
		pane, _ := p.sess.CapturePrimaryPaneFresh()
		t.Fatalf("err=%v receipt=%s\npane:\n%s", err, report.Receipt(), pane)
	}
	if got := p.keys(); got != "key:[ESC[B]\nkey:[]" {
		t.Fatalf("agent window read %q", got)
	}
}

// ASK-73 end to end: a real pane showing a tool-permission prompt whose
// command carries the theme and onboarding words. Settle stops; the pane
// reads no key at all.
func TestSwitchSettle_FakeClaudePane_PermissionPromptWithThemeWordsUntouched(t *testing.T) {
	project := t.TempDir()
	p := startSettleFakePane(t, "swfb-permission", "permission", project)
	var kicks []string
	opts := fakePaneOpts(project)
	opts.Bypass = true
	report, err := session.SettleSwitch(p.deps(t, &kicks), opts)
	if !errors.Is(err, session.ErrSettleStopped) || !strings.Contains(report.StoppedAt, "permission-prompt") {
		pane, _ := p.sess.CapturePrimaryPaneFresh()
		t.Fatalf("err=%v receipt=%s\npane:\n%s", err, report.Receipt(), pane)
	}
	time.Sleep(300 * time.Millisecond)
	if got := p.keys(); got != "" || len(kicks) != 0 {
		t.Fatalf("permission prompt read keys %q / kicked %d", got, len(kicks))
	}
}

// Lander re-review: a theme-picker block in earlier tool output above a
// live MCP-server dialog, on a real pane. The dialog reads no key.
func TestSwitchSettle_FakeClaudePane_DecoyAboveLiveMenuUntouched(t *testing.T) {
	project := t.TempDir()
	p := startSettleFakePane(t, "swfb-decoy", "decoy", project)
	var kicks []string
	report, err := session.SettleSwitch(p.deps(t, &kicks), fakePaneOpts(project))
	if !errors.Is(err, session.ErrSettleStopped) {
		pane, _ := p.sess.CapturePrimaryPaneFresh()
		t.Fatalf("err=%v receipt=%s\npane:\n%s", err, report.Receipt(), pane)
	}
	time.Sleep(300 * time.Millisecond)
	if got := p.keys(); got != "" || len(kicks) != 0 {
		t.Fatalf("live MCP dialog read keys %q / kicked %d", got, len(kicks))
	}
}
