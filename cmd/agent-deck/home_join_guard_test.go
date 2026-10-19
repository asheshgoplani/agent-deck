package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// guardPanic runs fn and returns the panic message, or "" when fn returned.
func guardPanic(fn func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	fn()
	return ""
}

// unisolatedRealHome points HOME at the real home and clears the tool
// overrides and XDG_DATA_HOME, the way an un-isolated test sees the
// environment, and returns that home.
func unisolatedRealHome(t *testing.T) string {
	t.Helper()
	homes := sandboxguard.RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment")
	}
	t.Setenv("HOME", homes[0])
	for _, k := range append(append([]string{}, sandboxguard.ToolConfigDirEnvs...), "XDG_DATA_HOME") {
		t.Setenv(k, "")
	}
	return homes[0]
}

// TestCLIHomeDerivedToolPathsRefuseTheRealHome covers the CLI call sites that
// build ~/.claude, ~/.claude.json, ~/.codex, ~/.local/share or ~/.pi
// themselves. With HOME at the real home, the way an un-isolated test sees it,
// each must panic in the guard before it reads anything there, and the panic
// must carry that site's path kind, so a guarded resolver further down the
// same function cannot stand in for it.
func TestCLIHomeDerivedToolPathsRefuseTheRealHome(t *testing.T) {
	cases := []struct {
		name, wantKind string
		fn             func()
	}{
		{"nearestClaudeAncestorKind", "Claude config dir (~/.claude)", func() { nearestClaudeAncestorKind(os.Getpid()) }},
		{"claudeLoggedIn default dir", "Claude root config (~/.claude.json)", func() { claudeLoggedIn("") }},
		{"loginFacts codex", "Codex home (~/.codex)", func() { loginFacts("codex", nil) }},
		{"loginFacts opencode", "XDG data dir (~/.local/share)", func() { loginFacts("opencode", nil) }},
		{"loginFacts pi", "Pi auth file (~/.pi)", func() { loginFacts("pi", nil) }},
		{"hooksFacts codex", "Codex home (~/.codex)", func() { hooksFacts("codex") }},
		{"codexLimits", "Codex home (~/.codex)", func() { codexLimits(nil, time.Now()) }},
		{"statuslineAccount", "Claude config dir (~/.claude)", func() { statuslineAccount("") }},
		{"codexNotifyHome", "Codex home (~/.codex)", func() { codexNotifyHome() }},
		{"applyClaudeTitleSync", "Claude config dir (~/.claude)", func() { applyClaudeTitleSync("sgf-inst", "sgf-no-such-session") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			real := unisolatedRealHome(t)
			msg := guardPanic(c.fn)
			if msg == "" {
				t.Fatalf("returned with HOME at the real home %q, want a sandboxguard panic", real)
			}
			if !strings.Contains(msg, "sandboxguard:") || !strings.Contains(msg, "through HOME=") {
				t.Fatalf("panic does not come from the guard naming HOME: %s", msg)
			}
			if !strings.Contains(msg, c.wantKind) {
				t.Fatalf("panic is not for %q, so this site is not guarded: %s", c.wantKind, msg)
			}
		})
	}
}

// TestCodexHooksConfigPathRefusesTheRealHome: codex-hooks install and
// uninstall write the file getCodexConfigPath returns. Under go test with an
// unisolated real home, or a CODEX_HOME leaked in from the shell, resolving
// it must refuse before anything is written. The test resolves only; it never
// runs the installer against the real home.
func TestCodexHooksConfigPathRefusesTheRealHome(t *testing.T) {
	t.Run("HOME", func(t *testing.T) {
		unisolatedRealHome(t)
		msg := guardPanic(func() { getCodexConfigPath() })
		if !strings.Contains(msg, "Codex config (~/.codex/config.toml)") || !strings.Contains(msg, "through HOME=") {
			t.Fatalf("getCodexConfigPath with HOME at the real home: panic = %q, want a guard panic naming HOME", msg)
		}
	})
	t.Run("leaked CODEX_HOME", func(t *testing.T) {
		real := unisolatedRealHome(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("CODEX_HOME", filepath.Join(real, ".codex"))
		msg := guardPanic(func() { getCodexConfigPath() })
		if !strings.Contains(msg, "Codex config (CODEX_HOME)") || !strings.Contains(msg, "through CODEX_HOME=") {
			t.Fatalf("getCodexConfigPath with a leaked CODEX_HOME: panic = %q, want a guard panic naming CODEX_HOME", msg)
		}
	})
	t.Run("sandbox unchanged", func(t *testing.T) {
		unisolatedRealHome(t)
		sandbox := t.TempDir()
		t.Setenv("HOME", sandbox)
		if got, want := getCodexConfigPath(), filepath.Join(sandbox, ".codex", "config.toml"); got != want {
			t.Fatalf("getCodexConfigPath() = %q, want %q", got, want)
		}
	})
}

// TestLoginFactsCodexConfigDirSkipsHomeGuard: with codex.config_dir set,
// loginFacts never uses ~/.codex, so it must not consult the guard for it.
func TestLoginFactsCodexConfigDirSkipsHomeGuard(t *testing.T) {
	unisolatedRealHome(t)
	cfg := &session.UserConfig{}
	cfg.Codex.ConfigDir = t.TempDir()
	if msg := guardPanic(func() { loginFacts("codex", cfg) }); msg != "" {
		t.Fatalf("loginFacts(codex) with codex.config_dir set panicked on ~/.codex it does not use: %s", msg)
	}
}
