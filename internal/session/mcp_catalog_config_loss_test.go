package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Regression tests for issue #1956.
//
// WriteProjectMCP performs a read-modify-write over the user's Claude
// configuration — a file agent-deck does not own, carrying the user's other
// project entries and unrelated settings. Before the fix, a read failure or a
// parse failure was treated as "there is no configuration": the document was
// initialised to an empty map and WRITTEN BACK, erasing everything else.
//
// Every test below asserts the same two things, because either one alone is
// insufficient:
//
//  1. the operation reports an error (a silent success teaches the caller the
//     configuration is fine), and
//  2. the file on disk is byte-for-byte unchanged (an error return after the
//     damage is done is no better than no error at all).
//
// All four fail against fb6cf40a.

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

// TestWriteProjectMCP_MalformedJSONIsNotAnEmptyConfig covers the file being
// temporarily malformed — a truncated or half-written document. Before the fix
// the parse error was swallowed and the truncated file was replaced by a
// document containing only the attached MCP.
func TestWriteProjectMCP_MalformedJSONIsNotAnEmptyConfig(t *testing.T) {
	configFile := claudeConfigSandbox(t)
	truncated := []byte(seededClaudeConfig[:len(seededClaudeConfig)/2])
	if err := os.WriteFile(configFile, truncated, 0o600); err != nil {
		t.Fatalf("seed malformed config: %v", err)
	}

	err := WriteProjectMCP(attachProject, []string{"ctx7"})
	assertRefusedAndUnchanged(t, configFile, truncated, err)
}

// TestWriteProjectMCP_NonObjectRootIsNotAnEmptyConfig covers a root that parses
// but is not an object. json.Unmarshal into a map errors for an array, and
// returns a nil map WITHOUT an error for `null` — so both shapes are checked.
func TestWriteProjectMCP_NonObjectRootIsNotAnEmptyConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"array root", `["not", "an", "object"]`},
		{"string root", `"just a string"`},
		{"null root", `null`},
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

// TestWriteProjectMCP_UnreadableFileIsNotAnEmptyConfig covers the transient I/O
// failure named in the issue, modelled as a permission error — the one I/O
// failure a test can produce deterministically. The config here is perfectly
// valid; only the read fails, which is exactly the case where rebuilding from
// an empty document destroys the most.
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

// TestWriteProjectMCP_ConcurrentWritesKeepEveryEntry is the web-plus-TUI race.
//
// Atomic file replacement does not make a read-modify-write safe: two writers
// that each read, modify and rename produce last-writer-wins, and the loser's
// project entry is gone. The web handler (internal/web/handlers_mcps.go) and
// the TUI apply path (internal/session/instance.go) both land here, so this is
// two goroutines in one binary as well as two processes.
//
// Rounds are repeated because the unlocked version loses an entry
// probabilistically; one round is enough to fail it most of the time, and the
// repeat makes that reliable without making the locked version any slower than
// a few milliseconds.
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
