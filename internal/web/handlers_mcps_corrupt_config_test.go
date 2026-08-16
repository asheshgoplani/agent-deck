package web

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/safeio"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Data-loss guard: a temporarily malformed .claude.json must never be replaced
// by a config reconstructed from a failed parse.
//
// .claude.json holds the user's whole Claude configuration — settings, every
// project entry, and the root mcpServers map. The MCP writers are
// read-modify-write. When the read degraded to an empty map on a parse error,
// the very next attach/detach/move persisted that empty map and the file was
// gone. A half-finished manual edit or a truncated write from another process
// is exactly when this fires.
//
// The rule these tests pin: parse failure aborts the mutation, names the
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
				} else if !mentionsParseFailure(err) {
					t.Errorf("%s in scope %q failed, but the error does not name the parse problem: %v",
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
	} else if !mentionsParseFailure(writeErr) {
		t.Errorf("WriteUserMCP error does not name the parse problem: %v", writeErr)
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

func mentionsParseFailure(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not valid json") ||
		strings.Contains(msg, "failed to parse") ||
		strings.Contains(msg, "invalid character")
}
