package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skipRecorder is a testing.TB that records Skip calls instead of stopping
// the goroutine, so the helpers can be checked in both directions.
type skipRecorder struct {
	testing.TB
	skipped bool
	msg     string
}

func (r *skipRecorder) Helper() {}
func (r *skipRecorder) Skip(args ...any) {
	r.skipped = true
	r.msg = strings.TrimSpace(strings.Join(func() []string {
		s := make([]string, len(args))
		for i, a := range args {
			s[i] = a.(string)
		}
		return s
	}(), " "))
}
func (r *skipRecorder) Skipf(format string, args ...any) { r.skipped = true; r.msg = format }

func TestRequireTmux_SkipsWhenTmuxMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := &skipRecorder{TB: t}
	RequireTmux(r)
	if !r.skipped || !strings.Contains(r.msg, "tmux") {
		t.Fatalf("RequireTmux with empty PATH: skipped=%v msg=%q; want skip naming tmux", r.skipped, r.msg)
	}
}

func TestRequireTmux_RunsWhenTmuxPresent(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	r := &skipRecorder{TB: t}
	RequireTmux(r)
	if r.skipped {
		t.Fatalf("RequireTmux with tmux on PATH skipped: %q", r.msg)
	}
}

func TestRequireWritableHome(t *testing.T) {
	writable := t.TempDir()
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o755) })
	cases := []struct {
		name, home string
		wantSkip   bool
	}{
		{"writable", writable, false},
		{"missing", filepath.Join(writable, "does-not-exist"), true},
		{"unset", "", true},
	}
	// root ignores directory modes, so the read-only case only proves anything as non-root.
	if os.Geteuid() != 0 {
		cases = append(cases, struct {
			name, home string
			wantSkip   bool
		}{"read-only", readOnly, true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home)
			r := &skipRecorder{TB: t}
			RequireWritableHome(r)
			if r.skipped != tc.wantSkip {
				t.Fatalf("HOME=%q: skipped=%v, want %v", tc.home, r.skipped, tc.wantSkip)
			}
		})
	}
	entries, err := os.ReadDir(writable)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("RequireWritableHome left %d probe entries in HOME", len(entries))
	}
}

func TestSkipShort_MatchesShortFlag(t *testing.T) {
	r := &skipRecorder{TB: t}
	SkipShort(r, "spawns the real binary")
	if r.skipped != testing.Short() {
		t.Fatalf("SkipShort skipped=%v, testing.Short()=%v", r.skipped, testing.Short())
	}
	if r.skipped && !strings.Contains(r.msg, "spawns the real binary") {
		t.Fatalf("skip reason %q does not carry the caller's why", r.msg)
	}
}
