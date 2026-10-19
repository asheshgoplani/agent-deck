package sandboxguard

import (
	"path/filepath"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestViolation_Matrix pins the decision for every environment shape the
// suite runs in: the shared sandbox (HOME is a throwaway dir, passwd home is
// elsewhere), CI (HOME is the runner's passwd home), a developer box with
// TMPDIR inside the home, and IsolateHome's nested-root marker.
func TestViolation_Matrix(t *testing.T) {
	cases := []struct {
		name    string
		homes   []string
		tmp     string
		env     map[string]string
		path    string
		wantHit string
	}{
		{"sandbox: un-isolated package writes into the start HOME", []string{"/tmp/home", "/home/tester"}, "/tmp", nil, "/tmp/home/.claude", "/tmp/home"},
		{"sandbox: IsolateHome temp dir is fine", []string{"/tmp/home", "/home/tester"}, "/tmp", nil, "/tmp/ad-home-123/.claude", ""},
		{"sandbox: t.TempDir override is fine", []string{"/tmp/home", "/home/tester"}, "/tmp", nil, "/tmp/TestX123/001/codex", ""},
		{"sandbox: passwd home still guarded", []string{"/tmp/home", "/home/tester"}, "/tmp", nil, "/home/tester/.gemini", "/home/tester"},
		{"ci: runner home", []string{"/home/runner"}, "/tmp", nil, "/home/runner/.codex", "/home/runner"},
		{"ci: the home itself", []string{"/home/runner"}, "/tmp", nil, "/home/runner", "/home/runner"},
		{"ci: sibling prefix is not under the home", []string{"/home/runner"}, "/tmp", nil, "/home/runner2/.claude", ""},
		{"dev: TMPDIR inside home, temp path fine", []string{"/home/u"}, "/home/u/tmp", nil, "/home/u/tmp/TestX/1", ""},
		{"dev: TMPDIR inside home, real dir refused", []string{"/home/u"}, "/home/u/tmp", nil, "/home/u/.claude", "/home/u"},
		{"marker: nested isolated HOME is fine", []string{"/home/u"}, "/tmp", map[string]string{"HOME": "/home/u/nested", homeIsolationMarkerEnv: "1"}, "/home/u/nested/.claude", ""},
		{"no marker: nested HOME refused", []string{"/home/u"}, "/tmp", map[string]string{"HOME": "/home/u/nested"}, "/home/u/nested/.claude", "/home/u"},
		{"marker: HOME pointed back at the real home refused", []string{"/home/u"}, "/tmp", map[string]string{"HOME": "/home/u", homeIsolationMarkerEnv: "1"}, "/home/u/.claude", "/home/u"},
		{"marker: path outside the nested root refused", []string{"/home/u"}, "/tmp", map[string]string{"HOME": "/home/u/nested", homeIsolationMarkerEnv: "1"}, "/home/u/.codex", "/home/u"},
		{"marker: only the exact value counts", []string{"/home/u"}, "/tmp", map[string]string{"HOME": "/home/u/nested", homeIsolationMarkerEnv: "true"}, "/home/u/nested/.claude", "/home/u"},
		{"marker: nested XDG root is fine", []string{"/home/u"}, "/tmp", map[string]string{"XDG_CONFIG_HOME": "/home/u/sb/cfg", homeIsolationMarkerEnv: "1"}, "/home/u/sb/cfg/opencode", ""},
		{"relative path is ignored", []string{"/home/u"}, "/tmp", nil, "rel/.claude", ""},
		{"empty path is ignored", []string{"/home/u"}, "/tmp", nil, "", ""},
		{"unclean path is cleaned first", []string{"/home/u"}, "/tmp", nil, "/tmp/x/../../home/u/.claude", "/home/u"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := violation(c.path, c.homes, c.tmp, envMap(c.env))
			if got != c.wantHit {
				t.Fatalf("violation(%q, homes=%v, tmp=%q) = %q, want %q", c.path, c.homes, c.tmp, got, c.wantHit)
			}
		})
	}
}

func TestRealHomes_FiltersUnusableValues(t *testing.T) {
	got := realHomes([]string{"/tmp/home", "/tmp/home/", "", "relative", "/", "/tmp", "/home/u"}, "/tmp")
	want := []string{"/tmp/home", "/home/u"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("realHomes = %v, want %v", got, want)
	}
}

// TestCheckAndMust_RefuseTheStartHome drives the exported entry points with
// this process's own captured start HOME, the value an un-isolated package
// would resolve under.
func TestCheckAndMust_RefuseTheStartHome(t *testing.T) {
	homes := RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment (HOME is the temp dir or unset)")
	}
	bad := filepath.Join(homes[0], ".claude")
	if Violation(bad) == "" {
		// The only allowed reason is a temp dir nested in that home that also
		// contains the path, which a ".claude" child of the home never is.
		t.Fatalf("Violation(%q) = \"\", want %q", bad, homes[0])
	}
	err := Check("Claude config dir", bad)
	if err == nil || !strings.Contains(err.Error(), "Claude config dir") || !strings.Contains(err.Error(), "IsolateHome") {
		t.Fatalf("Check error = %v, want one naming the kind and the fix", err)
	}
	ok := filepath.Join(t.TempDir(), ".claude")
	if err := Check("Claude config dir", ok); err != nil {
		t.Fatalf("Check(t.TempDir()) = %v, want nil", err)
	}
	if got := Must("Claude config dir", ok); got != ok {
		t.Fatalf("Must returned %q, want %q", got, ok)
	}
	defer func() {
		if recover() == nil {
			t.Fatalf("Must(%q) did not panic", bad)
		}
	}()
	Must("Claude config dir", bad)
}

func TestStartHomeIsReal(t *testing.T) {
	cases := []struct {
		home, tmp string
		isolated  bool
		want      bool
	}{
		{"/home/runner", "/tmp", false, true},       // CI
		{"/home/u", "/home/u/tmp", false, true},     // TMPDIR inside the home
		{"/tmp/home", "/tmp", false, false},         // sandbox runner's throwaway HOME
		{"/tmp/TestX123/001", "/tmp", false, false}, // re-exec helper with HOME=t.TempDir()
		{"/var/folders/ab/T/TestX/001", "/var/folders/ab/T", false, false},
		{"/home/runner", "/tmp", true, false}, // started with the isolation marker
		{"", "/tmp", false, false},
		{"relative", "/tmp", false, false},
	}
	for _, c := range cases {
		if got := startHomeIsReal(c.home, c.tmp, c.isolated); got != c.want {
			t.Errorf("startHomeIsReal(%q, %q, %v) = %v, want %v", c.home, c.tmp, c.isolated, got, c.want)
		}
	}
}
