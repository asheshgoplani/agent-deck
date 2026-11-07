package ui

// Welcome empty state (ASK-211): the main screen with no sessions is the
// welcome, three actions, full width at 80x24 and 120x40, header numbers and
// the Alt+j/k tip held back. Runs in a scratch HOME (onboardingEnv) with a
// fake PATH; the import command is returned, never run (it would talk to tmux).

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
)

// startWelcome starts an install past first run (config.toml exists), at
// width x height, with no sessions: the welcome is the main screen.
func startWelcome(t *testing.T, width, height int, found ...string) (*Home, *onboardingEnv) {
	t.Helper()
	e := newOnboardingEnv(t, found...)
	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h := NewHome()
	h.Update(tea.WindowSizeMsg{Width: width, Height: height})
	h.showSetupWizardIfFirstRun()
	h.Update(loadSessionsMsg{})
	if h.onboarding.IsVisible() {
		h.Update(keyEsc) // no agent: the check page opens first
	}
	if !h.welcomeActive() {
		t.Fatalf("no sessions, yet the welcome is not active:\n%s", onboardingScreen(h))
	}
	return h, e
}

func TestWelcome_FullWidthNoTruncation(t *testing.T) {
	for _, sz := range [][2]int{{120, 40}, {80, 24}} {
		w, hgt := sz[0], sz[1]
		t.Run(fmt.Sprintf("%dx%d", w, hgt), func(t *testing.T) {
			h, _ := startWelcome(t, w, hgt, "claude", "codex")
			screen := onboardingScreen(h)
			for _, want := range []string{
				"Welcome to Agent Deck",
				"Run many AI coding agents at once, each in its own session.",
				"Start here",
				"▶ n      New session with Claude Code in ~/billing-api",
				"  i      Import your tmux sessions",
				"  ?      Read the 5 keys",
				"↑↓ pick · Enter do it · q quit",
			} {
				if !strings.Contains(screen, want) {
					t.Errorf("welcome lacks %q:\n%s", want, screen)
				}
			}
			lines := strings.Split(screen, "\n")
			// The main area: below the header and filter bar, above the
			// footer (the filter bar's own "…" at 80 columns predates this).
			main := strings.Join(lines[2:len(lines)-2], "\n")
			for _, gone := range []string{"…", "...", "No Sessions Yet", "Ready to Go", "Get started by creating"} {
				if strings.Contains(main, gone) {
					t.Errorf("welcome shows %q (cut text or the old empty state):\n%s", gone, screen)
				}
			}
			if len(lines) != hgt {
				t.Errorf("frame is %d rows, want %d", len(lines), hgt)
			}
			for i, l := range lines {
				if lw := lipgloss.Width(l); lw > w {
					t.Errorf("row %d is %d wide, over %d: %q", i, lw, w, l)
				}
			}
		})
	}
}

// A long folder gives way from its left end; the agent and the folder's
// own name still show, and nothing spills past the box.
func TestWelcome_LongFolderTruncatesFromTheLeft(t *testing.T) {
	h, e := startWelcome(t, 80, 24, "claude")
	deep := e.home + "/" + strings.Repeat("very-long-directory-name/", 4) + "billing-api"
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deep)
	screen := onboardingScreen(h)
	if !strings.Contains(screen, "New session with Claude Code in …") || !strings.Contains(screen, "/billing-api") {
		t.Fatalf("long folder not cut from the left:\n%s", screen)
	}
}

func TestWelcome_NoAgentOffersPlainShellAndInstallLine(t *testing.T) {
	h, _ := startWelcome(t, 120, 40)
	screen := onboardingScreen(h)
	for _, want := range []string{"New session with a Plain shell in ~/billing-api",
		"No agent found yet. Install Claude Code:"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("no-agent welcome lacks %q:\n%s", want, screen)
		}
	}
}

func TestWelcome_HeaderNumbersHiddenUntilASessionExists(t *testing.T) {
	prev := headerSysStatsSnapshot
	t.Cleanup(func() { headerSysStatsSnapshot = prev })
	headerSysStatsSnapshot = func(*sysinfo.Collector) sysinfo.Stats {
		return sysinfo.Stats{CPU: sysinfo.CPUStat{Available: true, UsagePercent: 42}}
	}
	h, e := startWelcome(t, 120, 40, "claude")
	if h.sysStatsCollector == nil {
		t.Skip("no system stats collector on this build")
	}
	header := strings.Split(onboardingScreen(h), "\n")[0]
	if strings.Contains(header, "42%") {
		t.Fatalf("welcome header shows host numbers: %q", header)
	}
	inst := session.NewInstanceWithTool("billing-api", e.folder, "claude")
	h.Update(loadSessionsMsg{instances: []*session.Instance{inst}})
	if h.welcomeActive() {
		t.Fatal("a session exists, yet the welcome is still active")
	}
	header = strings.Split(onboardingScreen(h), "\n")[0]
	if !strings.Contains(header, "42%") {
		t.Fatalf("with a session the header lost its host numbers: %q", header)
	}
}

func TestWelcome_NavTipHeldBackWithoutSessions(t *testing.T) {
	h, _ := startWelcome(t, 120, 40, "claude")
	if h.navHintActive || strings.Contains(onboardingScreen(h), "Alt+j/k") {
		t.Fatalf("the Alt+j/k tip shows on the welcome:\n%s", onboardingScreen(h))
	}
}

func TestWelcome_NotShownWithSessionsOrFilters(t *testing.T) {
	newOnboardingEnv(t, "claude")
	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h := startDeck(t, session.NewInstanceWithTool("one", t.TempDir(), "claude"))
	if h.welcomeActive() || strings.Contains(onboardingScreen(h), "Welcome to Agent Deck") {
		t.Fatal("welcome shown with a session")
	}
	h2, _ := startWelcome(t, 120, 40, "claude")
	h2.statusFilter = FilterModeArchived
	if h2.welcomeActive() {
		t.Fatal("welcome shown over the archived filter's empty state")
	}
}

func TestWelcome_ArrowsAndEnter(t *testing.T) {
	h, _ := startWelcome(t, 120, 40, "claude")
	steps := []struct {
		key  tea.KeyMsg
		want int
	}{
		{keyDown, 1}, {keyDown, 2}, {keyDown, 0}, {keyUp, 2}, {runeKey('k'), 1}, {runeKey('j'), 2},
	}
	for _, s := range steps {
		h.Update(s.key)
		if h.welcomeSel != s.want {
			t.Fatalf("after %s selection = %d, want %d", s.key.String(), h.welcomeSel, s.want)
		}
	}
	if !strings.Contains(onboardingScreen(h), "▶ ?      Read the 5 keys") {
		t.Fatalf("selection marker not on the third action:\n%s", onboardingScreen(h))
	}
	h.Update(keyEnter)
	if !h.helpOverlay.IsVisible() {
		t.Fatal("Enter on Read the 5 keys did not open help")
	}
	h.Update(keyEsc)

	h.welcomeSel = 1
	_, cmd := h.Update(keyEnter)
	if cmd == nil || h.newDialog.IsVisible() || h.helpOverlay.IsVisible() {
		t.Fatalf("Enter on Import: cmd=%v dialog=%v help=%v, want the import command only", cmd != nil, h.newDialog.IsVisible(), h.helpOverlay.IsVisible())
	}

	h.welcomeSel = 0
	h.Update(keyEnter)
	// The form shows the folder with ~; GetValues gives the path it creates in.
	if _, path, _ := h.newDialog.GetValues(); !h.newDialog.IsVisible() || h.newDialog.GetSelectedCommand() != "claude" || path != onboardingFolder() {
		t.Fatalf("Enter on New session: dialog=%v tool=%q path=%q", h.newDialog.IsVisible(), h.newDialog.GetSelectedCommand(), path)
	}
}

func TestWelcome_ActionKeys(t *testing.T) {
	t.Run("n", func(t *testing.T) {
		h, _ := startWelcome(t, 80, 24, "codex")
		h.Update(runeKey('n'))
		if !h.newDialog.IsVisible() {
			t.Fatal("n did not open the New Session form")
		}
		if got := h.newDialog.GetSelectedCommand(); got != "codex" {
			t.Fatalf("n: tool = %q, want the first installed codex", got)
		}
		if _, got, _ := h.newDialog.GetValues(); got != onboardingFolder() {
			t.Fatalf("n: path = %q, want %q", got, onboardingFolder())
		}
	})
	t.Run("i", func(t *testing.T) {
		h, _ := startWelcome(t, 80, 24, "claude")
		_, cmd := h.Update(runeKey('i'))
		if cmd == nil {
			t.Fatal("i returned no import command")
		}
		if h.welcomeSel != int(welcomeImport) {
			t.Fatalf("i: selection = %d, want the import row", h.welcomeSel)
		}
	})
	t.Run("?", func(t *testing.T) {
		h, _ := startWelcome(t, 80, 24, "claude")
		h.Update(runeKey('?'))
		if !h.helpOverlay.IsVisible() {
			t.Fatal("? did not open help")
		}
	})
}

func TestWelcome_NothingToImportNotice(t *testing.T) {
	h, _ := startWelcome(t, 80, 24, "claude")
	h.Update(welcomeNoticeMsg{text: "No tmux sessions to import. Start one with n."})
	if !strings.Contains(onboardingScreen(h), "No tmux sessions to import. Start one with n.") {
		t.Fatalf("notice not shown:\n%s", onboardingScreen(h))
	}
	h.Update(keyDown)
	h.Update(runeKey('?'))
	h.Update(keyEsc)
	if strings.Contains(onboardingScreen(h), "No tmux sessions to import") {
		t.Fatal("the notice stayed after the next action")
	}
}

// pinWelcomeForGolden fixes what the welcome reads from the machine (the
// folder agent-deck runs in and the agents on PATH), so a frame golden of an
// empty install is the same in the container, on CI and on a laptop.
func pinWelcomeForGolden(t *testing.T) {
	t.Helper()
	prevFolder, prevLook, prevNames := welcomeFolder, onboardingLookPath, welcomeTmuxSessionNames
	t.Cleanup(func() { welcomeFolder, onboardingLookPath, welcomeTmuxSessionNames = prevFolder, prevLook, prevNames })
	welcomeFolder = func() string { return "/work/my-app" }
	onboardingLookPath = func(string) (string, error) { return "", errors.New("not found") }
	welcomeTmuxSessionNames = func() ([]string, error) { return nil, nil }
}
