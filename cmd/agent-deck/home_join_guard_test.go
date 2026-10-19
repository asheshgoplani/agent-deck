package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
)

// TestCLIHomeDerivedToolPathsRefuseTheRealHome covers the CLI call sites that
// build ~/.claude, ~/.claude.json, ~/.codex, ~/.local/share or ~/.pi
// themselves. With HOME at the real home, the way an un-isolated test sees it,
// each must panic in the guard before it reads anything there.
func TestCLIHomeDerivedToolPathsRefuseTheRealHome(t *testing.T) {
	homes := sandboxguard.RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment")
	}
	real := homes[0]

	cases := []struct {
		name string
		fn   func()
	}{
		{"nearestClaudeAncestorKind", func() { nearestClaudeAncestorKind(os.Getpid()) }},
		{"claudeLoggedIn default dir", func() { claudeLoggedIn("") }},
		{"loginFacts codex", func() { loginFacts("codex", nil) }},
		{"loginFacts opencode", func() { loginFacts("opencode", nil) }},
		{"loginFacts pi", func() { loginFacts("pi", nil) }},
		{"hooksFacts codex", func() { hooksFacts("codex") }},
		{"codexLimits", func() { codexLimits(nil, time.Now()) }},
		{"statuslineAccount", func() { statuslineAccount("") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", real)
			for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_DATA_HOME"} {
				t.Setenv(k, "")
			}
			msg := func() (msg string) {
				defer func() {
					if r := recover(); r != nil {
						msg = fmt.Sprint(r)
					}
				}()
				c.fn()
				return ""
			}()
			if msg == "" {
				t.Fatalf("returned with HOME at the real home %q, want a sandboxguard panic", real)
			}
			if !strings.Contains(msg, "sandboxguard:") || !strings.Contains(msg, "through HOME=") {
				t.Fatalf("panic does not come from the guard naming HOME: %s", msg)
			}
		})
	}
}
