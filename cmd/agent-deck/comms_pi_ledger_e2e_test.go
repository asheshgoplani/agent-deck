package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// End to end for a ledger-consuming parent with a pi child: the real hook
// handler writes the status file and the spool for one run, a daemon pass
// emits the inbox record and ingests the spool, and LedgerPromptContext shows
// it. The run_id is longer than the spool's id cap, so both sides must hash
// the same input.

type piLedgerE2E struct {
	t             *testing.T
	parent, child *session.Instance
	db            *statedb.StateDB
	d             *session.TransitionDaemon
	run           string
}

func newPiLedgerE2E(t *testing.T) *piLedgerE2E {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	for _, key := range []string{"XDG_CACHE_HOME", "XDG_STATE_HOME", "AGENT_DECK_HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_JOB_DIR"} {
		t.Setenv(key, "")
	}
	const profile = "pi_ledger_e2e"
	t.Setenv("AGENTDECK_PROFILE", profile)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	t.Cleanup(session.SetCommsLedgerForTest(true))

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	parent := &session.Instance{ID: "parent-pi-e2e", Title: "pi parent", ProjectPath: home, GroupPath: session.DefaultGroupPath, Tool: "claude", Status: session.StatusWaiting, CreatedAt: time.Now()}
	child := &session.Instance{ID: "child-pi-e2e", Title: "pi child", ProjectPath: home, GroupPath: session.DefaultGroupPath, Tool: "pi", Status: session.StatusWaiting, ParentSessionID: parent.ID, CreatedAt: time.Now()}
	if err := storage.SaveWithGroups([]*session.Instance{parent, child}, nil); err != nil {
		t.Fatal(err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil { // a live TUI heartbeat: the daemon reads DB rows
		t.Fatal(err)
	}
	t.Cleanup(session.SetCommsConsumersForTest([]string{parent.ID}))
	e := &piLedgerE2E{t: t, parent: parent, child: child, db: db, d: session.NewTransitionDaemon(),
		run: strings.Repeat("r", 300)}
	e.status(parent.ID, "waiting")
	e.status(child.ID, "waiting")
	e.pass() // enrolls the parent
	if _, ok := session.LoadCommsEnrollment(parent.ID); !ok {
		t.Fatal("listed parent not enrolled")
	}
	return e
}

func (e *piLedgerE2E) status(id, status string) {
	e.t.Helper()
	tool := "claude"
	if id == e.child.ID {
		tool = "pi"
	}
	if err := e.db.WriteStatus(id, status, tool); err != nil {
		e.t.Fatal(err)
	}
}

func (e *piLedgerE2E) pass() {
	e.d.SyncOnce(context.Background())
	e.d.Flush()
}

func (e *piLedgerE2E) hook(event, extra string) {
	e.t.Helper()
	e.t.Setenv("AGENTDECK_INSTANCE_ID", e.child.ID)
	runHookHandlerWith(e.t, fmt.Sprintf(`{"hook_event_name":%q,"source":"pi","settles":true,"run_id":%q,"cwd":"/tmp","session_id":"pi-sess-1"%s}`, event, e.run, extra))
}

// runOnce is one settled run, as the extension sends it.
func (e *piLedgerE2E) runOnce() {
	e.t.Helper()
	e.hook("turn_start", "")
	e.status(e.child.ID, "running")
	e.pass()
	e.hook("turn_end", "")
	e.hook("agent_settled", `,"turn_id":"entry-1","last_assistant_message":"Built and tested.","prompt":"build it"`)
	e.status(e.child.ID, "waiting")
}

func (e *piLedgerE2E) prompt(text string) string {
	e.t.Helper()
	dl, ok := session.LedgerPromptContext(e.parent.ID, text)
	if !ok {
		e.t.Fatal("enrolled parent fell back to the inbox path")
	}
	dl.Done(true)
	return dl.Text
}

// One pass emits the inbox record and ingests the spool: the ledger shows the
// run with its text, and its inbox twin is retired.
func TestPiLedgerE2E_TheLedgerFirstShowsTheRunOnce(t *testing.T) {
	e := newPiLedgerE2E(t)
	e.runOnce()
	e.pass()
	got := e.prompt("hi")
	if strings.Count(got, "Built and tested.") != 1 || strings.Count(got, e.child.Title) != 1 {
		t.Fatalf("the run once, with its text: %q", got)
	}
	if session.InboxHasPending(e.parent.ID) {
		t.Fatal("the inbox twin is retired once the ledger showed the run")
	}
}

// The parent reads the inbox record before the spool is ingested (here the
// ledger is off for that pass): the later ledger turn still shows the text.
func TestPiLedgerE2E_TheInboxFirstKeepsTheRunsText(t *testing.T) {
	e := newPiLedgerE2E(t)
	e.runOnce()
	restore := session.SetCommsLedgerForTest(false)
	e.pass()
	restore()
	if first := e.prompt("hi"); !strings.Contains(first, e.child.Title) || strings.Contains(first, "Built and tested.") {
		t.Fatalf("the inbox record alone: %q", first)
	}
	e.pass()
	if got := e.prompt("again"); !strings.Contains(got, "Built and tested.") {
		t.Fatalf("the run's text never reached the parent: %q", got)
	}
}
