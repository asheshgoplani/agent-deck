package ui

import (
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func remotePaneTestHome(t *testing.T, remoteName, sessionID string) *Home {
	t.Helper()
	home := NewHome()
	home.width = 100
	home.height = 30
	remote := session.RemoteSessionInfo{ID: sessionID, Title: "one", Status: "running", Tool: "claude", RemoteName: remoteName}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteName: remoteName, RemoteSession: &remote}}
	home.cursor = 0
	return home
}

// A pushed pane for the watched remote session lands in the preview cache
// exactly like a fetched preview would (content, TTL stamp, fetching flag
// cleared); a push for any other session is ignored; the change waiter is
// re-armed either way.
func TestRemotePaneWatch_PushedPaneUpdatesPreview(t *testing.T) {
	home := remotePaneTestHome(t, "box", "s1")
	home.remotePaneWatch = remotePaneWatchTarget{remote: "box", session: "s1"}
	key := remotePreviewCacheKey("box", "s1")
	home.previewCacheMu.Lock()
	home.previewFetchingID = key
	home.previewCacheMu.Unlock()

	_, cmd := home.Update(remoteChangedMsg{remoteName: "box", change: session.RemoteChange{Remote: "box", Pane: &session.RemotePaneEvent{Session: "s1", Content: "line\tone\n> "}}})
	if cmd == nil {
		t.Fatal("a pane event must re-arm the remote change waiter")
	}
	home.previewCacheMu.RLock()
	content, stamped := home.previewCache[key], !home.previewCacheTime[key].IsZero()
	fetching := home.previewFetchingID
	home.previewCacheMu.RUnlock()
	if content != expandTabs("line\tone\n> ") || !stamped {
		t.Fatalf("pane push must fill the preview cache (tabs expanded) and stamp its TTL; got %q stamped=%v", content, stamped)
	}
	if fetching != "" {
		t.Fatalf("a pane push for the key being fetched must clear the fetching flag, got %q", fetching)
	}

	// A capture failure keeps the last content and still advances the TTL.
	before := time.Now()
	home.Update(remoteChangedMsg{remoteName: "box", change: session.RemoteChange{Remote: "box", Pane: &session.RemotePaneEvent{Session: "s1", Err: "pane gone"}}})
	home.previewCacheMu.RLock()
	kept, at := home.previewCache[key], home.previewCacheTime[key]
	home.previewCacheMu.RUnlock()
	if kept != content || at.Before(before) {
		t.Fatalf("a failed capture must keep the last content and advance the TTL; got %q at %v", kept, at)
	}

	// A push for a session that is not under the cursor is dropped.
	home.Update(remoteChangedMsg{remoteName: "box", change: session.RemoteChange{Remote: "box", Pane: &session.RemotePaneEvent{Session: "s9", Content: "other"}}})
	home.previewCacheMu.RLock()
	_, leaked := home.previewCache[remotePreviewCacheKey("box", "s9")]
	home.previewCacheMu.RUnlock()
	if leaked {
		t.Fatal("a pane push for an unwatched session must not enter the cache")
	}
}

// A pushed pane that leaves the preview blank (the session is stopped and
// has no pane, or its screen is empty) starts one poll, whose transcript
// fallback fills the preview as it did before pushes existed; the watch
// stays. A blank push for a session with cached content polls nothing.
func TestRemotePaneWatch_BlankPanePollsOnce(t *testing.T) {
	home := remotePaneTestHome(t, "box", "s1")
	target := remotePaneWatchTarget{remote: "box", session: "s1"}
	home.remotePaneWatch = target
	key := remotePreviewCacheKey("box", "s1")

	_, cmd := home.Update(remoteChangedMsg{remoteName: "box", change: session.RemoteChange{Remote: "box", Pane: &session.RemotePaneEvent{Session: "s1", Err: "tmux session not initialized"}}})
	if cmd == nil {
		t.Fatal("a capture failure with nothing cached must poll the preview")
	}
	home.previewCacheMu.RLock()
	fetching := home.previewFetchingID
	home.previewCacheMu.RUnlock()
	if fetching != key {
		t.Fatalf("poll must be marked in flight for the remote key, got %q", fetching)
	}
	if home.remotePaneWatch != target {
		t.Fatalf("the watch must survive a capture failure, got %+v", home.remotePaneWatch)
	}
	// The poll lands: from now on a blank push keeps the transcript.
	home.Update(previewFetchedMsg{previewKey: key, content: "transcript text"})
	home.Update(remoteChangedMsg{remoteName: "box", change: session.RemoteChange{Remote: "box", Pane: &session.RemotePaneEvent{Session: "s1", Content: "  \n"}}})
	home.previewCacheMu.RLock()
	content, fetching := home.previewCache[key], home.previewFetchingID
	home.previewCacheMu.RUnlock()
	if content != "transcript text" || fetching != "" {
		t.Fatalf("an empty screen must keep the transcript and not poll again; content=%q fetching=%q", content, fetching)
	}
}

// When the watch request fails the TUI forgets the watch and polls that
// preview once more, so the pane is never left blank on an old remote or a
// dropped channel.
func TestRemotePaneWatch_FailedWatchFallsBackToPoll(t *testing.T) {
	home := remotePaneTestHome(t, "box", "s1")
	target := remotePaneWatchTarget{remote: "box", session: "s1"}
	home.remotePaneWatch = target

	_, cmd := home.Update(remotePaneWatchMsg{target: target, err: errors.New("verb not allowed over the channel")})
	if home.remotePaneWatch != (remotePaneWatchTarget{}) {
		t.Fatalf("a failed watch must be forgotten, still %+v", home.remotePaneWatch)
	}
	if cmd == nil {
		t.Fatal("a failed watch must trigger a preview poll for the focused remote session")
	}
	home.previewCacheMu.RLock()
	fetching := home.previewFetchingID
	home.previewCacheMu.RUnlock()
	if fetching != remotePreviewCacheKey("box", "s1") {
		t.Fatalf("poll must be marked in flight for the remote key, got %q", fetching)
	}

	// A stale failure (the cursor moved on) changes nothing.
	home.remotePaneWatch = remotePaneWatchTarget{remote: "box", session: "s2"}
	_, cmd = home.Update(remotePaneWatchMsg{target: target, err: errors.New("late")})
	if cmd != nil || home.remotePaneWatch.session != "s2" {
		t.Fatalf("a failure for an old target must be ignored; cmd=%v watch=%+v", cmd, home.remotePaneWatch)
	}
}

// With no channel open for the remote (old remote, channels disabled, or
// not yet dialled) the cursor sync asks for nothing and the poll path is
// used; leaving the remote row releases a watch it had asked for.
func TestRemotePaneWatch_NoChannelMeansPoll(t *testing.T) {
	home := remotePaneTestHome(t, "no-such-remote", "s1")
	if cmd := home.syncRemotePaneWatch(); cmd != nil || home.remotePaneWatch != (remotePaneWatchTarget{}) {
		t.Fatalf("without a channel nothing must be watched; cmd=%v watch=%+v", cmd, home.remotePaneWatch)
	}
	if remotePaneWatchActive("no-such-remote", "s1") {
		t.Fatal("no channel, no active watch")
	}
	home.remotePaneWatch = remotePaneWatchTarget{remote: "no-such-remote", session: "s1"}
	home.flatItems = nil
	if cmd := home.syncRemotePaneWatch(); home.remotePaneWatch != (remotePaneWatchTarget{}) {
		t.Fatalf("moving off the remote row must drop the watch target; cmd=%v watch=%+v", cmd, home.remotePaneWatch)
	}
}

// The transcript fallback is chosen once per remote: after --pane is seen
// to be unknown on a remote, the poll path stops trying it.
func TestRemotePaneWatch_PaneFlagUnsupportedIsRemembered(t *testing.T) {
	home := NewHome()
	if home.remotePaneIsUnsupported("box") {
		t.Fatal("no remote is unsupported before anything was fetched")
	}
	if !remotePaneFlagUnknown(errors.New("ssh command failed: exit status 2: flag provided but not defined: -pane")) {
		t.Fatal("the flag package's complaint about --pane must be recognised")
	}
	if remotePaneFlagUnknown(errors.New("ssh command failed: exit status 1: tmux session not initialized")) {
		t.Fatal("an ordinary failure must not mark --pane unsupported")
	}
	home.setRemotePaneUnsupported("box")
	if !home.remotePaneIsUnsupported("box") || home.remotePaneIsUnsupported("other") {
		t.Fatal("unsupported must be remembered per remote")
	}
}
