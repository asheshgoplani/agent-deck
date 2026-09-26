package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func TestRound5OverlaysCentered(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 40}, {200, 50}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			fork := NewForkDialog()
			fork.visible = true
			fork.SetSize(size.width, size.height)

			plugin := NewPluginDialog()
			plugin.visible = true
			plugin.SetSize(size.width, size.height)

			watchers := NewWatcherPanel()
			watchers.SetSize(size.width, size.height)
			watchers.SetWatchers([]WatcherDisplayItem{{ID: "w1", Name: "watcher", Type: "test", Status: "running"}})
			watchers.Show()

			views := []struct {
				name string
				view string
			}{
				{"fork", fork.View()},
				{"plugin empty", plugin.View()},
				{"watchers list", watchers.View()},
			}
			plugin.items = []pluginDialogItem{{name: "example", id: "example"}}
			views = append(views, struct{ name, view string }{"plugin populated", plugin.View()})
			watchers.detailMode = true
			views = append(views, struct{ name, view string }{"watchers detail", watchers.View()})

			for _, tc := range views {
				t.Run(tc.name, func(t *testing.T) {
					lines := strings.Split(ansi.Strip(tc.view), "\n")
					top, bottom := -1, -1
					for i, line := range lines {
						if strings.ContainsRune(line, '╭') {
							top = i
						}
						if strings.ContainsRune(line, '╰') {
							bottom = i
						}
					}
					if top < 0 || bottom < top {
						t.Fatalf("missing complete overlay border")
					}
					border := lines[top]
					left := len(border) - len(strings.TrimLeft(border, " "))
					boxWidth := cellWidth(strings.TrimSpace(border))
					if want := (size.width - boxWidth) / 2; left != want {
						t.Errorf("left padding = %d, want %d", left, want)
					}
					boxHeight := bottom - top + 1
					if want := max(0, (size.height-boxHeight)/2); top != want {
						t.Errorf("top padding = %d, want %d", top, want)
					}
				})
			}
		})
	}
}

func TestRound5WatcherRulesFitOneLine(t *testing.T) {
	for _, width := range []int{80, 120, 200} {
		t.Run(fmt.Sprintf("%d", width), func(t *testing.T) {
			watchers := NewWatcherPanel()
			watchers.SetSize(width, 50)
			watchers.SetWatchers([]WatcherDisplayItem{{ID: "w1", Name: "watcher", Type: "test", Status: "running"}})
			watchers.Show()
			for _, detail := range []bool{false, true} {
				watchers.detailMode = detail
				frame := ansi.Strip(watchers.View())
				rule := "│ " + strings.Repeat("─", 58) + " │"
				want := 2
				if detail {
					want = 3
				}
				if got := strings.Count(frame, rule); got != want {
					t.Errorf("detail=%v: complete rules = %d, want %d", detail, got, want)
				}
			}
		})
	}
}

func TestRound5ForkFitsShortScreenAndFollowsFocus(t *testing.T) {
	fork := NewForkDialog()
	fork.visible = true
	fork.worktreeCapable = true
	fork.worktreeEnabled = true
	fork.withStateEnabled = true
	fork.SetSize(80, 24)
	for _, focus := range []forkFocusTarget{forkFocusName, forkFocusOptions} {
		fork.setFocus(focus)
		view := ansi.Strip(fork.View())
		lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
		if len(lines) >= 24 || !strings.Contains(view, "Fork Session") || !strings.Contains(view, "╭") || !strings.Contains(view, "╰") || !strings.Contains(view, "Esc cancel") {
			t.Fatalf("focus %v: fork dialog clips its title, footer, or border in %d rows:\n%s", focus, len(lines), view)
		}
		if focus == forkFocusOptions && !strings.Contains(view, "Advanced Options") {
			t.Fatalf("focused options are outside the viewport:\n%s", view)
		}
	}
}

func TestRound5ForkHomeUsesTerminalSize(t *testing.T) {
	home := NewHome()
	home.width, home.height = 80, 24
	inst := session.NewInstanceWithTool("fork source", t.TempDir(), "claude")
	home.forkSessionWithDialog(inst)
	if home.forkDialog.width != 80 || home.forkDialog.height != 24 {
		t.Fatalf("fork dialog size = %dx%d, want 80x24", home.forkDialog.width, home.forkDialog.height)
	}
}
