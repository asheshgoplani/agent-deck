package web

import (
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/safeio"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Data-loss guard: a config that could not be READ must never be replaced by
// one reconstructed from that failure.
//
// The first version of this file said "parse", and so did its fixtures: the
// only degraded input it built was malformed JSON. That narrowness was the
// tell. A parse error is one way a read fails; an unreadable file (permissions,
// a directory where a file belongs, an I/O error) is another, and every one of
// them must abort the mutation rather than produce an empty document to write
// back. The cases below cover the whole read boundary.
//
// .claude.json holds the user's whole Claude configuration — settings, every
// project entry, and the root mcpServers map. The MCP writers are
// read-modify-write. When the read degraded to an empty map on a parse error,
// the very next attach/detach/move persisted that empty map and the file was
// gone. A half-finished manual edit or a truncated write from another process
// is exactly when this fires.
//
// The rule these tests pin: ANY read failure aborts the mutation, names the
// problem, and leaves the file byte-identical.

const corruptClaudeConfig = `{
  "numStartups": 42,
  "theme": "dark",
  "projects": {
    "/srv/important": {
      "hasTrustDialogAccepted": true,
      "mcpServers": {"gamma": {"command": "gamma-server"}}
    }
  },
  "mcpServers": {"beta": {"command": "beta-server"}}
  "oops": "the comma above is missing, so this file does not parse"
}`

// writeCorruptClaudeConfig corrupts BOTH Claude config files: the one under
// CLAUDE_CONFIG_DIR (which backs the project and global scopes) and the
// user-root ~/.claude.json (which backs the user scope). They are separate
// files, so corrupting only one leaves the other scope legitimately writable
// and the test would pass for the wrong reason.
func writeCorruptClaudeConfig(t *testing.T, home string) (paths []string, originals [][]byte) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(home, ".claude-cfg", ".claude.json"),
		filepath.Join(home, ".claude.json"),
	} {
		if err := os.WriteFile(path, []byte(corruptClaudeConfig), 0o600); err != nil {
			t.Fatalf("seed corrupt config %s: %v", path, err)
		}
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back seed %s: %v", path, err)
		}
		paths = append(paths, path)
		originals = append(originals, original)
	}
	return paths, originals
}

func assertByteIdentical(t *testing.T, path string, original []byte, what string) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: config disappeared entirely: %v", what, err)
	}
	if string(after) != string(original) {
		t.Errorf("%s REWROTE a malformed config — the user's settings and project entries are gone.\n"+
			"before (%d bytes):\n%s\nafter (%d bytes):\n%s",
			what, len(original), original, len(after), after)
	}
}

// TestMalformedClaudeConfigIsNeverRewritten walks every mutation the web MCP
// surface can perform against a Claude session and requires each to refuse.
func TestMalformedClaudeConfigIsNeverRewritten(t *testing.T) {
	scopes := []string{"project", "global", "user"}

	for _, scope := range scopes {
		for _, op := range []string{"attach", "detach", "move"} {
			t.Run(scope+"/"+op, func(t *testing.T) {
				home := mcpStoreEnv(t)
				project := t.TempDir()
				paths, originals := writeCorruptClaudeConfig(t, home)
				target := MCPTarget{Tool: "claude", ProjectPath: project}
				mgr := NewDefaultMCPManager()

				var err error
				switch op {
				case "attach":
					err = mgr.Attach(target, "alpha", scope)
				case "detach":
					err = mgr.Detach(target, "alpha", scope)
				case "move":
					err = mgr.Move(target, "alpha", scope, "local")
				}

				if err == nil {
					t.Errorf("%s in scope %q silently succeeded against a malformed config; "+
						"it must fail closed", op, scope)
				} else if !mentionsReadFailure(err) {
					t.Errorf("%s in scope %q failed, but the error does not name why the read failed: %v",
						op, scope, err)
				}

				for i, path := range paths {
					assertByteIdentical(t, path, originals[i], op+" in scope "+scope)
				}
			})
		}
	}
}

// TestMalformedClaudeConfigLeavesUserScopeAlone covers ~/.claude.json, the
// other file the same read-modify-write pattern owns.
func TestMalformedClaudeConfigLeavesUserScopeAlone(t *testing.T) {
	home := mcpStoreEnv(t)
	userConfig := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(userConfig, []byte(corruptClaudeConfig), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	original, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	// The project-scoped config must be valid so the failure under test is the
	// user-scope one.
	if err := session.WriteProjectMCP(t.TempDir(), nil); err != nil {
		t.Logf("project seed: %v", err)
	}

	writeErr := session.WriteUserMCP([]string{"alpha"})
	if writeErr == nil {
		t.Error("WriteUserMCP silently succeeded against a malformed ~/.claude.json")
	} else if !mentionsReadFailure(writeErr) {
		t.Errorf("WriteUserMCP error does not name why the read failed: %v", writeErr)
	}
	assertByteIdentical(t, userConfig, original, "WriteUserMCP")
}

// TestSafeioRefusesDroppingTopLevelKeys pins the second net. Even if some
// future path reconstructs a config from nothing, the write must be refused
// because it would remove keys the file already has.
func TestSafeioRefusesDroppingTopLevelKeys(t *testing.T) {
	home := mcpStoreEnv(t)
	path := filepath.Join(home, ".claude-cfg", ".claude.json")
	populated := []byte(`{"numStartups":42,"theme":"dark","mcpServers":{"beta":{"command":"b"}}}`)
	if err := os.WriteFile(path, populated, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A payload that keeps only mcpServers — the exact shape the old
	// empty-fallback produced.
	err := safeio.SafeOverwrite(path, []byte(`{"mcpServers":{}}`), safeio.Options{
		Perm:        0o600,
		RefuseEmpty: true,
		Guard:       session.RefuseDroppingTopLevelKeysForTest(path),
	})
	if err == nil {
		t.Fatal("safeio guard allowed a write that drops top-level keys")
	}
	if !strings.Contains(err.Error(), "numStartups") || !strings.Contains(err.Error(), "theme") {
		t.Errorf("guard error should name the dropped keys, got: %v", err)
	}
	assertByteIdentical(t, path, populated, "guarded SafeOverwrite")

	// And an outright empty payload is refused by RefuseEmpty.
	err = safeio.SafeOverwrite(path, nil, safeio.Options{Perm: 0o600, RefuseEmpty: true})
	if !errors.Is(err, safeio.ErrRefusingEmptyOverwrite) {
		t.Errorf("empty payload over a populated file should hit ErrRefusingEmptyOverwrite, got %v", err)
	}
	assertByteIdentical(t, path, populated, "empty SafeOverwrite")
}

// mentionsReadFailure accepts any error that names why the config could not be
// read — a parse problem or an I/O one. Named for the boundary, not for the one
// failure mode the first version of these tests happened to build.
var readPathErr = regexp.MustCompile(`read [^ ]+:`)

func mentionsReadFailure(err error) bool {
	msg := strings.ToLower(err.Error())
	// Matches the read-failure wrapper `read <path>: ...` specifically; the
	// bare substring "read " used to match almost any error text.
	if readPathErr.MatchString(msg) {
		return true
	}
	for _, want := range []string{
		"not valid json", "failed to parse", "invalid character",
		"permission denied", "is a directory", "root is null",
		"is unreadable", "is not an object", "unparseable",
		"refusing to overwrite",
	} {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

// TestUnreadableClaudeConfigIsNeverRewritten widens the guard past malformed
// JSON: a config that cannot be read at all must abort the mutation too, not
// fall through to a fresh document.
func TestUnreadableClaudeConfigIsNeverRewritten(t *testing.T) {
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("running as root: file permissions would not block the read")
	}
	for _, scope := range []string{"project", "global"} {
		t.Run(scope, func(t *testing.T) {
			home := mcpStoreEnv(t)
			project := t.TempDir()
			path := filepath.Join(home, ".claude-cfg", ".claude.json")
			populated := []byte(`{"numStartups":9,"theme":"dark","mcpServers":{"beta":{"command":"b"}}}`)
			if err := os.WriteFile(path, populated, 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			// Unreadable, but present and non-empty.
			if err := os.Chmod(path, 0o000); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

			err := NewDefaultMCPManager().Attach(MCPTarget{Tool: "claude", ProjectPath: project}, "alpha", scope)
			if err == nil {
				t.Fatalf("attach in scope %q silently succeeded against an unreadable config", scope)
			}

			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatalf("restore perms: %v", err)
			}
			assertByteIdentical(t, path, populated, "attach against an unreadable config")
		})
	}
}

// TestConfigDirectoryInPlaceOfFileIsNeverRewritten covers the other read
// failure: something that is not a regular file where the config belongs.
func TestConfigDirectoryInPlaceOfFileIsNeverRewritten(t *testing.T) {
	home := mcpStoreEnv(t)
	project := t.TempDir()
	path := filepath.Join(home, ".claude-cfg", ".claude.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove: %v", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir in place of config: %v", err)
	}
	marker := filepath.Join(path, "keep-me")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if err := NewDefaultMCPManager().Attach(MCPTarget{Tool: "claude", ProjectPath: project}, "alpha", "global"); err == nil {
		t.Error("attach silently succeeded with a directory where the config belongs")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the directory standing in for the config was clobbered: %v", err)
	}
}

// TestEmptyConfigStartsFreshRatherThanFailing draws the other side of the line:
// a missing or empty file is NOT a read failure. Refusing there would make a
// first-ever attach impossible, which is the inverted bug the hand-rolled
// ClearProjectMCPs copy had.
func TestEmptyConfigStartsFreshRatherThanFailing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"absent", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove: %v", err)
			}
		}},
		{"empty", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatalf("truncate: %v", err)
			}
		}},
		{"whitespace only", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("\n  \n"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := mcpStoreEnv(t)
			project := t.TempDir()
			tc.setup(t, filepath.Join(home, ".claude-cfg", ".claude.json"))

			if err := NewDefaultMCPManager().Attach(MCPTarget{Tool: "claude", ProjectPath: project}, "alpha", "global"); err != nil {
				t.Errorf("a %s config should start fresh, not fail: %v", tc.name, err)
			}
		})
	}
}

// TestMalformedMcpJSONIsNeverRewritten closes the gap the re-review found: the
// local scope writes <project>/.mcp.json through a DIFFERENT read path
// (readExistingLocalMCPServers), which swallowed read and parse errors and
// returned nil. A malformed .mcp.json therefore read as "no servers" and the
// save replaced the file with only the managed entries.
//
// Nothing pinned that guard, which is how it survived the sweep: reverting the
// fail-closed read broke no test.
func TestMalformedMcpJSONIsNeverRewritten(t *testing.T) {
	// Each tool keeps its project-local MCPs in a DIFFERENT file: Claude in
	// <project>/.mcp.json, Cursor in <project>/.cursor/mcp.json, OpenCode in
	// <project>/opencode.json. An earlier version of this test corrupted
	// .mcp.json for all three and "passed" for Cursor and OpenCode only because
	// it had corrupted a file they never read. Ask the production helper for
	// the path so the fixture cannot drift from the code again.
	for _, tool := range []string{"claude", "cursor", "opencode"} {
		t.Run(tool, func(t *testing.T) {
			mcpStoreEnv(t)
			project := t.TempDir()
			localFile := session.MCPLocalConfigPathForTool(tool, project)
			if localFile == "" {
				t.Skipf("%s has no project-local MCP file", tool)
			}
			if err := os.MkdirAll(filepath.Dir(localFile), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			corrupt := []byte(`{"mcpServers":{"beta":{"command":"b"}} "trailing":"no comma"}`)
			if err := os.WriteFile(localFile, corrupt, 0o644); err != nil {
				t.Fatalf("seed %s: %v", localFile, err)
			}

			err := NewDefaultMCPManager().Attach(MCPTarget{Tool: tool, ProjectPath: project}, "alpha", "local")
			if err == nil {
				t.Errorf("attach against a malformed %s silently succeeded; it must fail closed", localFile)
			} else if !mentionsReadFailure(err) {
				t.Errorf("error does not name why the read failed: %v", err)
			}
			assertByteIdentical(t, localFile, corrupt, "local attach against a malformed "+filepath.Base(localFile))
		})
	}
}

// TestMcpJSONWritePreservesUnknownTopLevelKeys pins the other half of routing
// that save through safeio: the old one rebuilt the file from the mcpServers
// field alone and dropped anything else the user had in it.
func TestMcpJSONWritePreservesUnknownTopLevelKeys(t *testing.T) {
	mcpStoreEnv(t)
	project := t.TempDir()
	mcpFile := filepath.Join(project, ".mcp.json")
	if err := os.WriteFile(mcpFile, []byte(`{"mcpServers":{},"$schema":"https://example/schema.json"}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := NewDefaultMCPManager().Attach(MCPTarget{Tool: "claude", ProjectPath: project}, "alpha", "local"); err != nil {
		t.Fatalf("attach: %v", err)
	}

	data, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("result is not valid JSON: %s", data)
	}
	if doc["$schema"] == nil {
		t.Errorf("the write dropped an unrelated top-level key: %s", data)
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if _, ok := servers["alpha"]; !ok {
		t.Errorf("attach did not land: %s", data)
	}
}

// TestNullRootConfigIsRefused covers the one input that walks past the parse
// check: json.Unmarshal("null", &map) succeeds and leaves a nil map, which the
// fresh-start branch would treat as "no config yet" and overwrite. There is no
// user data in a null root, so the stakes are low — but it is the single hole
// in the guard, so it gets a test.
func TestNullRootConfigIsRefused(t *testing.T) {
	for _, body := range []string{"null", "  null\n"} {
		t.Run(strings.TrimSpace(body), func(t *testing.T) {
			home := mcpStoreEnv(t)
			project := t.TempDir()
			path := filepath.Join(home, ".claude-cfg", ".claude.json")
			original := []byte(body)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}

			err := NewDefaultMCPManager().Attach(MCPTarget{Tool: "claude", ProjectPath: project}, "alpha", "global")
			if err == nil {
				t.Error("a null root should be refused, not treated as a fresh config")
			}
			assertByteIdentical(t, path, original, "attach against a null-root config")
		})
	}
}

// TestClearProjectMCPsRefusesMalformedConfig — the re-review noted the malformed
// case was covered for attach/detach/move but not for the clear path, which is
// the one that used to hand-roll its own read.
func TestClearProjectMCPsRefusesMalformedConfig(t *testing.T) {
	home := mcpStoreEnv(t)
	project := t.TempDir()
	paths, originals := writeCorruptClaudeConfig(t, home)

	err := session.ClearProjectMCPs(project)
	if err == nil {
		t.Error("ClearProjectMCPs silently succeeded against a malformed config")
	} else if !mentionsReadFailure(err) {
		t.Errorf("error does not name why the read failed: %v", err)
	}
	for i, path := range paths {
		assertByteIdentical(t, path, originals[i], "ClearProjectMCPs against a malformed config")
	}
}
