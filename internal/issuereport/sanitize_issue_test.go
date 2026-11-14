package issuereport

import "testing"

// MINOR-N2: SanitizeIssue re-applies Scrub and strips control/escape
// characters; a real Build output comes back byte-identical.
func TestSanitizeIssue_RescrubsAndStripsControl(t *testing.T) {
	ghp := fake("gh"+"p_", 36)
	body := Build(Report{Kind: KindBug, WhatHappened: "x"}, Diagnostics{}).Body
	in := Issue{
		Title:  "[bug] \x1b]0;evil\x07on devbox7\tnow \x1b[31mred\x1b[0m " + ghp,
		Labels: []string{"bug", "triage"},
		Body:   body + "token " + ghp + " me@example.com /home/alicew/secret \x1b[2J‮\r\n",
	}
	out, err := SanitizeIssue(KindBug, in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[bug] on <host> now red <token>"; out.Title != want {
		t.Fatalf("title %q, want %q", out.Title, want)
	}
	if want := body + "token <token> <email> ~/secret \n"; out.Body != want {
		t.Fatalf("body %q, want %q", out.Body, want)
	}
}

func TestSanitizeIssue_RealPreviewRoundTrips(t *testing.T) {
	withConfiguredIdentity(t, "", commonSSHConfig)
	for _, r := range []Report{
		{Kind: KindBug, Title: "typing doubles letters on buildmac-direct", WhatHappened: "git status in .git on dev; see https://pkg.go.dev/x\n# heading\n--- rule",
			Expected: "no doubles " + fake("gh"+"p_", 36), Steps: "1. ssh dev\n2. curl -u a:b x", Tool: "claude", Surface: "mac app"},
		{Kind: KindIdea, WhatHappened: "problem", Expected: "proposal", Steps: "alt"},
	} {
		d := Diagnostics{Version: "1.2.3", OS: "linux", Arch: "amd64", Terminal: "xterm",
			LogExcerpt: "{\"host\":\"deckbox\",\"msg\":\"x\"}\n\x1b[31mred\x1b[0m line\t tab\nhead 1af1da745d3c9c1f0f4f3a0e8b5f6a7c8d9e0f1a\n```fence"}
		is := Build(r, d)
		out, err := SanitizeIssue(r.Kind, is)
		if err != nil {
			t.Fatalf("%s: %v\n%s", r.Kind, err, is.Body)
		}
		if out.Title != is.Title || out.Body != is.Body {
			t.Fatalf("%s preview changed on rescrub\nbefore: %q\nafter:  %q", r.Kind, is.Body, out.Body)
		}
	}
}
