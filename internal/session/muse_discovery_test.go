package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Muse session discovery tests. Fixtures mirror the real store shape
// (<root>/YYYY/MM/DD/<uuid>/session.jsonl with framed + bare records),
// built with encoding/json so the escaping matches production logs.

// writeMuseFixtureSession writes one fixture session log and stamps its
// mtime. metaWS/metaProvider describe the metadata record; when metaWS is
// empty no metadata record is written (negative fixture).
func writeMuseFixtureSession(t *testing.T, root, uuid, metaWS, metaProvider string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(root, "2026", "09", "02", uuid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	var lines []string
	// A non-metadata framed line first (permission transaction shape).
	permInner, _ := json.Marshal(map[string]any{
		"schema_version": 1,
		"stream":         map[string]any{"kind": "session", "id": uuid},
		"record_type":    "event",
		"payload_type":   "runtime.session.permission_format_declared",
		"payload":        map[string]any{"format": "profile_v1"},
	})
	frame, _ := json.Marshal(map[string]any{
		"retained_frame":       "session_permission_transaction",
		"frame_schema_version": 1,
		"outer_log_ordinal":    1,
		"transaction_id":       "frame-1",
		"children":             []any{map[string]any{"child_index": 0, "record_json": string(permInner)}},
	})
	lines = append(lines, string(frame))
	if metaWS != "" {
		metaInner, _ := json.Marshal(map[string]any{
			"schema_version": 1,
			"stream":         map[string]any{"kind": "session", "id": uuid},
			"recorded_at":    1788381840883708,
			"record_type":    "event",
			"payload_type":   "runtime.session.metadata",
			"payload": map[string]any{
				"kind": "metadata",
				"record": map[string]any{
					"workspace_root": metaWS,
					"provider_id":    metaProvider,
				},
			},
		})
		metaFrame, _ := json.Marshal(map[string]any{
			"retained_frame":       "session_metadata",
			"frame_schema_version": 1,
			"outer_log_ordinal":    2,
			"transaction_id":       "frame-2",
			"children":             []any{map[string]any{"child_index": 0, "record_json": string(metaInner)}},
		})
		lines = append(lines, string(metaFrame))
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes fixture: %v", err)
	}
}

// withMuseFixtureRoot redirects discovery at a temp store for one test.
func withMuseFixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := museSessionsRootOverride
	museSessionsRootOverride = root
	t.Cleanup(func() { museSessionsRootOverride = old })
	return root
}

func TestParseMuseMetadataLine_Framed(t *testing.T) {
	root := withMuseFixtureRoot(t)
	writeMuseFixtureSession(t, root, "uuid-1", "/ws/proj", "echo", time.Now())
	data, err := os.ReadFile(filepath.Join(root, "2026", "09", "02", "uuid-1", "session.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	lines := splitMuseTestLines(string(data))
	if len(lines) != 2 {
		t.Fatalf("fixture has %d lines, want 2", len(lines))
	}
	if rec := parseMuseMetadataLine(lines[0]); rec != nil {
		t.Errorf("permission line parsed as metadata: %+v", rec)
	}
	rec := parseMuseMetadataLine(lines[1])
	if rec == nil {
		t.Fatal("metadata line returned nil")
	}
	if rec.Stream.ID != "uuid-1" {
		t.Errorf("stream id = %q, want uuid-1", rec.Stream.ID)
	}
	if rec.Payload.Record.WorkspaceRoot != "/ws/proj" {
		t.Errorf("workspace = %q, want /ws/proj", rec.Payload.Record.WorkspaceRoot)
	}
	if rec.Payload.Record.ProviderID != "echo" {
		t.Errorf("provider = %q, want echo", rec.Payload.Record.ProviderID)
	}
}

func splitMuseTestLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

func TestParseMuseMetadataLine_BareRecord(t *testing.T) {
	inner, _ := json.Marshal(map[string]any{
		"stream":       map[string]any{"kind": "session", "id": "uuid-bare"},
		"payload_type": "runtime.session.metadata",
		"payload": map[string]any{
			"record": map[string]any{"workspace_root": "/ws/bare"},
		},
	})
	rec := parseMuseMetadataLine(string(inner))
	if rec == nil {
		t.Fatal("bare metadata record returned nil")
	}
	if rec.Payload.Record.WorkspaceRoot != "/ws/bare" {
		t.Errorf("workspace = %q, want /ws/bare", rec.Payload.Record.WorkspaceRoot)
	}
}

func TestParseMuseMetadataLine_Miss(t *testing.T) {
	for _, line := range []string{"", "not json", `{"a":1}`, `{"payload_type":"other"}`} {
		if rec := parseMuseMetadataLine(line); rec != nil {
			t.Errorf("line %q parsed as metadata: %+v", line, rec)
		}
	}
}

func TestListMuseSessions(t *testing.T) {
	root := withMuseFixtureRoot(t)
	now := time.Now()
	writeMuseFixtureSession(t, root, "uuid-a", "/ws/a", "echo", now.Add(-time.Hour))
	writeMuseFixtureSession(t, root, "uuid-b", "/ws/b", "meta", now)
	writeMuseFixtureSession(t, root, "uuid-nometa", "", "", now)

	sessions := ListMuseSessions()
	if len(sessions) != 2 {
		t.Fatalf("listed %d sessions, want 2 (metadata-less excluded)", len(sessions))
	}
	// Newest first.
	if sessions[0].SessionID != "uuid-b" {
		t.Errorf("first = %q, want uuid-b (newest)", sessions[0].SessionID)
	}
	if sessions[1].WorkspaceRoot != "/ws/a" || sessions[1].ProviderID != "echo" {
		t.Errorf("second = %+v, want /ws/a echo", sessions[1])
	}
}

func TestListMuseSessions_MissingRoot(t *testing.T) {
	withMuseFixtureRoot(t)
	museSessionsRootOverride = filepath.Join(t.TempDir(), "does-not-exist")
	if got := ListMuseSessions(); len(got) != 0 {
		t.Errorf("missing root listed %d sessions, want 0", len(got))
	}
}

func TestFindLatestMuseSession(t *testing.T) {
	root := withMuseFixtureRoot(t)
	now := time.Now()
	ws := filepath.Join(root, "proj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	writeMuseFixtureSession(t, root, "uuid-old", ws, "echo", now.Add(-2*time.Hour))
	writeMuseFixtureSession(t, root, "uuid-new", ws, "echo", now.Add(-time.Hour))
	writeMuseFixtureSession(t, root, "uuid-other", filepath.Join(root, "other"), "echo", now)

	if got := FindLatestMuseSession(ws, time.Time{}); got != "uuid-new" {
		t.Errorf("latest = %q, want uuid-new", got)
	}
	if got := FindLatestMuseSession(filepath.Join(root, "other"), time.Time{}); got != "uuid-other" {
		t.Errorf("other ws = %q, want uuid-other", got)
	}
	if got := FindLatestMuseSession(filepath.Join(root, "missing"), time.Time{}); got != "" {
		t.Errorf("missing ws = %q, want empty", got)
	}
}

func TestFindLatestMuseSession_SinceBound(t *testing.T) {
	root := withMuseFixtureRoot(t)
	now := time.Now()
	ws := filepath.Join(root, "proj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	writeMuseFixtureSession(t, root, "uuid-old", ws, "echo", now.Add(-2*time.Hour))

	// Bound after the session: excluded.
	if got := FindLatestMuseSession(ws, now); got != "" {
		t.Errorf("bounded scan = %q, want empty", got)
	}
	// Bound before the session: included.
	if got := FindLatestMuseSession(ws, now.Add(-3*time.Hour)); got != "uuid-old" {
		t.Errorf("bounded scan = %q, want uuid-old", got)
	}
}

func TestFindLatestMuseSession_OverCapFindsNewest(t *testing.T) {
	root := withMuseFixtureRoot(t)
	old := time.Now().Add(-48 * time.Hour)
	wsOld := filepath.Join(root, "oldproj")
	wsNew := filepath.Join(root, "newproj")
	if err := os.MkdirAll(wsOld, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	if err := os.MkdirAll(wsNew, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	// More sessions than museDiscoveryMaxFiles, all lexically BEFORE the
	// newest UUID: a lexical-order cap would cut the newest session off.
	for i := 0; i < museDiscoveryMaxFiles+5; i++ {
		writeMuseFixtureSession(t, root, "aaa-"+padMuseTestNum(i), wsOld, "echo", old)
	}
	// Lexically last, newest by mtime: must still be found.
	writeMuseFixtureSession(t, root, "zzz-newest", wsNew, "echo", time.Now())
	if got := FindLatestMuseSession(wsNew, time.Time{}); got != "zzz-newest" {
		t.Errorf("over-cap scan = %q, want zzz-newest", got)
	}
	if got := FindLatestMuseSession(wsOld, time.Time{}); got == "" || got == "zzz-newest" {
		t.Errorf("over-cap scan for old ws = %q, want one of the old sessions", got)
	}
}

func padMuseTestNum(i int) string {
	return fmt.Sprintf("%04d", i)
}

func TestFindLatestMuseSession_SymlinkResolved(t *testing.T) {
	root := withMuseFixtureRoot(t)
	realWS := filepath.Join(root, "realproj")
	if err := os.MkdirAll(realWS, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	linkWS := filepath.Join(root, "linkproj")
	if err := os.Symlink(realWS, linkWS); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// Fixture records the resolved path (as muse does); query via symlink.
	writeMuseFixtureSession(t, root, "uuid-link", realWS, "echo", time.Now())
	if got := FindLatestMuseSession(linkWS, time.Time{}); got != "uuid-link" {
		t.Errorf("symlink query = %q, want uuid-link", got)
	}
}
