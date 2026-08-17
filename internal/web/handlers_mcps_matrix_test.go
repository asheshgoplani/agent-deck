package web

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The scope matrix: tool x scope x {attach, detach, move}, against the real
// stores. This exists so the chain of scope findings ends here rather than
// producing a fifth round. Every combination is exercised, and every one is
// asserted against the file the scope actually names.

// toolScopeMatrix is the authority this test enforces. It must agree with
// scopesForTool; the first entry of each list is the tool's default attach
// scope, so a UI that asks the server can never pick an unsupported one.
var toolScopeMatrix = map[string][]string{
	"claude":   {"local", "project", "global", "user"},
	"codex":    {"global"},
	"gemini":   {"global"},
	"cursor":   {"local", "global"},
	"opencode": {"local", "global"},
}

func allScopes() []string { return []string{"local", "project", "global", "user"} }

// TestScopeMatrixMatchesScopesForTool keeps the table above and the production
// predicate from drifting apart.
func TestScopeMatrixMatchesScopesForTool(t *testing.T) {
	for tool, want := range toolScopeMatrix {
		got := scopesForTool(tool)
		if !slices.Equal(got, want) {
			t.Errorf("scopesForTool(%q) = %v, want %v", tool, got, want)
		}
		if def := defaultAttachScope(tool); def != want[0] {
			t.Errorf("defaultAttachScope(%q) = %q, want %q (the most specific store)", tool, def, want[0])
		}
	}
	if got := scopesForTool("shell"); len(got) != 0 {
		t.Errorf("scopesForTool(shell) = %v, want none", got)
	}
	if got := defaultAttachScope("shell"); got != "" {
		t.Errorf("defaultAttachScope(shell) = %q, want empty", got)
	}
}

// TestScopeMatrixAttachDetachMove walks every tool x scope pair. Supported
// pairs must round-trip attach -> list -> detach; unsupported pairs must be
// refused for all three operations without writing anything.
func TestScopeMatrixAttachDetachMove(t *testing.T) {
	for tool, supported := range toolScopeMatrix {
		for _, scope := range allScopes() {
			t.Run(tool+"/"+scope, func(t *testing.T) {
				mcpStoreEnv(t)
				project := t.TempDir()
				target := MCPTarget{Tool: tool, ProjectPath: project}
				mgr := NewDefaultMCPManager()
				isSupported := slices.Contains(supported, scope)

				attachErr := mgr.Attach(target, "alpha", scope)
				if !isSupported {
					if attachErr == nil {
						t.Fatalf("attach in unsupported scope %q for %q should be refused", scope, tool)
					}
					if err := mgr.Detach(target, "alpha", scope); err == nil {
						t.Errorf("detach in unsupported scope %q for %q should be refused", scope, tool)
					}
					if err := mgr.Move(target, "alpha", scope, supported[0]); err == nil {
						t.Errorf("move FROM unsupported scope %q for %q should be refused", scope, tool)
					}
					if err := mgr.Move(target, "alpha", supported[0], scope); err == nil {
						t.Errorf("move TO unsupported scope %q for %q should be refused", scope, tool)
					}
					return
				}

				if attachErr != nil {
					t.Fatalf("attach alpha in %q for %q: %v", scope, tool, attachErr)
				}
				attached, err := mgr.ListAttached(target)
				if err != nil {
					t.Fatalf("ListAttached: %v", err)
				}
				if !slices.Contains(attached[scope], "alpha") {
					t.Fatalf("after attach, scope %q for %q reports %v, want it to contain alpha",
						scope, tool, attached[scope])
				}
				// It must appear in exactly the scope written, nowhere else.
				for _, other := range allScopes() {
					if other == scope {
						continue
					}
					if slices.Contains(attached[other], "alpha") {
						t.Errorf("attaching in %q for %q also made it appear in %q (%v) — scopes are leaking",
							scope, tool, other, attached[other])
					}
				}

				if err := mgr.Detach(target, "alpha", scope); err != nil {
					t.Fatalf("detach: %v", err)
				}
				attached, err = mgr.ListAttached(target)
				if err != nil {
					t.Fatalf("ListAttached after detach: %v", err)
				}
				if slices.Contains(attached[scope], "alpha") {
					t.Errorf("after detach, scope %q for %q still reports alpha: %v", scope, tool, attached[scope])
				}
			})
		}
	}
}

// TestScopeMatrixMoveBetweenSupportedScopes round-trips a move across every
// ordered pair of supported scopes and asserts the server lands in exactly one.
func TestScopeMatrixMoveBetweenSupportedScopes(t *testing.T) {
	for tool, supported := range toolScopeMatrix {
		if len(supported) < 2 {
			continue
		}
		for _, from := range supported {
			for _, to := range supported {
				if from == to {
					continue
				}
				t.Run(tool+"/"+from+"->"+to, func(t *testing.T) {
					mcpStoreEnv(t)
					project := t.TempDir()
					target := MCPTarget{Tool: tool, ProjectPath: project}
					mgr := NewDefaultMCPManager()

					if err := mgr.Attach(target, "alpha", from); err != nil {
						t.Fatalf("seed attach in %q: %v", from, err)
					}
					if err := mgr.Move(target, "alpha", from, to); err != nil {
						t.Fatalf("move %q -> %q: %v", from, to, err)
					}
					attached, err := mgr.ListAttached(target)
					if err != nil {
						t.Fatalf("ListAttached: %v", err)
					}
					if !slices.Contains(attached[to], "alpha") {
						t.Errorf("after move, destination %q = %v, want alpha", to, attached[to])
					}
					if slices.Contains(attached[from], "alpha") {
						t.Errorf("after move, source %q still holds alpha: %v", from, attached[from])
					}
				})
			}
		}
	}
}

// TestMoveWithUnsupportedDestinationLeavesSourceAttached is the regression test
// for the destructive move.
//
// Move used to detach from the source and only then discover the destination
// was unsupported, so the API returned an error with the server already gone
// from disk. The UI offered every scope for every tool, so this was one click
// away. Validation now happens before any write.
func TestMoveWithUnsupportedDestinationLeavesSourceAttached(t *testing.T) {
	for _, tc := range []struct {
		tool, from, to string
	}{
		{"cursor", "local", "user"},      // Cursor has no user scope
		{"opencode", "local", "project"}, // nor a Claude project map
		{"codex", "global", "local"},     // Codex is global-only
		{"gemini", "global", "local"},
	} {
		t.Run(tc.tool+"/"+tc.from+"->"+tc.to, func(t *testing.T) {
			mcpStoreEnv(t)
			project := t.TempDir()
			target := MCPTarget{Tool: tc.tool, ProjectPath: project}
			mgr := NewDefaultMCPManager()

			if err := mgr.Attach(target, "alpha", tc.from); err != nil {
				t.Fatalf("seed attach: %v", err)
			}
			before, err := mgr.ListAttached(target)
			if err != nil {
				t.Fatalf("ListAttached: %v", err)
			}
			if !slices.Contains(before[tc.from], "alpha") {
				t.Fatalf("precondition failed: %q should hold alpha, got %v", tc.from, before[tc.from])
			}

			if err := mgr.Move(target, "alpha", tc.from, tc.to); err == nil {
				t.Fatalf("move to unsupported scope %q should fail", tc.to)
			}

			after, err := mgr.ListAttached(target)
			if err != nil {
				t.Fatalf("ListAttached after failed move: %v", err)
			}
			if !slices.Contains(after[tc.from], "alpha") {
				t.Errorf("a failed move DELETED alpha from %q (now %v) — a rejected move must not "+
					"change the configuration at all", tc.from, after[tc.from])
			}
		})
	}
}

// TestMoveRestoresSourceWhenDestinationWriteFails covers the other half of the
// transaction: validation passes, but the destination write fails on disk. The
// source attachment must come back rather than being lost.
func TestMoveRestoresSourceWhenDestinationWriteFails(t *testing.T) {
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("running as root: file permissions would not block the write")
	}
	home := mcpStoreEnv(t)
	project := t.TempDir()
	target := MCPTarget{Tool: "claude", ProjectPath: project}
	mgr := NewDefaultMCPManager()

	if err := mgr.Attach(target, "alpha", "local"); err != nil {
		t.Fatalf("seed attach local: %v", err)
	}

	// Make the Claude config directory unwritable so the "project" write fails
	// after validation has already passed.
	claudeDir := filepath.Join(home, ".claude-cfg")
	if err := os.Chmod(claudeDir, 0o500); err != nil {
		t.Fatalf("chmod claude dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(claudeDir, 0o755) })

	err := mgr.Move(target, "alpha", "local", "project")
	if err == nil {
		t.Skip("destination write unexpectedly succeeded; cannot exercise the rollback path here")
	}

	if err := os.Chmod(claudeDir, 0o755); err != nil {
		t.Fatalf("restore perms: %v", err)
	}
	session.InvalidateProjectMCPIntegrationsCache(project)

	attached, listErr := mgr.ListAttached(target)
	if listErr != nil {
		t.Fatalf("ListAttached: %v", listErr)
	}
	if !slices.Contains(attached["local"], "alpha") {
		t.Errorf("a failed move lost alpha: local=%v project=%v. The source attachment must be "+
			"restored when the destination write fails (move error was: %v)",
			attached["local"], attached["project"], err)
	}
}

// TestClaudeProjectScopeIsWrittenWhereItIsReported is the regression test for
// the project/global conflation.
//
// projects[path].mcpServers used to be reported as "global" while writes went
// only to the root mcpServers map. Detaching such an entry therefore returned
// success while leaving it attached at project level, and attaching another
// global could copy project-only entries up into the root.
func TestClaudeProjectScopeIsWrittenWhereItIsReported(t *testing.T) {
	home := mcpStoreEnv(t)
	project := t.TempDir()
	target := MCPTarget{Tool: "claude", ProjectPath: project}
	mgr := NewDefaultMCPManager()
	configFile := filepath.Join(home, ".claude-cfg", ".claude.json")

	// gamma lives ONLY in projects[path].mcpServers.
	if err := session.WriteProjectMCP(project, []string{"gamma"}); err != nil {
		t.Fatalf("seed project scope: %v", err)
	}
	session.InvalidateProjectMCPIntegrationsCache(project)

	attached, err := mgr.ListAttached(target)
	if err != nil {
		t.Fatalf("ListAttached: %v", err)
	}
	if !slices.Contains(attached["project"], "gamma") {
		t.Fatalf("gamma should be reported in the project scope, got project=%v global=%v",
			attached["project"], attached["global"])
	}
	if slices.Contains(attached["global"], "gamma") {
		t.Errorf("a project-scoped entry must not also be reported as global: global=%v", attached["global"])
	}

	// Detaching it in the scope it was reported in must actually remove it.
	if err := mgr.Detach(target, "gamma", "project"); err != nil {
		t.Fatalf("detach project: %v", err)
	}
	session.InvalidateProjectMCPIntegrationsCache(project)
	if names := claudeProjectMCPNames(t, configFile, project); slices.Contains(names, "gamma") {
		t.Errorf("detach reported success but gamma is still in projects[%s].mcpServers: %v", project, names)
	}

	// Attaching a global must not drag project entries into the root map.
	if err := session.WriteProjectMCP(project, []string{"gamma"}); err != nil {
		t.Fatalf("re-seed project scope: %v", err)
	}
	session.InvalidateProjectMCPIntegrationsCache(project)
	if err := mgr.Attach(target, "alpha", "global"); err != nil {
		t.Fatalf("attach global: %v", err)
	}
	if names := claudeRootMCPNames(t, configFile); slices.Contains(names, "gamma") {
		t.Errorf("attaching a global copied the project-only entry gamma into root mcpServers: %v", names)
	}
	if names := claudeProjectMCPNames(t, configFile, project); !slices.Contains(names, "gamma") {
		t.Errorf("attaching a global removed the project entry gamma: %v", names)
	}
}

func claudeConfigDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

func claudeRootMCPNames(t *testing.T, path string) []string {
	t.Helper()
	servers, _ := claudeConfigDoc(t, path)["mcpServers"].(map[string]any)
	return sortedKeys(servers)
}

func claudeProjectMCPNames(t *testing.T, path, projectPath string) []string {
	t.Helper()
	projects, _ := claudeConfigDoc(t, path)["projects"].(map[string]any)
	proj, _ := projects[projectPath].(map[string]any)
	servers, _ := proj["mcpServers"].(map[string]any)
	return sortedKeys(servers)
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestLocalWriteInvalidatesMCPInfoCache reproduces the pane's exact sequence:
// refresh, mutate, refresh. The Claude/Cursor/OpenCode project readers cache
// for 30s, so without invalidation the second refresh returns the pre-mutation
// list and the UI shows the change as having failed — while inviting the user
// to repeat an action that already succeeded.
func TestLocalWriteInvalidatesMCPInfoCache(t *testing.T) {
	for _, tool := range []string{"claude", "cursor", "opencode"} {
		t.Run(tool, func(t *testing.T) {
			mcpStoreEnv(t)
			project := t.TempDir()
			target := MCPTarget{Tool: tool, ProjectPath: project}
			mgr := NewDefaultMCPManager()

			// First refresh: populates the reader cache with an empty list.
			before, err := mgr.ListAttached(target)
			if err != nil {
				t.Fatalf("initial ListAttached: %v", err)
			}
			if slices.Contains(before["local"], "alpha") {
				t.Fatalf("precondition failed: local already holds alpha: %v", before["local"])
			}

			if err := mgr.Attach(target, "alpha", "local"); err != nil {
				t.Fatalf("attach: %v", err)
			}

			// Second refresh, immediately after — no sleeping past the TTL.
			after, err := mgr.ListAttached(target)
			if err != nil {
				t.Fatalf("ListAttached after attach: %v", err)
			}
			if !slices.Contains(after["local"], "alpha") {
				t.Errorf("the refresh right after a successful local attach still reports %v: the "+
					"write path must invalidate the cached MCP info, or the UI shows pre-mutation state",
					after["local"])
			}

			if err := mgr.Detach(target, "alpha", "local"); err != nil {
				t.Fatalf("detach: %v", err)
			}
			afterDetach, err := mgr.ListAttached(target)
			if err != nil {
				t.Fatalf("ListAttached after detach: %v", err)
			}
			if slices.Contains(afterDetach["local"], "alpha") {
				t.Errorf("the refresh right after a successful detach still reports alpha: %v", afterDetach["local"])
			}
		})
	}
}
