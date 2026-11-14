package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// PR-B commit 3b, the #2789 rig with a pi parent: a node fake (testdata/
// pi_like_fake.mjs) draws a Claude-like composer and hosts the real extension
// against a fake of pi's extension API. Needs tmux, python3 (the child's fake)
// and node >= 22.13. The version file next to the fake `pi` decides whether
// the launch exports the socket (pi >= 1.2.0).

const fakePiWrapper = `#!/bin/sh
here=$(cd "$(dirname "$0")" && pwd -P)
if [ "$1" = "--version" ]; then cat "$here/pi-version"; exit 0; fi
exec node --experimental-strip-types --no-warnings "$here/pi_like_fake.mjs" "$@"
`

type piDeliveryEnv struct {
	*deliveryEnv
	piDir string // PI_CODING_AGENT_DIR
}

// newPiDeliveryEnv is a delivery env whose profile has the ledger on, lists the
// conductor-pi title as a consumer, has the extension installed, and a fake pi
// of the given version first on PATH.
func newPiDeliveryEnv(t *testing.T, piVersion string) *piDeliveryEnv {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("requires node")
	}
	e := &piDeliveryEnv{deliveryEnv: newDeliveryEnv(t)}
	e.piDir = filepath.Join(e.root, "piagent")
	bin := filepath.Join(e.root, "fakebin")
	src, err := os.ReadFile(filepath.Join("testdata", "pi_like_fake.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bin, "pi_like_fake.mjs"), string(src), 0o600)
	writeFile(t, filepath.Join(bin, "pi"), fakePiWrapper, 0o700)
	writeFile(t, filepath.Join(bin, "pi-version"), piVersion+"\n", 0o600)
	if err := os.Symlink(e.bin, filepath.Join(bin, "agent-deck")); err != nil {
		t.Fatal(err)
	}
	e.env = append(e.env,
		"PATH="+bin+string(os.PathListSeparator)+filepath.Dir(nodePath)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PI_CODING_AGENT_DIR="+e.piDir,
	)
	cfg := filepath.Join(e.root, "config", "agent-deck", "config.toml")
	writeFile(t, cfg, fmt.Sprintf("[claude]\ncommand = %q\n\n[comms]\nledger = true\nconsumers = [\"conductor-pi\"]\n", filepath.Join(bin, "claude")), 0o600)
	e.mustRun("pi-hooks", "install")
	return e
}

// startPi adds, starts and waits for a fake pi session.
func (e *piDeliveryEnv) startPi(title string) *fakeTarget {
	e.t.Helper()
	f := &fakeTarget{project: filepath.Join(e.root, "p-"+title)}
	if err := os.MkdirAll(f.project, 0o700); err != nil {
		e.t.Fatal(err)
	}
	var added struct {
		ID string `json:"id"`
	}
	out := e.mustRun("add", "-t", title, "-c", "pi", "--no-parent", "--json", f.project)
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
		e.t.Fatalf("add %s: %v\n%s", title, err, out)
	}
	f.id = added.ID
	e.mustRun("session", "start", f.id, "--json")
	var shown struct {
		TmuxSession string `json:"tmux_session"`
	}
	if err := json.Unmarshal([]byte(e.mustRun("session", "show", f.id, "--json")), &shown); err != nil || shown.TmuxSession == "" {
		e.t.Fatalf("session show %s: %v", title, err)
	}
	f.tmux = shown.TmuxSession
	issue2530WaitFor(e.t, 20*time.Second, "the pi fake to start", func() bool {
		b, err := os.ReadFile(filepath.Join(f.project, ".fake", "transcript"))
		f.transcript = string(b)
		return err == nil && f.transcript != ""
	})
	e.waitPane(f, "the pi composer", func(p string) bool { return strings.Contains(p, "pi is ready") && strings.Contains(p, "❯") })
	return f
}

// piEvents are the fake's own log lines (doorbells, injected context, ui access).
func (f *fakeTarget) piEvents(t *testing.T) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(f.project, ".fake", "events.jsonl"))
	var out []map[string]any
	for _, line := range strings.Split(string(b), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func piEventCount(evs []map[string]any, key string) int {
	n := 0
	for _, m := range evs {
		if _, ok := m[key]; ok {
			n++
		}
	}
	return n
}

func (e *piDeliveryEnv) cursor(f *fakeTarget) string {
	cmd := exec.Command("tmux", "display-message", "-p", "-t", f.tmux, "#{cursor_x} #{cursor_y}")
	cmd.Env = e.env
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func (e *piDeliveryEnv) paneStyled(f *fakeTarget) string {
	cmd := exec.Command("tmux", "capture-pane", "-p", "-e", "-t", f.tmux)
	cmd.Env = e.env
	out, _ := cmd.Output()
	return string(out)
}

// dumpOnFailure prints what the pane, the fake and the parent's inbox hold when
// the test fails: the first thing anyone debugging a missed wake needs.
func (e *piDeliveryEnv) dumpOnFailure(f *fakeTarget) {
	e.t.Cleanup(func() {
		if !e.t.Failed() {
			return
		}
		inbox, _, _ := e.run("", nil, "inbox", "peek", f.id, "--json")
		shown, _, _ := e.run("", nil, "session", "show", f.id, "--json")
		hooks, _ := filepath.Glob(filepath.Join(e.root, "data", "agent-deck", "**", "*"+f.id[:8]+"*"))
		e.t.Logf("fake events: %v\ninbox: %s\nsession: %s\nhook files: %v", f.piEvents(e.t), inbox, shown, hooks)
	})
}

// childFinishes drives a Claude child of the pi parent through one turn and one
// notifier pass that sees it end.
func (e *piDeliveryEnv) childFinishes(parent *fakeTarget, answer string) {
	e.t.Helper()
	child := e.startFake("child", "busy", parent.id)
	e.setPhase(child, "busy")
	e.mustRun("notify-daemon", "--once") // baseline: the child is running
	child.reply(e.t, answer+" the task is done\n===AGENTDECK_DONE=== status=ok summary=shipped")
	e.setPhase(child, "idle")
	e.mustRun("notify-daemon", "--once") // the pass that sees the finished turn and rings the parent
}

// A socket wake never touches the composer: an operator draft with the cursor
// mid-line is byte-identical (screen with escapes, cursor) before and after the
// doorbell, the extension never reaches for the editor or sendUserMessage, the
// doorbell is one custom message, and the draft then submits once, unchanged.
func TestDeliveryE2E_PiSocketWakeLeavesTheForeignDraftByteIdentical(t *testing.T) {
	testutil.RequireTmux(t)
	parallelSubprocessTest(t)
	e := newPiDeliveryEnv(t, "1.3.0")
	parent := e.startPi("conductor-pi")
	e.dumpOnFailure(parent)
	draft := deliveryToken("OPERATOR_DRAFT") + " half written note"
	keys := exec.Command("tmux", "send-keys", "-t", parent.tmux, "-l", draft)
	keys.Env = e.env
	if out, err := keys.CombinedOutput(); err != nil {
		t.Fatalf("type the draft: %v %s", err, out)
	}
	e.waitPane(parent, "the operator draft", func(p string) bool { return strings.Contains(p, "❯ "+draft) })
	before, cursorBefore := e.paneStyled(parent), e.cursor(parent)

	answer := deliveryToken("CHILD_REPLY")
	e.childFinishes(parent, answer)
	issue2530WaitFor(t, 20*time.Second, "the doorbell to reach pi", func() bool { return piEventCount(parent.piEvents(t), "doorbell") > 0 })
	time.Sleep(1500 * time.Millisecond) // a typed line would be on screen by now

	if after := e.paneStyled(parent); after != before {
		t.Fatalf("the wake changed the pane.\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if cursorAfter := e.cursor(parent); cursorAfter != cursorBefore {
		t.Fatalf("the wake moved the cursor from %q to %q", cursorBefore, cursorAfter)
	}
	if got := parent.draft(t); got != draft {
		t.Fatalf("composer holds %q after the wake, want %q", got, draft)
	}
	evs := parent.piEvents(t)
	if n := piEventCount(evs, "doorbell"); n != 1 {
		t.Fatalf("pi saw %d doorbells, want 1: %v", n, evs)
	}
	if n := piEventCount(evs, "ui"); n != 0 {
		t.Fatalf("the extension touched the editor or sendUserMessage: %v", evs)
	}
	if n := parent.received(t, "[agent-deck"); n != 0 {
		t.Fatalf("a wake line was typed into the composer %d time(s)", n)
	}
	enter := exec.Command("tmux", "send-keys", "-t", parent.tmux, "Enter")
	enter.Env = e.env
	if out, err := enter.CombinedOutput(); err != nil {
		t.Fatalf("submit the draft: %v %s", err, out)
	}
	issue2530WaitFor(t, 5*time.Second, "the operator draft to be submitted", func() bool { return parent.received(t, draft) == 1 })
	time.Sleep(time.Second)
	if n := parent.received(t, draft); n != 1 {
		t.Fatalf("operator draft submitted %d times, want 1", n)
	}
}

// With the socket off (pi below 1.2.0: the launch exports AGENTDECK_PI_SOCKET
// empty and the extension binds nothing), the doorbell is typed into the composer as
// before and lands once.
func TestDeliveryE2E_PiTypedFallbackLandsWhenTheSocketIsOff(t *testing.T) {
	testutil.RequireTmux(t)
	parallelSubprocessTest(t)
	e := newPiDeliveryEnv(t, "1.1.0")
	parent := e.startPi("conductor-pi")
	e.dumpOnFailure(parent)
	if entries, _ := os.ReadDir(filepath.Join(e.root, "data", "agent-deck", "runtime", "profiles", deliveryE2EProfile, "pi")); len(entries) != 0 {
		t.Fatalf("a socket exists with pi 1.1.0: %v", entries)
	}
	answer := deliveryToken("CHILD_REPLY")
	e.childFinishes(parent, answer)
	issue2530WaitFor(t, 30*time.Second, "the typed doorbell to land", func() bool { return parent.received(t, "[INBOX]") > 0 || parent.received(t, "child") > 0 })
	time.Sleep(time.Second)
	if n := piEventCount(parent.piEvents(t), "doorbell"); n != 0 {
		t.Fatalf("a doorbell reached pi although the socket is off: %d", n)
	}
	typed := 0
	for _, m := range parent.piEvents(t) {
		if s, ok := m["submitted"].(string); ok && strings.Contains(s, "child") {
			typed++
		}
	}
	if typed != 1 {
		t.Fatalf("the typed doorbell landed %d times, want 1: %v", typed, parent.piEvents(t))
	}
}

// R-B: with the extension alive, nothing is typed into pi, even when it refuses
// the doorbell. pi has no composer-draft guard, so a typed line would merge into
// an operator's multi-line draft and submit it. Here pi reports busy to the
// extension while its hook status still says waiting (the stale-status race),
// the extension refuses, and the draft, the screen and the cursor stay
// byte-identical; the draft then submits once.
func TestDeliveryE2E_PiRefusedWakeNeverTypesIntoAMultiLineDraft(t *testing.T) {
	testutil.RequireTmux(t)
	parallelSubprocessTest(t)
	e := newPiDeliveryEnv(t, "1.3.0")
	parent := e.startPi("conductor-pi")
	e.dumpOnFailure(parent)
	writeFile(t, filepath.Join(parent.project, ".fake", "busy-ctx"), "1", 0o600)

	first := deliveryToken("DRAFT_LINE_ONE") + " first line"
	second := deliveryToken("DRAFT_LINE_TWO") + " second line"
	for i, step := range [][]string{{"-l", first}, {"C-j"}, {"-l", second}} {
		args := append([]string{"send-keys", "-t", parent.tmux}, step...)
		cmd := exec.Command("tmux", args...)
		cmd.Env = e.env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("type draft step %d: %v %s", i, err, out)
		}
	}
	e.waitPane(parent, "the two-line draft", func(p string) bool {
		return strings.Contains(p, "❯ "+first) && strings.Contains(p, "  "+second)
	})
	draft := parent.draft(t)
	before, cursorBefore := e.paneStyled(parent), e.cursor(parent)

	e.childFinishes(parent, deliveryToken("CHILD_REPLY"))
	time.Sleep(3 * time.Second) // the doorbell would have arrived, or been typed, by now

	if after := e.paneStyled(parent); after != before {
		t.Fatalf("the refused wake changed the pane.\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if cursorAfter := e.cursor(parent); cursorAfter != cursorBefore {
		t.Fatalf("the refused wake moved the cursor from %q to %q", cursorBefore, cursorAfter)
	}
	if got := parent.draft(t); got != draft || !strings.Contains(got, "\n") {
		t.Fatalf("composer holds %q, want the two-line draft %q", got, draft)
	}
	evs := parent.piEvents(t)
	if n := piEventCount(evs, "doorbell"); n != 0 {
		t.Fatalf("pi reported busy but saw %d doorbells", n)
	}
	if n := piEventCount(evs, "submitted"); n != 0 {
		t.Fatalf("something was submitted into pi: %v", evs)
	}
	// The draft is the operator's: it submits once, whole.
	_ = os.Remove(filepath.Join(parent.project, ".fake", "busy-ctx"))
	enter := exec.Command("tmux", "send-keys", "-t", parent.tmux, "Enter")
	enter.Env = e.env
	if out, err := enter.CombinedOutput(); err != nil {
		t.Fatalf("submit the draft: %v %s", err, out)
	}
	issue2530WaitFor(t, 5*time.Second, "the draft to be submitted", func() bool { return parent.received(t, first) == 1 })
	if n := parent.received(t, second); n != 1 {
		t.Fatalf("the second draft line was submitted %d times with the first, want 1", n)
	}
}
