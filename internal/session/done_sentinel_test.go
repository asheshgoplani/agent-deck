package session

import "testing"

// Issue #1186: a worker asserts task completion by printing a machine-greppable
// sentinel line. These tests pin the parse contract for that line so the
// detection (cmd/agent-deck) and emit (daemon) sides agree on the format.

func TestParseDoneSentinel_OK(t *testing.T) {
	sig, ok := ParseDoneSentinel("===AGENTDECK_DONE=== status=ok summary=did the thing")
	if !ok {
		t.Fatalf("expected sentinel to parse, got ok=false")
	}
	if sig.Status != "ok" {
		t.Errorf("status = %q, want ok", sig.Status)
	}
	if sig.Summary != "did the thing" {
		t.Errorf("summary = %q, want %q", sig.Summary, "did the thing")
	}
}

func TestParseDoneSentinel_Fail(t *testing.T) {
	sig, ok := ParseDoneSentinel("===AGENTDECK_DONE=== status=fail summary=tests red")
	if !ok {
		t.Fatalf("expected sentinel to parse, got ok=false")
	}
	if sig.Status != "fail" {
		t.Errorf("status = %q, want fail", sig.Status)
	}
	if sig.Summary != "tests red" {
		t.Errorf("summary = %q, want %q", sig.Summary, "tests red")
	}
}

func TestParseDoneSentinel_LeadingPrefixOnLine(t *testing.T) {
	// The worker may print the sentinel with surrounding prose on the same
	// line; the marker just has to appear. Summary runs to end of line.
	sig, ok := ParseDoneSentinel("blah blah ===AGENTDECK_DONE=== status=ok summary=all green now")
	if !ok {
		t.Fatalf("expected sentinel to parse")
	}
	if sig.Status != "ok" || sig.Summary != "all green now" {
		t.Errorf("got status=%q summary=%q", sig.Status, sig.Summary)
	}
}

func TestParseDoneSentinel_EmptySummary(t *testing.T) {
	sig, ok := ParseDoneSentinel("===AGENTDECK_DONE=== status=ok summary=")
	if !ok {
		t.Fatalf("expected sentinel to parse")
	}
	if sig.Status != "ok" || sig.Summary != "" {
		t.Errorf("got status=%q summary=%q", sig.Status, sig.Summary)
	}
}

func TestParseDoneSentinel_Malformed(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"no marker", "just some ordinary assistant output"},
		{"marker no status", "===AGENTDECK_DONE=== summary=missing status"},
		{"invalid status value", "===AGENTDECK_DONE=== status=maybe summary=nope"},
		{"empty status value", "===AGENTDECK_DONE=== status= summary=x"},
		{"empty line", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ParseDoneSentinel(tc.line); ok {
				t.Errorf("expected malformed line %q to be rejected", tc.line)
			}
		})
	}
}

func TestScanDoneSentinel_LastWins(t *testing.T) {
	text := "doing work\n" +
		"===AGENTDECK_DONE=== status=fail summary=first attempt\n" +
		"retrying\n" +
		"===AGENTDECK_DONE=== status=ok summary=second attempt succeeded\n"
	sig, ok := ScanDoneSentinel(text)
	if !ok {
		t.Fatalf("expected scan to find a sentinel")
	}
	if sig.Status != "ok" || sig.Summary != "second attempt succeeded" {
		t.Errorf("expected last sentinel to win, got status=%q summary=%q", sig.Status, sig.Summary)
	}
}

func TestScanDoneSentinel_None(t *testing.T) {
	if _, ok := ScanDoneSentinel("nothing\nto\nsee\nhere"); ok {
		t.Errorf("expected no sentinel found in plain text")
	}
}
