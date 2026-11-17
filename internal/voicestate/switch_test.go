package voicestate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// replaceAt writes body the way the daemon does (tmp + rename, so a new
// inode) and stamps mtime.
func replaceAt(t *testing.T, dir, name, body string, mtime time.Time) {
	t.Helper()
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

// A voice switch rewrites current.json with another session of the same
// title length within one coarse mtime tick (ext4 stamps mtime from the
// coarse kernel clock, about 4 ms): size and mtime are unchanged, only the
// inode differs. The reader must still see the new target, or the badge
// stays on the old session until the next heartbeat (10 s).
func TestReader_SwitchWithinOneMtimeTickIsSeen(t *testing.T) {
	r, clock, dir := newTestReader(t, map[int]bool{4242: true})
	a := liveCurrent(1)
	b := strings.Replace(strings.Replace(a, "cf9a73f3-1789471273", "0b1c2d3e-1789471999", 1), "conductor-inno", "conductor-abcd", 1)
	if len(a) != len(b) {
		t.Fatalf("fixture sizes differ: %d vs %d", len(a), len(b))
	}
	mt := clock.t
	replaceAt(t, dir, "current.json", a, mt)
	if s := r.Poll(); s == nil || s.Target.SessionID != "cf9a73f3-1789471273" {
		t.Fatalf("first poll: %+v", s)
	}
	replaceAt(t, dir, "current.json", b, mt)
	clock.advance(100 * time.Millisecond)
	s := r.Poll()
	if s == nil || s.Target.SessionID != "0b1c2d3e-1789471999" {
		got := "<nil>"
		if s != nil {
			got = s.Target.SessionID + " " + s.Target.Title
		}
		t.Fatalf("after the switch the reader still shows %s; want 0b1c2d3e-1789471999 conductor-abcd (wrong session after a voice switch)", got)
	}
}

// The relay writes mirror.json on every stream host, not only on the host
// the target lives on. It marks the copy "relative": target.remote is then
// already named from this host ("" = here), and the reader must keep it.
func TestReader_RelativeMirrorKeepsRemote(t *testing.T) {
	r, clock, dir := newTestReader(t, nil)
	body := `{"schema":"claude-code-voice.mirror/v1","relative":true,"on":true,"mode":"listening","phase":"listening",
 "target":{"title":"alpha","sessionId":"aaaa1111-1789000000","profile":"default","remote":"laptop"},"heartbeatMs":10000,
 "from":"laptop","updatedAt":1}`
	replaceAt(t, dir, "mirror.json", body, clock.t)
	s := r.Poll()
	if s == nil {
		t.Fatal("live relative mirror read as no voice")
	}
	if s.Target.Remote != "laptop" {
		t.Fatalf("relative mirror target.remote = %q, want %q (the target is on another host, not here)", s.Target.Remote, "laptop")
	}
}

func TestRelativeRemote(t *testing.T) {
	cases := []struct{ target, dest, self, want string }{
		{"", "box", "laptop", "laptop"}, // target on the daemon's machine
		{"box", "box", "laptop", ""},    // target on the receiving host itself
		{"other", "box", "laptop", "other"},
	}
	for _, c := range cases {
		if got := RelativeRemote(c.target, c.dest, c.self); got != c.want {
			t.Errorf("RelativeRemote(%q,%q,%q) = %q, want %q", c.target, c.dest, c.self, got, c.want)
		}
	}
}

// The relay's mirror round-trips through the reader with the target named
// from the receiving host, carries no heard/spoken text, and an off mirror
// reads as no voice at once.
func TestWriteMirror_RoundTrip(t *testing.T) {
	src, _, srcDir := newTestReader(t, map[int]bool{4242: true})
	replaceAt(t, srcDir, "current.json", strings.ReplaceAll(liveCurrent(1), `"remote":"innobox"`, `"remote":"box-a"`), time.Now())
	st := src.Poll()
	if st == nil {
		t.Fatal("fixture not live")
	}
	dst := t.TempDir()
	if err := WriteMirror(dst, MirrorFor(st, "box-a", "laptop", time.Now().UnixMilli())); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dst, "mirror.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret words") || strings.Contains(string(raw), "more words") {
		t.Fatalf("mirror carries heard/spoken text: %s", raw)
	}
	fi, _ := os.Stat(filepath.Join(dst, "mirror.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mirror mode %v, want 0600", fi.Mode().Perm())
	}
	got := NewReader(dst).Poll()
	if got == nil || got.Source != SourceMirror || got.Target.SessionID != "cf9a73f3-1789471273" || got.Target.Remote != "" || got.From != "laptop" {
		t.Fatalf("mirror read back as %+v", got)
	}
	if err := WriteMirror(dst, MirrorFor(nil, "box-a", "laptop", time.Now().UnixMilli())); err != nil {
		t.Fatal(err)
	}
	if got := NewReader(dst).Poll(); got != nil {
		t.Fatalf("off mirror read as live: %+v", got)
	}
}

// voice state --json names the daemon's machine for a mirror (as voice.state
// frames do) and "" for this machine's own file.
func TestNewView_Origin(t *testing.T) {
	now := time.Now()
	m := NewView(&State{On: true, Phase: "listening", From: "laptop", Source: SourceMirror}, now)
	if m.Origin != "laptop" || m.From != "mirror" {
		t.Fatalf("mirror view origin %q from %q", m.Origin, m.From)
	}
	l := NewView(&State{On: true, Phase: "listening", From: "laptop", Source: SourceCurrent}, now)
	if l.Origin != "" || l.From != "local" {
		t.Fatalf("local view origin %q from %q", l.Origin, l.From)
	}
	if off := NewView(nil, now); off.Origin != "" {
		t.Fatalf("off view origin %q", off.Origin)
	}
}
