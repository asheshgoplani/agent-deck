package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #2473: a running workflow means a running session.
//
// testdata/background_work/probe-workflow-agent-bash.jsonl is the trimmed
// transcript of a live Claude Code 2.1.288 session (paths scrubbed, system
// prompt and attachments dropped) that ran, in order:
//
//	lines  1-10  a Workflow "probe-two-agents" (launch receipt, turn_duration pendingWorkflowCount=1)
//	lines 11-14  notifications for the workflow's OWN sub-agent tasks (not launched on the main chain)
//	line  15     the workflow's terminal <task-notification> (enqueued), 17 the notification turn
//	lines 21-28  a background Agent + a run_in_background Bash (pendingBackgroundAgentCount=1)
//	line  29     the agent's interim "completed" notification (it waits on its own sleep)
//	line  35     the Bash task's terminal notification
//	line  43     turn_duration pendingBackgroundAgentCount=1 again (the resumed agent)
//	line  45     the agent's final notification; 50 the last turn_duration (nothing pending)

func loadProbeTranscript(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "background_work", "probe-workflow-agent-bash.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

var transcriptTimestampRe = regexp.MustCompile(`"timestamp": ?"[^"]+"`)

// restamp rewrites every record timestamp to at, so the transcript hold is
// evaluated against a known age.
func restamp(lines []string, at time.Time) []string {
	out := make([]string, len(lines))
	stamp := `"timestamp": "` + at.UTC().Format(time.RFC3339Nano) + `"`
	for i, l := range lines {
		out[i] = transcriptTimestampRe.ReplaceAllString(l, stamp)
	}
	return out
}

func TestScanTranscriptBackground_LiveProbeTimeline(t *testing.T) {
	lines := loadProbeTranscript(t)
	if len(lines) != 50 {
		t.Fatalf("fixture has %d lines, want 50", len(lines))
	}
	cases := []struct {
		upTo int
		kind string
		task string
	}{
		{10, tmux.BackgroundKindWorkflow, "probe-two-agents"},
		// The workflow's own sub-agent tasks report into the main queue; they
		// were never launched on the main chain and change nothing.
		{14, tmux.BackgroundKindWorkflow, "probe-two-agents"},
		// The terminal notification is enqueued: the workflow is done.
		{15, "", ""},
		{20, "", ""},
		{28, tmux.BackgroundKindAgent, "probe sleeper agent"},
		// The interim notification says the agent is waiting on background
		// work of its own: it is still in flight, although the turn_duration
		// that follows (line 34) counts no pending agent.
		{34, tmux.BackgroundKindAgent, "probe sleeper agent"},
		{37, tmux.BackgroundKindAgent, "probe sleeper agent"},
		{43, tmux.BackgroundKindAgent, "probe sleeper agent"},
		{50, "", ""},
	}
	for _, c := range cases {
		sc := scanTranscriptBackground(lines[:c.upTo])
		got, _ := sc.inFlight()
		if got.Kind != c.kind || got.Task != c.task {
			t.Errorf("first %d lines: in flight = %+v, want kind %q task %q", c.upTo, got, c.kind, c.task)
		}
	}
	if sc := scanTranscriptBackground(lines); !sc.FinishedWorkflows["probe-two-agents"] {
		t.Errorf("finished workflows = %v, want probe-two-agents", sc.FinishedWorkflows)
	}
}

func TestScanTranscriptBackground_NewProcessAndMonitorExpiry(t *testing.T) {
	lines := loadProbeTranscript(t)
	resume := `{"type":"attachment","isSidechain":false,"attachment":{"type":"hook_success","hookName":"SessionStart:resume"}}`
	if got, _ := scanTranscriptBackground(append(append([]string{}, lines[:10]...), resume)).inFlight(); got.InFlight() {
		t.Fatalf("work launched by a previous Claude process survived a resume: %+v", got)
	}
	compact := `{"type":"attachment","isSidechain":false,"attachment":{"type":"hook_success","hookName":"SessionStart:compact"}}`
	if got, _ := scanTranscriptBackground(append(append([]string{}, lines[:10]...), compact)).inFlight(); !got.InFlight() {
		t.Fatal("a compaction (same process) must not drop in-flight work")
	}

	monitor := []string{
		`{"type":"assistant","isSidechain":false,"message":{"content":[{"type":"tool_use","id":"tm1","name":"Monitor","input":{"description":"CI on main"}}]}}`,
		`{"type":"user","isSidechain":false,"toolUseResult":{"taskId":"bmon1","timeoutMs":90000,"persistent":false},"message":{"content":[{"type":"tool_result","tool_use_id":"tm1","content":"Monitor started"}]}}`,
		`{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>bmon1</task-id>\n<summary>Monitor event: \"CI on main\"</summary>\n<event>shard 1: pass</event>\n</task-notification>"}`,
	}
	if got, _ := scanTranscriptBackground(monitor).inFlight(); got.Kind != tmux.BackgroundKindMonitor || got.Task != "CI on main" {
		t.Fatalf("monitor with a non-terminal event = %+v, want monitor in flight", got)
	}
	expired := append(append([]string{}, monitor...),
		`{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>bmon1</task-id>\n<event>[Monitor expired after 1m 30s with no events delivered.]</event>\n</task-notification>"}`)
	if got, _ := scanTranscriptBackground(expired).inFlight(); got.InFlight() {
		t.Fatalf("expired monitor = %+v, want nothing in flight", got)
	}
	// A sidechain launch (a sub-agent's own background Bash) is not the
	// session's work.
	side := `{"type":"user","isSidechain":true,"toolUseResult":{"backgroundTaskId":"bside"},"message":{"content":[{"type":"tool_result","tool_use_id":"x","content":"bg"}]}}`
	if got, _ := scanTranscriptBackground([]string{side}).inFlight(); got.InFlight() {
		t.Fatalf("sidechain launch = %+v, want ignored", got)
	}
}

// Claude Code 2.1.307 names the interim state in the summary as well.
func TestScanTranscriptBackground_InterimAgentNotification(t *testing.T) {
	launch := []string{
		`{"type":"assistant","isSidechain":false,"message":{"content":[{"type":"tool_use","id":"ta1","name":"Agent","input":{"description":"Background sleep probe"}}]}}`,
		`{"type":"user","isSidechain":false,"toolUseResult":{"status":"async_launched","agentId":"a433","description":"Background sleep probe"},"message":{"content":[{"type":"tool_result","tool_use_id":"ta1","content":"launched"}]}}`,
	}
	interim := `{"type":"user","isSidechain":false,"timestamp":"2026-10-21T23:26:10Z","message":{"content":"<task-notification>\n<task-id>a433</task-id>\n<status>completed</status>\n<summary>Agent \"Background sleep probe\" is waiting on its own background work</summary>\n<result>still sleeping</result>\n</task-notification>"}}`
	final := `{"type":"user","isSidechain":false,"timestamp":"2026-10-21T23:29:42Z","message":{"content":"<task-notification>\n<task-id>a433</task-id>\n<status>completed</status>\n<summary>Agent \"Background sleep probe\" finished</summary>\n<note>A task-notification fires each time this agent stops with no live background children of its own.</note>\n<result>done</result>\n</task-notification>"}}`

	sc := scanTranscriptBackground(append(append([]string{}, launch...), interim))
	got, at := sc.inFlight()
	if got.Kind != tmux.BackgroundKindAgent || got.Task != "Background sleep probe" {
		t.Fatalf("after the interim notification: in flight = %+v, want the agent", got)
	}
	if want, _ := time.Parse(time.RFC3339, "2026-10-21T23:26:10Z"); !at.Equal(want) {
		t.Fatalf("hold anchored at %v, want the interim notification %v", at, want)
	}
	if _, ok := sc.awaitingAgent(); !ok {
		t.Fatal("agent not marked as awaiting its own work")
	}
	if got, _ := scanTranscriptBackground(append(append([]string{}, launch...), interim, final)).inFlight(); got.InFlight() {
		t.Fatalf("after the final notification: in flight = %+v, want nothing", got)
	}
}

func TestMergeBackgroundWork(t *testing.T) {
	now := time.Now()
	lines := loadProbeTranscript(t)
	pendingWF := scanTranscriptBackground(restamp(lines[:10], now.Add(-30*time.Second)))
	staleWF := scanTranscriptBackground(restamp(lines[:10], now.Add(-10*time.Minute)))
	finished := scanTranscriptBackground(lines)
	shells := scanTranscriptBackground(restamp(lines[:26], now.Add(-20*time.Second)))
	awaitingOwn := scanTranscriptBackground(restamp(lines[:34], now.Add(-20*time.Second)))
	staleAwaiting := scanTranscriptBackground(restamp(lines[:34], now.Add(-10*time.Minute)))
	row := tmux.BackgroundWork{Kind: tmux.BackgroundKindWorkflow, Task: "probe-two-agents", Step: 1, Steps: 2, Elapsed: "22s", Source: "pane"}
	awaiting := tmux.BackgroundWork{Kind: tmux.BackgroundKindAgent, Task: "1 background agent", Source: "pane"}
	agentPending := transcriptBackgroundScan{Pending: []transcriptBackgroundTask{{ID: "a1", Kind: tmux.BackgroundKindAgent, Name: "probe agent", At: now.Add(-time.Hour)}}}
	turnAt := now.Add(-time.Hour)
	countedZero := transcriptBackgroundScan{TurnAgentsKnown: true, TurnAt: turnAt} // 0, or absent from a Claude Code that writes it
	oldPositive := transcriptBackgroundScan{TurnAgents: 1, TurnAgentsKnown: true, TurnAt: turnAt}
	noCountField := transcriptBackgroundScan{TurnAt: turnAt}
	// A launch with no terminal notification, older than the zero count: the
	// task died unreported, and Claude Code no longer counts it.
	diedBeforeZero := transcriptBackgroundScan{TurnAgentsKnown: true, TurnAt: turnAt, Pending: []transcriptBackgroundTask{{ID: "a1", Kind: tmux.BackgroundKindAgent, Name: "probe agent", At: turnAt.Add(-time.Minute)}}}
	launchedAfterZero := transcriptBackgroundScan{TurnAgentsKnown: true, TurnAt: turnAt, Pending: []transcriptBackgroundTask{{ID: "a2", Kind: tmux.BackgroundKindAgent, Name: "probe agent", At: turnAt.Add(time.Minute)}}}

	cases := []struct {
		name     string
		pane     tmux.BackgroundWork
		sc       transcriptBackgroundScan
		scOK     bool
		seen     time.Time
		wantKind string
		wantTask string
		wantPane bool
	}{
		{"pane row alone", row, transcriptBackgroundScan{}, false, time.Time{}, tmux.BackgroundKindWorkflow, "probe-two-agents", true},
		{"pane row, transcript agrees", row, pendingWF, true, time.Time{}, tmux.BackgroundKindWorkflow, "probe-two-agents", true},
		{"stale row vetoed by finished task", row, finished, true, time.Time{}, "", "", false},
		{"redrawn pane, fresh transcript holds", tmux.BackgroundWork{}, pendingWF, true, time.Time{}, tmux.BackgroundKindWorkflow, "probe-two-agents", false},
		{"redrawn pane, transcript past the hold", tmux.BackgroundWork{}, staleWF, true, time.Time{}, "", "", false},
		{"recent pane sighting extends the hold", tmux.BackgroundWork{}, staleWF, true, now.Add(-time.Minute), tmux.BackgroundKindWorkflow, "probe-two-agents", false},
		// A5: the shell counter holds only through its fresh launch, so the
		// pane sighting is not stamped (paneShown false).
		{"footer counter named from transcript", tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: "1 shell", Source: "pane"}, shells, true, time.Time{}, tmux.BackgroundKindBash, "Sleep for 50 seconds in background", false},
		// A sub-agent's own shells are on the session's footer: while the
		// agent awaits its own work the counter is that agent running, within
		// background_shell_hold of the interim notification (A5). The
		// sighting never extends it (paneShown false).
		{"footer counter, agent awaiting its own shell", tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: "1 shell", Source: "pane"}, awaitingOwn, true, time.Time{}, tmux.BackgroundKindAgent, "probe sleeper agent", false},
		{"footer counter, agent awaiting its own shell, past the shell hold", tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: "1 shell", Source: "pane"}, staleAwaiting, true, time.Time{}, "", "", false},
		{"agent awaiting its own shell, fresh notification holds", tmux.BackgroundWork{}, awaitingOwn, true, time.Time{}, tmux.BackgroundKindAgent, "probe sleeper agent", false},
		{"agent awaiting its own shell, past the hold", tmux.BackgroundWork{}, staleAwaiting, true, time.Time{}, "", "", false},
		{"footer counter, no transcript", tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: "1 shell", Source: "pane"}, transcriptBackgroundScan{}, false, time.Time{}, "", "", false},
		{"nothing anywhere", tmux.BackgroundWork{}, finished, true, time.Time{}, "", "", false},
		{"Waiting line alone", awaiting, transcriptBackgroundScan{}, false, time.Time{}, tmux.BackgroundKindAgent, "1 background agent", true},
		{"Waiting line, agent launch pending", awaiting, agentPending, true, time.Time{}, tmux.BackgroundKindAgent, "probe agent", true},
		// Only a count Claude Code wrote, down to zero, proves the line is history.
		{"Waiting line, count field absent", awaiting, noCountField, true, time.Time{}, tmux.BackgroundKindAgent, "1 background agent", true},
		{"Waiting line, old positive count", awaiting, oldPositive, true, time.Time{}, tmux.BackgroundKindAgent, "1 background agent", true},
		{"Waiting line, agent launched after the zero count", awaiting, launchedAfterZero, true, time.Time{}, tmux.BackgroundKindAgent, "probe agent", true},
		{"stale Waiting line vetoed by a zero count", awaiting, countedZero, true, now, "", "", false},
		// The veto stops the pane sightings, so the last one still bounds the hold.
		{"stale Waiting line vetoed past an unreported launch", awaiting, diedBeforeZero, true, now.Add(-5 * time.Minute), "", "", false},
		{"vetoed Waiting line, last sighting holds", awaiting, diedBeforeZero, true, now.Add(-time.Minute), tmux.BackgroundKindAgent, "probe agent", false},
		{"stale Waiting line vetoed by finished transcript", awaiting, finished, true, now, "", "", false},
		{"stale workflow Waiting line vetoed", tmux.BackgroundWork{Kind: tmux.BackgroundKindWorkflow, Task: "1 dynamic workflow", Source: "pane"}, finished, true, now, "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, paneShown := mergeBackgroundWork(c.pane, c.sc, c.scOK, c.seen, now)
			if got.Kind != c.wantKind || got.Task != c.wantTask || paneShown != c.wantPane {
				t.Fatalf("merge = %+v paneShown=%v, want kind %q task %q paneShown=%v", got, paneShown, c.wantKind, c.wantTask, c.wantPane)
			}
		})
	}
}

// The count on the last turn_duration can lag one notification behind: the
// field report (a conductor held running for a day by its pane's "Waiting
// for 1 background agent" line) ended with pendingBackgroundAgentCount 1 and
// that agent's completed notification right after it.
func TestScanTranscriptBackground_CountLagsLastNotification(t *testing.T) {
	launch := `{"type":"user","isSidechain":false,"timestamp":"2026-10-09T15:46:41Z","toolUseResult":{"agentId":"ab0b","isAsync":true,"status":"async_launched","description":"review"},"message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"launched"}]}}`
	turn := `{"type":"system","subtype":"turn_duration","isSidechain":false,"timestamp":"2026-10-09T15:47:34Z","pendingBackgroundAgentCount":1}`
	done := `{"type":"user","isSidechain":false,"timestamp":"2026-10-09T15:47:34Z","message":{"content":"<task-notification>\n<task-id>ab0b</task-id>\n<status>completed</status>\n</task-notification>"}}`
	if sc := scanTranscriptBackground([]string{launch, turn, done}); !sc.awaitedLineStale(tmux.BackgroundKindAgent) {
		t.Fatalf("scan = %+v, want the Waiting line proven stale", sc)
	}
	if sc := scanTranscriptBackground([]string{launch, turn}); sc.awaitedLineStale(tmux.BackgroundKindAgent) {
		t.Fatal("an agent Claude Code still counts must keep the Waiting line")
	}
	// Claude Code omits a zero count, but one too old to write the field
	// omits it always: absent counts as zero only once the tail shows it.
	unstamped := `{"type":"system","subtype":"turn_duration","isSidechain":false,"timestamp":"2026-10-09T15:48:00Z"}`
	if sc := scanTranscriptBackground([]string{launch, unstamped, done}); sc.awaitedLineStale(tmux.BackgroundKindAgent) {
		t.Fatal("a tail that never shows the count field proves nothing")
	}
	if sc := scanTranscriptBackground([]string{launch, turn, done, unstamped}); !sc.awaitedLineStale(tmux.BackgroundKindAgent) {
		t.Fatal("an absent count after the field was seen is zero")
	}
	// A restart zeroes the counts only until the next turn_duration: on a
	// Claude Code that never writes the field, that one proves nothing.
	startup := `{"type":"attachment","isSidechain":false,"timestamp":"2026-10-09T15:40:00Z","attachment":{"hookName":"SessionStart:startup"}}`
	if sc := scanTranscriptBackground([]string{startup, launch, unstamped}); sc.awaitedLineStale(tmux.BackgroundKindAgent) {
		t.Fatal("a launch after a restart must keep the Waiting line on a Claude Code that never writes the count")
	}
}

func loadPaneFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "tmux", "testdata", "background_work", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeInstanceTranscript places lines where GetJSONLPath resolves inst's
// Claude transcript (session id from the Stop hook file: sess-610).
func writeInstanceTranscript(t *testing.T, inst *Instance, lines []string) {
	t.Helper()
	resolved := inst.ProjectPath
	if r, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = r
	}
	dir := filepath.Join(GetClaudeConfigDir(), "projects", ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sess-610.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Duration(len(lines)) * time.Second)
	_ = os.Chtimes(path, future, future)
}

// The status merge: the Stop hook says waiting (the foreground turn ended),
// the pane shows the workflow row at 1/2. Before #2473 this read waiting
// (idle once acknowledged) — the screenshot in the issue.
func TestBackgroundWork2473_StopHookWaitingWithWorkflowRowIsRunning(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	inst, cleanup := startHookLagInstance(t, "bg-workflow", loadPaneFixture(t, "workflow-running.txt"))
	defer cleanup()
	writeHookLagStopFile(t, inst.ID)

	status, sub := cliPass(t, inst)
	if status != StatusRunning || sub != SubstateBackgroundWork {
		t.Fatalf("Stop hook + workflow row = %q/%q, want running/background-work", status, sub)
	}
	work := inst.BackgroundWork()
	if work.Kind != tmux.BackgroundKindWorkflow || work.Task != "probe-two-agents" || work.Step != 1 || work.Steps != 2 {
		t.Fatalf("background work = %+v", work)
	}
	if got := inst.SubstateDetail(); got != "workflow probe-two-agents 1/2 · 22s" {
		t.Fatalf("substate detail = %q", got)
	}
	if got := inst.CachedSubstate(); got != SubstateBackgroundWork {
		t.Fatalf("cached substate (TUI) = %q", got)
	}
	// An acknowledgement (attach) does not make running work idle.
	inst.tmuxSession.Acknowledge()
	if status, _ := cliPass(t, inst); status != StatusRunning {
		t.Fatalf("acknowledged while the workflow runs = %q, want running", status)
	}
}

// The work ended: the row sits at 2/2 and the turn summary is below the old
// Waiting line. Waiting, then idle once acknowledged.
func TestBackgroundWork2473_FinishedWorkflowIsWaitingThenIdle(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	inst, cleanup := startHookLagInstance(t, "bg-finished", loadPaneFixture(t, "workflow-finished.txt"))
	defer cleanup()
	writeHookLagStopFile(t, inst.ID)
	if status, sub := cliPass(t, inst); status != StatusWaiting || sub != SubstateIdleAtEmptyPrompt {
		t.Fatalf("finished workflow = %q/%q, want waiting/idle-at-empty-prompt", status, sub)
	}
	if work := inst.BackgroundWork(); work.InFlight() {
		t.Fatalf("background work after it finished = %+v", work)
	}
	inst.tmuxSession.Acknowledge()
	if status, _ := cliPass(t, inst); status != StatusIdle {
		t.Fatalf("acknowledged after the work ended = %q, want idle", status)
	}
}

// The scrolled / redrawn capture: the pane shows neither the row nor the
// Waiting line, but the transcript still holds the pending Workflow launch.
// Running stays running; once the transcript records the terminal
// notification, the session settles to waiting.
func TestBackgroundWork2473_TranscriptHoldsRedrawnPane(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	running := loadPaneFixture(t, "workflow-running.txt")
	redrawn := strings.Replace(running[:strings.Index(running, "  [p] u@host")], "✻ Waiting for 1 dynamic workflow to finish", "", 1)
	inst, cleanup := startHookLagInstance(t, "bg-redrawn", redrawn)
	defer cleanup()
	writeHookLagStopFile(t, inst.ID)
	inst.ClaudeSessionID = "sess-610" // the hook binds it too, after the first probe

	lines := loadProbeTranscript(t)
	writeInstanceTranscript(t, inst, restamp(lines[:10], time.Now().Add(-20*time.Second)))
	status, sub := cliPass(t, inst)
	if status != StatusRunning || sub != SubstateBackgroundWork {
		t.Fatalf("redrawn pane + pending transcript = %q/%q, want running/background-work", status, sub)
	}
	if work := inst.BackgroundWork(); work.Task != "probe-two-agents" || work.Source != "transcript" {
		t.Fatalf("background work = %+v, want the transcript's probe-two-agents", work)
	}

	writeInstanceTranscript(t, inst, restamp(lines[:20], time.Now().Add(-5*time.Second)))
	writeHookLagStopFile(t, inst.ID)
	if status, _ := cliPass(t, inst); status != StatusWaiting {
		t.Fatalf("after the workflow reported back = %q, want waiting", status)
	}

	// A launch whose notification never came (a task stopped from the UI
	// leaves no transcript marker) holds only for the bounded window.
	writeInstanceTranscript(t, inst, restamp(lines[:10], time.Now().Add(-10*time.Minute)))
	writeHookLagStopFile(t, inst.ID)
	if status, _ := cliPass(t, inst); status != StatusWaiting {
		t.Fatalf("stale transcript-only evidence = %q, want waiting (bounded hold)", status)
	}
}

// Shells and monitors at the prompt, tmux path (no hook), no transcript.
// #2473 read this as running + background-work
// (TestBackgroundWork2473_ShellsAtPromptTmuxPathIsRunning); since A5 the shells
// need a fresh background Bash launch in the transcript, so the pane alone
// leaves the monitor: waiting + watching. TestA5_LeftoverShellTranscriptHold
// covers the transcript-backed running case.
func TestBackgroundWork2473_ShellsAtPromptTmuxPathNeedsTranscript(t *testing.T) {
	inst, cleanup := startPaneInstance(t, "claude", "claude-bgshell-2473", loadPaneFixture(t, "agent-and-shells.txt"))
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-2473-bgshell")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	// A fresh process (list --json, session show) loading the row as waiting.
	fresh := persistAndReload(t, storage, inst, StatusWaiting)
	status, sub := cliPass(t, fresh)
	if status != StatusWaiting || sub != SubstateWatching {
		t.Fatalf("shells at the prompt = %q/%q, want waiting/watching", status, sub)
	}
	if work := fresh.BackgroundWork(); work.Kind != tmux.BackgroundKindMonitor || work.Count != 1 {
		t.Fatalf("background work = %+v, want the 1 monitor watching", work)
	}
}

// The precedence rule in one table: a hook-driven waiting never overrides
// background evidence, and live foreground cues / errors keep their substate.
func TestReconcileBackgroundSubstate(t *testing.T) {
	cases := []struct {
		sub    Substate
		status Status
		active bool
		want   Substate
	}{
		{SubstateIdleAtEmptyPrompt, StatusRunning, true, SubstateBackgroundWork},
		{SubstateNone, StatusRunning, true, SubstateBackgroundWork},
		{SubstateHookLag, StatusRunning, true, SubstateBackgroundWork},
		{SubstateRunning, StatusRunning, true, SubstateRunning},
		{SubstateInteractiveMenu, StatusRunning, true, SubstateInteractiveMenu},
		{SubstateAuth401, StatusRunning, true, SubstateAuth401},
		{SubstateBackgroundWork, StatusWaiting, false, SubstateIdleAtEmptyPrompt},
		{SubstateIdleAtEmptyPrompt, StatusWaiting, false, SubstateIdleAtEmptyPrompt},
	}
	for _, c := range cases {
		if got := reconcileBackgroundSubstate(c.sub, c.status, c.active); got != c.want {
			t.Errorf("reconcile(%q, %q, %v) = %q, want %q", c.sub, c.status, c.active, got, c.want)
		}
	}
}

// fxWorkflowLaunch is the Workflow tool_use + launch receipt pair.
func fxWorkflowLaunch(taskID, name string) []string {
	use, _ := json.Marshal(map[string]any{
		"type": "assistant", "uuid": "a-wf-" + taskID, "isSidechain": false,
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "tu-" + taskID, "name": "Workflow", "input": map[string]any{"script": "…"}}}},
	})
	receipt, _ := json.Marshal(map[string]any{
		"type": "user", "uuid": "u-wf-" + taskID, "isSidechain": false,
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
		"toolUseResult": map[string]any{"status": "async_launched", "taskId": taskID, "taskType": "local_workflow", "workflowName": name},
		"message":       map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "tu-" + taskID, "content": "Workflow launched in background. Task ID: " + taskID}}},
	})
	return []string{string(use), string(receipt)}
}

func fxTurnDuration(pendingWorkflows int) string {
	rec := map[string]any{"type": "system", "subtype": "turn_duration", "isSidechain": false, "timestamp": time.Now().UTC().Format(time.RFC3339Nano)}
	if pendingWorkflows > 0 {
		rec["pendingWorkflowCount"] = pendingWorkflows
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

func fxWorkflowNotification(uuid, taskID string) string {
	return fxUser(uuid, "<task-notification>\n<task-id>"+taskID+"</task-id>\n<status>completed</status>\n<summary>Dynamic workflow completed</summary>\n</task-notification>", map[string]any{
		"turnOrigin": "task_notification",
		"origin":     map[string]any{"kind": "task-notification", "producer": "session-task"},
	})
}

// The daemon: while the workflow runs, the Stop hook of the launching turn is
// fresh and says waiting, but the merged status is running. No inbox record
// may be written (before #2473 the hook candidate emitted running -> waiting
// for the launch turn). When the workflow reports back and the session
// settles, exactly one record is written, through emitTurn, as a background
// (task) turn.
func TestBackgroundWork2473_DaemonWritesNoRecordWhileWorkflowRuns(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxHuman("u0", "run the follow-on workflow"))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	f.appendTurn(t, fxAssistantText("a0", "Launched comms-followon-round3 in the background."), fxTurnDuration(1))

	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting"}
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now()}}
	for i := 0; i < 10; i++ {
		f.d.recordTerminalTurns("default", f.byID, running, nil)
		f.d.emitHookTransitionCandidates("default", f.byID, running, running, stop)
	}
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("records written while the workflow runs: %+v", got)
	}

	// The workflow reports back; the notification turn ends; the session is
	// waiting. Every observation path fires, as in the real daemon.
	f.appendTurn(t, fxWorkflowNotification("u1", "wqphbmkuj"), fxAssistantText("a1", "comms-followon-round3 finished: 5/5 lanes merged."), fxTurnDuration(0))
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	stop = map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now()}}
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	for i := 0; i < 5; i++ {
		f.d.recordTerminalTurns("default", f.byID, waiting, nil)
		f.d.emitHookTransitionCandidates("default", f.byID, running, waiting, stop)
	}
	got := f.inboxRecords(t)
	if len(got) != 1 {
		t.Fatalf("want exactly one record when the workflow ends, got %d: %+v", len(got), got)
	}
	if got[0].Trigger != TurnTriggerTask || got[0].ToStatus != "waiting" || !strings.Contains(got[0].Text, "5/5 lanes merged") {
		t.Fatalf("record = %+v, want a waiting task turn carrying the child's text", got[0])
	}
}

// The shared conductor template names background-work, and a conductor file
// written before the sentence existed (main builds after #2478; v1.16.24
// itself is pinned by TestShippedConductorTemplatesAreRecognizedAsGenerated)
// is still recognised as generated and migrated to it.
func TestBackgroundWork2473_ConductorTemplateMigratesV1624(t *testing.T) {
	if !strings.Contains(conductorSharedClaudeMDTemplate, conductorBackgroundWorkGuidance) {
		t.Fatal("shared conductor template lacks the background-work guidance")
	}
	spec, err := GetConductorAgentSpec(ConductorAgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	current := renderConductorInstructionsTemplate(conductorSharedClaudeMDTemplate, "", DefaultProfile, spec)
	// Rebuilt from what shipped: the /cmd quick command, the no-op notice
	// rule, the plain (non -q) reads and the #2972 archived-skip clause came
	// after this sentence, so the old file has none of them.
	v1624 := renderConductorInstructionsTemplate(preBackgroundWorkConductorInstructionsTemplate(preNoOpNoticeRuleConductorInstructionsTemplate(prePlainReadConductorInstructionsTemplate(preHarnessControlConductorInstructionsTemplate(preArchivedSessionsConductorInstructionsTemplate(preStandingOrdersConductorInstructionsTemplate(conductorSharedClaudeMDTemplate)))))), "", DefaultProfile, spec)
	if v1624 == current || strings.Contains(v1624, "background-work") {
		t.Fatal("reconstructed v1.16.24 template still carries the new sentence")
	}
	path := filepath.Join(t.TempDir(), spec.InstructionsFileName)
	if err := os.WriteFile(path, []byte(v1624), 0o644); err != nil {
		t.Fatal(err)
	}
	gens := renderConductorInstructionsGenerations(conductorSharedClaudeMDTemplate, "", DefaultProfile, spec)
	if err := writeGeneratedFileOrMigrate(path, gens, current, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !matchesTemplateContent(string(got), current) {
		t.Fatal("a v1.16.24 conductor file was not migrated to the template with background-work guidance")
	}
}

// JSON surfaces: background_work is omitted unless the session is running
// because of background work, and carries the documented keys when it is.
func TestBackgroundWork2473_JSONShape(t *testing.T) {
	inst := NewInstanceWithTool("bg-json", t.TempDir(), "claude")
	if inst.BackgroundWorkJSON() != nil {
		t.Fatal("background_work present with nothing in flight")
	}
	inst.mu.Lock()
	inst.Status = StatusRunning
	inst.bgWorkActive = true
	inst.bgWork = tmux.BackgroundWork{Kind: tmux.BackgroundKindWorkflow, Task: "comms-followon-round3", Step: 3, Steps: 5, Elapsed: "18m32s", Source: "pane"}
	inst.mu.Unlock()
	b, err := json.Marshal(map[string]any{"background_work": inst.BackgroundWorkJSON()})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"background_work":{"kind":"workflow","task":"comms-followon-round3","step":3,"steps":5,"elapsed":"18m32s","source":"pane"}}`
	if string(b) != want {
		t.Fatalf("json = %s\nwant %s", b, want)
	}
	if got := inst.SubstateDetail(); got != "workflow comms-followon-round3 3/5 · 18m32s" {
		t.Fatalf("substate_detail = %q", got)
	}
	// Running for another reason (a foreground turn): no background_work.
	inst.mu.Lock()
	inst.bgWorkActive = false
	inst.mu.Unlock()
	if inst.BackgroundWorkJSON() != nil {
		t.Fatal("background_work present on a foreground-running session")
	}
}

// The one record written when a workflow ends follows the #2478 urgent rule:
// a plain summary is info (it never wakes the parent), and only a completion
// sentinel or an explicit question to the parent is urgent. Background work
// changes WHEN the record is written, never its tier.
func TestBackgroundWork2473_WorkflowEndRecordFollowsUrgentRule(t *testing.T) {
	cases := []struct {
		name, text, tier string
	}{
		{"plain summary", "comms-followon-round3 finished: 5/5 lanes merged.", TurnTierInfo},
		{"question to the parent", "comms-followon-round3 finished: 4/5 lanes merged.\nShould I retry lane 3 or skip it?", TurnTierUrgent},
		{"completion sentinel", "All lanes merged.\n===AGENTDECK_DONE=== status=ok summary=5/5 merged", TurnTierUrgent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			f.appendTurn(t, fxHuman("u0", "run the follow-on workflow"))
			f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
			f.appendTurn(t, fxAssistantText("a0", "Launched comms-followon-round3 in the background."), fxTurnDuration(1))
			running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting"}
			stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now()}}
			for i := 0; i < 3; i++ {
				f.d.recordTerminalTurns("default", f.byID, running, nil)
				f.d.emitHookTransitionCandidates("default", f.byID, running, running, stop)
			}
			if got := f.inboxRecords(t); len(got) != 0 {
				t.Fatalf("records written while the workflow runs: %+v", got)
			}

			f.appendTurn(t, fxWorkflowNotification("u1", "wqphbmkuj"), fxAssistantText("a1", c.text), fxTurnDuration(0))
			waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
			stop = map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now()}}
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
			for i := 0; i < 3; i++ {
				f.d.recordTerminalTurns("default", f.byID, waiting, nil)
				f.d.emitHookTransitionCandidates("default", f.byID, running, waiting, stop)
			}
			got := f.inboxRecords(t)
			if len(got) != 1 {
				t.Fatalf("want exactly one record when the workflow ends, got %d: %+v", len(got), got)
			}
			if got[0].Trigger != TurnTriggerTask || got[0].Tier != c.tier {
				t.Fatalf("record trigger/tier = %q/%q, want %q/%q (%+v)", got[0].Trigger, got[0].Tier, TurnTriggerTask, c.tier, got[0])
			}
		})
	}
}

// A background agent that starts a server nobody reaps and stops gets the
// interim notification, never the final one, and the footer counter stays up.
// The counter keeps that agent running only within background_shell_hold of
// the interim notification (A5), and its sightings never extend that.
func TestMergeBackgroundWork_AwaitingAgentCounterBoundedByShellHold(t *testing.T) {
	t.Cleanup(func() { setBackgroundShellHold(DefaultBackgroundShellHold) })
	setBackgroundShellHold(DefaultBackgroundShellHold)
	now := time.Now()
	lines := loadProbeTranscript(t)
	counter := tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: "1 shell", Source: "pane"}
	sc := scanTranscriptBackground(restamp(lines[:34], now.Add(-6*time.Hour)))
	if _, ok := sc.awaitingAgent(); !ok {
		t.Fatal("fixture: agent not awaiting its own work")
	}
	seen := time.Time{}
	for step := 0; step < 360; step++ { // a poll every 10s for an hour
		at := now.Add(time.Duration(step) * 10 * time.Second)
		w, shown := mergeBackgroundWork(counter, sc, true, seen, at)
		if shown {
			seen = at
		}
		if w.Running() {
			t.Fatalf("poll %d: 6h-old interim notification + footer counter = %+v shown=%v, want not running (bounded by background_shell_hold)", step, w, shown)
		}
	}

	// Inside the hold: the counter is the agent, the sighting is not stamped.
	fresh := scanTranscriptBackground(restamp(lines[:34], now.Add(-2*time.Minute)))
	if w, shown := mergeBackgroundWork(counter, fresh, true, time.Time{}, now); w.Kind != tmux.BackgroundKindAgent || shown {
		t.Fatalf("2m-old interim notification + footer counter = %+v shown=%v, want agent, not stamped", w, shown)
	}

	// The user's hold applies: 30m keeps a 10m-old interim notification.
	stale := scanTranscriptBackground(restamp(lines[:34], now.Add(-10*time.Minute)))
	setBackgroundShellHold(30 * time.Minute)
	if w, shown := mergeBackgroundWork(counter, stale, true, time.Time{}, now); w.Kind != tmux.BackgroundKindAgent || shown {
		t.Fatalf("hold 30m, 10m-old interim notification + counter = %+v shown=%v, want agent, not stamped", w, shown)
	}

	// Hold 0 (the rule before A5): the counter alone is work, named as the agent.
	setBackgroundShellHold(0)
	if w, _ := mergeBackgroundWork(counter, sc, true, time.Time{}, now); w.Kind != tmux.BackgroundKindAgent {
		t.Fatalf("hold 0, 6h-old interim notification + counter = %+v, want agent", w)
	}
}

// The interim wording is matched outside the agent's own <result> text: a
// final notification whose result quotes it still ends the agent.
func TestScanTranscriptBackground_InterimWordingInResultIsFinal(t *testing.T) {
	launch := []string{
		`{"type":"assistant","isSidechain":false,"message":{"content":[{"type":"tool_use","id":"ta1","name":"Agent","input":{"description":"Review status code"}}]}}`,
		`{"type":"user","isSidechain":false,"toolUseResult":{"status":"async_launched","agentId":"a433","description":"Review status code"},"message":{"content":[{"type":"tool_result","tool_use_id":"ta1","content":"launched"}]}}`,
	}
	for name, final := range map[string]string{
		"closed result":    `{"type":"user","isSidechain":false,"timestamp":"2026-10-21T23:29:42Z","message":{"content":"<task-notification>\n<task-id>a433</task-id>\n<status>completed</status>\n<summary>Agent \"Review status code\" finished</summary>\n<result>Done. The scan keeps an agent pending when its note says it is waiting on its own background work, or stopped with background work of its own still running.</result>\n</task-notification>"}}`,
		"truncated result": `{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>a433</task-id>\n<status>completed</status>\n<summary>Agent \"Review status code\" finished</summary>\n<result>Done. It is waiting on its own background work wh"}`,
	} {
		sc := scanTranscriptBackground(append(append([]string{}, launch...), final))
		if a, ok := sc.awaitingAgent(); ok {
			t.Errorf("%s: final notification quoting the interim wording in <result> left the agent awaiting: %+v", name, a)
		}
		if got, _ := sc.inFlight(); got.InFlight() {
			t.Errorf("%s: in flight = %+v after the final notification, want nothing", name, got)
		}
	}
}

// The interim wording is matched only in Claude Code's own text: the <note>
// and the summary after the agent's quoted description. An agent whose
// description happens to hold the wording still ends on its final
// notification, and still awaits on a real interim one.
func TestScanTranscriptBackground_InterimWordingInDescriptionIsFinal(t *testing.T) {
	const desc = `check waiting on its own background work`
	launch := []string{
		`{"type":"assistant","isSidechain":false,"message":{"content":[{"type":"tool_use","id":"ta1","name":"Agent","input":{"description":"` + desc + `"}}]}}`,
		`{"type":"user","isSidechain":false,"toolUseResult":{"status":"async_launched","agentId":"a433","description":"` + desc + `"},"message":{"content":[{"type":"tool_result","tool_use_id":"ta1","content":"launched"}]}}`,
	}
	notify := func(body string) string {
		return `{"type":"user","isSidechain":false,"timestamp":"2026-10-21T23:29:42Z","message":{"content":"<task-notification>\n<task-id>a433</task-id>\n<status>completed</status>\n` + body + `\n</task-notification>"}}`
	}
	finalNote := `<note>A task-notification fires each time this agent stops with no live background children of its own. The user can send it another message and resume it, so the same task-id may notify more than once.</note>`
	for name, final := range map[string]string{
		"final summary":           notify(`<summary>Agent \"` + desc + `\" finished</summary>\n<result>done</result>`),
		"final summary and note":  notify(`<summary>Agent \"` + desc + `\" finished</summary>\n` + finalNote + `\n<result>done</result>`),
		"truncated final summary": `{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>a433</task-id>\n<status>completed</status>\n<summary>Agent \"` + desc + `\" fini"}`,
	} {
		sc := scanTranscriptBackground(append(append([]string{}, launch...), final))
		if a, ok := sc.awaitingAgent(); ok {
			t.Errorf("%s: final notification of an agent described %q left it awaiting: %+v", name, desc, a)
		}
		if got, _ := sc.inFlight(); got.InFlight() {
			t.Errorf("%s: in flight = %+v after the final notification, want nothing", name, got)
		}
	}
	for name, interim := range map[string]string{
		"interim summary (2.1.307)": notify(`<summary>Agent \"` + desc + `\" is waiting on its own background work</summary>\n<result>still sleeping</result>`),
		"interim note (2.1.288)":    notify(`<summary>Agent \"` + desc + `\" finished</summary>\n<note>This agent stopped with background work of its own still running. It may resume on its own when that work completes or reports, and the same task-id notifies again if it does; the result below may be interim.</note>\n<result>still sleeping</result>`),
	} {
		sc := scanTranscriptBackground(append(append([]string{}, launch...), interim))
		if _, ok := sc.awaitingAgent(); !ok {
			t.Errorf("%s: interim notification of an agent described %q did not leave it awaiting", name, desc)
		}
	}
}

// A new Claude process starts with no background agents: an interim
// notification for an agent the old process launched does not revive it.
func TestScanTranscriptBackground_InterimAfterNewProcessDoesNotRevive(t *testing.T) {
	lines := []string{
		`{"type":"assistant","isSidechain":false,"message":{"content":[{"type":"tool_use","id":"ta1","name":"Agent","input":{"description":"Background sleep probe"}}]}}`,
		`{"type":"user","isSidechain":false,"toolUseResult":{"status":"async_launched","agentId":"a433","description":"Background sleep probe"},"message":{"content":[{"type":"tool_result","tool_use_id":"ta1","content":"launched"}]}}`,
		"",
		`{"type":"user","isSidechain":false,"timestamp":"2026-10-21T23:26:10Z","message":{"content":"<task-notification>\n<task-id>a433</task-id>\n<status>completed</status>\n<summary>Agent \"Background sleep probe\" is waiting on its own background work</summary>\n<result>still sleeping</result>\n</task-notification>"}}`,
	}
	for _, hook := range []string{"SessionStart:startup", "SessionStart:resume"} {
		lines[2] = `{"type":"attachment","isSidechain":false,"attachment":{"type":"hook_success","hookName":"` + hook + `"}}`
		sc := scanTranscriptBackground(lines)
		if got, _ := sc.inFlight(); got.InFlight() {
			t.Errorf("%s: interim notification revived an agent of the previous process: %+v", hook, got)
		}
	}
}
