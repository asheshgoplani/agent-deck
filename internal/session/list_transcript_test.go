package session

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestListedTranscriptPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	claude := &Instance{Tool: "claude", ProjectPath: filepath.Join(home, "project"), ClaudeSessionID: "22222222-2222-2222-2222-222222222222"}
	claudePath := ClaudeTranscriptPathForInstance(claude)
	if claudePath == "" {
		t.Fatal("Claude path missing")
	}
	codex := &Instance{Tool: "codex", CodexSessionID: "33333333-3333-3333-3333-333333333333"}
	codexPath := filepath.Join(home, ".codex", "sessions", "2026", "09", "28", "rollout-now-"+codex.CodexSessionID+".jsonl")
	for _, item := range []struct {
		inst          *Instance
		path, content string
	}{
		{claude, claudePath, "{}\n"},
		{codex, codexPath, `{"type":"session_meta","payload":{"id":"33333333-3333-3333-3333-333333333333","thread_source":"user"}}` + "\n"},
	} {
		if got := ListedTranscriptPath(item.inst); got != "" {
			t.Fatalf("missing file got %q", got)
		}
		if err := os.MkdirAll(filepath.Dir(item.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(item.path, []byte(item.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ListedTranscriptPath(item.inst); got != item.path {
			t.Fatalf("got %q want %q", got, item.path)
		}
		remote := Instance{Tool: item.inst.Tool, ProjectPath: item.inst.ProjectPath, ClaudeSessionID: item.inst.ClaudeSessionID, CodexSessionID: item.inst.CodexSessionID, SSHHost: "other-host"}
		if got := ListedTranscriptPath(&remote); got != "" {
			t.Fatalf("remote resolved locally: %q", got)
		}
	}
	if err := os.WriteFile(codexPath, []byte(`{"type":"session_meta","payload":{"thread_source":"subagent"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ListedTranscriptPath(codex); got != "" {
		t.Fatalf("subagent path %q", got)
	}
	if ListedTranscriptPath(nil) != "" || ListedTranscriptPath(&Instance{Tool: "shell"}) != "" {
		t.Fatal("unknown transcript must be omitted")
	}
}

func TestRemoteSessionTranscriptMetadata(t *testing.T) {
	for _, payload := range []string{`[{"id":"old"}]`, `[{"id":"new","claude_session_id":"claude-id","codex_session_id":"codex-id","transcript_path":"/remote/path.jsonl"}]`} {
		rows, err := parseRemoteSessions([]byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		var decoded []map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if rows[0].ID == "old" {
			if _, present := decoded[0]["transcript_path"]; present {
				t.Fatal("old core field should stay omitted")
			}
		} else if decoded[0]["transcript_path"] != "/remote/path.jsonl" || decoded[0]["claude_session_id"] != "claude-id" || decoded[0]["codex_session_id"] != "codex-id" {
			t.Fatalf("metadata lost: %s", encoded)
		}
	}
}

func TestListedTranscriptPathsEnumeratesHomesOnce(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("CODEX_HOME", filepath.Join(base, "unused"))
	var instances []*Instance
	for homeIndex := 0; homeIndex < 2; homeIndex++ {
		home := filepath.Join(base, fmt.Sprintf("codex-%d", homeIndex))
		day := filepath.Join(home, "sessions", "2026", "09", "28")
		if err := os.MkdirAll(day, 0755); err != nil {
			t.Fatal(err)
		}
		for fileIndex := 0; fileIndex < 50; fileIndex++ {
			id := fmt.Sprintf("%08d-1111-2222-3333-444444444444", fileIndex)
			path := filepath.Join(day, "rollout-now-"+id+".jsonl")
			body := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"thread_source":"user"}}`, id) + "\nnot parsed transcript body\n"
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if fileIndex < 25 {
				for duplicate := 0; duplicate < 20; duplicate++ {
					instances = append(instances, &Instance{Tool: "codex", CodexSessionID: id, Command: "CODEX_HOME=" + home + " codex"})
				}
			}
		}
		// An absent requested ID forces a full enumeration, making the count
		// independent of traversal order or early-stop behavior.
		instances = append(instances, &Instance{Tool: "codex", CodexSessionID: "ffffffff-1111-2222-3333-444444444444", Command: "CODEX_HOME=" + home + " codex"})
	}
	walks, visited, headers := 0, 0, 0
	paths := listedTranscriptPaths(instances, func(root string, fn fs.WalkDirFunc) error {
		walks++
		return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			visited++
			return fn(path, entry, err)
		})
	}, func(path string) (codexRolloutHead, bool) {
		headers++
		return readCodexRolloutHead(path)
	})
	if walks != 2 || visited != 108 || headers != 50 || len(paths) != 1000 {
		t.Fatalf("1002 sessions: walks=%d visited=%d headers=%d paths=%d", walks, visited, headers, len(paths))
	}
}

func TestListedTranscriptPathsClaudeDoesNotSearchOtherProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	inst := &Instance{Tool: "claude", ProjectPath: filepath.Join(home, "expected"), ClaudeSessionID: "22222222-2222-2222-2222-222222222222"}
	other := filepath.Join(dir, "projects", "-another-project", inst.ClaudeSessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(other), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ListedTranscriptPaths([]*Instance{inst})[inst]; got != "" {
		t.Fatalf("guessed other-project transcript: %s", got)
	}
}

func TestListedTranscriptPathsClaudePhysicalProjectFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	physical := filepath.Join(home, "physical")
	logical := filepath.Join(home, "logical")
	if err := os.MkdirAll(physical, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, logical); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "claude", ProjectPath: logical, ClaudeSessionID: "22222222-2222-2222-2222-222222222222"}
	var want string
	for _, project := range []string{logical, physical} {
		path := filepath.Join(dir, "projects", ConvertToClaudeDirName(project), inst.ClaudeSessionID+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if project == physical {
			want = path
		}
	}
	if got := ListedTranscriptPaths([]*Instance{inst})[inst]; got != want {
		t.Fatalf("got %s want physical transcript %s", got, want)
	}
}
