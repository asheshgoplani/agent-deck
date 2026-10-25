package ask

import (
	"strings"
	"testing"
)

func TestAskValidateName(t *testing.T) {
	good := []string{
		"conductor/GH_TOKEN_DECKBOX",
		"conductor/a",
		"my-scope_2/Name.with-dots_1",
		"conductor/" + strings.Repeat("A", 64),
	}
	for _, name := range good {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want ok", name, err)
		}
	}
	bad := []string{
		"",
		"GH_TOKEN",                             // no scope
		"conductor/",                           // empty name
		"Conductor/GH_TOKEN",                   // scope must be lowercase
		"conductor/.hidden",                    // leading dot
		"conductor/..",                         // traversal
		"conductor/a..b",                       // no ".." anywhere
		"conductor/a/b",                        // one level only
		"/conductor/a",                         // leading slash
		"conductor/a b",                        // space
		"conductor/a\nb",                       // newline
		"conductor/\u00E4",                     // non-ASCII
		"conductor/" + strings.Repeat("A", 65), // too long
		"../etc/passwd",
		"conductor/a;rm",
	}
	for _, name := range bad {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) accepted, want refusal", name)
		} else if !IsRefusal(err) {
			t.Errorf("ValidateName(%q) = %v, want a refusal", name, err)
		}
	}
}

func TestAskScopeAllowed(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		want   bool
	}{
		{"conductor/GH_TOKEN", nil, true}, // default ["conductor/"]
		{"other/GH_TOKEN", nil, false},
		{"conductor/GH_TOKEN", []string{"conductor/"}, true},
		{"conductor/GH_TOKEN", []string{"conductor"}, true},   // a bare scope means the scope
		{"conductorx/GH_TOKEN", []string{"conductor"}, false}, // never a prefix of another scope
		{"conductor/GH_TOKEN", []string{}, false},             // explicit empty list: no scope
		{"conductor/GH_TOKEN", []string{"", "/"}, false},      // empty entries never match all
		{"work/KEY", []string{"conductor/", "work/"}, true},
	}
	for _, tt := range tests {
		if got := ScopeAllowed(tt.name, tt.scopes); got != tt.want {
			t.Errorf("ScopeAllowed(%q, %q) = %v, want %v", tt.name, tt.scopes, got, tt.want)
		}
	}
}

func TestAskSanitizeUntrustedText(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"GitHub key with workflow permission", 200, "GitHub key with workflow permission"},
		{"line one\nline two\r\n\tthree", 200, "line one line two three"},
		{"bidi \u202Eevil\u202C text", 200, "bidi evil text"},
		{"zero\u200Bwidth\uFEFF", 200, "zerowidth"},
		{"para\u2028sep\u2029end", 200, "para sep end"},
		{"bell\x07 esc\x1b[31m red", 200, "bell esc[31m red"},
		{"  lots   of    space  ", 200, "lots of space"},
		{"\u00E9\u00E9\u00E9\u00E9\u00E9", 3, "\u00E9\u00E9\u2026"}, // rune cut, never a byte cut
	}
	for _, tt := range tests {
		if got := SanitizeText(tt.in, tt.max); got != tt.want {
			t.Errorf("SanitizeText(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}

func TestAskLooksLikeCredential(t *testing.T) {
	secrets := []string{
		"ghp_" + strings.Repeat("a1B2", 9),
		"my token is gho_abcdefghijklmnop1234",
		"github_pat_11ABCDEFG0123456789_abcdefghijk",
		"sk-proj-abcdefghij1234567890",
		"AKIAIOSFODNN7EXAMPLE",
		"xoxb-1234-5678-abcdefgh",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"3f9c0a1b2c3d4e5f6a7b8c9d0e1f2a3b",
	}
	for _, s := range secrets {
		if !LooksLikeCredential(s) {
			t.Errorf("LooksLikeCredential(%q) = false, want true", s)
		}
	}
	plain := []string{
		"yes",
		"use the staging database, not prod",
		"ask-117 is the ticket",
		"task-list and disk-usage",
		"agent-deck-work/ask-dialog-20261024/repo",
		"Supercalifragilisticexpialidocious",
		"version 1.16.33 on 2026-10-24",
	}
	for _, s := range plain {
		if LooksLikeCredential(s) {
			t.Errorf("LooksLikeCredential(%q) = true, want false", s)
		}
	}
}
