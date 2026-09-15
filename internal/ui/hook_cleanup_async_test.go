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
	}{
		{"delete", sessionDeletedMsg{deletedID: "gone"}},
		{"finish", worktreeFinishResultMsg{sessionID: "gone", sessionTitle: "gone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			h := NewHome()
			require.NotNil(t, h.storage)
			t.Cleanup(func() { _ = h.storage.Close() })
			h.width, h.height = 100, 30
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
			// RDWR avoids blocking fixture setup. Cleanup's ReadFile waits for EOF.
			writer, err := os.OpenFile(fifo, os.O_RDWR, 0600)
			require.NoError(t, err)
			defer writer.Close()
			returned := make(chan tea.Cmd, 1)
			go func() { _, cmd := h.Update(tc.msg); returned <- cmd }()
			var cmd tea.Cmd
			select {
			case cmd = <-returned:
			case <-time.After(time.Second):
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
			select {
			case <-done:
				t.Fatal("cleanup did not wait for the delayed registry")
			case <-time.After(50 * time.Millisecond):
			}
			// Input and rendering still work while the command waits on the registry.
			h.Update(tea.WindowSizeMsg{Width: 110, Height: 35})
			require.Equal(t, 110, h.width)
			h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
			require.True(t, h.search.IsVisible(), "search input must work while cleanup is blocked")
			require.NotEmpty(t, h.View())
			require.FileExists(t, artifact)
			_, err = writer.Write([]byte(`{"instances":[]}`))
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("background cleanup did not complete")
			}
			require.NoFileExists(t, artifact)
		})
	}
}
