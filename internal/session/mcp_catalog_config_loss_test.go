package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Regression tests for issue #1956, at the session layer.
//
// SCOPE (after #1957 landed as 9d330898). #1957 fixed the fail-closed read for
// the four .claude.json writers, added the shared config-file lock, and covers
// the MALFORMED-JSON shape at the web layer across every scope × operation
// (internal/web/handlers_mcps_corrupt_config_test.go). None of that is
// duplicated here.
//
// What was still live on main after it, and is pinned here:
//
//   - `<project>/.mcp.json`, the FIFTH writer path. readExistingLocalMCPServers
//     returned nil for a read error and a parse error alike and the caller wrote
//     the file back from that nil — the original defect, untouched, in the one
//     file that lives in the user's own repository.
//   - a `null` root, the single input that walks past the parse check because
//     json.Unmarshal("null", &map) succeeds and yields a nil map.
//   - a `projects` value that is not an object. refuseDroppingTopLevelKeys
//     compares top-level KEY SETS, so rewriting the VALUE of a key present in
//     both documents is invisible to it while every entry under it is lost.
//   - a file that exists but cannot be READ. Covered by #1957's read helper but
//     untested there, so pinned rather than assumed.
//   - concurrent writers. Covered by #1957's lock; pinned here because the end
//     state cannot distinguish a lock held across the whole read-modify-write
//     from one taken and dropped per write.
//
// Each asserts the same two things, because either alone is insufficient: the
// operation must report an error, AND the file must be byte-for-byte unchanged.

// seededClaudeConfig is a realistic user configuration: three project entries,
// only one of which is the project being attached to, plus unrelated top-level
// settings that have nothing to do with MCP.
const seededClaudeConfig = `{
  "numStartups": 412,
  "theme": "dark",
  "oauthAccount": {"emailAddress": "user@example.com"},
  "mcpServers": {"user-level-server": {"type": "stdio", "command": "user-mcp"}},
  "projects": {
    "/home/user/projects/alpha": {
      "allowedTools": ["Bash(git:*)"],
      "hasTrustDialogAccepted": true,
      "mcpServers": {"alpha-hand-written": {"type": "stdio", "command": "alpha-mcp"}}
    },
    "/home/user/projects/beta": {
      "allowedTools": ["Read", "Edit"],
      "hasTrustDialogAccepted": true,
      "mcpServers": {"beta-server": {"type": "stdio", "command": "beta-mcp"}}
    },
    "/home/user/work/gamma": {
      "hasTrustDialogAccepted": true,
      "mcpServers": {"gamma-server": {"type": "stdio", "command": "gamma-mcp"}}
    }
  }
}
`

const attachProject = "/home/user/projects/alpha"

// claudeConfigSandbox points HOME and CLAUDE_CONFIG_DIR at a fresh temp dir and
// returns the path of the .claude.json inside it. Nothing here can reach the
// developer's real Claude configuration.
func claudeConfigSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create sandbox config dir: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	configFile := filepath.Join(configDir, ".claude.json")
	if got := filepath.Join(GetClaudeConfigDir(), ".claude.json"); got != configFile {
		t.Fatalf("sandbox not in effect: resolved config %q, want %q", got, configFile)
	}
	return configFile
}

// assertRefusedAndUnchanged is the whole contract in one place: the write must
// fail loudly AND leave the bytes alone.
func assertRefusedAndUnchanged(t *testing.T, configFile string, before []byte, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("WriteProjectMCP returned nil error; it must refuse rather than rebuild the config from scratch")
	}
	after, readErr := os.ReadFile(configFile)
	if readErr != nil {
		t.Fatalf("read config after write: %v", readErr)
	}
	if string(after) != string(before) {
		t.Errorf("config was modified despite an unreadable/unparseable source.\n--- before (%d bytes) ---\n%s\n--- after (%d bytes) ---\n%s",
			len(before), before, len(after), after)
	}
}

// TestWriteProjectMCP_NonObjectRootIsNotAnEmptyConfig covers a root that is not
// a JSON object. json.Unmarshal into a map reports an error for an array or a
// string, so these are the shapes a parse guard catches.
//
// The `null` root has its own test below: it unmarshals into a nil map WITHOUT
// an error, so it is the one shape a parse check cannot catch.
func TestWriteProjectMCP_NonObjectRootIsNotAnEmptyConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"array root", `["not", "an", "object"]`},
		{"string root", `"just a string"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configFile := claudeConfigSandbox(t)
			body := []byte(tc.body)
			if err := os.WriteFile(configFile, body, 0o600); err != nil {
				t.Fatalf("seed non-object config: %v", err)
			}

			err := WriteProjectMCP(attachProject, []string{"ctx7"})
			assertRefusedAndUnchanged(t, configFile, body, err)
		})
	}
}

// TestWriteProjectMCP_NonObjectNestedValueIsNotAnEmptyConfig covers a document
// that parses fine but whose `projects` value is not an object.
//
// This is the gap a top-level drop-guard cannot see. The guard compares the KEY
// SETS of the old and new documents; "projects" is present in both, so replacing
// its value with a fresh map containing only the attached project reads as no
// dropped keys — while every project entry the user had is gone.
func TestWriteProjectMCP_NonObjectNestedValueIsNotAnEmptyConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"projects is a string", `{"numStartups": 412, "projects": "corrupted by a bad merge"}`},
		{"projects is an array", `{"numStartups": 412, "projects": [{"/home/user/projects/beta": {}}]}`},
		{"project entry is a string", `{"projects": {"/home/user/projects/alpha": "corrupted"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configFile := claudeConfigSandbox(t)
			body := []byte(tc.body)
			if err := os.WriteFile(configFile, body, 0o600); err != nil {
				t.Fatalf("seed config: %v", err)
			}

			err := WriteProjectMCP(attachProject, []string{"ctx7"})
			assertRefusedAndUnchanged(t, configFile, body, err)
		})
	}
}

// TestWriteProjectMCP_UnreadableFileIsNotAnEmptyConfig covers the transient I/O
// failure named in the issue, modelled as a permission error — the one I/O
// failure a test can produce deterministically. The config here is perfectly
// valid; only the read fails, which is exactly the case where rebuilding from
// an empty document destroys the most, and the case a parse guard never sees
// because the parse never happens.
func TestWriteProjectMCP_UnreadableFileIsNotAnEmptyConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 does not deny reads")
	}
	configFile := claudeConfigSandbox(t)
	body := []byte(seededClaudeConfig)
	if err := os.WriteFile(configFile, body, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := os.Chmod(configFile, 0o000); err != nil {
		t.Fatalf("chmod config unreadable: %v", err)
	}
	// Restore before the test's TempDir cleanup, which must be able to remove it.
	t.Cleanup(func() { _ = os.Chmod(configFile, 0o600) })

	err := WriteProjectMCP(attachProject, []string{"ctx7"})

	if err == nil {
		t.Errorf("WriteProjectMCP returned nil error for an unreadable config")
	}
	if err := os.Chmod(configFile, 0o600); err != nil {
		t.Fatalf("chmod config back: %v", err)
	}
	after, readErr := os.ReadFile(configFile)
	if readErr != nil {
		t.Fatalf("read config after write: %v", readErr)
	}
	if string(after) != string(body) {
		t.Errorf("config was rewritten from an empty document after a failed read.\n--- before (%d bytes) ---\n%s\n--- after (%d bytes) ---\n%s",
			len(body), body, len(after), after)
	}
}

// TestWriteProjectMCP_ConcurrentWritesKeepEveryEntry is the web-plus-TUI race
// the issue calls for, and the one failure mode neither atomic replacement nor
// a drop-guard addresses.
//
// Two writers each read, modify and replace. The rename is atomic and the key
// sets match — both documents have "projects" — so the drop-guard sees nothing
// wrong while the loser's project entry is silently gone. Only a lock held
// across the whole read-modify-write prevents it.
//
// The web handler (internal/web/handlers_mcps.go) and the TUI apply path
// (internal/session/instance.go) both land here, so this is two goroutines in
// one binary as well as two processes.
//
// Rounds are repeated because the unlocked version loses an entry
// probabilistically; the repeat makes the failure reliable without making the
// locked version take more than a few milliseconds.
func TestWriteProjectMCP_ConcurrentWritesKeepEveryEntry(t *testing.T) {
	configFile := claudeConfigSandbox(t)

	writers := []string{
		"/home/user/projects/one",
		"/home/user/projects/two",
		"/home/user/projects/three",
		"/home/user/projects/four",
		"/home/user/projects/five",
		"/home/user/projects/six",
	}

	const rounds = 20
	for round := 0; round < rounds; round++ {
		if err := os.WriteFile(configFile, []byte(seededClaudeConfig), 0o600); err != nil {
			t.Fatalf("round %d: seed config: %v", round, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, len(writers))
		for i, project := range writers {
			wg.Add(1)
			go func(i int, project string) {
				defer wg.Done()
				<-start // widen the overlap: all writers enter together
				errs[i] = WriteProjectMCP(project, []string{"ctx7"})
			}(i, project)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: WriteProjectMCP(%s): %v", round, writers[i], err)
			}
		}

		data, err := os.ReadFile(configFile)
		if err != nil {
			t.Fatalf("round %d: read config: %v", round, err)
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("round %d: concurrent writes produced unparseable config: %v\n%s", round, err, data)
		}
		projects, ok := raw["projects"].(map[string]interface{})
		if !ok {
			t.Fatalf("round %d: projects map missing after concurrent writes:\n%s", round, data)
		}
		for _, project := range writers {
			if _, present := projects[project]; !present {
				t.Fatalf("round %d: entry for %s was lost — a concurrent writer overwrote it from a stale read (%d of %d writer entries survived)",
					round, project, countPresent(projects, writers), len(writers))
			}
		}
		// The three pre-existing entries must survive every round too.
		for _, project := range []string{"/home/user/projects/alpha", "/home/user/projects/beta", "/home/user/work/gamma"} {
			if _, present := projects[project]; !present {
				t.Fatalf("round %d: pre-existing entry for %s was lost:\n%s", round, project, data)
			}
		}
	}
}

func countPresent(projects map[string]interface{}, want []string) int {
	n := 0
	for _, k := range want {
		if _, ok := projects[k]; ok {
			n++
		}
	}
	return n
}

// --- pins for guards that were live but unpinned after #1957 ---------------

// TestWriteProjectMCP_NullRootIsNotAnEmptyConfig pins the one input that walks
// past the parse check: json.Unmarshal("null", &map) SUCCEEDS and yields a nil
// map, so a `null` root reaches the "start fresh" branch without ever looking
// like a parse failure. Low severity — a `null` document holds no user data —
// but it is the single hole in the fail-closed read, and nothing failed when
// the branch was reverted.
func TestWriteProjectMCP_NullRootIsNotAnEmptyConfig(t *testing.T) {
	configFile := claudeConfigSandbox(t)
	body := []byte("null")
	if err := os.WriteFile(configFile, body, 0o600); err != nil {
		t.Fatalf("seed null config: %v", err)
	}

	err := WriteProjectMCP(attachProject, []string{"ctx7"})
	assertRefusedAndUnchanged(t, configFile, body, err)
}

// TestWriteLocalMCPJSON_UnreadableOrUnparseableIsNotEmpty covers the FIFTH
// writer path, `<project>/.mcp.json`, which kept the original defect after
// #1957 fixed the four .claude.json writers: readExistingLocalMCPServers
// returned nil for a read error and for a parse error alike, and the caller
// wrote the whole file back from that nil.
//
// This one lands in the user's own repository — .mcp.json is a file they
// commit — so a transient I/O failure or a half-written file silently deleted
// every server they had declared.
func TestWriteLocalMCPJSON_UnreadableOrUnparseableIsNotEmpty(t *testing.T) {
	const seeded = `{
  "mcpServers": {
    "hand-written-one": {"command": "one-server"},
    "hand-written-two": {"command": "two-server"}
  }
}
`
	t.Run("malformed", func(t *testing.T) {
		claudeConfigSandbox(t)
		dir := t.TempDir()
		mcpFile := filepath.Join(dir, ".mcp.json")
		body := []byte(seeded[:len(seeded)/2])
		if err := os.WriteFile(mcpFile, body, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}

		err := WriteMergedMcpJSONFile(mcpFile, []string{"ctx7"}, "")
		if err == nil {
			t.Errorf("WriteMergedMcpJSONFile silently succeeded against a malformed .mcp.json")
		}
		after, readErr := os.ReadFile(mcpFile)
		if readErr != nil {
			t.Fatalf("read back: %v", readErr)
		}
		if string(after) != string(body) {
			t.Errorf(".mcp.json was rewritten from a failed parse.\n--- before (%d) ---\n%s\n--- after (%d) ---\n%s",
				len(body), body, len(after), after)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: mode 0000 does not deny reads")
		}
		claudeConfigSandbox(t)
		dir := t.TempDir()
		mcpFile := filepath.Join(dir, ".mcp.json")
		body := []byte(seeded)
		if err := os.WriteFile(mcpFile, body, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := os.Chmod(mcpFile, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(mcpFile, 0o644) })

		err := WriteMergedMcpJSONFile(mcpFile, []string{"ctx7"}, "")
		if err == nil {
			t.Errorf("WriteMergedMcpJSONFile silently succeeded against an unreadable .mcp.json")
		}
		if err := os.Chmod(mcpFile, 0o644); err != nil {
			t.Fatalf("chmod back: %v", err)
		}
		after, readErr := os.ReadFile(mcpFile)
		if readErr != nil {
			t.Fatalf("read back: %v", readErr)
		}
		if string(after) != string(body) {
			t.Errorf(".mcp.json was rewritten from a failed read — the user's own committed servers are gone.\n"+
				"--- before (%d) ---\n%s\n--- after (%d) ---\n%s", len(body), body, len(after), after)
		}
	})
}
