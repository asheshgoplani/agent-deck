package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Messaging audit P1-1: `hooks status` must say when the installed hook runs
// a different binary than this one, and when the versions differ, instead of
// a bare INSTALLED / NOT INSTALLED.
func TestPrintClaudeHooksStatus_ReportsShadowAndVersionMismatch(t *testing.T) {
	report := session.ClaudeHooksStatusReport{
		ConfigDir:  "/cfg",
		Present:    true,
		Installed:  false,
		Executable: "/opt/new/agent-deck",
		Version:    "1.16.11",
		Binaries: []session.ClaudeHookBinaryStatus{{
			Command: "agent-deck hook-handler", Bare: true,
			ResolvedPath: "/usr/local/bin/agent-deck.real", Shadowed: true,
			Version: "1.16.3", VersionMismatch: true,
		}},
	}
	var out bytes.Buffer
	printClaudeHooksStatus(&out, report)
	text := out.String()
	for _, want := range []string{
		"Status: INSTALLED (needs reinstall)",
		"PATH shadow: bare `agent-deck hook-handler` resolves to /usr/local/bin/agent-deck.real, not this binary (/opt/new/agent-deck)",
		"version mismatch: hook binary is v1.16.3, this binary is v1.16.11",
		"Run 'agent-deck hooks install'",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status output missing %q:\n%s", want, text)
		}
	}

	clean := session.ClaudeHooksStatusReport{
		ConfigDir: "/cfg", Present: true, Installed: true, Executable: "/opt/new/agent-deck", Version: "1.16.11",
		Binaries: []session.ClaudeHookBinaryStatus{{Command: "/opt/new/agent-deck hook-handler", ResolvedPath: "/opt/new/agent-deck", Version: "1.16.11"}},
	}
	out.Reset()
	printClaudeHooksStatus(&out, clean)
	if !strings.HasPrefix(out.String(), "Status: INSTALLED\n") || strings.Contains(out.String(), "WARNING") {
		t.Errorf("clean install must print plain INSTALLED with no warning:\n%s", out.String())
	}
}
