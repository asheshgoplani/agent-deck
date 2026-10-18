package ui

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// recordUISessionClosed captures macapp.session.closed publishes as
// "profile/id/reason".
func recordUISessionClosed(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	t.Cleanup(session.SetSessionClosedPublisherForTest(func(profile, kind, id string, data any) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, profile+"/"+id+"/"+data.(map[string]string)["reason"])
	}))
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// TUI delete and TUI worktree finish announce the removed session to a
// following Mac app, as `agent-deck rm` does.
func TestTUIDeletePathsPublishSessionClosed(t *testing.T) {
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
			h.initialLoading = false
			inst := &session.Instance{ID: "gone", Title: "gone", Tool: "shell", Status: session.StatusStopped, CreatedAt: time.Now()}
			h.instances = []*session.Instance{inst}
			h.instanceByID = map[string]*session.Instance{"gone": inst}
			h.groupTree = session.NewGroupTree(h.instances)
			h.rebuildFlatItems()
			require.NoError(t, h.storage.SaveWithGroups(h.instances, h.groupTree))
			frames := recordUISessionClosed(t)
			h.Update(tc.msg)
			want := []string{h.storage.Profile() + "/gone/removed"}
			if got := frames(); !reflect.DeepEqual(got, want) {
				t.Fatalf("closed frames %v, want %v", got, want)
			}
		})
	}
}

// The web delete announces the removed session too.
func TestWebDeletePublishesSessionClosed(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_closed_web_delete")
	seedSession(t, storage, nil, "web-closed-001", "doomed")
	frames := recordUISessionClosed(t)
	if err := NewWebMutator(h).DeleteSession("web-closed-001"); err != nil {
		t.Fatal(err)
	}
	want := []string{storage.Profile() + "/web-closed-001/removed"}
	if got := frames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("closed frames %v, want %v", got, want)
	}
}
