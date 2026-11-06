package ui

// ASK-211 onboarding, review round 2 (plan and prototype by Sharjeel):
// regressions for the findings of the second review walk.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// The form shows the start folder as ~/...; Tab from that prefilled,
// existing folder moves on like it did with the absolute path, instead of
// re-completing the same folder for ever.
func TestOnboardingRound2_TabLeavesTildePath(t *testing.T) {
	home := setXDGTestHome(t)
	project := filepath.Join(home, "code", "my-app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	stubToolInstalled(t, "claude")
	d := NewNewDialog()
	d.SetSize(80, 24)
	d.ShowInGroup("", "", project, nil, "")
	if got := d.pathInput.Value(); got != "~/code/my-app" {
		t.Fatalf("path field = %q, want ~/code/my-app", got)
	}
	d.focusIndex = d.indexOf(focusPath)
	d.updateFocus()
	for i := 0; i < 2; i++ {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})
		if d.currentTarget() != focusPath {
			return
		}
	}
	t.Fatalf("Tab on %q stayed on Path (value now %q)", "~/code/my-app", d.pathInput.Value())
}

// The startup box asks about Claude's settings only when Claude is around:
// the CLI is installed or the profile has a Claude-compatible session.
func TestOnboardingRound2_HooksBoxOnlyWithClaude(t *testing.T) {
	for _, tc := range []struct {
		name  string
		found []string
		tool  string
		want  bool
	}{
		{"no agent, shell session", nil, "shell", false},
		{"codex only", []string{"codex"}, "codex", false},
		{"claude installed, shell session", []string{"claude"}, "shell", true},
		{"claude session, CLI gone", nil, "claude", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newOnboardingEnv(t, tc.found...)
			h := NewHome()
			h.pendingHooksPrompt = true
			h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			h.Update(loadSessionsMsg{instances: []*session.Instance{session.NewInstanceWithTool("old", t.TempDir(), tc.tool)}})
			shown := h.confirmDialog.IsVisible() && h.confirmDialog.GetConfirmType() == ConfirmInstallHooks
			if shown != tc.want {
				t.Fatalf("hooks box shown = %v, want %v", shown, tc.want)
			}
			if !tc.want && !h.pendingHooksPrompt {
				t.Fatal("the question was closed for a user who was never asked")
			}
		})
	}
}

// After quick create the box preselects Install like the form's ticked
// line, and Esc means later: nothing recorded, the question stays open.
func TestOnboardingRound2_QuickCreateHooksBoxDefaultsToInstall(t *testing.T) {
	setXDGTestHome(t)
	installs := 0
	prev := claudeHooksInstaller
	claudeHooksInstaller = func(string) error { installs++; return nil }
	t.Cleanup(func() { claudeHooksInstaller = prev })

	h := NewHome()
	if db := statedb.GetGlobal(); db != nil {
		_ = db.SetMeta("hooks_prompted", "") // another test's answer
	}
	h.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.initialLoading = false
	h.pendingHooksPrompt = true
	h.Update(sessionCreatedMsg{instance: session.NewInstanceWithTool("quick", t.TempDir(), "claude")})
	if !h.confirmDialog.IsVisible() || h.confirmDialog.GetConfirmType() != ConfirmInstallHooks {
		t.Fatal("quick create did not ask the hooks question")
	}
	if h.confirmDialog.GetFocusedButton() != 0 {
		t.Fatalf("focused button = %d, want Install (0)", h.confirmDialog.GetFocusedButton())
	}
	h.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.confirmDialog.IsVisible() || !h.pendingHooksPrompt || installs != 0 {
		t.Fatalf("Esc: box=%v pending=%v installs=%d, want closed, still pending, none", h.confirmDialog.IsVisible(), h.pendingHooksPrompt, installs)
	}
	if db := statedb.GetGlobal(); db != nil {
		if v, _ := db.GetMeta("hooks_prompted"); v != "" {
			t.Fatalf("Esc recorded hooks_prompted=%q", v)
		}
	}
}

// The box's text wraps once as sentences: no word stranded on its own line.
func TestOnboardingRound2_HooksBoxTextWrapsCleanly(t *testing.T) {
	for _, w := range []int{80, 120} {
		c := NewConfirmDialog()
		c.ShowInstallHooks(true)
		c.SetSize(w, 30)
		view := stripAnsi(c.View())
		if !strings.Contains(view, "busy or needs me") {
			t.Fatalf("%d cols: box lacks the plain wording:\n%s", w, view)
		}
		for _, line := range strings.Split(view, "\n") {
			inner := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│"))
			if inner != "" && !strings.ContainsAny(inner, " \u00a0─╭╮╰╯") {
				t.Fatalf("%d cols: a word alone on a line %q:\n%s", w, inner, view)
			}
		}
		if !strings.Contains(strings.ReplaceAll(view, "\u00a0", " "), "y install · n skip · Esc later") {
			t.Fatalf("%d cols: footer not on one line:\n%s", w, view)
		}
	}
}

// At 80 columns the short step 1 keeps the way back (Ctrl+Q) and drops the
// name, which a generated quick-create name would push off.
func TestOnboardingRound2_Step1KeepsCtrlQAt80(t *testing.T) {
	h := hintsHome(t, 80, 24)
	h.startOnboardingHints("spring-mesa-and-the-actual-project")
	line := hintBarLine(h)
	if !strings.Contains(line, "Ctrl+Q") {
		t.Fatalf("80-col step 1 lost Ctrl+Q: %q", line)
	}
	if w := len([]rune(line)); w > 80 {
		t.Fatalf("bar is %d cells", w)
	}
}

// Step 2 is drawn in the attached session's own status-left (the bar is not
// on screen then) and taken away again on the way back.
func TestOnboardingRound2_Step2InAttachedStatusLine(t *testing.T) {
	var set []string
	cleared := 0
	prevSet, prevClear := onboardingSetStatusHint, onboardingClearStatusHint
	onboardingSetStatusHint = func(_ *tmux.Session, text string) { set = append(set, text) }
	onboardingClearStatusHint = func(*tmux.Session) { cleared++ }
	t.Cleanup(func() { onboardingSetStatusHint, onboardingClearStatusHint = prevSet, prevClear })

	h := hintsHome(t, 80, 24)
	h.startOnboardingHints("my-app")
	sess := &tmux.Session{Name: "agentdeck_my-app"}
	h.advanceOnboardingHint(hintStepOpen) // what attachSession does first
	h.showBackHintInStatus(sess)
	if len(set) != 1 || !strings.Contains(set[0], "Step 2 of 4 · Ctrl+Q comes back here") {
		t.Fatalf("status-left hint = %q", set)
	}
	h.Update(statusUpdateMsg{attachedSessionID: "x"})
	if cleared != 1 || h.hints.statusTmux != nil {
		t.Fatalf("on return: cleared=%d statusTmux=%v", cleared, h.hints.statusTmux)
	}
	if h.hints.step != hintStepNew {
		t.Fatalf("step = %d, want %d", h.hints.step, hintStepNew)
	}
	// A later attach (step 3 on) leaves the status line alone.
	h.showBackHintInStatus(sess)
	if len(set) != 1 {
		t.Fatalf("status hint set again at step %d", h.hints.step)
	}
}

// Step 3 ("Press n to start another session") opens the same short form as
// the first session, not the full one, while the hint bar runs.
func TestOnboardingRound2_SecondFormShortDuringHints(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := NewHome()
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.initialLoading = false
	h.instancesMu.Lock()
	h.instances = []*session.Instance{session.NewInstanceWithTool("first", t.TempDir(), "claude")}
	h.instancesMu.Unlock()
	h.hints.step = hintStepHelp // n at step 3 moved the bar on
	h.openLocalNewSessionDialog()
	if !h.newDialog.firstSession || !h.newDialog.modelRowsFolded() {
		t.Fatal("the form opened during the hint bar is the full one")
	}
	if strings.Contains(stripAnsi(h.newDialog.View()), "Model ID") {
		t.Fatal("Model ID shown in the short form")
	}

	h.hints.step = hintStepOff
	h.openLocalNewSessionDialog()
	if h.newDialog.firstSession {
		t.Fatal("after the hint bar, a profile with sessions still gets the short form")
	}
}

// cellBackgrounds returns each printed cell's background SGR ("" when none).
func cellBackgrounds(row string) []string {
	var out []string
	bg := ""
	for i := 0; i < len(row); {
		if row[i] == 0x1b && i+1 < len(row) && row[i+1] == '[' {
			j := i + 2
			for j < len(row) && (row[j] < 0x40 || row[j] > 0x7e) {
				j++
			}
			if j >= len(row) {
				break
			}
			if row[j] == 'm' {
				params := strings.Split(row[i+2:j], ";")
				for k := 0; k < len(params); k++ {
					switch p := params[k]; {
					case p == "" || p == "0" || p == "49":
						bg = ""
					case p == "48" && k+1 < len(params) && params[k+1] == "2" && k+4 < len(params):
						bg = strings.Join(params[k+2:k+5], ";")
						k += 4
					case (p == "38" || p == "58") && k+1 < len(params) && params[k+1] == "2":
						k += 4
					case (p == "38" || p == "48" || p == "58") && k+1 < len(params) && params[k+1] == "5":
						k += 2
					}
				}
			}
			i = j + 1
			continue
		}
		r := []rune(row[i:])[0]
		out = append(out, bg)
		i += len(string(r))
	}
	return out
}

// The dark-theme form is one surface (no terminal-background strips), and
// Name and Path are two field bars of the same width, at 80x24 (viewport
// path) and 120x40.
func TestOnboardingRound2_DarkFormHasNoStripsAndEqualFields(t *testing.T) {
	stubToolInstalled(t)
	forceTrueColorProfile()
	useThemeForTest(t, "dark")
	field := strings.TrimPrefix(lipgloss.ColorProfile().Color(string(dropdownMenuBg())).Sequence(true), "48;2;")
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 40}} {
		d := onboardingDialog(t, "", size.w, size.h)
		d.SetFirstSession(true)
		d.SetClaudeHooksUndecided(true)
		view := d.View()
		bars := map[string]int{}
		for i, row := range strings.Split(view, "\n") {
			if holes := backgroundHoles(row); len(holes) > 0 {
				t.Errorf("%dx%d row %d shows the terminal background at %q: %q", size.w, size.h, i, holes, stripAnsi(row))
			}
			plain := stripAnsi(row)
			for _, f := range []string{"Name:", "Path:"} {
				if strings.Contains(plain, f+"  >") {
					n := 0
					for _, bg := range cellBackgrounds(row) {
						if bg == field {
							n++
						}
					}
					bars[f] = n
				}
			}
		}
		if bars["Name:"] == 0 || bars["Name:"] != bars["Path:"] {
			t.Errorf("%dx%d field bars: Name %d cells, Path %d cells, want equal and non-zero", size.w, size.h, bars["Name:"], bars["Path:"])
		}
	}
}

// With Path focused, the path menu does not cover the hooks line under it,
// the consent the next Enter gives.
func TestOnboardingRound2_PathMenuLeavesHooksLine(t *testing.T) {
	stubToolInstalled(t, "claude")
	for _, size := range []struct{ w, h int }{{80, 24}, {60, 20}} {
		d := onboardingDialog(t, "", size.w, size.h)
		d.SetFirstSession(true)
		d.SetClaudeHooksUndecided(true)
		d.focusIndex = d.indexOf(focusPath)
		d.updateFocus()
		view := stripAnsi(d.View())
		if !strings.Contains(view, "Type custom path") {
			t.Fatalf("%dx%d: path menu not open:\n%s", size.w, size.h, view)
		}
		if !strings.Contains(view, "[x] Show me when Claude is busy") {
			t.Fatalf("%dx%d: the menu covers the hooks line:\n%s", size.w, size.h, view)
		}
	}
}
