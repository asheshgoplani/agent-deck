package ui

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// A blocked legacy registry read stands in for arbitrarily slow cleanup IO.
// Exercise the actual deletion handlers, without a replacement cleanup stub.
func TestHookCleanupDeletionKeepsUIResponsive(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.Msg
		undo bool
	}{
		{"delete", sessionDeletedMsg{deletedID: "gone"}, false},
		{"finish", worktreeFinishResultMsg{sessionID: "gone", sessionTitle: "gone"}, false},
		{"undo", sessionDeletedMsg{deletedID: "gone"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			h := NewHome()
			require.NotNil(t, h.storage)
			t.Cleanup(func() { _ = h.storage.Close() })
			h.width, h.height = 100, 30
			h.initialLoading = false // Fixture represents an already loaded session list.
			inst := &session.Instance{ID: "gone", Title: "gone", Tool: "shell", Status: session.StatusStopped, CreatedAt: time.Now()}
			h.instances = []*session.Instance{inst}
			h.instanceByID = map[string]*session.Instance{"gone": inst}
			h.groupTree = session.NewGroupTree(h.instances)
			h.rebuildFlatItems()
			require.NoError(t, h.storage.SaveWithGroups(h.instances, h.groupTree))
			hooks := session.GetHooksDir()
			require.NoError(t, os.MkdirAll(hooks, 0700))
			artifact := filepath.Join(hooks, "gone.json")
			require.NoError(t, os.WriteFile(artifact, []byte("{}"), 0600))
			profile, err := session.GetProfileDir("delayed")
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(profile, 0700))
			fifo := filepath.Join(profile, "sessions.json")
			require.NoError(t, syscall.Mkfifo(fifo, 0600))
			// Nonblocking writer open succeeds only when cleanup has opened the
			// read end. This handshake avoids guessing when the command is ready.
			openWriter := func() *os.File {
				deadline := time.Now().Add(5 * time.Second)
				for {
					fd, err := syscall.Open(fifo, syscall.O_WRONLY|syscall.O_NONBLOCK, 0600)
					if err == nil {
						return os.NewFile(uintptr(fd), fifo)
					}
					require.ErrorIs(t, err, syscall.ENXIO)
					if time.Now().After(deadline) {
						t.Fatal("cleanup did not open the delayed registry")
					}
					time.Sleep(time.Millisecond)
				}
			}
			returned := make(chan tea.Cmd, 1)
			go func() { _, cmd := h.Update(tc.msg); returned <- cmd }()
			var cmd tea.Cmd
			select {
			case cmd = <-returned:
			case <-time.After(time.Second):
				writer := openWriter()
				_, _ = writer.Write([]byte(`{"instances":[]}`))
				_ = writer.Close()
				select {
				case <-returned:
				case <-time.After(5 * time.Second):
					t.Fatal("deletion did not recover after cleanup was released")
				}
				t.Fatal("UI deletion handler blocked on hook cleanup")
			}
			require.NotNil(t, cmd, "deletion must schedule cleanup")
			rows, _, err := h.storage.LoadLite()
			require.NoError(t, err)
			require.Empty(t, rows, "registry deletion must commit before returning")
			done := make(chan struct{})
			go func() { cmd(); close(done) }()
			writer := openWriter()
			defer writer.Close()
			select {
			case <-done:
				t.Fatal("cleanup did not wait for the delayed registry")
			default:
			}
			// Input and rendering still work while the command waits on the registry.
			h.Update(tea.WindowSizeMsg{Width: 110, Height: 35})
			require.Equal(t, 110, h.width)
			h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
			require.True(t, h.search.IsVisible(), "search input must work while cleanup is blocked")
			if !tc.undo {
				// The undo case is also run against a pinned pre-fix revision
				// (hooks-fd-macos.yml) whose search overlay renders
				// differently; it must reach the undo assertion below, so the
				// frame is pinned by the two cases that stop here.
				frame := stripAnsi(h.View())
				if os.Getenv("UPDATE_GOLDEN") == "1" {
					require.NoError(t, os.WriteFile("testdata/hook_cleanup_search.txt", []byte(frame), 0644))
				}
				golden, err := os.ReadFile("testdata/hook_cleanup_search.txt")
				require.NoError(t, err)
				require.Equal(t, string(golden), frame, "search frame while deletion cleanup is blocked")
			}
			require.FileExists(t, artifact)
			var undoResult chan tea.Msg
			if tc.undo {
				// A deliberately invalid account makes Restart return before any
				// process launch. Its error still reveals when Restart was attempted.
				inst.Account = "missing-undo-test-account"
				h.Update(tea.KeyMsg{Type: tea.KeyEsc})
				_, undoCmd := h.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
				require.NotNil(t, undoCmd)
				undoResult = make(chan tea.Msg, 1)
				go func() { undoResult <- undoCmd() }()
				select {
				case result := <-undoResult:
					t.Error("undo restarted before hook cleanup completed")
					undoResult <- result
				case <-time.After(100 * time.Millisecond):
				}
			}
			_, err = writer.Write([]byte(`{"instances":[]}`))
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("background cleanup did not complete")
			}
			require.NoFileExists(t, artifact)
			if undoResult != nil {
				select {
				case result := <-undoResult:
					restored, ok := result.(sessionRestoredMsg)
					require.True(t, ok)
					require.ErrorContains(t, restored.err, "missing-undo-test-account")
				case <-time.After(5 * time.Second):
					t.Fatal("undo did not resume after cleanup completed")
				}
			}
		})
	}
}
