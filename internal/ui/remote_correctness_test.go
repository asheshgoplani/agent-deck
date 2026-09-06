// Remote-deck correctness (#2170): a stale fleet poll must not undo an
// action, a broken config must not wipe the remotes, and a refused rename
// must revert with a message instead of snapping back silently.

package ui

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func remoteTitles(h *Home, remote string) []string {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	var out []string
	for _, s := range h.remoteSessions[remote] {
		out = append(out, s.Title)
	}
	return out
}

func TestRemoteFetch_StaleResultIsIgnored(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	fresh := map[string][]session.RemoteSessionInfo{"box": {{ID: "a", Title: "kept"}}}
	stale := map[string][]session.RemoteSessionInfo{"box": {{ID: "a", Title: "kept"}, {ID: "b", Title: "deleted-meanwhile"}}}

	model, _ := home.Update(remoteSessionsFetchedMsg{gen: 5, sessions: fresh})
	h := model.(*Home)
	model, _ = h.Update(remoteSessionsFetchedMsg{gen: 4, sessions: stale})
	h = model.(*Home)

	if got := remoteTitles(h, "box"); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("a fetch older than the last applied one must be ignored; sessions = %v", got)
	}
	if h.remotesFetchActive {
		t.Fatal("a stale result must still release the in-flight guard")
	}
}

func TestRemoteFetch_ConfigErrorKeepsRemotes(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	model, _ := home.Update(remoteSessionsFetchedMsg{gen: 1, sessions: map[string][]session.RemoteSessionInfo{"box": {{ID: "a", Title: "kept"}}}})
	h := model.(*Home)
	model, _ = h.Update(remoteSessionsFetchedMsg{gen: 2, configErr: errors.New("toml: line 3: bad key")})
	h = model.(*Home)

	if got := remoteTitles(h, "box"); len(got) != 1 {
		t.Fatalf("an unreadable config must not drop cached remotes; sessions = %v", got)
	}
	if h.err == nil {
		t.Fatal("an unreadable config must be reported in the footer")
	}
}

func TestRemoteRename_RefusedRevertsTitle(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{"box": {{ID: "a", Title: "old"}}}

	if old := home.setRemoteSessionTitle("box", "a", "new"); old != "old" {
		t.Fatalf("setRemoteSessionTitle returned %q, want the previous title", old)
	}
	model, _ := home.Update(remoteRenameResultMsg{remoteName: "box", sessionID: "a", oldTitle: "old", newTitle: "new", err: errors.New("title already taken")})
	h := model.(*Home)

	if got := remoteTitles(h, "box"); got[0] != "old" {
		t.Fatalf("a refused rename must revert the cached title; got %v", got)
	}
	if h.err == nil {
		t.Fatal("a refused rename must be reported")
	}
}

func TestRemoteCreate_QueuedIsNoticeNotError(t *testing.T) {
	var queued *session.RemoteSessionQueuedError
	err := error(&session.RemoteSessionQueuedError{ID: "x", Title: "job"})
	if !errors.As(err, &queued) || queued.Title != "job" {
		t.Fatal("the queued outcome must be a typed error the TUI can tell apart from a failure")
	}
}
