package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeClaudeSessionFile writes claudeDir/sessions/<filename> with the given
// raw JSON body (or literal garbage when body isn't JSON), for
// ClaudeSessionRecordIn tests. Unlike seedClaudeSession (claude_title_reconcile_test.go)
// this lets a test control the exact filename and full field set, including
// the #2089 socket-transport fields.
func writeClaudeSessionFile(t *testing.T, claudeDir, filename, body string) {
	t.Helper()
	dir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

func TestClaudeSessionRecordIn_FreshestWinsByUpdatedAt(t *testing.T) {
	claudeDir := t.TempDir()
	// Stale entry: earlier updatedAt, different pid/socket.
	writeClaudeSessionFile(t, claudeDir, "1111.json", `{
		"pid":1111,"sessionId":"sid-1","updatedAt":1000,
		"procStart":"stale","peerProtocol":1,
		"messagingSocketPath":"/tmp/cc-socks/1111.sock","status":"idle"
	}`)
	// Fresh entry: later updatedAt, should win.
	writeClaudeSessionFile(t, claudeDir, "2222.json", `{
		"pid":2222,"sessionId":"sid-1","updatedAt":2000,
		"procStart":"fresh","peerProtocol":1,
		"messagingSocketPath":"/tmp/cc-socks/2222.sock","status":"busy"
	}`)

	rec, ok := ClaudeSessionRecordIn(claudeDir, "sid-1")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if rec.Pid != 2222 {
		t.Errorf("Pid = %d, want 2222 (freshest by updatedAt)", rec.Pid)
	}
	if rec.ProcStart != "fresh" {
		t.Errorf("ProcStart = %q, want %q", rec.ProcStart, "fresh")
	}
	if rec.MessagingSocketPath != "/tmp/cc-socks/2222.sock" {
		t.Errorf("MessagingSocketPath = %q, want the fresh entry's path", rec.MessagingSocketPath)
	}
}

func TestClaudeSessionRecordIn_FreshestWinsByMtimeWhenUpdatedAtAbsent(t *testing.T) {
	claudeDir := t.TempDir()
	writeClaudeSessionFile(t, claudeDir, "1111.json", `{"pid":1111,"sessionId":"sid-1","procStart":"first-written"}`)
	writeClaudeSessionFile(t, claudeDir, "2222.json", `{"pid":2222,"sessionId":"sid-1","procStart":"second-written"}`)
	// Force a distinguishable mtime ordering without relying on wall-clock
	// sleeps: set the first file's mtime an hour in the past.
	past := time.Now().Add(-1 * time.Hour)
	stale := filepath.Join(claudeDir, "sessions", "1111.json")
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	rec, ok := ClaudeSessionRecordIn(claudeDir, "sid-1")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if rec.Pid != 2222 {
		t.Errorf("Pid = %d, want 2222 (freshest by mtime)", rec.Pid)
	}
}

func TestClaudeSessionRecordIn_NoSocketPath_ReturnsEmptyPath(t *testing.T) {
	claudeDir := t.TempDir()
	writeClaudeSessionFile(t, claudeDir, "3333.json", `{"pid":3333,"sessionId":"sid-2","peerProtocol":1}`)

	rec, ok := ClaudeSessionRecordIn(claudeDir, "sid-2")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if rec.MessagingSocketPath != "" {
		t.Errorf("MessagingSocketPath = %q, want empty (reader must not fabricate a path)", rec.MessagingSocketPath)
	}
	// The reader still resolves the record (ok=true, checked above); the
	// selector, not the reader, decides what to do about an empty path.
}

func TestClaudeSessionRecordIn_PeerProtocolAbsent_DefaultsZero(t *testing.T) {
	claudeDir := t.TempDir()
	writeClaudeSessionFile(t, claudeDir, "4444.json", `{"pid":4444,"sessionId":"sid-3"}`)

	rec, ok := ClaudeSessionRecordIn(claudeDir, "sid-3")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if rec.PeerProtocol != 0 {
		t.Errorf("PeerProtocol = %d, want 0 when absent from the record", rec.PeerProtocol)
	}
}

func TestClaudeSessionRecordIn_SkipsUnparseableAndBadFilenames(t *testing.T) {
	claudeDir := t.TempDir()
	// Not valid JSON.
	writeClaudeSessionFile(t, claudeDir, "5555.json", `{not json`)
	// Doesn't match the ^\d+\.json$ filename shape Claude enforces; the
	// directory scan should simply skip whatever doesn't parse as an entry
	// worth reading (extension check mirrors ClaudeSessionNameIn's).
	writeClaudeSessionFile(t, claudeDir, "not-a-pid.txt", `{"pid":9999,"sessionId":"sid-4"}`)
	// A genuinely valid entry for a DIFFERENT session id, to prove the scan
	// doesn't just accidentally match everything.
	writeClaudeSessionFile(t, claudeDir, "6666.json", `{"pid":6666,"sessionId":"sid-other"}`)

	_, ok := ClaudeSessionRecordIn(claudeDir, "sid-4")
	if ok {
		t.Fatalf("expected ok=false: only unparseable/non-.json entries exist for sid-4")
	}
}

func TestClaudeSessionRecordIn_NoMatch_ReturnsFalse(t *testing.T) {
	claudeDir := t.TempDir()
	writeClaudeSessionFile(t, claudeDir, "7777.json", `{"pid":7777,"sessionId":"sid-5"}`)

	_, ok := ClaudeSessionRecordIn(claudeDir, "sid-does-not-exist")
	if ok {
		t.Fatalf("expected ok=false for a sessionID with no matching record")
	}
}

func TestClaudeSessionRecordIn_EmptyClaudeDirOrSessionID(t *testing.T) {
	claudeDir := t.TempDir()
	writeClaudeSessionFile(t, claudeDir, "8888.json", `{"pid":8888,"sessionId":"sid-6"}`)

	if _, ok := ClaudeSessionRecordIn("", "sid-6"); ok {
		t.Errorf("expected ok=false for empty claudeDir")
	}
	if _, ok := ClaudeSessionRecordIn(claudeDir, ""); ok {
		t.Errorf("expected ok=false for empty sessionID")
	}
}

func TestClaudeSessionRecordFor_ResolvesRealHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClaudeSessionFile(t, filepath.Join(home, ".claude"), "9999.json", `{
		"pid":9999,"sessionId":"sid-7","peerProtocol":1,
		"messagingSocketPath":"/tmp/cc-socks/9999.sock"
	}`)

	rec, ok := ClaudeSessionRecordFor("sid-7")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if rec.Pid != 9999 || rec.MessagingSocketPath != "/tmp/cc-socks/9999.sock" {
		t.Errorf("unexpected record: %+v", rec)
	}
}

// TestClaudeSessionRecordsIn_ReturnsEveryMatch pins the difference from
// ClaudeSessionRecordIn: no freshest-wins rule, because the #2100 send path
// disambiguates by pane-tree membership and needs every candidate to choose
// from. Non-matching, unreadable and non-JSON files are skipped, not fatal.
func TestClaudeSessionRecordsIn_ReturnsEveryMatch(t *testing.T) {
	claudeDir := t.TempDir()
	writeClaudeSessionFile(t, claudeDir, "1111.json", `{
		"pid":1111,"sessionId":"sid-1","updatedAt":1000,
		"procStart":"a","peerProtocol":1,"messagingSocketPath":"/tmp/1111.sock"
	}`)
	writeClaudeSessionFile(t, claudeDir, "2222.json", `{
		"pid":2222,"sessionId":"sid-1","updatedAt":2000,
		"procStart":"b","peerProtocol":1,"messagingSocketPath":"/tmp/2222.sock"
	}`)
	writeClaudeSessionFile(t, claudeDir, "3333.json", `{
		"pid":3333,"sessionId":"other","updatedAt":3000,
		"procStart":"c","peerProtocol":1,"messagingSocketPath":"/tmp/3333.sock"
	}`)
	writeClaudeSessionFile(t, claudeDir, "4444.json", `not json at all`)
	writeClaudeSessionFile(t, claudeDir, "notes.txt", `{"pid":5555,"sessionId":"sid-1"}`)

	got := ClaudeSessionRecordsIn(claudeDir, "sid-1")
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(got), got)
	}
	byPid := map[int]ClaudeSessionRecord{}
	for _, rec := range got {
		byPid[rec.Pid] = rec
	}
	for _, pid := range []int{1111, 2222} {
		rec, ok := byPid[pid]
		if !ok {
			t.Fatalf("record for pid %d missing from %+v", pid, got)
		}
		if rec.SessionID != "sid-1" || rec.PeerProtocol != 1 || rec.MessagingSocketPath == "" {
			t.Errorf("record for pid %d is not fully populated: %+v", pid, rec)
		}
	}
}

// TestClaudeSessionRecordsIn_EmptyCases: nothing to return is nil, never a
// partial or fabricated record.
func TestClaudeSessionRecordsIn_EmptyCases(t *testing.T) {
	claudeDir := t.TempDir()
	writeClaudeSessionFile(t, claudeDir, "1111.json", `{"pid":1111,"sessionId":"sid-1","peerProtocol":1}`)

	if got := ClaudeSessionRecordsIn(claudeDir, "nope"); got != nil {
		t.Errorf("no matching session id: got %+v, want nil", got)
	}
	if got := ClaudeSessionRecordsIn("", "sid-1"); got != nil {
		t.Errorf("empty claudeDir: got %+v, want nil", got)
	}
	if got := ClaudeSessionRecordsIn(claudeDir, ""); got != nil {
		t.Errorf("empty sessionID: got %+v, want nil", got)
	}
	if got := ClaudeSessionRecordsIn(t.TempDir(), "sid-1"); got != nil {
		t.Errorf("unreadable sessions dir: got %+v, want nil", got)
	}
}

// TestClaudeConfigDirForSend_UsesTheInstanceChain confirms a send resolves
// records under the dir the instance actually runs in, not $HOME/.claude
// (maintainer review of #2100).
func TestClaudeConfigDirForSend_UsesTheInstanceChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accountDir := filepath.Join(t.TempDir(), "account-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", accountDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	inst := &Instance{ID: "i1", Title: "target", Tool: "claude", ClaudeSessionID: "sid-1"}
	if got := ClaudeConfigDirForSend(inst); got != accountDir {
		t.Errorf("ClaudeConfigDirForSend = %q, want the instance's dir %q", got, accountDir)
	}
	if got := ClaudeConfigDirForSend(nil); got != "" {
		t.Errorf("ClaudeConfigDirForSend(nil) = %q, want empty", got)
	}
}
