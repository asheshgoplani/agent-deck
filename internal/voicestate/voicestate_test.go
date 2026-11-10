package voicestate

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeClock is the reader's own clock in these tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestReader(t *testing.T, alive map[int]bool) (*Reader, *fakeClock, string) {
	t.Helper()
	dir := t.TempDir()
	clock := &fakeClock{t: time.Unix(1_794_308_000, 0)}
	r := NewReader(dir)
	r.Now = clock.now
	r.PIDAlive = func(pid int) bool { return alive[pid] }
	return r, clock, dir
}

// writeAt writes body to dir/name and stamps its mtime, the way the daemon's
// tmp + rename write lands.
func writeAt(t *testing.T, dir, name, body string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// liveCurrent is a current.json as runtime.ts writeContract writes it (v1.2).
func liveCurrent(updatedAt int64) string {
	return `{"schema":"claude-code-voice.state/v1","on":true,"mode":"listening","muted":false,"hibernating":false,"inCall":null,"micReleased":false,
 "target":{"title":"conductor-inno","sessionId":"cf9a73f3-1789471273","profile":null,"remote":"innobox","pane":"ssh:innobox:%19"},
 "heard":"secret words","spoken":"more words","doing":null,"tts":"local","stt":"qwen3-asr","ttsEffective":"local","sttEffective":"qwen3-asr",
 "daemon":{"pid":4242,"pane":"%59","socket":"/x/pane-59.sock","band":"0.1.6"},"updatedAt":` + itoa(updatedAt) + `,
 "phase":"listening","phaseSince":1794308000000,"wakePhrase":"jarvis","heartbeatMs":10000,"fallingBack":false}`
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestReader_LiveCurrentJSON(t *testing.T) {
	r, clock, dir := newTestReader(t, map[int]bool{4242: true})
	writeAt(t, dir, "current.json", liveCurrent(1), clock.t)
	s := r.Poll()
	if s == nil {
		t.Fatal("live current.json read as no voice")
	}
	if s.Target.SessionID != "cf9a73f3-1789471273" || s.Target.Remote != "innobox" || s.Phase != "listening" {
		t.Errorf("parsed %+v", s)
	}
	if s.Target.Profile != "" {
		t.Errorf("null profile parsed as %q, want empty", s.Target.Profile)
	}
	if s.WakePhrase == nil || *s.WakePhrase != "jarvis" || s.HeartbeatMs != 10000 || s.Daemon.PID != 4242 {
		t.Errorf("parsed %+v", s)
	}
	if s.Source != SourceCurrent {
		t.Errorf("source = %q, want %q", s.Source, SourceCurrent)
	}
}

func TestReader_NoBadgeCases(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"on false", strings.Replace(liveCurrent(1), `"on":true`, `"on":false`, 1)},
		{"malformed", `{"schema":"claude-code-voice.state/v1","on":tr`},
		{"wrong schema", strings.Replace(liveCurrent(1), "claude-code-voice.state/v1", "something.else/v1", 1)},
		{"dead pid", strings.Replace(liveCurrent(1), `"pid":4242`, `"pid":999`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, clock, dir := newTestReader(t, map[int]bool{4242: true})
			writeAt(t, dir, "current.json", tc.body, clock.t)
			if s := r.Poll(); s != nil {
				t.Errorf("got a live state %+v, want none", s)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		r, _, _ := newTestReader(t, nil)
		if s := r.Poll(); s != nil {
			t.Errorf("got %+v from an empty dir", s)
		}
	})
	t.Run("missing dir", func(t *testing.T) {
		r := NewReader(filepath.Join(t.TempDir(), "nope"))
		if s := r.Poll(); s != nil {
			t.Errorf("got %+v from a missing dir", s)
		}
	})
}

// The reader never follows the state file into another file, never blocks
// on a FIFO, and never reads a file past the size cap: anything that is not
// the daemon's own regular file reads as no voice.
func TestReader_OnlyReadsItsOwnRegularFile(t *testing.T) {
	t.Run("symlink to a live state file", func(t *testing.T) {
		r, clock, dir := newTestReader(t, map[int]bool{4242: true})
		elsewhere := t.TempDir()
		writeAt(t, elsewhere, "other.json", liveCurrent(1), clock.t)
		if err := os.Symlink(filepath.Join(elsewhere, "other.json"), filepath.Join(dir, "current.json")); err != nil {
			t.Fatal(err)
		}
		if s := r.Poll(); s != nil {
			t.Errorf("followed a symlink to %+v, want no voice", s)
		}
	})
	t.Run("symlinked mirror", func(t *testing.T) {
		r, clock, dir := newTestReader(t, nil)
		elsewhere := t.TempDir()
		writeAt(t, elsewhere, "m.json", liveMirror(true), clock.t)
		if err := os.Symlink(filepath.Join(elsewhere, "m.json"), filepath.Join(dir, "mirror.json")); err != nil {
			t.Fatal(err)
		}
		if s := r.Poll(); s != nil {
			t.Errorf("followed a symlink to %+v, want no voice", s)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		r, _, dir := newTestReader(t, nil)
		if err := syscall.Mkfifo(filepath.Join(dir, "current.json"), 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		done := make(chan *State, 1)
		go func() { done <- r.Poll() }()
		select {
		case s := <-done:
			if s != nil {
				t.Errorf("got %+v from a FIFO", s)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Poll blocked on a FIFO")
		}
	})
	t.Run("fifo opened directly", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "current.json")
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		done := make(chan error, 1)
		go func() { _, _, err := readStateFile(p); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Error("readStateFile read a FIFO")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("readStateFile blocked on a FIFO (the Lstat-then-open race)")
		}
	})
	t.Run("over the size cap", func(t *testing.T) {
		r, clock, dir := newTestReader(t, map[int]bool{4242: true})
		body := liveCurrent(1)
		body = body[:len(body)-1] + `,"pad":"` + strings.Repeat("x", maxFileSize) + `"}`
		writeAt(t, dir, "current.json", body, clock.t)
		if s := r.Poll(); s != nil {
			t.Error("read a file over the size cap")
		}
	})
}

// Free-text fields are drawn on a row: control characters (a newline, an
// ESC sequence) are dropped and the length is capped, so the row stays one
// line and nothing reaches the terminal as a command.
func TestReader_SanitizesDrawnText(t *testing.T) {
	r, clock, dir := newTestReader(t, map[int]bool{4242: true})
	body := strings.Replace(liveCurrent(1), `"wakePhrase":"jarvis"`, `"wakePhrase":"jar\nvis\u001b[2J"`, 1)
	body = strings.Replace(body, `"phase":"listening"`, `"phase":"resting"`, 1)
	body = strings.Replace(body, `"fallingBack":false`, `"fallingBack":true`, 1)
	body = strings.Replace(body, `"ttsEffective":"local"`, `"ttsEffective":"`+strings.Repeat("q", 200)+`\r"`, 1)
	writeAt(t, dir, "current.json", body, clock.t)
	s := r.Poll()
	if s == nil {
		t.Fatal("no state")
	}
	b := s.Badge(clock.t)
	for _, text := range []string{b.Full, b.Narrow, s.Words(clock.t)} {
		if strings.ContainsAny(text, "\n\r\x1b") {
			t.Errorf("control character reaches the row: %q", text)
		}
	}
	if !strings.Contains(b.Full, `say "jarvis[2J"`) {
		t.Errorf("badge = %q, want the wake phrase without control characters", b.Full)
	}
	if n := len([]rune(b.Full)); n > 80 {
		t.Errorf("badge is %d runes; a 200-rune engine name must be capped", n)
	}
}

// The heartbeat rule: content unchanged for longer than 3*heartbeatMs+5s,
// measured on the reader's clock from when it first saw that content.
func TestReader_HeartbeatStale(t *testing.T) {
	r, clock, dir := newTestReader(t, map[int]bool{4242: true})
	writeAt(t, dir, "current.json", liveCurrent(1), clock.t)
	if r.Poll() == nil {
		t.Fatal("fresh file read as no voice")
	}
	clock.advance(34 * time.Second)
	if r.Poll() == nil {
		t.Fatal("34 s unchanged is inside the 35 s window")
	}
	clock.advance(2 * time.Second)
	if s := r.Poll(); s != nil {
		t.Fatal("36 s unchanged with a live pid must read as stale (hung daemon)")
	}
	// The next beat rewrites the file: live again.
	writeAt(t, dir, "current.json", liveCurrent(2), clock.t)
	if r.Poll() == nil {
		t.Fatal("a new beat did not bring the badge back")
	}
}

// The window is measured from when the reader saw the content change, not
// from the writer's updatedAt (the Mac's clock may differ from the box's).
func TestReader_HeartbeatIgnoresWriterClock(t *testing.T) {
	r, clock, dir := newTestReader(t, map[int]bool{4242: true})
	// updatedAt far in the past on the writer's clock: still live, because
	// this reader just saw the content change.
	writeAt(t, dir, "current.json", liveCurrent(5), clock.t)
	if r.Poll() == nil {
		t.Fatal("writer clock skew hid a fresh file")
	}
}

// A file the reader sees for the first time with an old mtime has been
// unchanged at least that long.
func TestReader_FirstSightUsesMtime(t *testing.T) {
	r, clock, dir := newTestReader(t, map[int]bool{4242: true})
	writeAt(t, dir, "current.json", liveCurrent(1), clock.t.Add(-2*time.Minute))
	if s := r.Poll(); s != nil {
		t.Errorf("a 2-minute-old file with a 10 s heartbeat read as live: %+v", s)
	}
}

// An older daemon without heartbeatMs: the pid rule only.
func TestReader_NoHeartbeatPIDOnly(t *testing.T) {
	alive := map[int]bool{4242: true}
	r, clock, dir := newTestReader(t, alive)
	body := strings.Replace(liveCurrent(1), `"heartbeatMs":10000,`, ``, 1)
	writeAt(t, dir, "current.json", body, clock.t)
	clock.advance(10 * time.Minute)
	if r.Poll() == nil {
		t.Fatal("old-schema file with a live pid went stale; want the pid rule only")
	}
	alive[4242] = false
	if s := r.Poll(); s != nil {
		t.Error("old-schema file with a dead pid still read as live")
	}
}

// Re-read on mtime (or size) change only: identical stat means no read.
func TestReader_RereadsOnlyOnMtimeChange(t *testing.T) {
	r, clock, dir := newTestReader(t, map[int]bool{4242: true})
	body := liveCurrent(1)
	writeAt(t, dir, "current.json", body, clock.t)
	if s := r.Poll(); s == nil || s.Phase != "listening" {
		t.Fatalf("first read = %+v", s)
	}
	// Same size, same mtime, different bytes: the reader must not read it.
	same := strings.Replace(body, `"phase":"listening"`, `"phase":"speaking!"`, 1)
	if len(same) != len(body) {
		t.Fatalf("test body sizes differ: %d vs %d", len(same), len(body))
	}
	writeAt(t, dir, "current.json", same, clock.t)
	if s := r.Poll(); s == nil || s.Phase != "listening" {
		t.Errorf("reader re-read an unchanged stat: %+v", s)
	}
	writeAt(t, dir, "current.json", same, clock.t.Add(time.Second))
	if s := r.Poll(); s == nil || s.Phase != "speaking!" {
		t.Errorf("reader missed an mtime change: %+v", s)
	}
}

func liveMirror(heartbeat bool) string {
	hb := ""
	if heartbeat {
		hb = `"heartbeatMs":10000,`
	}
	return `{"schema":"claude-code-voice.mirror/v1","on":true,"mode":"speaking","muted":false,"hibernating":false,"micReleased":false,
 "target":{"title":"conductor-inno","sessionId":"cf9a73f3-1789471273","profile":""},` + hb + `
 "phase":"speaking","phaseSince":1794308000000,"wakePhrase":"jarvis","fallingBack":true,"tts":"local","ttsEffective":"qwen","from":"buildmac","updatedAt":1}`
}

func TestReader_Mirror(t *testing.T) {
	r, clock, dir := newTestReader(t, nil)
	writeAt(t, dir, "mirror.json", liveMirror(true), clock.t.Add(-30*time.Second))
	s := r.Poll()
	if s == nil {
		t.Fatal("fresh mirror.json read as no voice")
	}
	if s.Source != SourceMirror || s.From != "buildmac" || !s.FallingBack || s.TTSEffective != "qwen" {
		t.Errorf("parsed %+v", s)
	}
	clock.advance(6 * time.Second) // mtime now 36 s old
	if s := r.Poll(); s != nil {
		t.Errorf("mirror.json 36 s old (3 beats + 5 s) read as live: %+v", s)
	}
}

func TestReader_MirrorWithoutHeartbeatUses45s(t *testing.T) {
	r, clock, dir := newTestReader(t, nil)
	writeAt(t, dir, "mirror.json", liveMirror(false), clock.t.Add(-40*time.Second))
	if r.Poll() == nil {
		t.Fatal("40 s old mirror without heartbeatMs went stale; want 45 s")
	}
	clock.advance(6 * time.Second)
	if s := r.Poll(); s != nil {
		t.Errorf("46 s old mirror read as live: %+v", s)
	}
}

// current.json wins when it is live; a dead current.json falls back to the
// mirror.
func TestReader_CurrentWinsOverMirror(t *testing.T) {
	alive := map[int]bool{4242: true}
	r, clock, dir := newTestReader(t, alive)
	writeAt(t, dir, "current.json", liveCurrent(1), clock.t)
	writeAt(t, dir, "mirror.json", liveMirror(true), clock.t)
	if s := r.Poll(); s == nil || s.Source != SourceCurrent {
		t.Fatalf("got %+v, want current.json", s)
	}
	alive[4242] = false
	if s := r.Poll(); s == nil || s.Source != SourceMirror {
		t.Fatalf("got %+v, want mirror.json once current.json is dead", s)
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("CLAUDE_VOICE_STATE_DIR", "/some/where")
	if got := DefaultDir(); got != "/some/where" {
		t.Errorf("DefaultDir = %q", got)
	}
}

func strp(s string) *string { return &s }

// The badge words per phase (DESIGN-voice-badge.md, Addendum table).
func TestBadge(t *testing.T) {
	now := time.UnixMilli(1_794_308_004_000)
	cases := []struct {
		name   string
		s      State
		full   string
		narrow string
		tone   Tone
	}{
		{"listening", State{On: true, Phase: "listening"}, "🎙 listening", "🎙", ToneNormal},
		{"hearing", State{On: true, Phase: "hearing"}, "🎙 hearing", "🎙", ToneNormal},
		{"transcribing", State{On: true, Phase: "transcribing"}, "🎙 hearing", "🎙", ToneNormal},
		{"thinking", State{On: true, Phase: "thinking", PhaseSince: 1_794_308_000_000}, "🎙 … thinking 4s", "🎙…", ToneNormal},
		{"thinking no since", State{On: true, Phase: "thinking"}, "🎙 … thinking", "🎙…", ToneNormal},
		{"thinking minutes", State{On: true, Phase: "thinking", PhaseSince: 1_794_307_879_000}, "🎙 … thinking 2m", "🎙…", ToneNormal},
		{"speaking", State{On: true, Phase: "speaking"}, "🎙 ▶ speaking", "🎙▶", ToneNormal},
		{"resting", State{On: true, Phase: "resting", WakePhrase: strp("jarvis")}, `☾ say "jarvis"`, "☾", ToneDim},
		{"resting no wake", State{On: true, Phase: "resting"}, "☾ resting", "☾", ToneDim},
		{"muted", State{On: true, Phase: "muted", Muted: true}, "🎙 ⏸ muted", "🎙⏸", ToneDim},
		{"muted mic off", State{On: true, Phase: "muted", Muted: true, MicReleased: true}, "🎙 ⏸ muted mic off", "🎙⏸", ToneDim},
		{"paused", State{On: true, Phase: "paused"}, "🎙 ○ call", "🎙○", ToneDim},
		{"meeting", State{On: true, Phase: "meeting"}, "🎙 ○ meeting", "🎙○", ToneDim},
		{"falling back tts", State{On: true, Phase: "speaking", FallingBack: true, TTS: "local", TTSEffective: "qwen", STT: "scribe", STTEffective: "scribe"}, "🎙 ▶ speaking ⚠ qwen", "🎙▶⚠", ToneWarn},
		{"falling back stt", State{On: true, Phase: "listening", FallingBack: true, TTS: "local", TTSEffective: "local", STT: "scribe", STTEffective: "qwen3-asr"}, "🎙 listening ⚠ qwen3-asr", "🎙⚠", ToneWarn},
		{"no phase (older daemon)", State{On: true, Mode: "hibernate", Hibernating: true}, "☾ resting", "☾", ToneDim},
		{"no phase, muted", State{On: true, Mode: "muted", Muted: true}, "🎙 ⏸ muted", "🎙⏸", ToneDim},
		{"no phase, doing", State{On: true, Mode: "listening", Doing: "thinking"}, "🎙 … thinking", "🎙…", ToneNormal},
		{"unknown phase", State{On: true, Phase: "zzz"}, "🎙", "🎙", ToneNormal},
		{"mic released dims", State{On: true, Phase: "listening", MicReleased: true}, "🎙 listening", "🎙", ToneDim},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.s.Badge(now)
			if b.Full != tc.full || b.Narrow != tc.narrow || b.Tone != tc.tone {
				t.Errorf("Badge = {%q %q %v}, want {%q %q %v}", b.Full, b.Narrow, b.Tone, tc.full, tc.narrow, tc.tone)
			}
		})
	}
	off := State{On: true, Phase: "off"}
	if b := off.Badge(now); b.Full != "" || b.Narrow != "" {
		t.Errorf("phase off rendered %+v, want nothing", b)
	}
}
