package update

import (
	"strings"
	"testing"
)

// TestAutoUpdateSuppressed_EachCondition pins every skip condition of
// issue #2251 on its own, and that a plain interactive environment is not
// suppressed.
func TestAutoUpdateSuppressed_EachCondition(t *testing.T) {
	cases := []struct {
		name   string
		env    []string
		goTest bool
		want   string // substring of the reason; "" means not suppressed
	}{
		{"interactive", []string{"HOME=/h", "TERM=xterm", "CI=", "CI_JOB=x"}, false, ""},
		{"go test", []string{"HOME=/h"}, true, "running under go test"},
		{"skip env", []string{SkipUpdateCheckEnv + "=1"}, false, SkipUpdateCheckEnv + " set"},
		{"skip env off value", []string{SkipUpdateCheckEnv + "=false"}, false, ""},
		{"CI true", []string{"CI=true"}, false, "CI=true"},
		{"CI 1", []string{"CI=1"}, false, "CI=1"},
		{"CI false", []string{"CI=false"}, false, ""},
		{"test marker", []string{"AGENTDECK_TEST_ARGV_LOG=/tmp/x"}, false, "AGENTDECK_TEST_ARGV_LOG set"},
		{"other AGENTDECK var", []string{"AGENTDECK_COLOR=none", "AGENTDECK_TELEMETRY=0"}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := autoUpdateSuppressed(tc.env, tc.goTest)
			if tc.want == "" && got != "" {
				t.Fatalf("suppressed with %q, want interactive", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("reason = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// TestTUIAutoUpdateSuppressed_NeedsBothTTYs pins the TUI rule: a pipe on
// either side suppresses, and the process-wide reasons win over the
// terminal reason.
func TestTUIAutoUpdateSuppressed_NeedsBothTTYs(t *testing.T) {
	if got := tuiAutoUpdateSuppressed(nil, false, true, true); got != "" {
		t.Fatalf("two TTYs must not be suppressed, got %q", got)
	}
	if got := tuiAutoUpdateSuppressed(nil, false, false, true); got != "stdin is not a terminal" {
		t.Fatalf("stdin pipe: %q", got)
	}
	if got := tuiAutoUpdateSuppressed(nil, false, true, false); got != "stdout is not a terminal" {
		t.Fatalf("stdout pipe: %q", got)
	}
	if got := tuiAutoUpdateSuppressed(nil, false, false, false); got != "stdin and stdout are not a terminal" {
		t.Fatalf("both piped: %q", got)
	}
	if got := tuiAutoUpdateSuppressed([]string{"CI=true"}, false, true, true); got != "CI=true" {
		t.Fatalf("CI must win even with two TTYs, got %q", got)
	}
}

// TestAutoUpdateSuppressed_UnderGoTest is the live check: this very
// process is a go test binary, so the real predicates must refuse without
// any environment help.
func TestAutoUpdateSuppressed_UnderGoTest(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "")
	t.Setenv("CI", "")
	if got := AutoUpdateSuppressed(); got != "running under go test" {
		t.Fatalf("AutoUpdateSuppressed() = %q under go test", got)
	}
	if got := TUIAutoUpdateSuppressed(); got != "running under go test" {
		t.Fatalf("TUIAutoUpdateSuppressed() = %q under go test", got)
	}
}
