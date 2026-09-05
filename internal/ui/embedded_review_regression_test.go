package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

func TestEmbeddedSettingRequiresRestartOfTransport(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			setIsolatedAgentDeckDir(t)
			if err := session.SaveUserConfig(&session.UserConfig{UI: session.UISettings{EmbeddedTerminal: &enabled}}); err != nil {
				t.Fatal(err)
			}
			session.ClearUserConfigCache()
			h, _, _ := armHomeWithOneSession(t)
			h.embeddedLayout = enabled
			h.settingsPanel.Show()
			h.settingsPanel.cursor = int(SettingEmbeddedTerminal)
			h.Update(tea.KeyMsg{Type: tea.KeySpace})
			if h.embeddedLayout != enabled {
				t.Errorf("active layout changed without replacing startup transport: %v", h.embeddedLayout)
			}
			if !strings.Contains(h.settingsPanel.View(), "applies at next launch") {
				t.Error("setting did not disclose next-launch behavior")
			}
			cfg, err := session.LoadUserConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.UI.GetEmbeddedTerminal() == enabled {
				t.Error("new preference was not saved")
			}
		})
	}
}

func TestEmbeddedBannerResizeKeepsPTYEmulatorAndMouseTogether(t *testing.T) {
	h, _, _ := armHomeWithOneSession(t)
	h.embeddedLayout = true
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	rect := h.embeddedPaneRect()
	emu := vt.NewSafeEmulator(rect.Width, rect.Height)
	defer emu.Close()
	h.embeddedTerminal = &embeddedTerminal{ptmx: master, emulator: emu, dirty: make(chan struct{}, 1)}
	h.sessionInput = &SessionInputRouter{}
	h.syncEmbeddedTerminalGeometry()
	h.Update(maintenanceCompleteMsg{result: session.MaintenanceResult{PrunedLogs: 1}})
	for _, shown := range []bool{true, false} {
		if !shown {
			h.Update(clearMaintenanceMsg{})
		}
		current := h.embeddedPaneRect()
		rows, cols, err := pty.Getsize(master)
		if err != nil {
			t.Fatal(err)
		}
		if rows != current.Height || cols != current.Width {
			t.Errorf("banner=%v PTY=%dx%d rect=%dx%d", shown, cols, rows, current.Width, current.Height)
		}
		if emu.Width() != current.Width || emu.Height() != current.Height {
			t.Errorf("banner=%v stale emulator size", shown)
		}
		h.sessionInput.mu.RLock()
		mouse := h.sessionInput.rect
		h.sessionInput.mu.RUnlock()
		if mouse != current {
			t.Errorf("banner=%v mouse rect=%+v want%+v", shown, mouse, current)
		}
	}
}

func TestEmbeddedDoubleClickUsesEnterTargets(t *testing.T) {
	for _, kind := range []string{"local", "window", "remote"} {
		t.Run(kind, func(t *testing.T) {
			h, inst, _ := armHomeWithOneSession(t)
			if kind == "remote" {
				h = armHomeWithOneRemoteSession(t)
			} else {
				h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
				if kind == "window" {
					h.flatItems = []session.Item{{Type: session.ItemTypeWindow, WindowSessionID: inst.ID, WindowIndex: 3}}
				}
			}
			h.embeddedLayout = true
			h.initialLoading = false
			h.cursor = 0
			h.sessionExists = func(*session.Instance) bool { return true }
			var opened insertTargetRef
			h.insertOpenKeySender = func(target insertTargetRef) (insertKeySender, error) {
				opened = target
				return &fakeInsertKeySender{}, nil
			}
			click := tea.MouseMsg{X: 1, Y: h.getListContentStartY(), Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
			h.Update(click)
			h.Update(click)
			if !h.embeddedMode {
				t.Fatalf("double-click %s did not attach", kind)
			}
			if kind == "window" && (!opened.hasWindow || opened.windowIndex != 3) {
				t.Fatalf("wrong window target: %+v", opened)
			}
			if kind == "remote" && !opened.isRemote() {
				t.Fatalf("wrong remote target: %+v", opened)
			}
		})
	}
}

func TestEmbeddedPagingUsesRenderedLines(t *testing.T) {
	for _, density := range []string{session.SidebarDensityMinimal, session.SidebarDensityCompact, session.SidebarDensityFull} {
		for _, key := range []tea.KeyType{tea.KeyCtrlD, tea.KeyCtrlU, tea.KeyCtrlF, tea.KeyCtrlB, tea.KeyPgUp, tea.KeyPgDown} {
			t.Run(fmt.Sprintf("%s/%d", density, key), func(t *testing.T) {
				h, inst, _ := armHomeWithOneSession(t)
				h.embeddedLayout = true
				h.sidebarDensity = density
				h.flatItems = nil
				for n := 0; n < 80; n++ {
					h.flatItems = append(h.flatItems, session.Item{Type: session.ItemTypeSession, Session: inst})
				}
				h.cursor = 40
				lines := h.getVisibleHeight()
				direction := 1
				if key == tea.KeyCtrlD || key == tea.KeyCtrlU || key == tea.KeyPgUp || key == tea.KeyPgDown {
					lines /= 2
				}
				if key == tea.KeyCtrlU || key == tea.KeyCtrlB || key == tea.KeyPgUp {
					direction = -1
				}
				rowLines := h.sidebarRowLines()
				want := 40 + direction*max(1, lines/rowLines)
				h.handleMainKey(tea.KeyMsg{Type: key})
				if h.cursor != want {
					t.Errorf("cursor=%d want%d for%d visible lines and%d-line rows", h.cursor, want, lines, rowLines)
				}
			})
		}
	}
}

func TestEmbeddedUpdateBannerGeometryAcrossLayouts(t *testing.T) {
	t.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "")
	for _, width := range []int{70, 160} {
		for _, debug := range []bool{false, true} {
			t.Run(fmt.Sprintf("width%d/debug%v", width, debug), func(t *testing.T) {
				h, _, _ := armHomeWithOneSession(t)
				h.embeddedLayout = true
				h.width, h.debugMode = width, debug
				wantLayout := LayoutModeDual
				if width == 70 {
					wantLayout = LayoutModeStacked
				}
				if h.getLayoutMode() != wantLayout {
					t.Fatalf("layout=%s want%s", h.getLayoutMode(), wantLayout)
				}
				master, slave, err := pty.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer master.Close()
				defer slave.Close()
				rect := h.embeddedPaneRect()
				emu := vt.NewSafeEmulator(rect.Width, rect.Height)
				defer emu.Close()
				h.embeddedTerminal = &embeddedTerminal{ptmx: master, emulator: emu, dirty: make(chan struct{}, 1)}
				h.sessionInput = &SessionInputRouter{}
				h.syncEmbeddedTerminalGeometry()
				changedGeometry := 0
				for _, msg := range []tea.Msg{
					updateCheckMsg{info: &update.UpdateInfo{Available: true, ReleasesBehind: 30}},
					maintenanceCompleteMsg{result: session.MaintenanceResult{PrunedLogs: 1}},
					updateCheckMsg{info: &update.UpdateInfo{Available: false}},
					clearMaintenanceMsg{},
				} {
					previous := h.embeddedPaneRect()
					h.Update(msg)
					current := h.embeddedPaneRect()
					if current != previous {
						changedGeometry++
					}
					switch m := msg.(type) {
					case updateCheckMsg:
						if h.shouldRenderUpdateNudge() != m.info.Available {
							t.Fatal("update banner state did not change")
						}
					case maintenanceCompleteMsg:
						if h.maintenanceMsg == "" {
							t.Fatal("maintenance banner did not appear")
						}
					case clearMaintenanceMsg:
						if h.maintenanceMsg != "" {
							t.Fatal("maintenance banner did not clear")
						}
					}
					rows, cols, err := pty.Getsize(master)
					if err != nil {
						t.Fatal(err)
					}
					if rows != current.Height || cols != current.Width || emu.Width() != current.Width || emu.Height() != current.Height {
						t.Errorf("%T: PTY %dx%d emulator %dx%d want %dx%d", msg, cols, rows, emu.Width(), emu.Height(), current.Width, current.Height)
					}
					h.sessionInput.mu.RLock()
					mouse := h.sessionInput.rect
					h.sessionInput.mu.RUnlock()
					if mouse != current {
						t.Errorf("%T mouse=%+v want%+v", msg, mouse, current)
					}
				}
				if changedGeometry == 0 {
					t.Fatal("no transition exercised a changed pane rectangle")
				}
			})
		}
	}
}
