package sandboxguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// passwdLookedUpBeforeTests records whether package init (or anything before
// the first test) already ran the passwd lookup.
var passwdLookedUpBeforeTests bool

func TestMain(m *testing.M) {
	passwdLookedUpBeforeTests = passwdHome.done.Load()
	os.Exit(m.Run())
}

// TestPasswdHomeIsLookedUpOnFirstUse pins that the guard does no passwd
// lookup at init: only the first check pays for it, so a production binary
// (every hook call) never does.
func TestPasswdHomeIsLookedUpOnFirstUse(t *testing.T) {
	if passwdLookedUpBeforeTests {
		t.Fatal("the passwd home was looked up at package init; it must wait for the first check")
	}
	calls := 0
	l := &lazyValue{lookup: func() string { calls++; return "/home/u" }}
	if calls != 0 {
		t.Fatalf("lookup ran %d times before first use", calls)
	}
	for i := 0; i < 3; i++ {
		if got := l.get(); got != "/home/u" {
			t.Fatalf("get() = %q, want /home/u", got)
		}
	}
	if calls != 1 {
		t.Fatalf("lookup ran %d times over three uses, want 1", calls)
	}
}

// TestCaptureStart_NoLookupsOutsideTests pins that the init capture reads
// nothing in a production binary and still captures the start HOME early in a
// test binary.
func TestCaptureStart_NoLookupsOutsideTests(t *testing.T) {
	getenvCalls, tempCalls := 0, 0
	getenv := func(k string) string {
		getenvCalls++
		return map[string]string{"HOME": "/home/u", "TMUX": "/tmp/tmux-1/default,1,0", homeIsolationMarkerEnv: "1"}[k]
	}
	tempDir := func() string { tempCalls++; return "/tmp" }

	if got := captureStart(false, getenv, tempDir); got != (startEnv{}) || getenvCalls != 0 || tempCalls != 0 {
		t.Fatalf("captureStart(prod) = %+v with %d env and %d temp-dir lookups, want an empty capture and no lookups", got, getenvCalls, tempCalls)
	}
	got := captureStart(true, getenv, tempDir)
	want := startEnv{home: "/home/u", tmux: "/tmp/tmux-1/default,1,0", tempDir: "/tmp", isolated: true}
	if got != want {
		t.Fatalf("captureStart(test) = %+v, want %+v", got, want)
	}
}

// TestViolationError_NamesTheLeakedEnvVar pins that a refusal says which env
// var the path came through, so a leaked CLAUDE_CONFIG_DIR or DSH_HOME reads
// as that, not only as "call IsolateHome".
func TestViolationError_NamesTheLeakedEnvVar(t *testing.T) {
	cases := []struct {
		name, path string
		env        map[string]string
		want       []string
	}{
		{"leaked CLAUDE_CONFIG_DIR", "/home/u/.claude/settings.json",
			map[string]string{"HOME": "/tmp/ad-home-1", "CLAUDE_CONFIG_DIR": "/home/u/.claude"},
			[]string{`through CLAUDE_CONFIG_DIR="/home/u/.claude"`, "leaked in from the environment", `t.Setenv("CLAUDE_CONFIG_DIR"`}},
		{"leaked DSH_HOME", "/home/u/.dsh",
			map[string]string{"DSH_HOME": "/home/u/.dsh"},
			[]string{`through DSH_HOME="/home/u/.dsh"`}},
		{"tool override beats HOME", "/home/u/.codex",
			map[string]string{"HOME": "/home/u", "CODEX_HOME": "/home/u/.codex"},
			[]string{`through CODEX_HOME=`}},
		{"HOME not isolated", "/home/u/.codex",
			map[string]string{"HOME": "/home/u"},
			[]string{`through HOME="/home/u"`, "HOME still points at the real home", "IsolateHome"}},
		{"XDG root", "/home/u/.config/opencode",
			map[string]string{"HOME": "/tmp/x", "XDG_CONFIG_HOME": "/home/u/.config"},
			[]string{`through XDG_CONFIG_HOME=`}},
		{"no env var matches", "/home/u/.claude",
			map[string]string{"HOME": "/tmp/x"},
			[]string{"the test is not sandboxed", "IsolateHome"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, val := leakSource(c.path, envMap(c.env))
			msg := violationError("Claude config dir", c.path, "/home/u", env, val).Error()
			for _, w := range c.want {
				if !strings.Contains(msg, w) {
					t.Errorf("message %q does not contain %q", msg, w)
				}
			}
			if c.name == "no env var matches" && strings.Contains(msg, "through ") {
				t.Errorf("message names an env var although none matched: %s", msg)
			}
		})
	}
}

// TestCheck_NamesTheLeakedEnvVar drives the exported Check with a leaked
// override pointing into this process's real home.
func TestCheck_NamesTheLeakedEnvVar(t *testing.T) {
	homes := RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment")
	}
	leaked := filepath.Join(homes[0], ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", leaked)
	err := Check("Claude config dir", filepath.Join(leaked, "settings.json"))
	if err == nil || !strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("Check error = %v, want one naming CLAUDE_CONFIG_DIR", err)
	}
}

// TestHomeJoin_RefusesTheRealHome pins the shared helper the home-derived
// defaults go through: under the real home it panics, under a sandbox HOME it
// is os.UserHomeDir plus filepath.Join.
func TestHomeJoin_RefusesTheRealHome(t *testing.T) {
	homes := RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment")
	}
	sandbox := t.TempDir()
	t.Setenv("HOME", sandbox)
	got, err := HomeJoin("Claude config dir", ".claude")
	if err != nil || got != filepath.Join(sandbox, ".claude") {
		t.Fatalf("HomeJoin under sandbox = %q, %v; want %q", got, err, filepath.Join(sandbox, ".claude"))
	}
	t.Setenv("HOME", homes[0])
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), `through HOME=`) {
			t.Fatalf("HomeJoin under the real home: recover() = %v, want a panic naming HOME", r)
		}
	}()
	HomeJoin("Claude config dir", ".claude")
}

// TestHomeJoin_NamesHOMEWithClaudeConfigDirAtHomeClaude: a HomeJoin path
// always comes from HOME. With CLAUDE_CONFIG_DIR=$HOME/.claude inherited from
// the shell, a prefix guess would blame CLAUDE_CONFIG_DIR, and clearing it
// would not stop the site. The refusal must name HOME.
func TestHomeJoin_NamesHOMEWithClaudeConfigDirAtHomeClaude(t *testing.T) {
	homes := RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment")
	}
	t.Setenv("HOME", homes[0])
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(homes[0], ".claude"))
	for name, fn := range map[string]func(){
		"HomeJoin": func() { HomeJoin("Claude config dir", ".claude", "projects") },
		"MustHome": func() { MustHome("Claude config dir", filepath.Join(homes[0], ".claude", "projects")) },
	} {
		t.Run(name, func(t *testing.T) {
			msg := func() (msg string) {
				defer func() { msg = fmt.Sprint(recover()) }()
				fn()
				return ""
			}()
			if !strings.Contains(msg, "through HOME=") || strings.Contains(msg, "CLAUDE_CONFIG_DIR") {
				t.Fatalf("panic = %s, want one naming HOME and not CLAUDE_CONFIG_DIR", msg)
			}
		})
	}
}
