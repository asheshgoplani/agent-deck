package source

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func fmtInt(n int) string { return strconv.Itoa(n) }

// TestMain isolates tests from the real ~/.claude registry by pointing
// agent-deck's config-dir resolver at an empty temp dir. Individual tests
// that need their own projects/sessions layout override CLAUDE_CONFIG_DIR
// via t.Setenv.
//
// agent-hopdeck: this package now imports internal/session, whose
// GetClaudeConfigDir() transitively resolves agent-deck's own user config
// under the real HOME unless sandboxed. testutil.IsolateHome() closes that
// gap (see internal/testutil/homeenv.go — 2026-06-04 data-loss incident);
// without it, running this suite could read/write the developer's real
// ~/.agent-deck config.
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body so the deferred cleanup below
// actually runs (os.Exit does not run defers).
func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	tmp, err := os.MkdirTemp("", "agent-hopdeck-claude-config")
	if err == nil {
		os.Setenv("CLAUDE_CONFIG_DIR", tmp)
		defer os.RemoveAll(tmp)
	}
	return m.Run()
}

func writeSession(t *testing.T, dir, id, cwd, title string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".jsonl")
	body := `{"type":"user","cwd":"` + cwd + `","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"ai-title","aiTitle":"` + title + `"}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mod, mod)
}

func TestDiscoverGroupsByCwdAndSorts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	root := filepath.Join(dir, "projects")
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	writeSession(t, filepath.Join(root, "enc-a"), "s1", "/work/app", "older", base)
	writeSession(t, filepath.Join(root, "enc-a"), "s2", "/work/app", "newer", base.Add(time.Hour))
	writeSession(t, filepath.Join(root, "enc-b"), "s3", "/work/lib", "solo", base)

	tool := NewClaudeCodeTool()
	projects, err := tool.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("projects = %d, want 2", len(projects))
	}
	var app model.Project
	for _, p := range projects {
		if p.Path == "/work/app" {
			app = p
		}
	}
	if len(app.Sessions) != 2 || app.Sessions[0].ID != "s2" {
		t.Fatalf("app sessions not sorted newest-first: %+v", app.Sessions)
	}
	if got := tool.Command(app.Sessions[0], false); got != "claude --resume s2" {
		t.Errorf("Command = %q", got)
	}
}

func TestCommandForkFlag(t *testing.T) {
	tool := NewClaudeCodeTool()
	s := model.Session{ID: "abc"}
	if got := tool.Command(s, false); got != "claude --resume abc" {
		t.Errorf("plain = %q", got)
	}
	if got := tool.Command(s, true); got != "claude --resume abc --fork-session" {
		t.Errorf("fork = %q", got)
	}
}

func TestDeleteRemovesTranscript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	root := filepath.Join(dir, "projects")
	sessDir := filepath.Join(root, "enc")
	writeSession(t, sessDir, "22222222-2222-2222-2222-222222222222", "/w/a", "t", time.Now())
	tool := NewClaudeCodeTool()
	projects, _ := tool.Discover()
	s := projects[0].Sessions[0]
	if err := tool.Delete(s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
		t.Fatal("transcript not deleted")
	}
}

func TestDiscoverOverlaysRunningStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	root := filepath.Join(dir, "projects")
	sess := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	writeSession(t, filepath.Join(root, "enc"), "11111111-1111-1111-1111-111111111111", "/work/app", "live one", now)
	self := os.Getpid()
	reg := `{"pid":` + fmtInt(self) + `,"sessionId":"11111111-1111-1111-1111-111111111111","cwd":"/work/app","status":"busy"}`
	os.WriteFile(filepath.Join(sess, fmtInt(self)+".json"), []byte(reg), 0o644)

	projects, _ := NewClaudeCodeTool().Discover()
	var found bool
	for _, p := range projects {
		for _, s := range p.Sessions {
			if s.ID == "11111111-1111-1111-1111-111111111111" {
				found = true
				if s.Status != model.StatusRunningBusy || s.PID != self {
					t.Fatalf("status=%v pid=%d, want running/%d", s.Status, s.PID, self)
				}
			}
		}
	}
	if !found {
		t.Fatal("session not discovered")
	}
}

func TestDiscoverOverlaysWaitingStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	root := filepath.Join(dir, "projects")
	sess := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "22222222-2222-2222-2222-222222222222"
	writeSession(t, filepath.Join(root, "enc"), id, "/work/app", "waiter", time.Now())
	self := os.Getpid()
	reg := `{"pid":` + fmtInt(self) + `,"sessionId":"` + id + `","cwd":"/work/app","status":"waiting","waitingFor":"permission prompt"}`
	os.WriteFile(filepath.Join(sess, fmtInt(self)+".json"), []byte(reg), 0o644)

	projects, _ := NewClaudeCodeTool().Discover()
	for _, p := range projects {
		for _, s := range p.Sessions {
			if s.ID == id {
				if s.Status != model.StatusWaiting || s.WaitingFor != "permission prompt" {
					t.Fatalf("status=%v waitingFor=%q, want waiting/permission prompt", s.Status, s.WaitingFor)
				}
				return
			}
		}
	}
	t.Fatal("waiting session not discovered")
}

func TestDiscoverSkipsUnsafeSessionID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	root := filepath.Join(dir, "projects")
	now := time.Now()
	writeSession(t, filepath.Join(root, "enc"), "good-id", "/work/app", "ok", now)
	writeSession(t, filepath.Join(root, "enc"), "evil;calc", "/work/app", "bad", now)

	projects, err := NewClaudeCodeTool().Discover()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		for _, s := range p.Sessions {
			if s.ID == "evil;calc" {
				t.Fatalf("unsafe session id was not skipped: %q", s.ID)
			}
		}
	}
}
