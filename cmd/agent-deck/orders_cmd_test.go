package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/orders"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// Standing orders, end to end through the real binary: a turn that an
// order started (the hook handler saw a "[ORDER ...]" prompt) may not add or
// change orders, and a permission prompt in it is denied with a NEED for the
// owner. After the turn's Stop the owner's next turn may edit orders again.
func TestOrders_OrderTurnCannotChangeOrdersAndPromptIsDenied(t *testing.T) {
	home := t.TempDir()
	runConductorSetupCLI(t, home, "ops")
	env := []string{"AGENTDECK_INSTANCE_ID=0a1b2c3d-1700000000", "AGENTDECK_TITLE=conductor-ops"}
	run := func(stdin string, args ...string) (string, string, int) {
		t.Helper()
		return runAgentDeckEnv(t, home, stdin, env, args...)
	}
	hook := func(payload string) string {
		t.Helper()
		out, errOut, code := run(payload, "hook-handler")
		if code != 0 {
			t.Fatalf("hook-handler exit %d: %s", code, errOut)
		}
		return out
	}

	if _, errOut, code := run("", "orders", "add", "watch-ci", "--conductor", "ops", "--check", "echo green"); code != 0 {
		t.Fatalf("attended add: exit %d: %s", code, errOut)
	}

	hook(`{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"[ORDER watch-ci] its check changed.\nWas: green\nNow: red"}`)
	for _, args := range [][]string{
		{"orders", "pause", "watch-ci", "--conductor", "ops"},
		{"orders", "rm", "watch-ci", "--conductor", "ops"},
		{"orders", "add", "more", "--conductor", "ops", "--check", "true"},
	} {
		_, errOut, code := run("", args...)
		if code != 2 || !strings.Contains(errOut, "order-started turn") {
			t.Fatalf("%v from an order turn: exit %d stderr %q, want exit 2 refusal", args, code, errOut)
		}
	}
	denied := hook(`{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"gh pr merge 12"}}`)
	if !strings.Contains(denied, `"behavior":"deny"`) || !strings.Contains(denied, "order:watch-ci") {
		t.Fatalf("permission prompt in an order turn must be denied, hook printed %q", denied)
	}
	outbox, errOut, code := run("", "conductor", "outbox", "--conductor", "ops")
	if code != 0 || !strings.Contains(outbox, "NEED:") || !strings.Contains(outbox, "gh pr merge 12") {
		t.Fatalf("owner must get a NEED for the denied prompt: exit %d out %q err %q", code, outbox, errOut)
	}

	hook(`{"hook_event_name":"Stop","session_id":"s1"}`)
	if _, errOut, code := run("", "orders", "pause", "watch-ci", "--conductor", "ops"); code != 0 {
		t.Fatalf("after the order turn ended, pause must work: exit %d: %s", code, errOut)
	}
	if out := hook(`{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`); strings.Contains(out, `"deny"`) {
		t.Fatalf("an attended turn's prompt must not be denied: %q", out)
	}

	// A heartbeat turn may still edit orders (only order turns are refused).
	hook(`{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"[HEARTBEAT] [ops] Status: 1 waiting."}`)
	if _, errOut, code := run("", "orders", "resume", "watch-ci", "--conductor", "ops"); code != 0 {
		t.Fatalf("heartbeat turn resume: exit %d: %s", code, errOut)
	}
	list, _, _ := run("", "orders", "list", "--conductor", "ops")
	if !strings.Contains(list, "watch-ci") || !strings.Contains(list, "active") {
		t.Fatalf("list: %q", list)
	}
}

// The tick, in process with a fake check and a recording sender: silent
// baseline, silent repeat, one wake on change, addressed to the conductor
// session and tagged with the timer's trigger source.
func TestOrdersTick_WakesConductorOnlyOnChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: "ops", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	dir, err := session.ConductorNameDir("ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, orders.FileName), []byte("## watch-ci\nevery: 1m\ncheck: ci-state\ndo: fix it\nask_first: merge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	value := "green"
	type sent struct{ profile, target, source, text string }
	var sends []sent
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.Local)
	oldRun, oldSend, oldNow := ordersRun, ordersSend, ordersNow
	t.Cleanup(func() { ordersRun, ordersSend, ordersNow = oldRun, oldSend, oldNow })
	ordersRun = func(ctx context.Context, command, dir string) string { return value }
	ordersSend = func(profile, target, source, text string) (orders.SendResult, error) {
		sends = append(sends, sent{profile, target, source, text})
		return orders.SendResult{Delivered: true}, nil
	}
	ordersNow = func() time.Time { return now }

	tick := func() string {
		t.Helper()
		var buf bytes.Buffer
		if err := runOrders(&buf, "default", []string{"tick", "--all"}); err != nil {
			t.Fatalf("tick: %v", err)
		}
		now = now.Add(time.Minute)
		return strings.TrimSpace(buf.String())
	}
	if got := tick(); got != "ops/watch-ci: baseline" || len(sends) != 0 {
		t.Fatalf("tick 1: %q sends=%d", got, len(sends))
	}
	if got := tick(); got != "ops/watch-ci: silent" || len(sends) != 0 {
		t.Fatalf("tick 2: %q sends=%d", got, len(sends))
	}
	value = "red"
	if got := tick(); !strings.HasPrefix(got, "ops/watch-ci: woke") || len(sends) != 1 {
		t.Fatalf("tick 3: %q sends=%d", got, len(sends))
	}
	s := sends[0]
	if s.profile != "default" || s.target != "conductor-ops" || s.source != session.OrdersTickSource ||
		!strings.HasPrefix(s.text, "[ORDER watch-ci] its check changed.") || !strings.Contains(s.text, "before: merge.") {
		t.Fatalf("send: %+v", s)
	}
	if got := session.TurnOriginForPrompt(s.text); got != "order:watch-ci" {
		t.Fatalf("the wake text must be recognised as an order turn by the hook, got %q", got)
	}
}

func TestParseOrderSendJSON_MutedIsNotDelivered(t *testing.T) {
	for in, want := range map[string]bool{
		`{"success":true,"muted":true}`:   false,
		`{"success":true,"skipped":true}`: false,
		`{"success":true}`:                true,
		``:                                true,
	} {
		if got := parseOrderSendJSON([]byte(in)).Delivered; got != want {
			t.Errorf("%q: delivered=%v want %v", in, got, want)
		}
	}
}

// `agent-deck triggers` must list the host's orders timer, and the wakes it
// sends (tagged --source orders from inside the unit) must land on the same
// row.
func TestOrdersTimerIsListedByTriggers(t *testing.T) {
	dir := t.TempDir()
	svc, err := session.GenerateSystemdOrdersService()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session.SystemdOrdersServiceName()), []byte(svc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session.SystemdOrdersTimerName()), []byte(session.GenerateSystemdOrdersTimer()), 0o644); err != nil {
		t.Fatal(err)
	}
	found := triggers.Scan(triggers.ScanDirs{SystemdUser: dir})
	if len(found) != 1 || found[0].ID() != "systemd:agent-deck-orders" || !strings.Contains(found[0].Schedule, "every 60s") {
		t.Fatalf("scan: %+v", found)
	}
	cgroup := "0::/user.slice/user-1000.slice/user@1000.service/app.slice/agent-deck-orders.service\n"
	env := triggers.Env{
		Getenv: func(k string) string {
			if k == "INVOCATION_ID" {
				return "x"
			}
			return ""
		},
		ReadFile: func(string) ([]byte, error) { return []byte(cgroup), nil },
	}
	if got := triggers.Resolve(session.OrdersTickSource, "", env).ID(); got != found[0].ID() {
		t.Fatalf("a wake sent from the unit keys %s, the scanned timer %s: two rows", got, found[0].ID())
	}
}

// Audit defect 13: a heartbeat turn that hits a permission prompt waited
// forever (nobody is there to answer). It is now denied at once and the
// owner gets an urgent NEED; the owner's own turn is untouched.
func TestHookHandler_HeartbeatTurnPermissionPromptIsDenied(t *testing.T) {
	home := t.TempDir()
	runConductorSetupCLI(t, home, "ops")
	env := []string{"AGENTDECK_INSTANCE_ID=0a1b2c3d-1700000001", "AGENTDECK_TITLE=conductor-ops"}
	hook := func(payload string) string {
		t.Helper()
		out, errOut, code := runAgentDeckEnv(t, home, payload, env, "hook-handler")
		if code != 0 {
			t.Fatalf("hook-handler exit %d: %s", code, errOut)
		}
		return out
	}
	hook(`{"hook_event_name":"UserPromptSubmit","session_id":"s2","prompt":"[HEARTBEAT] [ops] Status: 1 waiting, 0 running."}`)
	out := hook(`{"hook_event_name":"PermissionRequest","session_id":"s2","tool_name":"Bash","tool_input":{"command":"git push origin main"}}`)
	if !strings.Contains(out, `"behavior":"deny"`) || !strings.Contains(out, "NEED:") {
		t.Fatalf("permission prompt in a heartbeat turn must be denied with a NEED hint, hook printed %q", out)
	}
	outbox, errOut, code := runAgentDeckEnv(t, home, "", env, "conductor", "outbox", "--conductor", "ops")
	if code != 0 || !strings.Contains(outbox, "NEED:") || !strings.Contains(outbox, "git push origin main") {
		t.Fatalf("owner must get a NEED: exit %d out %q err %q", code, outbox, errOut)
	}
	hook(`{"hook_event_name":"UserPromptSubmit","session_id":"s2","prompt":"owner: please push it"}`)
	if out := hook(`{"hook_event_name":"PermissionRequest","session_id":"s2","tool_name":"Bash","tool_input":{"command":"git push origin main"}}`); strings.Contains(out, `"deny"`) {
		t.Fatalf("the owner's own turn must not be denied: %q", out)
	}
}

// A pi launched from a heartbeat turn (a Bash tool call) inherits the
// conductor's AGENTDECK_INSTANCE_ID, so its extension's session_start reaches
// the same hook handler. It is pi's, not a Claude SessionStart: the
// heartbeat turn stays unattended and its permission prompt is still denied.
func TestHookHandler_NestedPiSessionStartKeepsTheTurnUnattended(t *testing.T) {
	home := t.TempDir()
	runConductorSetupCLI(t, home, "ops")
	env := []string{"AGENTDECK_INSTANCE_ID=0a1b2c3d-1700000002", "AGENTDECK_TITLE=conductor-ops"}
	hook := func(payload string) string {
		t.Helper()
		out, errOut, code := runAgentDeckEnv(t, home, payload, env, "hook-handler")
		if code != 0 {
			t.Fatalf("hook-handler exit %d: %s", code, errOut)
		}
		return out
	}
	hook(`{"hook_event_name":"UserPromptSubmit","session_id":"s3","prompt":"[HEARTBEAT] [ops] Status: 1 waiting, 0 running."}`)
	hook(`{"hook_event_name":"session_start","source":"pi"}`)
	out := hook(`{"hook_event_name":"PermissionRequest","session_id":"s3","tool_name":"Bash","tool_input":{"command":"gh pr merge 7"}}`)
	if !strings.Contains(out, `"behavior":"deny"`) {
		t.Fatalf("a nested pi's session_start cleared the heartbeat turn's origin; the permission prompt was not denied: %q", out)
	}
	// A Claude SessionStart still starts with no unattended turn.
	hook(`{"hook_event_name":"SessionStart","session_id":"s3","source":"startup"}`)
	if out := hook(`{"hook_event_name":"PermissionRequest","session_id":"s3","tool_name":"Bash","tool_input":{"command":"ls"}}`); strings.Contains(out, `"deny"`) {
		t.Fatalf("after a Claude SessionStart the prompt must not be denied: %q", out)
	}
}
