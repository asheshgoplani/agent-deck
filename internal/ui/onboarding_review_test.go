package ui

// ASK-211 review fixes (onboarding from Sharjeel's plan and prototype):
// existing users keep their defaults and never get first-run screens, the
// hooks question still reaches users who skip the form, and the welcome and
// the hint bar follow rebound keys.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func rebindHotkeys(h *Home, overrides map[string]string) {
	h.hotkeys = resolveHotkeys(overrides)
	h.hotkeyLookup, h.blockedHotkeys = buildHotkeyLookup(h.hotkeys)
}

// An existing user (config.toml present) who opens an empty profile and
// creates a session does not get the 4-step bar; a fresh install does.
func TestOnboardingReview_HintsOnlyOnFreshInstall(t *testing.T) {
	newOnboardingEnv(t, "claude")
	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h := startDeck(t)
	if h.hints.armed {
		t.Fatal("an existing config with an empty profile armed the hint bar")
	}
	h.Update(sessionCreatedMsg{instance: session.NewInstanceWithTool("demo", t.TempDir(), "shell")})
	if h.hintBarVisible() {
		t.Fatalf("hint bar shown to an existing user: %q", hintBarLine(h))
	}
	if h.handleMainKey(keyEsc); onboardingHintsSeen() {
		t.Fatal("Esc on the main screen was taken by a hidden hint bar")
	}

	newOnboardingEnv(t, "claude")
	fresh := startDeck(t)
	if !fresh.hints.armed {
		t.Fatal("a fresh install did not arm the hint bar")
	}
}

// The welcome's new session follows the configured default_tool, as the
// New Session form does, not the first installed agent.
func TestOnboardingReview_WelcomeKeepsDefaultTool(t *testing.T) {
	h, _ := startWelcome(t, 120, 40, "claude", "codex")
	if err := session.SaveUserConfig(&session.UserConfig{DefaultTool: "codex"}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	if !strings.Contains(onboardingScreen(h), "New session with Codex") {
		t.Fatalf("welcome does not offer the configured Codex:\n%s", onboardingScreen(h))
	}
	h.Update(runeKey('n'))
	if !h.newDialog.IsVisible() {
		t.Fatal("n did not open the form")
	}
	if got := h.newDialog.GetSelectedCommand(); got != "codex" {
		t.Fatalf("welcome n with default_tool=codex: tool = %q, want codex", got)
	}
	if _, path, _ := h.newDialog.GetValues(); path != onboardingFolder() {
		t.Fatalf("path = %q, want %q", path, onboardingFolder())
	}
}

// The welcome's action keys are the bound ones, the same the screen shows
// (Home normalizes a rebound key to its action's default before the
// welcome sees it).
func TestOnboardingReview_WelcomeKeysFollowBindings(t *testing.T) {
	h, _ := startWelcome(t, 120, 40, "claude")
	rebindHotkeys(h, map[string]string{hotkeyNewSession: "ctrl+n"})
	h.welcomeSel = int(welcomeHelp) // the welcome's action moves it back
	if !strings.Contains(onboardingScreen(h), "ctrl+n") {
		t.Fatalf("welcome does not show the rebound key:\n%s", onboardingScreen(h))
	}
	h.Update(runeKey('n'))
	if h.newDialog.IsVisible() {
		t.Fatal("n opened the welcome's form after new_session was rebound")
	}
	h.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if !h.newDialog.IsVisible() || h.welcomeSel != int(welcomeNew) {
		t.Fatalf("the rebound ctrl+n did not run the welcome's new session (dialog=%v sel=%d)", h.newDialog.IsVisible(), h.welcomeSel)
	}
	if _, path, _ := h.newDialog.GetValues(); path != onboardingFolder() || h.newDialog.GetSelectedCommand() != "claude" {
		t.Fatalf("rebound key opened an unfilled form: tool=%q path=%q", h.newDialog.GetSelectedCommand(), path)
	}
}

// Steps 1 and 2 teach the detach key the user has, and the done line the
// help key.
func TestOnboardingReview_HintBarUsesBoundDetachKey(t *testing.T) {
	h := hintsHome(t, 120, 40)
	rebindHotkeys(h, map[string]string{hotkeyDetach: "ctrl+b", hotkeyHelp: "f1"})
	h.startOnboardingHints("my-app")
	if line := hintBarLine(h); !strings.Contains(line, "Ctrl+B brings you back here.") || strings.Contains(line, "Ctrl+Q") {
		t.Fatalf("step 1 with detach on ctrl+b: %q", line)
	}
	h.advanceOnboardingHint(hintStepOpen)
	if line := hintBarLine(h); !strings.Contains(line, "Press Ctrl+B to come back to the list.") {
		t.Fatalf("step 2 with detach on ctrl+b: %q", line)
	}
	h.hints.step = hintStepDone
	if line := hintBarLine(h); !strings.Contains(line, "Press f1 any time") {
		t.Fatalf("done line with help on f1: %q", line)
	}
}

// A Docker sandbox runs the tool from its image: a tool missing on the
// host shows no warning.
func TestOnboardingReview_SandboxNotBlockedByHostPath(t *testing.T) {
	stubToolInstalled(t) // nothing installed on the host
	d := onboardingDialog(t, "claude", 120, 50)
	d.nameInput.SetValue("demo")
	if !d.selectedToolMissing() {
		t.Fatal("precondition: claude missing on the host is flagged")
	}
	d.sandboxEnabled = true
	if d.selectedToolMissing() {
		t.Fatal("a sandboxed create is flagged for a tool missing on the host")
	}
	if msg := d.Validate(); msg != "" {
		t.Fatalf("sandboxed Create refused: %q", msg)
	}
	if strings.Contains(stripAnsi(d.View()), "not found here") {
		t.Fatal("sandboxed form still shows the missing warning")
	}
}

// The hooks question still reaches a user who never uses the form's hooks
// line: a Claude session made by quick create (or any other path) brings
// the old box once, and its answer is recorded.
func TestOnboardingReview_HooksAskedWithoutTheForm(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     tea.KeyMsg
		install bool
	}{{"install", runeKey('y'), true}, {"skip", runeKey('n'), false}} {
		t.Run(tc.name, func(t *testing.T) {
			setXDGTestHome(t)
			installs := 0
			prev := claudeHooksInstaller
			claudeHooksInstaller = func(string) error { installs++; return nil }
			t.Cleanup(func() { claudeHooksInstaller = prev })

			h := NewHome()
			h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			h.initialLoading = false
			h.pendingHooksPrompt = true
			h.Update(sessionCreatedMsg{instance: session.NewInstanceWithTool("quick", t.TempDir(), "claude")})
			if !h.confirmDialog.IsVisible() || h.confirmDialog.GetConfirmType() != ConfirmInstallHooks {
				t.Fatal("a Claude session created without the form did not ask the hooks question")
			}
			h.Update(tc.key)
			if h.pendingHooksPrompt || h.confirmDialog.IsVisible() {
				t.Fatalf("hooks question still open: pending=%v box=%v", h.pendingHooksPrompt, h.confirmDialog.IsVisible())
			}
			if got := installs == 1; got != tc.install {
				t.Fatalf("installs = %d, want install=%v", installs, tc.install)
			}
		})
	}

	// A shell session does not ask; the question stays open for later.
	setXDGTestHome(t)
	h := NewHome()
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.initialLoading = false
	h.pendingHooksPrompt = true
	h.Update(sessionCreatedMsg{instance: session.NewInstanceWithTool("sh", t.TempDir(), "shell")})
	if h.confirmDialog.IsVisible() {
		t.Fatal("a shell session asked the Claude hooks question")
	}
}

// A profile that already has sessions keeps the startup box it always had;
// an empty one leaves the question to the form.
func TestOnboardingReview_HooksBoxAtStartOnlyWithSessions(t *testing.T) {
	newOnboardingEnv(t, "claude")
	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h := NewHome()
	h.pendingHooksPrompt = true
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.Update(loadSessionsMsg{instances: []*session.Instance{session.NewInstanceWithTool("old", t.TempDir(), "claude")}})
	if !h.confirmDialog.IsVisible() || h.confirmDialog.GetConfirmType() != ConfirmInstallHooks {
		t.Fatal("a profile with sessions lost the startup hooks box")
	}

	h2 := NewHome()
	h2.pendingHooksPrompt = true
	h2.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h2.Update(loadSessionsMsg{})
	if h2.confirmDialog.IsVisible() {
		t.Fatal("an empty profile got the hooks box; the form's line asks there")
	}
}

// The in-TUI check page and the printed one share the tmux install lines.
func TestOnboardingReview_TmuxInstallLinesShared(t *testing.T) {
	got := strings.Join(TmuxInstallLines("linux"), "\n")
	for _, want := range []string{"sudo apt install tmux", "sudo dnf install tmux", "sudo pacman -S tmux"} {
		if !strings.Contains(got, want) {
			t.Fatalf("linux install lines lack %q:\n%s", want, got)
		}
	}
}
