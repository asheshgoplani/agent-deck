package configdoctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// writeCodexHome materializes a Codex home with the given config.toml body and
// returns its path. Homes live under t.TempDir() so nothing touches the real
// ~/.agent-deck.
func writeCodexHome(t *testing.T, name, body string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", home, err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return home
}

func findingsFor(report Report, check string) []Finding {
	var out []Finding
	for _, f := range report.Findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

// A Codex home whose notify key sits inside another table parses as
// tui.notify, never fires, and must be reported exactly like a missing one.
// This is the real-world failure the check exists for: grep sees the line and
// says it is fine, TOML scoping says otherwise.
func TestNotifyNestedInAnotherTableIsReportedMissing(t *testing.T) {
	home := writeCodexHome(t, "nested", `
[tui]
status_line = ["model"]
notify = ["/usr/local/bin/notifier", "codex"]
`)
	cfg := &session.UserConfig{
		Groups: map[string]session.GroupSettings{
			"team": {Codex: session.GroupCodexSettings{ConfigDir: home}},
		},
	}

	got := findingsFor(Diagnose(cfg), "codex-notify-missing")
	if len(got) != 1 {
		t.Fatalf("expected 1 notify finding for a table-scoped notify, got %d: %+v", len(got), got)
	}
	if got[0].Severity != SeverityError {
		t.Fatalf("notify absence must be an error, got %q", got[0].Severity)
	}
}

func TestRootLevelNotifyIsAccepted(t *testing.T) {
	home := writeCodexHome(t, "healthy", `
notify = ["/usr/local/bin/notifier", "codex"]

[tui]
status_line = ["model"]
`)
	cfg := &session.UserConfig{
		Groups: map[string]session.GroupSettings{
			"team": {Codex: session.GroupCodexSettings{ConfigDir: home}},
		},
	}

	if got := findingsFor(Diagnose(cfg), "codex-notify-missing"); len(got) != 0 {
		t.Fatalf("root-level notify must not be reported: %+v", got)
	}
}

// The headline check: an entry declared for one tool and not the other, in a
// group that declares both sides. A group that declares only one side has not
// expressed an opinion about the other and must stay quiet.
func TestToolAsymmetryReportsOneSidedSkill(t *testing.T) {
	cfg := &session.UserConfig{
		Groups: map[string]session.GroupSettings{
			"team": {
				Claude: session.GroupClaudeSettings{Skills: []string{"shared/port-registry", "shared/web-perf"}},
				Codex:  session.GroupCodexSettings{Skills: []string{"shared/port-registry"}},
			},
		},
	}

	got := findingsFor(Diagnose(cfg), "tool-asymmetry")
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 asymmetry, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Summary, "shared/web-perf") {
		t.Fatalf("wrong skill reported: %q", got[0].Summary)
	}
	if !strings.Contains(got[0].Summary, "Claude but not Codex") {
		t.Fatalf("asymmetry direction lost: %q", got[0].Summary)
	}
}

func TestToolAsymmetrySilentWhenOnlyOneToolDeclaresAnything(t *testing.T) {
	cfg := &session.UserConfig{
		Groups: map[string]session.GroupSettings{
			// Claude-only loadout: the group never said Codex should have these.
			"team": {Claude: session.GroupClaudeSettings{Skills: []string{"shared/web-perf"}}},
		},
	}

	if got := findingsFor(Diagnose(cfg), "tool-asymmetry"); len(got) != 0 {
		t.Fatalf("a one-sided declaration is not an asymmetry: %+v", got)
	}
}

// Homes shared through config_dir inheritance must be read once, and an entry
// declared by any owner must not be reported as undeclared.
func TestSharedHomeDoesNotReportSiblingEntriesAsUndeclared(t *testing.T) {
	home := writeCodexHome(t, "shared", `
notify = ["/usr/local/bin/notifier", "codex"]

[mcp_servers]
  [mcp_servers.dart]
    command = "dart"
`)
	cfg := &session.UserConfig{
		MCPs: map[string]session.MCPDef{"dart": {Command: "dart"}},
		Groups: map[string]session.GroupSettings{
			"team":       {Codex: session.GroupCodexSettings{ConfigDir: home}},
			"team/child": {Codex: session.GroupCodexSettings{MCPs: []string{"dart"}}},
		},
	}

	if got := findingsFor(Diagnose(cfg), "undeclared-in-home"); len(got) != 0 {
		t.Fatalf("entry declared by a sibling owner must not read as undeclared: %+v", got)
	}
}

func TestUndeclaredMCPInHomeIsReported(t *testing.T) {
	home := writeCodexHome(t, "stray", `
notify = ["/usr/local/bin/notifier", "codex"]

[mcp_servers]
  [mcp_servers.alpha]
    command = "alpha-server"
`)
	cfg := &session.UserConfig{
		Groups: map[string]session.GroupSettings{
			"team": {Codex: session.GroupCodexSettings{ConfigDir: home}},
		},
	}

	got := findingsFor(Diagnose(cfg), "undeclared-in-home")
	if len(got) != 1 {
		t.Fatalf("expected the stray MCP to be reported, got %d: %+v", len(got), got)
	}
	if got[0].Severity != SeverityInfo {
		t.Fatalf("hand-installed entries are informational, got %q", got[0].Severity)
	}
	// The fix must name the home-owning group, not an arbitrary descendant.
	if !strings.Contains(got[0].Fix, `"team"`) {
		t.Fatalf("fix should point at the owning group: %q", got[0].Fix)
	}
}

// A home that cannot be read must produce a finding, never silence — otherwise
// "I could not look" is indistinguishable from "everything is fine".
func TestUnreadableHomeIsReportedNotSkipped(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfg := &session.UserConfig{
		Groups: map[string]session.GroupSettings{
			"team": {Codex: session.GroupCodexSettings{ConfigDir: missing}},
		},
	}

	report := Diagnose(cfg)
	got := findingsFor(report, "home-unreadable")
	if len(got) != 1 {
		t.Fatalf("expected an unreadable-home finding, got %d: %+v", len(got), got)
	}
	if report.Checked != 0 {
		t.Fatalf("an unreadable home must not count as checked, got %d", report.Checked)
	}
}

func TestMarketplaceSourceConflictAcrossHomes(t *testing.T) {
	first := writeCodexHome(t, "one", `
notify = ["/usr/local/bin/notifier", "codex"]

[marketplaces.agent-deck]
source = "/srv/generated/agent-deck"
`)
	second := writeCodexHome(t, "two", `
notify = ["/usr/local/bin/notifier", "codex"]

[marketplaces.agent-deck]
source = "/home/dev/agent-deck"
`)
	cfg := &session.UserConfig{
		Groups: map[string]session.GroupSettings{
			"a": {Codex: session.GroupCodexSettings{ConfigDir: first}},
			"b": {Codex: session.GroupCodexSettings{ConfigDir: second}},
		},
	}

	got := findingsFor(Diagnose(cfg), "marketplace-source-conflict")
	if len(got) != 1 {
		t.Fatalf("expected 1 conflict, got %d: %+v", len(got), got)
	}
	for _, want := range []string{"/srv/generated/agent-deck", "/home/dev/agent-deck"} {
		if !strings.Contains(got[0].Summary, want) {
			t.Fatalf("conflict must name both sources, missing %q in %q", want, got[0].Summary)
		}
	}
}

func TestFindingsSortErrorsFirst(t *testing.T) {
	home := writeCodexHome(t, "mixed", `
[mcp_servers]
  [mcp_servers.stray]
    command = "stray"
`)
	cfg := &session.UserConfig{
		Groups: map[string]session.GroupSettings{
			"team": {Codex: session.GroupCodexSettings{ConfigDir: home}},
		},
	}

	report := Diagnose(cfg)
	if len(report.Findings) < 2 {
		t.Fatalf("expected both an error and an info finding, got %+v", report.Findings)
	}
	if report.Findings[0].Severity != SeverityError {
		t.Fatalf("errors must sort first, got %q", report.Findings[0].Severity)
	}
	if report.Errors() != 1 {
		t.Fatalf("expected 1 error for the exit code, got %d", report.Errors())
	}
}

// A home path that is relative or escapes upward must be refused before any
// filesystem access rather than joined and read.
func TestUnsafeHomePathIsRefused(t *testing.T) {
	for _, bad := range []string{"relative/home", "/tmp/../etc"} {
		cfg := &session.UserConfig{
			Groups: map[string]session.GroupSettings{
				"team": {Codex: session.GroupCodexSettings{ConfigDir: bad}},
			},
		}
		got := findingsFor(Diagnose(cfg), "home-unreadable")
		if len(got) != 1 {
			t.Fatalf("home %q should be refused, got %+v", bad, got)
		}
	}
}
