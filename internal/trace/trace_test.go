package trace

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// t0 is a fixed base time so durations are exact.
var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func ctxAt(tool string, level Level, offMS int) HookContext {
	return HookContext{Tool: tool, Instance: "inst-1", Profile: "default", Host: "box", At: t0.Add(time.Duration(offMS) * time.Millisecond), Level: level}
}

// spool normalizes each (payload, offset) and appends it under dir.
func spool(t *testing.T, dir, tool string, level Level, steps []step) {
	t.Helper()
	for i, s := range steps {
		rec, ok := NormalizeHook([]byte(s.payload), ctxAt(tool, level, s.at))
		if !ok {
			t.Fatalf("step %d: payload not normalized: %s", i, s.payload)
		}
		if err := AppendSpool(dir, rec); err != nil {
			t.Fatalf("step %d: append: %v", i, err)
		}
	}
}

type step struct {
	at      int
	payload string
}

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := Open(DBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func ingest(t *testing.T, st *Store, dir string) IngestResult {
	t.Helper()
	res, err := st.Ingest(dir, IngestOptions{Grace: -1})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

type spanRow struct {
	ID, Parent, Kind, Name, Status string
	Started, Ended, Dur            sql.NullInt64
	Attrs                          sql.NullString
}

func spans(t *testing.T, st *Store) []spanRow {
	t.Helper()
	rows, err := st.DB().Query(`SELECT span_id, COALESCE(parent_span_id,''), kind, COALESCE(name,''), status, started_at, ended_at, duration_ms, attrs FROM spans ORDER BY started_at, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []spanRow
	for rows.Next() {
		var r spanRow
		if err := rows.Scan(&r.ID, &r.Parent, &r.Kind, &r.Name, &r.Status, &r.Started, &r.Ended, &r.Dur, &r.Attrs); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func count(t *testing.T, st *Store, table string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Claude Code hook payloads (shape per Claude Code's hooks reference; the
// fields agent-deck's hook-handler already decodes, plus tool fields).
var claudeSession = []step{
	{0, `{"hook_event_name":"SessionStart","session_id":"sess-c1","source":"startup","model":"claude-opus-4-5"}`},
	{1000, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-c1","prompt":"fix the build"}`},
	{1100, `{"hook_event_name":"PreToolUse","session_id":"sess-c1","tool_name":"Bash","tool_use_id":"toolu_01","tool_input":{"command":"go build ./...","description":"build"}}`},
	{1350, `{"hook_event_name":"PostToolUse","session_id":"sess-c1","tool_name":"Bash","tool_use_id":"toolu_01","tool_input":{"command":"go build ./..."},"tool_response":{"stdout":"ok","stderr":"","interrupted":false}}`},
	{1400, `{"hook_event_name":"PreToolUse","session_id":"sess-c1","tool_name":"Edit","tool_use_id":"toolu_02","tool_input":{"file_path":"/repo/main.go","old_string":"a","new_string":"bb"}}`},
	{1410, `{"hook_event_name":"Notification","session_id":"sess-c1","matcher":"permission_prompt","message":"Claude needs your permission"}`},
	{3410, `{"hook_event_name":"PostToolUseFailure","session_id":"sess-c1","tool_name":"Edit","tool_use_id":"toolu_02","tool_input":{"file_path":"/repo/main.go"},"error":"old_string not found"}`},
	{3500, `{"hook_event_name":"PreToolUse","session_id":"sess-c1","tool_name":"Read","tool_input":{"file_path":"/repo/a.go"}}`},
	{3530, `{"hook_event_name":"PostToolUse","session_id":"sess-c1","tool_name":"Read","tool_input":{"file_path":"/repo/a.go"},"tool_response":{"type":"text"}}`},
	{4000, `{"hook_event_name":"Stop","session_id":"sess-c1","stop_hook_active":false,"last_assistant_message":"done"}`},
}

func TestTraceNormalizeClaudePayloads(t *testing.T) {
	want := []struct{ kind, toolName, status string }{
		{KindSessionStart, "", ""},
		{KindTurnStart, "", ""},
		{KindToolStart, "Bash", ""},
		{KindToolEnd, "Bash", StatusOK},
		{KindToolStart, "Edit", ""},
		{KindWaitStart, "", ""},
		{KindToolEnd, "Edit", StatusError},
		{KindToolStart, "Read", ""},
		{KindToolEnd, "Read", StatusOK},
		{KindTurnEnd, "", ""},
	}
	for i, s := range claudeSession {
		rec, ok := NormalizeHook([]byte(s.payload), ctxAt("", LevelSummaries, s.at))
		if !ok {
			t.Fatalf("%d: not normalized", i)
		}
		if rec.Tool != "claude" || rec.SessionID != "sess-c1" || rec.Instance != "inst-1" || rec.ID == "" || rec.At != t0.Add(time.Duration(s.at)*time.Millisecond).UnixMilli() {
			t.Fatalf("%d: base fields %+v", i, rec)
		}
		if rec.Kind != want[i].kind || rec.ToolName != want[i].toolName || rec.Status != want[i].status {
			t.Fatalf("%d: got kind=%s tool=%s status=%s, want %+v", i, rec.Kind, rec.ToolName, rec.Status, want[i])
		}
	}
	rec, _ := NormalizeHook([]byte(claudeSession[2].payload), ctxAt("", LevelSummaries, 0))
	if rec.Summary != "cmd=go len=14" || rec.ToolUseID != "toolu_01" {
		t.Fatalf("Bash summary: %+v", rec)
	}
	rec, _ = NormalizeHook([]byte(claudeSession[4].payload), ctxAt("", LevelSummaries, 0))
	if rec.Summary != "file=main.go old=1 new=2" {
		t.Fatalf("Edit summary: %q", rec.Summary)
	}
	rec, _ = NormalizeHook([]byte(claudeSession[1].payload), ctxAt("", LevelSummaries, 0))
	if rec.Summary != "prompt_len=13" {
		t.Fatalf("prompt summary: %q", rec.Summary)
	}
	// Metadata level carries no summaries at all.
	rec, _ = NormalizeHook([]byte(claudeSession[2].payload), ctxAt("", LevelMetadata, 0))
	if rec.Summary != "" || rec.ToolName != "Bash" {
		t.Fatalf("metadata level: %+v", rec)
	}
	// Other tools: key names only.
	rec, _ = NormalizeHook([]byte(`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"mcp__gh__search","tool_input":{"query":"secret plans","limit":3}}`), ctxAt("", LevelSummaries, 0))
	if rec.Summary != "keys=limit,query" {
		t.Fatalf("generic summary: %q", rec.Summary)
	}
}

func TestTraceIngestClaudeSessionRows(t *testing.T) {
	dir := t.TempDir()
	spool(t, dir, "claude", LevelSummaries, claudeSession)
	st := openStore(t, dir)
	res := ingest(t, st, dir)
	if res.Applied != len(claudeSession) || res.Bad != 0 || res.Removed != 1 {
		t.Fatalf("ingest result %+v", res)
	}
	if n := count(t, st, "traces"); n != 1 {
		t.Fatalf("traces = %d", n)
	}
	if n := count(t, st, "events"); n != len(claudeSession) {
		t.Fatalf("events = %d", n)
	}
	var traceID, tool, model string
	if err := st.DB().QueryRow(`SELECT trace_id, tool, json_extract(extras,'$.model') FROM traces`).Scan(&traceID, &tool, &model); err != nil {
		t.Fatal(err)
	}
	if traceID != "claude:sess-c1" || tool != "claude" || model != "claude-opus-4-5" {
		t.Fatalf("trace row %s %s %s", traceID, tool, model)
	}
	sp := spans(t, st)
	byName := map[string]spanRow{}
	var turn spanRow
	for _, s := range sp {
		if s.Kind == SpanTurn {
			turn = s
		}
		byName[s.Kind+"/"+s.Name] = s
	}
	if len(sp) != 5 {
		t.Fatalf("spans = %d: %+v", len(sp), sp)
	}
	if turn.Status != StatusOK || turn.Dur.Int64 != 3000 {
		t.Fatalf("turn span %+v", turn)
	}
	bash := byName["tool_call/Bash"]
	if bash.Dur.Int64 != 250 || bash.Status != StatusOK || bash.Parent != turn.ID {
		t.Fatalf("bash span %+v", bash)
	}
	edit := byName["tool_call/Edit"]
	if edit.Dur.Int64 != 2010 || edit.Status != StatusError {
		t.Fatalf("edit span %+v", edit)
	}
	read := byName["tool_call/Read"] // no tool_use_id: paired by name + order
	if read.Dur.Int64 != 30 || read.Status != StatusOK {
		t.Fatalf("read span %+v", read)
	}
	wait := byName["wait/permission_prompt"]
	if wait.Dur.Int64 != 2000 || wait.Parent != turn.ID {
		t.Fatalf("wait span %+v", wait)
	}
}

// Codex hooks use the same hook-handler shape plus turn_id; codex-notify
// sends agent-turn-complete for the same turn.
func TestTraceCodexHookAndNotifyRows(t *testing.T) {
	dir := t.TempDir()
	steps := []step{
		{0, `{"hook_event_name":"SessionStart","session_id":"thr-9","turn_id":"t-1","model":"gpt-5-codex","source":"startup"}`},
		{100, `{"hook_event_name":"UserPromptSubmit","session_id":"thr-9","turn_id":"t-1","prompt":"run tests"}`},
		{200, `{"hook_event_name":"PreToolUse","session_id":"thr-9","turn_id":"t-1","tool_name":"shell","tool_use_id":"call_A","tool_input":{"command":["bash","-lc","go test ./..."]}}`},
		{900, `{"hook_event_name":"PostToolUse","session_id":"thr-9","turn_id":"t-1","tool_name":"shell","tool_use_id":"call_A","tool_response":{"exit_code":1}}`},
		{950, `{"hook_event_name":"PreToolUse","session_id":"thr-9","turn_id":"t-1","tool_name":"apply_patch","tool_use_id":"call_B","tool_input":{"input":"*** Begin Patch\n*** Update File: a.go\n*** Add File: b.go\n*** End Patch"}}`},
		{1000, `{"hook_event_name":"PostToolUse","session_id":"thr-9","turn_id":"t-1","tool_name":"apply_patch","tool_use_id":"call_B","tool_response":"Success"}`},
		{1200, `{"hook_event_name":"Stop","session_id":"thr-9","turn_id":"t-1"}`},
	}
	spool(t, dir, "", LevelSummaries, steps) // tool detected from turn_id
	rec, ok := NormalizeHook([]byte(steps[2].payload), ctxAt("", LevelSummaries, 0))
	if !ok || rec.Tool != "codex" || rec.Summary != "cmd=go len=13" || rec.TurnID != "t-1" {
		t.Fatalf("codex shell normalize %+v", rec)
	}
	rec, _ = NormalizeHook([]byte(steps[4].payload), ctxAt("", LevelSummaries, 0))
	if rec.Summary != "files=2 bytes=70" {
		t.Fatalf("apply_patch summary %q", rec.Summary)
	}
	// codex-notify for the same turn, after the Stop hook: must not add a turn.
	nrec, ok := NormalizeCodexNotify("agent-turn-complete", "thr-9", "t-1", false, true, ctxAt("codex", LevelMetadata, 1250))
	if !ok || nrec.Kind != KindTurnEnd {
		t.Fatalf("notify normalize %+v %v", nrec, ok)
	}
	if err := AppendSpool(dir, nrec); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, dir)
	ingest(t, st, dir)
	sp := spans(t, st)
	if len(sp) != 3 {
		t.Fatalf("spans %+v", sp)
	}
	if sp[0].Kind != SpanTurn || sp[0].ID != "turn:codex:thr-9:t-1" || sp[0].Dur.Int64 != 1100 || sp[0].Status != StatusOK {
		t.Fatalf("turn %+v", sp[0])
	}
	if sp[1].Name != "shell" || sp[1].Dur.Int64 != 700 || sp[1].Status != StatusError {
		t.Fatalf("shell %+v", sp[1])
	}
	if sp[2].Name != "apply_patch" || sp[2].Dur.Int64 != 50 || sp[2].Status != StatusOK {
		t.Fatalf("apply_patch %+v", sp[2])
	}
	// Notify-only Codex (no hooks): a turn end without a start still lands.
	dir2 := t.TempDir()
	nrec2, _ := NormalizeCodexNotify("agent-turn-complete", "thr-10", "", false, true, ctxAt("codex", LevelMetadata, 0))
	_ = AppendSpool(dir2, nrec2)
	st2 := openStore(t, dir2)
	ingest(t, st2, dir2)
	sp2 := spans(t, st2)
	if len(sp2) != 1 || sp2[0].Kind != SpanTurn || sp2[0].Status != StatusOK {
		t.Fatalf("notify-only turn %+v", sp2)
	}
	if _, ok := NormalizeCodexNotify("thread.started", "thr-10", "", false, false, ctxAt("codex", LevelMetadata, 0)); ok {
		t.Fatal("non-turn notify must not trace")
	}
}

// Post arrives (in the spool) before Pre — async hooks do not order — and
// still pairs by tool_use_id, with the right duration.
func TestTracePrePostPairingOutOfOrder(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	spool(t, dir, "claude", LevelMetadata, []step{
		{0, `{"hook_event_name":"UserPromptSubmit","session_id":"s2","prompt":"x"}`},
		{500, `{"hook_event_name":"PostToolUse","session_id":"s2","tool_name":"Grep","tool_use_id":"toolu_x","tool_response":{}}`},
	})
	ingest(t, st, dir) // Post lands in an earlier batch than Pre
	spool(t, dir, "claude", LevelMetadata, []step{
		{120, `{"hook_event_name":"PreToolUse","session_id":"s2","tool_name":"Grep","tool_use_id":"toolu_x","tool_input":{"pattern":"p"}}`},
	})
	ingest(t, st, dir)
	sp := spans(t, st)
	var g spanRow
	for _, s := range sp {
		if s.Name == "Grep" {
			g = s
		}
	}
	if g.Dur.Int64 != 380 || g.Status != StatusOK || strings.Contains(g.Attrs.String, `"start"`) {
		t.Fatalf("grep span %+v", g)
	}
	if n := count(t, st, "spans"); n != 2 {
		t.Fatalf("spans = %d", n)
	}
}

// Claude's install does not subscribe PostToolUse today: a call's end is
// inferred at the next call / turn end and marked as such.
func TestTraceInferredEndWithoutPost(t *testing.T) {
	dir := t.TempDir()
	spool(t, dir, "claude", LevelMetadata, []step{
		{0, `{"hook_event_name":"UserPromptSubmit","session_id":"s3","prompt":"x"}`},
		{10, `{"hook_event_name":"PreToolUse","session_id":"s3","tool_name":"Bash","tool_use_id":"a","tool_input":{"command":"ls"}}`},
		{110, `{"hook_event_name":"PreToolUse","session_id":"s3","tool_name":"Bash","tool_use_id":"b","tool_input":{"command":"ls"}}`},
		{410, `{"hook_event_name":"Stop","session_id":"s3"}`},
	})
	st := openStore(t, dir)
	ingest(t, st, dir)
	sp := spans(t, st)
	if len(sp) != 3 || sp[1].Dur.Int64 != 100 || sp[2].Dur.Int64 != 300 || sp[1].Status != StatusUnset ||
		!strings.Contains(sp[1].Attrs.String, `"end":"inferred"`) {
		t.Fatalf("inferred spans %+v", sp)
	}
	stats, err := st.Stats(StatsOptions{})
	if err != nil || len(stats) != 1 || stats[0].Inferred != 2 || stats[0].TotalMS != 0 {
		t.Fatalf("stats over inferred ends %+v %v", stats, err)
	}
}

func TestTraceStatsCountP95ErrorRate(t *testing.T) {
	dir := t.TempDir()
	steps := []step{{0, `{"hook_event_name":"UserPromptSubmit","session_id":"s4","prompt":"x"}`}}
	at := 10
	for i := 1; i <= 20; i++ {
		id := "id" + string(rune('a'+i))
		steps = append(steps, step{at, `{"hook_event_name":"PreToolUse","session_id":"s4","tool_name":"Bash","tool_use_id":"` + id + `","tool_input":{"command":"x"}}`})
		resp := `{}`
		if i%5 == 0 {
			resp = `{"is_error":true}`
		}
		steps = append(steps, step{at + i*10, `{"hook_event_name":"PostToolUse","session_id":"s4","tool_name":"Bash","tool_use_id":"` + id + `","tool_response":` + resp + `}`})
		at += i*10 + 1
	}
	spool(t, dir, "claude", LevelMetadata, steps)
	st := openStore(t, dir)
	ingest(t, st, dir)
	stats, err := st.Stats(StatsOptions{Tool: "claude"})
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	s := stats[0]
	if s.Tool != "Bash" || s.Count != 20 || s.Errors != 4 || s.ErrorRate != 0.2 || s.TotalMS != 2100 || s.P95MS != 190 || s.MaxMS != 200 {
		t.Fatalf("stat %+v", s)
	}
	if none, _ := st.Stats(StatsOptions{Tool: "codex"}); len(none) != 0 {
		t.Fatalf("tool filter: %+v", none)
	}
	tree, err := st.Tree("claude:s4")
	if err != nil || len(tree) != 1 || len(tree[0].Children) != 20 {
		t.Fatalf("tree %v %+v", err, tree)
	}
}

func TestTraceIngestIdempotent(t *testing.T) {
	dir := t.TempDir()
	spool(t, dir, "claude", LevelSummaries, claudeSession)
	live, err := os.ReadFile(SpoolPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	st := openStore(t, dir)
	first := ingest(t, st, dir)
	before := map[string]int{"traces": count(t, st, "traces"), "spans": count(t, st, "spans"), "events": count(t, st, "events")}
	snapshot := spans(t, st)

	// Replay the exact same lines (a crash between commit and spool removal).
	if err := os.WriteFile(SpoolPath(dir), live, 0o600); err != nil {
		t.Fatal(err)
	}
	second := ingest(t, st, dir)
	if first.Applied != len(claudeSession) || second.Applied != 0 || second.Duplicate != len(claudeSession) {
		t.Fatalf("first %+v second %+v", first, second)
	}
	for table, n := range before {
		if got := count(t, st, table); got != n {
			t.Fatalf("%s: %d after replay, want %d", table, got, n)
		}
	}
	after := spans(t, st)
	if len(after) != len(snapshot) {
		t.Fatalf("spans changed on replay")
	}
	for i := range after {
		if after[i] != snapshot[i] {
			t.Fatalf("span %d changed on replay: %+v -> %+v", i, snapshot[i], after[i])
		}
	}
	// Empty spool: a no-op.
	if res := ingest(t, st, dir); res.Lines != 0 || res.Applied != 0 {
		t.Fatalf("empty ingest %+v", res)
	}
	// A torn/garbage line is skipped, not fatal.
	if err := os.WriteFile(SpoolPath(dir), []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := ingest(t, st, dir); res.Bad != 1 {
		t.Fatalf("bad line %+v", res)
	}
}

// A rotated spool file that is still being written (not quiet for the grace
// period) is kept and re-read next time; replay applies nothing twice.
func TestTraceIngestKeepsRecentSpoolUntilQuiet(t *testing.T) {
	dir := t.TempDir()
	spool(t, dir, "claude", LevelMetadata, claudeSession[:2])
	st := openStore(t, dir)
	res, err := st.Ingest(dir, IngestOptions{Grace: time.Hour})
	if err != nil || res.Applied != 2 || res.Removed != 0 {
		t.Fatalf("first %+v %v", res, err)
	}
	res, err = st.Ingest(dir, IngestOptions{Grace: -1})
	if err != nil || res.Applied != 0 || res.Duplicate != 2 || res.Removed != 1 {
		t.Fatalf("second %+v %v", res, err)
	}
}

func TestTraceLevelOffWritesNothing(t *testing.T) {
	for _, s := range claudeSession {
		if _, ok := NormalizeHook([]byte(s.payload), ctxAt("claude", LevelOff, 0)); ok {
			t.Fatalf("level off normalized %s", s.payload)
		}
	}
	if _, ok := NormalizeCodexNotify("agent-turn-complete", "x", "", false, true, ctxAt("codex", LevelOff, 0)); ok {
		t.Fatal("level off normalized codex notify")
	}
	for in, want := range map[string]Level{"": LevelOff, "off": LevelOff, "bogus": LevelOff, "metadata": LevelMetadata, "SUMMARIES": LevelSummaries} {
		if got := ParseLevel(in); got != want {
			t.Fatalf("ParseLevel(%q) = %v", in, got)
		}
	}
	if ResolveLevel("off", "summaries") != LevelOff || ResolveLevel("", "metadata") != LevelMetadata {
		t.Fatal("env must win over config")
	}
}

// Sentinel secrets planted in every free-form field of every payload shape.
const (
	sentAnt = "sk-ant-SENTINEL123456789abcdef"
	sentGH  = "ghp_SENTINEL0123456789abcdefABCDEFxyz"
	sentAWS = "AKIASENTINEL12345678"
)

func sentinelPayloads() []step {
	return []step{
		{0, `{"hook_event_name":"SessionStart","session_id":"s5","model":"` + sentAnt + `"}`},
		{1, `{"hook_event_name":"UserPromptSubmit","session_id":"s5","prompt":"use key ` + sentAnt + ` and ` + sentGH + `"}`},
		{2, `{"hook_event_name":"PreToolUse","session_id":"s5","tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"` + sentAnt + ` --flag"}}`},
		{3, `{"hook_event_name":"PostToolUse","session_id":"s5","tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"curl -H 'Authorization: Bearer ` + sentGH + `' https://x"},"tool_response":{"stdout":"` + sentAnt + `"}}`},
		{4, `{"hook_event_name":"PreToolUse","session_id":"s5","tool_name":"Bash","tool_use_id":"t2","tool_input":{"command":"/usr/bin/` + sentGH + ` x"}}`},
		{5, `{"hook_event_name":"PreToolUse","session_id":"s5","tool_name":"Write","tool_use_id":"t3","tool_input":{"file_path":"/tmp/` + sentGH + `.env","content":"TOKEN=` + sentAnt + `"}}`},
		{6, `{"hook_event_name":"PreToolUse","session_id":"s5","tool_name":"mcp__x__call","tool_use_id":"t4","tool_input":{"` + sentAnt + `":"v","password":"` + sentAWS + `"}}`},
		{7, `{"hook_event_name":"PreToolUse","session_id":"` + sentAnt + `","tool_name":"` + sentGH + `","tool_use_id":"` + sentAWS + `","tool_input":{"command":"x"}}`},
		{8, `{"hook_event_name":"PreToolUse","session_id":"s5","turn_id":"t-1","tool_name":"shell","tool_use_id":"t5","tool_input":{"command":["bash","-lc","` + sentGH + ` push"]}}`},
		{9, `{"hook_event_name":"PostToolUseFailure","session_id":"s5","tool_name":"Edit","tool_use_id":"t6","tool_input":{"file_path":"/a/` + sentAWS + `"},"error":"` + sentAnt + `"}`},
		{10, `{"hook_event_name":"Stop","session_id":"s5","last_assistant_message":"` + sentAnt + `"}`},
	}
}

func TestRedactSentinelNeverInSpoolOrTraceDB(t *testing.T) {
	dir := t.TempDir()
	spool(t, dir, "", LevelSummaries, sentinelPayloads())
	nrec, _ := NormalizeCodexNotify("agent-turn-complete", sentAnt, sentGH, false, true, ctxAt("codex", LevelSummaries, 11))
	_ = AppendSpool(dir, nrec)

	sentinels := []string{"SENTINEL", sentAnt, sentGH, sentAWS}
	assertClean := func(label string) {
		t.Helper()
		files := 0
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			files++
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				t.Fatalf("%s: read %s: %v", label, p, rerr)
			}
			for _, s := range sentinels {
				if bytes.Contains(b, []byte(s)) {
					t.Fatalf("%s: sentinel %q found in %s", label, s[:8], filepath.Base(p))
				}
			}
			return nil
		})
		if files == 0 {
			t.Fatalf("%s: no files written", label)
		}
	}
	assertClean("spool")

	// Keep the spool (grace) so the check covers spool, db, wal and shm.
	st, err := Open(DBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.Ingest(dir, IngestOptions{Grace: time.Hour})
	if err != nil || res.Applied != 12 {
		t.Fatalf("ingest %+v %v", res, err)
	}
	assertClean("trace.db with WAL open")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	assertClean("trace.db after checkpoint")
}

func TestRedactScrubPatterns(t *testing.T) {
	cases := map[string]string{
		"key " + sentAnt:                             "key [redacted]",
		"Authorization: Bearer abc.def.ghi-123":      "Authorization: Bearer [redacted]",
		"password=hunter2 next":                      "password=[redacted] next",
		"API_KEY: 'zzz'":                             "API_KEY: '[redacted]'",
		sentAWS:                                      "[redacted]",
		"x " + strings.Repeat("ab12", 10) + " y":     "x [redacted] y",
		"eyJhbGciOiJ.eyJzdWIiOiIx.SflKxwRJSMeKKF2QT": "[redacted]",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----": "[redacted]",
		"go build ./...": "go build ./...",
		"toolu_01ABC":    "toolu_01ABC",
		"sess-8f3c2a1e-0000-4000-8000-000000000001": "sess-8f3c2a1e-0000-4000-8000-000000000001",
	}
	for in, want := range cases {
		if got := Scrub(in); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

// "No measurable slowdown of the hook handler": the hook's whole tracing
// work is NormalizeHook + AppendSpool. Median over 200 runs must stay well
// under 1ms (it is tens of microseconds on a laptop).
func TestSpoolAppendMedianUnderOneMillisecond(t *testing.T) {
	dir := t.TempDir()
	payload := []byte(claudeSession[2].payload)
	const n = 200
	d := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		rec, ok := NormalizeHook(payload, HookContext{Tool: "claude", Instance: "inst", Level: LevelSummaries})
		if !ok || AppendSpool(dir, rec) != nil {
			t.Fatal("append failed")
		}
		d[i] = time.Since(start)
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	median := d[n/2]
	t.Logf("normalize+append: median %v p95 %v max %v", median, d[n*95/100], d[n-1])
	if median >= time.Millisecond {
		t.Fatalf("median normalize+append %v >= 1ms", median)
	}
	b, _ := os.ReadFile(SpoolPath(dir))
	if lines := bytes.Count(b, []byte("\n")); lines != n {
		t.Fatalf("spool lines %d, want %d", lines, n)
	}
}

func BenchmarkSpoolAppend(b *testing.B) {
	dir := b.TempDir()
	rec, _ := NormalizeHook([]byte(claudeSession[2].payload), HookContext{Tool: "claude", Instance: "inst", Level: LevelSummaries})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = AppendSpool(dir, rec)
	}
}

func BenchmarkTraceHookPath(b *testing.B) {
	dir := b.TempDir()
	payload := []byte(claudeSession[3].payload)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec, _ := NormalizeHook(payload, HookContext{Tool: "claude", Instance: "inst", Level: LevelSummaries})
		_ = AppendSpool(dir, rec)
	}
}
