package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
	return newPiLedgerE2EFor(t, "claude")
}

// newPiLedgerE2EFor is the rig with a parent of the given tool; a pi parent is
// one launched with the socket exported.
func newPiLedgerE2EFor(t *testing.T, parentTool string) *piLedgerE2E {
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
	parent := &session.Instance{ID: "parent-pi-e2e", Title: "pi parent", ProjectPath: home, GroupPath: session.DefaultGroupPath, Tool: parentTool,
		PiSocketExported: parentTool == "pi", Status: session.StatusWaiting, CreatedAt: time.Now()}
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
	tool := e.parent.Tool
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
	e.hookAs(e.child.ID, e.run, "pi-sess-1", event, extra)
}

// hookAs runs the real hook handler for one pi event of the given session.
func (e *piLedgerE2E) hookAs(id, run, piSession, event, extra string) {
	e.t.Helper()
	e.t.Setenv("AGENTDECK_INSTANCE_ID", id)
	runHookHandlerWith(e.t, fmt.Sprintf(`{"hook_event_name":%q,"source":"pi","settles":true,"run_id":%q,"cwd":"/tmp","session_id":%q%s}`, event, run, piSession, extra))
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
	dl, ok := session.LedgerPromptContext(e.parent.ID, text, "")
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

// fakePiSocket is the pi extension's wake socket for the rig: it counts the
// doorbell frames and answers each submitted.
type fakePiSocket struct {
	path   string
	mu     sync.Mutex
	frames int
}

func newFakePiSocket(t *testing.T) *fakePiSocket {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pe") // a socket path stays under 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := filepath.Join(root, "pi")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &fakePiSocket{path: filepath.Join(dir, "w.sock")}
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var frame map[string]any
				if json.Unmarshal(line, &frame) != nil {
					return
				}
				s.mu.Lock()
				s.frames++
				s.mu.Unlock()
				raw, _ := json.Marshal(map[string]any{"id": frame["id"], "state": "submitted"})
				_, _ = conn.Write(append(raw, '\n'))
			}()
		}
	}()
	return s
}

func (s *fakePiSocket) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames
}

// Lab L4b on real pi, end to end: a listed pi parent is busy (its own run
// started through before_agent_start and is in its turn) when its pi child
// settles a run with text. The daemon polls on through the parent's run, then
// the parent's run ends; exactly one doorbell rings.
func TestPiLedgerE2E_ABusyPiParentIsRungOnceForItsChildsRunWhenItSettles(t *testing.T) {
	e := newPiLedgerE2EFor(t, "pi")
	sock := newFakePiSocket(t)
	restore := session.SetPiWakeSeamsForTest(e.parent.ID, sock.path, map[int]bool{os.Getpid(): true})
	t.Cleanup(func() {
		e.d.Flush()
		restore()
	})
	// The parent's own run starts and runs a long tool call.
	e.hookAs(e.parent.ID, "parent-run-1", "pi-sess-p", "before_agent_start", `,"prompt":"Run the bash command: sleep 60"`)
	e.hookAs(e.parent.ID, "parent-run-1", "pi-sess-p", "turn_start", "")
	e.status(e.parent.ID, "running")
	e.pass()
	// The child's run settles with a question while the parent is busy.
	e.hook("turn_start", "")
	e.status(e.child.ID, "running")
	e.pass()
	e.hook("turn_end", "")
	e.hook("agent_settled", `,"turn_id":"entry-1","last_assistant_message":"Should we ship on Friday or Monday?","prompt":"ask the parent"`)
	e.status(e.child.ID, "waiting")
	for i := 0; i < 5; i++ {
		e.pass()
	}
	if n := sock.count(); n != 0 {
		t.Fatalf("a doorbell rang while the parent was busy: %d frames", n)
	}
	// The parent's run ends.
	e.hookAs(e.parent.ID, "parent-run-1", "pi-sess-p", "turn_end", "")
	e.hookAs(e.parent.ID, "parent-run-1", "pi-sess-p", "agent_settled", `,"turn_id":"p-entry-1","last_assistant_message":"BUSY DONE","prompt":"Run the bash command: sleep 60"`)
	e.status(e.parent.ID, "waiting")
	for i := 0; i < 5; i++ {
		e.pass()
	}
	if n := sock.count(); n != 1 {
		t.Errorf("doorbells after the busy pi parent settled = %d, want exactly 1", n)
	}
}

// Review item 1: a pi started inside a listed pi parent's pane (a subagent's
// `pi -p`, a script) inherits AGENTDECK_INSTANCE_ID and AGENTDECK_PI_SOCKET,
// and the parent's pid in AGENTDECK_PI_HOST_PID, so its extension marks its
// payloads pane_guest. Through the real hook handler and daemon
// passes, that guest's run start is given none of the parent's records, and a
// receipt it echoes is never applied, so the parent's own next run start
// still carries them.
func TestPiLedgerE2E_APiStartedInsideTheParentsPaneNeitherTakesNorAcksItsRecords(t *testing.T) {
	e := newPiLedgerE2EFor(t, "pi")
	e.runOnce()
	e.pass()

	// The guest's run start, as its extension sends it.
	guest := captureStdout(t, func() {
		e.hookAs(e.parent.ID, "guest-run", "pi-guest-sess", "before_agent_start",
			`,"prompt":"summarize the diff","pane_guest":true`)
	})
	if strings.Contains(guest, "Built and tested.") {
		t.Errorf("the guest pi was given the parent's records: %q", guest)
	}
	// Whatever token its stdout carried, the guest echoes it on turn_start
	// (the extension does so for any context it injected), and the daemon ingests it.
	token := ""
	if m := regexp.MustCompile(`<!-- agent-deck-delivery:([0-9A-Za-z]+) -->`).FindStringSubmatch(guest); m != nil {
		token = m[1]
	}
	e.hookAs(e.parent.ID, "guest-run", "pi-guest-sess", "turn_start",
		fmt.Sprintf(`,"injected_token":%q,"pane_guest":true`, token))
	e.pass()

	// The launched pi's next run start still carries the child's run.
	parent := captureStdout(t, func() {
		e.hookAs(e.parent.ID, "parent-run", "pi-parent-sess", "before_agent_start", `,"prompt":"what is next?"`)
	})
	if !strings.Contains(parent, "Built and tested.") {
		t.Fatalf("the launched pi's run start lacks the record a guest pi took and acknowledged: %q", parent)
	}
}
