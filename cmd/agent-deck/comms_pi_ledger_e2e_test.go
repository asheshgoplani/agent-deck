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

	"github.com/asheshgoplani/agent-deck/internal/comms"
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
	// Built with its tmux session, which the launch decision is recorded for.
	parent := session.NewInstanceWithTool("pi parent", home, parentTool)
	parent.ID, parent.GroupPath, parent.PiSocketExported = "parent-pi-e2e", session.DefaultGroupPath, parentTool == "pi"
	parent.Status, parent.CreatedAt = session.StatusWaiting, time.Now()
	child := &session.Instance{ID: "child-pi-e2e", Title: "pi child", ProjectPath: home, GroupPath: session.DefaultGroupPath, Tool: "pi", Status: session.StatusWaiting, ParentSessionID: parent.ID, CreatedAt: time.Now()}
	if err := storage.SaveWithGroups([]*session.Instance{parent, child}, nil); err != nil {
		t.Fatal(err)
	}
	db := storage.GetDB()
	// The parent's launch, as Start records it.
	if _, err := db.WriteLaunchDecision(parent.ID, parent.GetTmuxSession().TmuxName(), parentTool, parent.PiSocketExported); err != nil {
		t.Fatal(err)
	}
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

// piServes is the field every payload of the pi serving a parent's wake socket
// carries (the extension's pane_host).
const piServes = `,"pane_host":true`

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
	dl, ok := session.LedgerPromptContext(e.parent.ID, text, "", e.parent.Tool == "pi")
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
	e.hookAs(e.parent.ID, "parent-run-1", "pi-sess-p", "before_agent_start", `,"prompt":"Run the bash command: sleep 60"`+piServes)
	e.hookAs(e.parent.ID, "parent-run-1", "pi-sess-p", "turn_start", piServes)
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
	e.hookAs(e.parent.ID, "parent-run-1", "pi-sess-p", "turn_end", piServes)
	e.hookAs(e.parent.ID, "parent-run-1", "pi-sess-p", "agent_settled", `,"turn_id":"p-entry-1","last_assistant_message":"BUSY DONE","prompt":"Run the bash command: sleep 60"`+piServes)
	e.status(e.parent.ID, "waiting")
	for i := 0; i < 5; i++ {
		e.pass()
	}
	if n := sock.count(); n != 1 {
		t.Errorf("doorbells after the busy pi parent settled = %d, want exactly 1", n)
	}
}

// CodeRabbit (hook_handler.go:170): a pi that is not the parent's consumer (a
// guest of the pane, a pi in another pane of the session) settles a run of its
// own. Its turn is recorded, but its prompt is not the parent's own: it neither
// acknowledges the parent's wake nor resets the parent's automatic wake count.
func TestPiLedgerE2E_ANonConsumerPisSettledRunNeitherAcksNorResetsTheParentsWakes(t *testing.T) {
	e := newPiLedgerE2EFor(t, "pi")
	sock := newFakePiSocket(t)
	restore := session.SetPiWakeSeamsForTest(e.parent.ID, sock.path, map[int]bool{os.Getpid(): true})
	t.Cleanup(func() {
		e.d.Flush()
		restore()
	})
	// The child asks; the idle parent is rung once.
	e.hook("turn_start", "")
	e.status(e.child.ID, "running")
	e.pass()
	e.hook("turn_end", "")
	e.hook("agent_settled", `,"turn_id":"entry-1","last_assistant_message":"Should we ship on Friday or Monday?","prompt":"ask the parent"`)
	e.status(e.child.ID, "waiting")
	for i := 0; i < 5; i++ {
		e.pass()
	}
	if n := sock.count(); n != 1 {
		t.Fatalf("test setup: doorbells = %d, want 1", n)
	}
	en, ok := session.LoadCommsEnrollment(e.parent.ID)
	if !ok {
		t.Fatal("test setup: the parent is not enrolled")
	}
	before, _, err := comms.ReadConsumer(en.Dir, e.parent.ID)
	if err != nil || before.AutoWakes != 1 {
		t.Fatalf("test setup: AutoWakes %d (%v), want 1", before.AutoWakes, err)
	}
	for name, extra := range map[string]string{"a guest of the pane": `,"pane_guest":true`, "a pi in another pane": ""} {
		e.hookAs(e.parent.ID, "other-run", "pi-other-sess", "agent_settled",
			`,"turn_id":"o-1","last_assistant_message":"done here","prompt":"summarise the diff"`+extra)
		e.pass()
		after, _, err := comms.ReadConsumer(en.Dir, e.parent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.AutoWakes != before.AutoWakes {
			t.Errorf("%s: its settled run reset the parent's automatic wakes (%d, was %d)", name, after.AutoWakes, before.AutoWakes)
		}
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
		e.hookAs(e.parent.ID, "parent-run", "pi-parent-sess", "before_agent_start", `,"prompt":"what is next?"`+piServes)
	})
	if !strings.Contains(parent, "Built and tested.") {
		t.Fatalf("the launched pi's run start lacks the record a guest pi took and acknowledged: %q", parent)
	}
}

// Forge round 2 #1: a pi typed into another tmux pane or window of a listed pi
// parent's session inherits the session's AGENTDECK_INSTANCE_ID but not the
// launch's AGENTDECK_PI_SOCKET or AGENTDECK_PI_HOST_PID, so it is no guest of
// the pane and serves no socket: its payloads carry neither pane_guest nor
// pane_host. Through the real hook handler and daemon passes, its run start is
// given none of the parent's records, its receipt and its run start are not
// applied, and the launched pi's next run start (pane_host) still carries them.
func TestPiLedgerE2E_APiInAnotherPaneOfTheParentsSessionNeitherTakesNorAcksItsRecords(t *testing.T) {
	e := newPiLedgerE2EFor(t, "pi")
	e.runOnce()
	e.pass()

	other := captureStdout(t, func() {
		e.hookAs(e.parent.ID, "split-run", "pi-split-sess", "before_agent_start", `,"prompt":"what changed?"`)
	})
	if strings.Contains(other, "Built and tested.") {
		t.Errorf("a pi in another pane of the parent's session was given the parent's records: %q", other)
	}
	token := ""
	if m := regexp.MustCompile(`<!-- agent-deck-delivery:([0-9A-Za-z]+) -->`).FindStringSubmatch(other); m != nil {
		token = m[1]
	}
	e.hookAs(e.parent.ID, "split-run", "pi-split-sess", "turn_start", fmt.Sprintf(`,"injected_token":%q`, token))
	e.pass()
	// Its next run start would carry the receipt for the run before it.
	_ = captureStdout(t, func() {
		e.hookAs(e.parent.ID, "split-run-2", "pi-split-sess", "before_agent_start",
			fmt.Sprintf(`,"prompt":"and now?","prior_injected_token":%q`, token))
	})
	e.pass()

	parent := captureStdout(t, func() {
		e.hookAs(e.parent.ID, "parent-run", "pi-parent-sess", "before_agent_start", `,"prompt":"what is next?"`+piServes)
	})
	if !strings.Contains(parent, "Built and tested.") {
		t.Fatalf("the serving pi's run start lacks the record a pi in another pane took and acknowledged: %q", parent)
	}
}

// CodeRabbit (hook_handler.go:226): a pi started in another pane of a listed
// Claude parent's session inherits the session's AGENTDECK_INSTANCE_ID. Its run
// start is given none of the Claude parent's records, and its receipt is never
// applied, so the Claude parent's own prompt still shows them.
func TestPiLedgerE2E_APiInAClaudeParentsSessionNeitherTakesNorAcksItsRecords(t *testing.T) {
	e := newPiLedgerE2E(t) // a Claude parent, enrolled
	if en, ok := session.LoadCommsEnrollment(e.parent.ID); !ok || en.Tool != "claude" {
		t.Fatalf("test setup: marker %+v (found %v), want an enrolled claude marker", en, ok)
	}
	e.runOnce()
	e.pass()

	other := captureStdout(t, func() {
		e.hookAs(e.parent.ID, "split-run", "pi-split-sess", "before_agent_start", `,"prompt":"what changed?"`)
	})
	if strings.Contains(other, "Built and tested.") {
		t.Errorf("a pi in the Claude parent's session was given the parent's records: %q", other)
	}
	token := ""
	if m := regexp.MustCompile(`<!-- agent-deck-delivery:([0-9A-Za-z]+) -->`).FindStringSubmatch(other); m != nil {
		token = m[1]
	}
	e.hookAs(e.parent.ID, "split-run", "pi-split-sess", "turn_start", fmt.Sprintf(`,"injected_token":%q`, token))
	e.pass()
	if got := e.prompt("what is next?"); !strings.Contains(got, "Built and tested.") {
		t.Errorf("the Claude parent's prompt lacks the record a pi in its session took: %q", got)
	}
}

// edit changes the parent's tool in place, under the same id, as a session
// edit or a conductor re-registered with another agent does: the stored Tool
// changes at once, and the pane keeps running what it launched until a restart.
func (e *piLedgerE2E) edit(tool string) {
	e.t.Helper()
	storage, err := session.NewStorageWithProfile("pi_ledger_e2e")
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = storage.Close() }()
	e.parent.Tool = tool
	if err := storage.SaveWithGroups([]*session.Instance{e.parent, e.child}, nil); err != nil {
		e.t.Fatal(err)
	}
}

// relaunch restarts the parent with its current tool, recording the launch
// decision as the restart's outcome write does.
func (e *piLedgerE2E) relaunch(exported bool) {
	e.t.Helper()
	storage, err := session.NewStorageWithProfile("pi_ledger_e2e")
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = storage.Close() }()
	e.parent.PiSocketExported = exported
	if err := storage.SaveWithGroups([]*session.Instance{e.parent, e.child}, nil); err != nil {
		e.t.Fatal(err)
	}
	if _, err := storage.GetDB().WriteLaunchDecision(e.parent.ID, e.parent.GetTmuxSession().TmuxName(), e.parent.Tool, exported); err != nil {
		e.t.Fatal(err)
	}
	e.status(e.parent.ID, "waiting")
}

// piRulesHold checks, through the real hook handler, that the parent's ledger
// follows pi's rules: a pi without pane_host takes none of its records, and the
// serving pi's injection is bound to a receipt token.
func (e *piLedgerE2E) piRulesHold(what string) {
	e.t.Helper()
	other := captureStdout(e.t, func() {
		e.hookAs(e.parent.ID, "split-run", "pi-split-sess", "before_agent_start", `,"prompt":"what changed?"`)
	})
	if strings.Contains(other, "Built and tested.") {
		e.t.Errorf("%s: a pi in another pane was given the parent's records: %q", what, other)
	}
	host := captureStdout(e.t, func() {
		e.hookAs(e.parent.ID, "parent-run", "pi-parent-sess", "before_agent_start", `,"prompt":"what is next?"`+piServes)
	})
	if !strings.Contains(host, "Built and tested.") || !regexp.MustCompile(`<!-- agent-deck-delivery:[0-9A-Za-z]+ -->`).MatchString(host) {
		e.t.Errorf("%s: the serving pi's run start lacks the record or its receipt token: %q", what, host)
	}
}

// Forge round 3 #1: a listed Claude parent turned pi in place and restarted as
// a pi with the socket. After the daemon's next poll its hooks follow pi's rules.
func TestPiLedgerE2E_AClaudeParentRestartedAsPiTakesThePiRulesAtTheNextPoll(t *testing.T) {
	e := newPiLedgerE2E(t) // a Claude parent, enrolled
	e.edit("pi")
	e.relaunch(true)
	e.pass()
	e.runOnce()
	e.pass()
	e.piRulesHold("after the restart as pi")
}

// Forge round 4: a tool edit takes effect at the next restart, and the pane runs
// pi until then. A listed pi parent edited to Claude keeps pi's rules through
// the daemon's polls (the marker stays pi, pane_host is still required, the
// injection still waits for its receipt); once it restarts as Claude, the
// marker says claude and a printed context is its delivery.
func TestPiLedgerE2E_APiParentEditedToClaudeKeepsThePiRulesUntilItRestarts(t *testing.T) {
	e := newPiLedgerE2EFor(t, "pi")
	e.edit("claude")
	e.pass()
	if en, ok := session.LoadCommsEnrollment(e.parent.ID); !ok || en.Tool != "pi" || !en.PiSocket || en.Draining {
		t.Fatalf("marker after an edit without a restart = %+v (found %v), want the pi's, enrolled with pi_socket", en, ok)
	}
	e.runOnce()
	e.pass()
	e.piRulesHold("edited to Claude, not restarted")

	e.relaunch(false)
	e.pass()
	if en, ok := session.LoadCommsEnrollment(e.parent.ID); !ok || en.Tool != "claude" || en.PiSocket || en.Draining {
		t.Fatalf("marker after the restart as Claude = %+v (found %v), want an enrolled claude marker", en, ok)
	}
	// A second run of the child, after the restart.
	e.hookAs(e.child.ID, "run-2", "pi-sess-1", "turn_start", "")
	e.status(e.child.ID, "running")
	e.pass()
	e.hookAs(e.child.ID, "run-2", "pi-sess-1", "turn_end", "")
	e.hookAs(e.child.ID, "run-2", "pi-sess-1", "agent_settled", `,"turn_id":"entry-2","last_assistant_message":"Second run done.","prompt":"again"`)
	e.status(e.child.ID, "waiting")
	e.pass()
	dl, ok := session.LedgerPromptContext(e.parent.ID, "hi", "", e.parent.Tool == "pi")
	if !ok {
		t.Fatal("enrolled parent fell back to the inbox path")
	}
	if !strings.Contains(dl.Text, "Second run done.") || dl.Token != "" {
		t.Errorf("the Claude parent's prompt = %q with token %q, want the new run and no receipt token", dl.Text, dl.Token)
	}
	dl.Done(true)
	if again := e.prompt("and now?"); strings.Contains(again, "Second run done.") {
		t.Errorf("the record was shown again: %q", again)
	}
}

// CodeRabbit (hook_handler.go:226, the restart window): a listed pi parent is
// restarted as Claude, and its hooks run before the daemon's next poll moves the
// marker. Its pane runs Claude now, and every pi left in the parent's session
// serves no socket (the restart replaced the pane that served it), so none is
// pane_host: a pi run start is given none of its records, its receipts are not
// applied, and the Claude parent's prompt shows the record once.
func TestPiLedgerE2E_APiParentRestartedAsClaudeAdmitsNoPiBeforeTheNextPoll(t *testing.T) {
	e := newPiLedgerE2EFor(t, "pi")
	e.runOnce()
	e.pass()
	e.edit("claude")
	e.relaunch(false) // no poll follows yet
	if en, ok := session.LoadCommsEnrollment(e.parent.ID); !ok || en.Tool != "pi" || !en.PiSocket {
		t.Fatalf("test setup: marker %+v (found %v), want the pi's until the next poll", en, ok)
	}

	other := captureStdout(t, func() {
		e.hookAs(e.parent.ID, "split-run", "pi-split-sess", "before_agent_start", `,"prompt":"what changed?"`)
	})
	if strings.Contains(other, "Built and tested.") {
		t.Errorf("a pi in the restarted parent's session was given its records before the poll: %q", other)
	}
	token := ""
	if m := regexp.MustCompile(`<!-- agent-deck-delivery:([0-9A-Za-z]+) -->`).FindStringSubmatch(other); m != nil {
		token = m[1]
	}
	e.hookAs(e.parent.ID, "split-run", "pi-split-sess", "turn_start", fmt.Sprintf(`,"injected_token":%q`, token))

	dl, ok := session.LedgerPromptContext(e.parent.ID, "what is next?", "", false)
	if !ok {
		t.Fatal("enrolled parent fell back to the inbox path")
	}
	if !strings.Contains(dl.Text, "Built and tested.") || dl.Token != "" {
		t.Errorf("the restarted Claude parent's prompt = %q with token %q, want the record and no receipt token", dl.Text, dl.Token)
	}
	dl.Done(true)
	e.pass()
	if again := e.prompt("and now?"); strings.Contains(again, "Built and tested.") {
		t.Errorf("the record was shown again after the poll: %q", again)
	}
}
