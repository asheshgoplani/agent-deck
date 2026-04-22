// Tests for the --select flag (issue #709).
//
// --select lets users launch the TUI with the cursor positioned on a specific
// session while keeping every group visible. This is intentionally different
// from -g / --group, which hides everything outside the chosen group.
package main

import (
	"testing"
)

func TestExtractSelectFlag(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantSelect    string
		wantRemaining []string
	}{
		{
			name:          "no flag",
			args:          []string{"list"},
			wantSelect:    "",
			wantRemaining: []string{"list"},
		},
		{
			name:          "--select=abc equals form",
			args:          []string{"--select=abc"},
			wantSelect:    "abc",
			wantRemaining: nil,
		},
		{
			name:          "--select abc space form",
			args:          []string{"--select", "abc"},
			wantSelect:    "abc",
			wantRemaining: nil,
		},
		{
			name:          "combined with -g",
			args:          []string{"-g", "work", "--select", "proj-a"},
			wantSelect:    "proj-a",
			wantRemaining: []string{"-g", "work"},
		},
		{
			name:          "combined with -p and -g",
			args:          []string{"-p", "work", "-g", "clients", "--select=acme"},
			wantSelect:    "acme",
			wantRemaining: []string{"-p", "work", "-g", "clients"},
		},
		{
			name:          "session id with dashes",
			args:          []string{"--select", "sess-1234-abcd"},
			wantSelect:    "sess-1234-abcd",
			wantRemaining: nil,
		},
		{
			name:          "title with spaces (equals form)",
			args:          []string{"--select=My Project"},
			wantSelect:    "My Project",
			wantRemaining: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSelect, gotRemaining := extractSelectFlag(tt.args)
			if gotSelect != tt.wantSelect {
				t.Errorf("select: got %q, want %q", gotSelect, tt.wantSelect)
			}
			if len(gotRemaining) != len(tt.wantRemaining) {
				t.Errorf("remaining length: got %d (%v), want %d (%v)",
					len(gotRemaining), gotRemaining, len(tt.wantRemaining), tt.wantRemaining)
				return
			}
			for i, arg := range gotRemaining {
				if arg != tt.wantRemaining[i] {
					t.Errorf("remaining[%d]: got %q, want %q", i, arg, tt.wantRemaining[i])
				}
			}
		})
	}
}

// TestExtractSelectFlag_PreservesGroupFlag verifies that --select does NOT
// consume or interfere with -g / --group. Both flags must survive independently.
func TestExtractSelectFlag_PreservesGroupFlag(t *testing.T) {
	args := []string{"-g", "work", "--select", "sess-1"}
	selectVal, remaining := extractSelectFlag(args)
	if selectVal != "sess-1" {
		t.Fatalf("--select: got %q, want %q", selectVal, "sess-1")
	}
	// -g and work must still be present in remaining
	group, _ := extractGroupFlag(remaining)
	if group != "work" {
		t.Fatalf("-g preserved: got %q, want %q", group, "work")
	}
}
