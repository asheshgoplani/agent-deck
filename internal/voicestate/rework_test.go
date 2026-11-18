package voicestate

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Rework round 1 (review of 1020576613b9): S4, S8, mirror window.

func inodeOf(t *testing.T, p string) uint64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return uint64(fi.Sys().(*syscall.Stat_t).Ino)
}

// S4 (reviewer's R1): three tmp+rename writes in one mtime tick; the third
// can get the first one's inode back, so (mtime, size, inode) all match the
// cached file while the target differs. The reader must show the third.
func TestReader_InodeReuseWithinOneTickIsSeen(t *testing.T) {
	r, clock, dir := newTestReader(t, map[int]bool{4242: true})
	a := liveCurrent(1)
	b := strings.Replace(strings.Replace(a, "cf9a73f3-1789471273", "0b1c2d3e-1789471999", 1), "conductor-alex", "conductor-abcd", 1)
	c := strings.Replace(strings.Replace(a, "cf9a73f3-1789471273", "11112222-1789471555", 1), "conductor-alex", "conductor-wxyz", 1)
	mt := clock.t
	p := filepath.Join(dir, "current.json")
	reused := false
	for try := 0; try < 50 && !reused; try++ {
		replaceAt(t, dir, "current.json", a, mt)
		inoA := inodeOf(t, p)
		if s := r.Poll(); s == nil || s.Target.SessionID != "cf9a73f3-1789471273" {
			t.Fatalf("first poll: %+v", s)
		}
		replaceAt(t, dir, "current.json", b, mt)
		replaceAt(t, dir, "current.json", c, mt)
		if inodeOf(t, p) != inoA {
			clock.advance(time.Second)
			mt = clock.t
			r.Poll()
			continue
		}
		reused = true
	}
	if !reused {
		t.Skip("this filesystem did not reuse the inode in 50 rounds")
	}
	clock.advance(100 * time.Millisecond)
	if s := r.Poll(); s == nil || s.Target.SessionID != "11112222-1789471555" {
		t.Fatalf("inode reused within one mtime tick: reader shows %+v, want 11112222-1789471555", s)
	}
}

// S4 (reviewer's R2): a same-size rewrite in place inside one mtime tick.
func TestReader_InPlaceSameSizeSameTickIsSeen(t *testing.T) {
	r, clock, dir := newTestReader(t, map[int]bool{4242: true})
	a := liveCurrent(1)
	b := strings.Replace(a, "cf9a73f3-1789471273", "0b1c2d3e-1789471999", 1)
	writeAt(t, dir, "current.json", a, clock.t)
	if s := r.Poll(); s == nil {
		t.Fatal("first poll nil")
	}
	writeAt(t, dir, "current.json", b, clock.t)
	clock.advance(100 * time.Millisecond)
	if s := r.Poll(); s == nil || s.Target.SessionID != "0b1c2d3e-1789471999" {
		t.Fatalf("in-place same-size rewrite in one tick not seen: %+v", s)
	}
}

// S8 (reviewer's R3): voice state --json carries the client source.
func TestNewView_CarriesClientSource(t *testing.T) {
	s := &State{On: true, Phase: "listening", ClientSource: "glasses", Source: SourceCurrent}
	if got := NewView(s, time.Unix(1_794_308_000, 0)).Source; got != "glasses" {
		t.Fatalf("view source = %q, want glasses", got)
	}
}

// B2/S1: a relay-written mirror expires on the relay's own window
// (RelayMirrorWindow), not on the daemon's heartbeat it carries, so a
// stopped relay, a sleeping laptop or a dead link reads as no voice soon.
func TestReader_RelayMirrorExpiresOnRelayWindow(t *testing.T) {
	r, clock, dir := newTestReader(t, nil)
	body := `{"schema":"claude-code-voice.mirror/v1","relative":true,"on":true,"mode":"listening","phase":"listening",
 "target":{"title":"alpha","sessionId":"aaaa1111-1789000000","profile":"default","remote":""},"heartbeatMs":10000000,
 "from":"laptop","updatedAt":1}`
	writeAt(t, dir, "mirror.json", body, clock.t)
	if r.Poll() == nil {
		t.Fatal("fresh relay mirror read as no voice")
	}
	clock.advance(RelayMirrorWindow + time.Second)
	if s := r.Poll(); s != nil {
		t.Fatalf("relay mirror %s old (window %s) still live: %+v", RelayMirrorWindow+time.Second, RelayMirrorWindow, s)
	}
	if RelayMirrorWindow > 15*time.Second {
		t.Fatalf("relay mirror window %s: a stopped relay leaves remotes stale too long", RelayMirrorWindow)
	}
}

// S3: the relay reads only the daemon's own current.json, never a mirror
// another host's relay wrote here.
func TestReader_PollCurrentIgnoresMirror(t *testing.T) {
	r, clock, dir := newTestReader(t, nil)
	writeAt(t, dir, "mirror.json", `{"schema":"claude-code-voice.mirror/v1","relative":true,"on":true,"phase":"listening","from":"box","updatedAt":1}`, clock.t)
	if r.Poll() == nil {
		t.Fatal("fixture: mirror not live for Poll")
	}
	if s := r.PollCurrent(); s != nil {
		t.Fatalf("PollCurrent returned a mirror: %+v", s)
	}
}
