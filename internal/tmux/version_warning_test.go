package tmux

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsVulnerableTmuxVersion(t *testing.T) {
	cases := []struct {
		ver        string
		vulnerable bool
	}{
		{"3.6a", true},
		{"3.6", true},
		{"3.5a", true},
		{"3.4", true},
		{"2.8", true},
		{"3.6b", false},
		{"3.7", false},
		{"4.0", false},
		{"master", false},
		{"next", false},
		{"", false},
		{"garbage", false},
	}
	for _, c := range cases {
		got := isVulnerableTmuxVersion(c.ver)
		if got != c.vulnerable {
			t.Errorf("isVulnerableTmuxVersion(%q) = %v, want %v", c.ver, got, c.vulnerable)
		}
	}
}

func TestParseTmuxVersion(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"tmux 3.6a", "3.6a"},
		{"tmux 3.6a\n", "3.6a"},
		{"tmux 3.5\n", "3.5"},
		{"tmux master", "master"},
		{"tmux next-3.7", "next-3.7"},
		{"", ""},
		{"not tmux", ""},
	}
	for _, c := range cases {
		got := parseTmuxVersion(c.raw)
		if got != c.want {
			t.Errorf("parseTmuxVersion(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestDoCheckAndWarnTmuxVersion_WarnsOnVulnerableDarwin(t *testing.T) {
	var buf bytes.Buffer
	probe := func() (string, error) { return "tmux 3.6a", nil }
	doCheckAndWarnTmuxVersion(probe, &buf, "darwin", "")
	out := buf.String()
	if !strings.Contains(out, "3.6a") {
		t.Errorf("expected warning to contain version 3.6a, got: %q", out)
	}
	if !strings.Contains(out, "tmux") {
		t.Errorf("expected warning to mention tmux, got: %q", out)
	}
	if !strings.Contains(out, "AGENTDECK_SUPPRESS_TMUX_WARNING") {
		t.Errorf("expected warning to mention suppression env var, got: %q", out)
	}
}

func TestDoCheckAndWarnTmuxVersion_SilentOnPatched(t *testing.T) {
	var buf bytes.Buffer
	probe := func() (string, error) { return "tmux 3.6b", nil }
	doCheckAndWarnTmuxVersion(probe, &buf, "darwin", "")
	if buf.Len() != 0 {
		t.Errorf("expected no output on patched tmux, got: %q", buf.String())
	}
}

func TestDoCheckAndWarnTmuxVersion_SilentOnLinux(t *testing.T) {
	var buf bytes.Buffer
	probe := func() (string, error) { return "tmux 3.6a", nil }
	doCheckAndWarnTmuxVersion(probe, &buf, "linux", "")
	if buf.Len() != 0 {
		t.Errorf("expected no output on linux even if vulnerable version, got: %q", buf.String())
	}
}

func TestDoCheckAndWarnTmuxVersion_SuppressEnv(t *testing.T) {
	for _, v := range []string{"1", "true"} {
		var buf bytes.Buffer
		probe := func() (string, error) { return "tmux 3.6a", nil }
		doCheckAndWarnTmuxVersion(probe, &buf, "darwin", v)
		if buf.Len() != 0 {
			t.Errorf("expected suppression via %q, got output: %q", v, buf.String())
		}
	}
}

func TestDoCheckAndWarnTmuxVersion_SilentOnProbeError(t *testing.T) {
	var buf bytes.Buffer
	probe := func() (string, error) { return "", assertAnyErr{} }
	doCheckAndWarnTmuxVersion(probe, &buf, "darwin", "")
	if buf.Len() != 0 {
		t.Errorf("expected no output when probe errors, got: %q", buf.String())
	}
}

type assertAnyErr struct{}

func (assertAnyErr) Error() string { return "probe failed" }

func TestCheckAndWarnTmuxVersion_PrintsAtMostOnce(t *testing.T) {
	ResetVersionWarningOnceForTest()
	var buf bytes.Buffer
	probe := func() (string, error) { return "tmux 3.6a", nil }
	checkAndWarnTmuxVersion(probe, &buf, "darwin", "")
	checkAndWarnTmuxVersion(probe, &buf, "darwin", "")
	checkAndWarnTmuxVersion(probe, &buf, "darwin", "")
	out := buf.String()
	count := strings.Count(out, "agent-deck:")
	if count != 1 {
		t.Errorf("expected exactly 1 warning line, got %d. Output: %q", count, out)
	}
}
