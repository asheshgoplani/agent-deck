// Package voicestate reads the voice daemon's state file so the TUI can mark
// the session row that holds the voice mic.
//
// The voice daemon (claude-code-voice, on the owner's Mac) writes
// ~/.claude/voice/current.json atomically (tmp + rename) on every change and
// at least every heartbeatMs; schema "claude-code-voice.state/v1", documented
// in that repo's docs/STATE-CONTRACT.md. On a box the mic talks to, the daemon
// may push a smaller copy, ~/.claude/voice/mirror.json (schema
// "claude-code-voice.mirror/v1", no heard/spoken text, "from" = the Mac's
// hostname).
//
// Everything here fails open: a missing, unreadable, malformed or stale file
// means "no voice", never an error the caller has to handle.
package voicestate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Source names the file a State came from.
const (
	SourceCurrent = "current.json"
	SourceMirror  = "mirror.json"
)

const (
	schemaStatePrefix  = "claude-code-voice.state/"
	schemaMirrorPrefix = "claude-code-voice.mirror/"

	// fallbackWindow is the stale window for a mirror.json that carries no
	// heartbeatMs (the design's original 45 s rule).
	fallbackWindow = 45 * time.Second
	// maxFileSize caps the read: the state file is a few KB.
	maxFileSize = 1 << 20
)

// Tone is how a badge is drawn.
type Tone int

const (
	// ToneNormal is the plain badge.
	ToneNormal Tone = iota
	// ToneDim: muted, resting, mic released, on a call or in a meeting.
	ToneDim
	// ToneWarn: an engine is falling back (yellow).
	ToneWarn
)

// Target is the session the mic talks to.
type Target struct {
	Title     string `json:"title"`
	SessionID string `json:"sessionId"`
	Profile   string `json:"profile"`
	Remote    string `json:"remote"` // box name for a remote target; "" (null) for local
	Pane      string `json:"pane"`
}

// Daemon is the running voice daemon.
type Daemon struct {
	PID int `json:"pid"`
}

// State is the part of current.json / mirror.json the TUI uses. JSON names
// are runtime.ts contractState()'s; null strings decode to "".
type State struct {
	Schema       string  `json:"schema"`
	On           bool    `json:"on"`
	Mode         string  `json:"mode"`
	Phase        string  `json:"phase"`
	PhaseSince   int64   `json:"phaseSince"`
	WakePhrase   *string `json:"wakePhrase"`
	HeartbeatMs  int64   `json:"heartbeatMs"`
	Muted        bool    `json:"muted"`
	Hibernating  bool    `json:"hibernating"`
	MicReleased  bool    `json:"micReleased"`
	FallingBack  bool    `json:"fallingBack"`
	Doing        string  `json:"doing"`
	TTS          string  `json:"tts"`
	STT          string  `json:"stt"`
	TTSEffective string  `json:"ttsEffective"`
	STTEffective string  `json:"sttEffective"`
	Target       Target  `json:"target"`
	Daemon       Daemon  `json:"daemon"`
	From         string  `json:"from"`
	UpdatedAt    int64   `json:"updatedAt"`

	// Source is the file this state was read from (SourceCurrent or
	// SourceMirror). Not part of the file.
	Source string `json:"-"`
}

// DefaultDir is where the daemon writes: $CLAUDE_VOICE_STATE_DIR, else
// ~/.claude/voice. Empty when neither can be resolved.
func DefaultDir() string {
	if d := strings.TrimSpace(os.Getenv("CLAUDE_VOICE_STATE_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "voice")
}

// fileCache is what the reader remembers about one file between polls.
type fileCache struct {
	present   bool
	mtime     time.Time
	size      int64
	raw       []byte
	changedAt time.Time // reader's clock: when it first saw this content
	state     *State    // nil when unparseable
}

// Reader polls the voice state files. It stats them on every Poll and
// reads one only when its mtime or size changed. Not safe for concurrent
// use; the TUI calls it from its Update loop.
type Reader struct {
	Dir string
	// Now is the reader's clock (default time.Now).
	Now func() time.Time
	// PIDAlive reports whether a pid runs on this machine (default kill -0).
	PIDAlive func(int) bool

	current fileCache
	mirror  fileCache
}

// NewReader returns a reader for dir (see DefaultDir).
func NewReader(dir string) *Reader {
	return &Reader{Dir: dir, Now: time.Now, PIDAlive: pidAlive}
}

// Poll returns the live voice state, or nil when there is none: no file,
// "on": false, a dead daemon pid, or content older than the heartbeat
// window. A live current.json wins over mirror.json.
func (r *Reader) Poll() *State {
	if r == nil || r.Dir == "" {
		return nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	r.refresh(&r.current, filepath.Join(r.Dir, SourceCurrent), now)
	if s := r.liveCurrent(now); s != nil {
		return s
	}
	r.refresh(&r.mirror, filepath.Join(r.Dir, SourceMirror), now)
	return r.liveMirror(now)
}

// refresh stats path and re-reads it only when the stat changed.
func (r *Reader) refresh(c *fileCache, path string, now time.Time) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxFileSize {
		*c = fileCache{}
		return
	}
	if c.present && fi.ModTime().Equal(c.mtime) && fi.Size() == c.size {
		return
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed name under the voice state dir
	if err != nil {
		*c = fileCache{}
		return
	}
	first := !c.present
	c.present, c.mtime, c.size = true, fi.ModTime(), fi.Size()
	if !first && bytes.Equal(raw, c.raw) {
		return // touched, not changed: the content's age keeps running
	}
	c.raw = raw
	c.changedAt = now
	if first && fi.ModTime().Before(now) {
		// Seen for the first time: it has been unchanged at least since its
		// mtime (the file is on this machine, so mtime is this clock).
		c.changedAt = fi.ModTime()
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		c.state = nil
		return
	}
	c.state = &s
}

func (r *Reader) liveCurrent(now time.Time) *State {
	s := r.current.state
	if s == nil || !s.On || !strings.HasPrefix(s.Schema, schemaStatePrefix) {
		return nil
	}
	// current.json is written by the daemon on this machine, so its pid is
	// checkable here: a crash leaves "on": true behind with a dead pid.
	alive := r.PIDAlive
	if alive == nil {
		alive = pidAlive
	}
	if s.Daemon.PID > 0 && !alive(s.Daemon.PID) {
		return nil
	}
	switch {
	case s.HeartbeatMs > 0:
		if now.Sub(r.current.changedAt) > heartbeatWindow(s.HeartbeatMs) {
			return nil // a hung daemon: alive pid, no beats
		}
	case s.Daemon.PID <= 0:
		// Neither a heartbeat nor a pid to check: the plain 45 s rule.
		if now.Sub(r.current.changedAt) > fallbackWindow {
			return nil
		}
	}
	// An older daemon without heartbeatMs: the pid rule only.
	out := *s
	out.Source = SourceCurrent
	return &out
}

func (r *Reader) liveMirror(now time.Time) *State {
	s := r.mirror.state
	if s == nil || !s.On || !strings.HasPrefix(s.Schema, schemaMirrorPrefix) {
		return nil
	}
	// No pid to check (the daemon is on the Mac): stale by the file's own
	// mtime, which the box stamps with its own clock.
	window := fallbackWindow
	if s.HeartbeatMs > 0 {
		window = heartbeatWindow(s.HeartbeatMs)
	}
	if now.Sub(r.mirror.mtime) > window {
		return nil
	}
	out := *s
	out.Source = SourceMirror
	out.Target.Remote = "" // the mirror is on the box the target lives on
	return &out
}

// heartbeatWindow is three missed beats plus 5 s (35 s at the 10 s beat).
func heartbeatWindow(heartbeatMs int64) time.Duration {
	return 3*time.Duration(heartbeatMs)*time.Millisecond + 5*time.Second
}

// pidAlive is the kill -0 check; EPERM means the process exists.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Badge is the row badge: Full when the row has room, Narrow (glyphs only)
// when it does not. Both empty: draw nothing.
type Badge struct {
	Full, Narrow string
	Tone         Tone
}

// phase returns the state to show: the v1.2 phase, or one derived from mode
// for an older daemon (the mapping in STATE-CONTRACT.md).
func (s *State) phase() string {
	if s.Phase != "" {
		return s.Phase
	}
	switch s.Mode {
	case "hibernate":
		if s.Muted {
			return "muted"
		}
		return "resting"
	case "muted":
		return "muted"
	case "call":
		return "paused"
	case "pending", "compacting":
		return "thinking"
	case "listening", "asking", "enrolling":
		if s.Muted {
			return "muted"
		}
		if s.Doing != "" {
			return "thinking"
		}
		return "listening"
	case "":
		if s.Muted {
			return "muted"
		}
		if s.Hibernating {
			return "resting"
		}
	}
	return s.Mode
}

// fallbackEngine is the engine really in use while falling back.
func (s *State) fallbackEngine() string {
	if s.TTSEffective != "" && s.TTSEffective != s.TTS {
		return s.TTSEffective
	}
	if s.STTEffective != "" && s.STTEffective != s.STT {
		return s.STTEffective
	}
	return s.TTSEffective
}

// thinkingFor is " 4s" / " 2m" since phaseSince, or "" when unknown or
// implausible (the writer's clock may differ from this one).
func (s *State) thinkingFor(now time.Time) string {
	if s.PhaseSince <= 0 {
		return ""
	}
	d := now.Sub(time.UnixMilli(s.PhaseSince))
	switch {
	case d < 0 || d > 24*time.Hour:
		return ""
	case d < time.Minute:
		return fmt.Sprintf(" %ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf(" %dm", int(d/time.Minute))
	default:
		return fmt.Sprintf(" %dh", int(d/time.Hour))
	}
}

// Badge is the row badge for this state (DESIGN-voice-badge.md, Addendum).
func (s *State) Badge(now time.Time) Badge {
	if s == nil || !s.On {
		return Badge{}
	}
	var b Badge
	switch s.phase() {
	case "off":
		return Badge{}
	case "resting":
		b = Badge{Full: "☾ resting", Narrow: "☾", Tone: ToneDim}
		if s.WakePhrase != nil && *s.WakePhrase != "" {
			b.Full = `☾ say "` + *s.WakePhrase + `"`
		}
	case "listening":
		b = Badge{Full: "🎙 listening", Narrow: "🎙"}
	case "hearing", "transcribing":
		b = Badge{Full: "🎙 hearing", Narrow: "🎙"}
	case "thinking":
		b = Badge{Full: "🎙 … thinking" + s.thinkingFor(now), Narrow: "🎙…"}
	case "speaking":
		b = Badge{Full: "🎙 ▶ speaking", Narrow: "🎙▶"}
	case "muted":
		b = Badge{Full: "🎙 ⏸ muted", Narrow: "🎙⏸", Tone: ToneDim}
		if s.MicReleased {
			b.Full += " mic off"
		}
	case "paused":
		b = Badge{Full: "🎙 ○ call", Narrow: "🎙○", Tone: ToneDim}
	case "meeting":
		b = Badge{Full: "🎙 ○ meeting", Narrow: "🎙○", Tone: ToneDim}
	default:
		b = Badge{Full: "🎙", Narrow: "🎙"}
	}
	if s.Muted || s.MicReleased || s.Hibernating {
		b.Tone = ToneDim
	}
	if s.FallingBack {
		b.Tone = ToneWarn
		if e := s.fallbackEngine(); e != "" {
			b.Full += " ⚠ " + e
		} else {
			b.Full += " ⚠"
		}
		b.Narrow += "⚠"
	}
	return b
}

// Words is the state in words, without glyphs, for the preview pane:
// "listening", "thinking 4s", `resting, say "jarvis"`, "speaking · falling
// back to qwen".
func (s *State) Words(now time.Time) string {
	if s == nil || !s.On {
		return ""
	}
	var w string
	switch p := s.phase(); p {
	case "off":
		return ""
	case "resting":
		w = "resting"
		if s.WakePhrase != nil && *s.WakePhrase != "" {
			w += `, say "` + *s.WakePhrase + `"`
		}
	case "hearing", "transcribing":
		w = "hearing"
	case "thinking":
		w = "thinking" + s.thinkingFor(now)
	case "muted":
		w = "muted"
		if s.MicReleased {
			w += ", mic off"
		}
	case "paused":
		w = "on a call"
	case "":
		w = "on"
	default:
		w = p
	}
	if s.FallingBack {
		if e := s.fallbackEngine(); e != "" {
			w += " · falling back to " + e
		} else {
			w += " · falling back"
		}
	}
	return w
}
