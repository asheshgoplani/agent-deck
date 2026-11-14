package issuereport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NIT-9: a long log line is scrubbed before it is cut to logLineCells, so a
// token that straddles the cut leaves no prefix behind.
func TestCollectDiagnostics_ScrubBeforeTruncate(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "debug.log")
	line := strings.Repeat("x", logLineCells-10) + " " + fake("gh"+"p_", 36) + " tail"
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := debugLogPath
	debugLogPath = func() (string, error) { return logPath, nil }
	t.Cleanup(func() { debugLogPath = prev })

	got := CollectDiagnostics("1", 5).LogExcerpt
	if strings.Contains(got, "gh"+"p_") || strings.Contains(got, "aB3") {
		t.Fatalf("a cut token left a prefix in the excerpt: %q", got[len(got)-30:])
	}
	if !strings.Contains(got, PlaceholderToken) {
		t.Fatalf("token placeholder missing: %q", got[len(got)-30:])
	}

	long := strings.Repeat("ab ", logLineCells)
	if err := os.WriteFile(logPath, []byte(long+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := CollectDiagnostics("1", 5).LogExcerpt; len([]rune(got)) != logLineCells {
		t.Fatalf("long line not cut to %d cells: %d", logLineCells, len([]rune(got)))
	}
}

// NIT-9: --surface text reaches the footer, so it is scrubbed too.
func TestBuild_ScrubsSurface(t *testing.T) {
	is := Build(Report{Kind: KindBug, WhatHappened: "x", Surface: "mac app on devbox7 as alicew"}, Diagnostics{})
	if !strings.Contains(is.Body, footerPrefix+"mac app on <host> as <user>\n") {
		t.Fatalf("surface not scrubbed in footer:\n%s", is.Body)
	}
}

func TestSanitizeIssue_Shape(t *testing.T) {
	bugBody := Build(Report{Kind: KindBug, WhatHappened: "x"}, Diagnostics{}).Body
	ideaBody := Build(Report{Kind: KindIdea, WhatHappened: "y"}, Diagnostics{}).Body
	good := Issue{Title: "[bug] x", Labels: []string{"bug", "triage"}, Body: bugBody}
	if _, err := SanitizeIssue(KindBug, good); err != nil {
		t.Fatalf("good bug issue rejected: %v", err)
	}
	if _, err := SanitizeIssue(KindIdea, Issue{Title: "[feature] y", Labels: []string{"triage", "enhancement"}, Body: ideaBody}); err != nil {
		t.Fatalf("good idea issue rejected: %v", err)
	}
	bad := map[string]Issue{
		"wrong prefix":   {Title: "[feature] x", Labels: good.Labels, Body: bugBody},
		"no prefix":      {Title: "x", Labels: good.Labels, Body: bugBody},
		"prefix only":    {Title: "[bug]  ", Labels: good.Labels, Body: bugBody},
		"two lines":      {Title: "[bug] x\ny", Labels: good.Labels, Body: bugBody},
		"extra label":    {Title: good.Title, Labels: []string{"bug", "triage", "p0"}, Body: bugBody},
		"missing label":  {Title: good.Title, Labels: []string{"bug"}, Body: bugBody},
		"no labels":      {Title: good.Title, Body: bugBody},
		"empty body":     {Title: good.Title, Labels: good.Labels, Body: " \n"},
		"oversized body": {Title: good.Title, Labels: good.Labels, Body: strings.Repeat("a", MaxBodyLen+1)},
		"no sections":    {Title: good.Title, Labels: good.Labels, Body: "just text\n"},
		"idea sections":  {Title: good.Title, Labels: good.Labels, Body: ideaBody},
		"long title":     {Title: "[bug] " + strings.Repeat("x", MaxTitleLen), Labels: good.Labels, Body: bugBody},
	}
	for name, is := range bad {
		if _, err := SanitizeIssue(KindBug, is); CodeOf(err) != ErrInvalidInput {
			t.Errorf("%s: err = %v, want invalid_input", name, err)
		}
	}
}
