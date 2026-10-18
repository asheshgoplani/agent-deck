package triggers

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestFirstLineRedactsSecrets(t *testing.T) {
	cases := []struct {
		in       string
		mustNot  []string
		mustHave []string
	}{
		{"push with " + "gh" + "p_" + "abcdefghijklmnopqrstuvwxyz0123 now", []string{"p_abc"}, []string{"push with [redacted] now"}},
		{"key " + "sk" + "-proj_" + "ABCDEFGHIJKLMNOPqrstu used", []string{"proj_"}, []string{"key [redacted] used"}},
		{"slack " + "xo" + "xb-" + "1234567890-abcdefghij posted", []string{"1234567890-abc"}, []string{"slack [redacted] posted"}},
		{"Authorization: Bearer abcdefghijklmnop.qrstuv", []string{"abcdefghijklmnop"}, []string{"[redacted]"}},
		{"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N", []string{"eyJhbGci"}, []string{"jwt [redacted]"}},
		{"API_KEY=supersecretvalue and GITHUB_TOKEN: 'abc def'", []string{"supersecretvalue", "abc def"}, []string{"API_KEY=[redacted]", "GITHUB_TOKEN: [redacted]"}},
		{`password="hunter2 hunter2" done`, []string{"hunter2"}, []string{"password=[redacted] done"}},
	}
	for _, c := range cases {
		got := FirstLine(c.in)
		for _, bad := range c.mustNot {
			if strings.Contains(got, bad) {
				t.Errorf("FirstLine(%q) = %q still holds %q", c.in, got, bad)
			}
		}
		for _, want := range c.mustHave {
			if !strings.Contains(got, want) {
				t.Errorf("FirstLine(%q) = %q, want it to contain %q", c.in, got, want)
			}
		}
	}
}

func TestFirstLineShape(t *testing.T) {
	// The envelope and blank lines go; only the first real line is kept.
	if got := FirstLine("\n  \n[agent-deck from:abc-123] tick: 0 waiting jobs\nsecond line"); got != "tick: 0 waiting jobs" {
		t.Fatalf("envelope/first line: %q", got)
	}
	if got := FirstLine("[agent-deck from:abc]\n\nreal start\nmore"); got != "real start" {
		t.Fatalf("envelope on its own line: %q", got)
	}
	// Control characters never reach the store.
	if got := FirstLine("red \x1b[31malert\x07 bell"); strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("control characters kept: %q", got)
	}
	// Cut at LineCells display cells, counting wide runes as two.
	long := strings.Repeat("tick ", 40) // words, so no token shape matches
	if got := FirstLine(long); runewidth.StringWidth(got) > LineCells || !strings.HasSuffix(got, "…") {
		t.Fatalf("ascii cut: width %d %q", runewidth.StringWidth(got), got)
	}
	wide := strings.Repeat("界", 60) // 120 cells
	got := FirstLine(wide)
	if w := runewidth.StringWidth(got); w > LineCells || w < LineCells-2 {
		t.Fatalf("wide cut: width %d", w)
	}
	if short := "exactly fits"; FirstLine(short) != short {
		t.Fatalf("short line changed: %q", FirstLine(short))
	}
	if FirstLine("") != "" || FirstLine("\n\n") != "" {
		t.Fatal("empty message must give an empty line")
	}
}

func TestTextHash(t *testing.T) {
	a := TextHash("[agent-deck from:x] same text")
	b := TextHash("same text\n")
	if a == "" || a != b {
		t.Fatalf("hash must ignore the envelope and outer space: %q %q", a, b)
	}
	if TextHash("other text") == a {
		t.Fatal("different text, same hash")
	}
	if TextHash("  ") != "" {
		t.Fatal("blank message must have no hash (never a no-op)")
	}
}
