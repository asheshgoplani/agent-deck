package session

import (
	"os"
	"path/filepath"
	"testing"
)

// envFileValue reads only absolute, clean, ".."-free env files under HOME or
// the project tree (CodeQL go/path-injection barrier before os.ReadFile).
func TestEnvFileValue_PathGuard(t *testing.T) {
	home := withTempAgentDeckHome(t, "")
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("export TELEGRAM_STATE_DIR=/srv/tg\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inHome := filepath.Join(home, "ops.env")
	write(inHome)
	inProject := filepath.Join(project, ".env")
	write(inProject)
	outside := filepath.Join(t.TempDir(), "outside.env")
	write(outside)
	odd := filepath.Join(home, "a..b.env")
	write(odd)
	t.Chdir(home)

	cases := []struct {
		name, path string
		want       bool
	}{
		{"home file", inHome, true},
		{"project file", inProject, true},
		{"cleaned dot-dot inside home", filepath.Join(project, "..", "ops.env"), true},
		{"relative", "ops.env", false},
		{"relative dot", "./ops.env", false},
		{"relative dot-dot", "../" + filepath.Base(home) + "/ops.env", false},
		{"outside roots", outside, false},
		{"dot-dot escaping home", home + "/../" + filepath.Base(filepath.Dir(outside)) + "/outside.env", false},
		{"dot-dot in name", odd, false},
		{"empty", "", false},
	}
	for _, c := range cases {
		v, ok := envFileValue(c.path, project, "TELEGRAM_STATE_DIR")
		if ok != c.want {
			t.Errorf("%s: envFileValue(%q) ok = %v (v=%q), want %v", c.name, c.path, ok, v, c.want)
		}
		if ok && v != "/srv/tg" {
			t.Errorf("%s: value = %q, want /srv/tg", c.name, v)
		}
	}
}

// ConductorNameFromPath only compares an absolute, clean project path with
// the conductor dir (CodeQL go/path-injection barrier before os.Stat).
func TestConductorNameFromPath_PathGuard(t *testing.T) {
	setupSwitchConductor(t, "")
	dir, err := ConductorNameDir("ops")
	if err != nil {
		t.Fatal(err)
	}
	if got := ConductorNameFromPath(dir); got != "ops" {
		t.Fatalf("absolute conductor dir: got %q, want ops", got)
	}
	if got := ConductorNameFromPath(dir + "/"); got != "ops" {
		t.Errorf("trailing slash: got %q, want ops", got)
	}
	if got := ConductorNameFromPath(filepath.Join(dir, "..", "ops")); got != "ops" {
		t.Errorf("cleanable absolute path: got %q, want ops", got)
	}
	t.Chdir(dir)
	for _, p := range []string{".", "../ops", "./../ops"} {
		if got := ConductorNameFromPath(p); got != "" {
			t.Errorf("relative %q: got %q, want \"\"", p, got)
		}
	}
	t.Chdir(filepath.Dir(dir))
	for _, p := range []string{"ops", "./ops"} {
		if got := ConductorNameFromPath(p); got != "" {
			t.Errorf("relative %q: got %q, want \"\"", p, got)
		}
	}
	for _, p := range []string{"  ", "\x00ops"} {
		if got := ConductorNameFromPath(p); got != "" {
			t.Errorf("odd %q: got %q, want \"\"", p, got)
		}
	}
}
