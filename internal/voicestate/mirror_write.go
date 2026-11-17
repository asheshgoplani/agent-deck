package voicestate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MirrorSchema is the schema the relay writes.
const MirrorSchema = "claude-code-voice.mirror/v1"

// MirrorTarget is the target as the receiving host names it.
type MirrorTarget struct {
	Title     string `json:"title"`
	SessionID string `json:"sessionId"`
	Profile   string `json:"profile"`
	Remote    string `json:"remote"`
}

// Mirror is the smaller copy of current.json the relay writes on another
// host (mirror.json). It carries no heard or spoken text by construction:
// only these fields exist.
type Mirror struct {
	Schema       string        `json:"schema"`
	Relative     bool          `json:"relative"`
	On           bool          `json:"on"`
	Mode         string        `json:"mode"`
	Phase        string        `json:"phase"`
	PhaseSince   int64         `json:"phaseSince"`
	WakePhrase   *string       `json:"wakePhrase"`
	HeartbeatMs  int64         `json:"heartbeatMs"`
	Muted        bool          `json:"muted"`
	Hibernating  bool          `json:"hibernating"`
	MicReleased  bool          `json:"micReleased"`
	FallingBack  bool          `json:"fallingBack"`
	TTS          string        `json:"tts"`
	TTSEffective string        `json:"ttsEffective"`
	STT          string        `json:"stt"`
	STTEffective string        `json:"sttEffective"`
	Target       *MirrorTarget `json:"target"`
	ClientSource string        `json:"source,omitempty"`
	From         string        `json:"from"`
	UpdatedAt    int64         `json:"updatedAt"`
}

// RelativeRemote names a target host from the host dest: "" when the
// target is on dest itself, self (the daemon's host as dest names it) when
// the target is on the daemon's host, else the name unchanged.
func RelativeRemote(targetRemote, dest, self string) string {
	switch targetRemote {
	case dest:
		return ""
	case "":
		return self
	}
	return targetRemote
}

// MirrorFor is the mirror of s for the host dest. s nil or off is a mirror
// with "on": false, which the reader reads as no voice at once.
func MirrorFor(s *State, dest, self string, nowMs int64) Mirror {
	m := Mirror{Schema: MirrorSchema, Relative: true, From: self, UpdatedAt: nowMs}
	if s == nil || !s.On {
		return m
	}
	m.On, m.Mode, m.Phase, m.PhaseSince = true, s.Mode, s.phase(), s.PhaseSince
	m.WakePhrase, m.HeartbeatMs = s.WakePhrase, s.HeartbeatMs
	m.Muted, m.Hibernating, m.MicReleased, m.FallingBack = s.Muted, s.Hibernating, s.MicReleased, s.FallingBack
	m.TTS, m.TTSEffective, m.STT, m.STTEffective = s.TTS, s.TTSEffective, s.STT, s.STTEffective
	m.ClientSource = s.ClientSource
	if s.Target.SessionID != "" || s.Target.Title != "" {
		m.Target = &MirrorTarget{Title: s.Target.Title, SessionID: s.Target.SessionID, Profile: s.Target.Profile,
			Remote: RelativeRemote(s.Target.Remote, dest, self)}
	}
	return m
}

// WriteMirror writes m as dir/mirror.json the way the daemon writes its own
// file: a private tmp file in the same directory, then rename, so a reader
// never sees half a file. The directory is created 0700 when missing.
func WriteMirror(dir string, m Mirror) error {
	if dir == "" {
		return errors.New("no voice state dir")
	}
	if m.Schema != MirrorSchema {
		return fmt.Errorf("mirror schema %q, want %q", m.Schema, MirrorSchema)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encoding mirror: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating voice state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".mirror-*.tmp")
	if err != nil {
		return fmt.Errorf("creating mirror tmp file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("writing mirror tmp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("closing mirror tmp file: %w", err)
	}
	if err := os.Rename(name, filepath.Join(dir, SourceMirror)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("renaming mirror into place: %w", err)
	}
	return nil
}
