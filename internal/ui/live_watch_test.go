package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

func writeVoice(t *testing.T, dir, body string) {
	t.Helper()
	tmp := filepath.Join(dir, ".c.tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "current.json")); err != nil {
		t.Fatal(err)
	}
}

func voiceBody(sessionID, phase string, updatedAt int) string {
	return `{"schema":"claude-code-voice.state/v1","on":true,"mode":"listening","phase":"` + phase + `","heartbeatMs":10000,` +
		`"target":{"title":"alpha","sessionId":"` + sessionID + `"},"updatedAt":` + strings.Repeat("1", updatedAt) + `}`
}

// The badge follows the file within a few watch intervals (it used to wait
// for the 2 s tick), and a heartbeat that changes nothing sends nothing.
func TestVoiceWatch_SendsChangesOnly(t *testing.T) {
	dir := t.TempDir()
	writeVoice(t, dir, voiceBody("aaaa1111-1794950000", "listening", 1))
	w := startVoiceWatch(voicestate.NewReader(dir), 10*time.Millisecond)
	defer w.Stop()
	next := func(what string) *voicestate.State {
		t.Helper()
		select {
		case st := <-w.ch:
			return st
		case <-time.After(time.Second):
			t.Fatalf("%s: no voice message within 1 s", what)
		}
		return nil
	}
	if st := next("initial"); st == nil || st.Phase != "listening" {
		t.Fatalf("initial: %+v", st)
	}
	writeVoice(t, dir, voiceBody("aaaa1111-1794950000", "listening", 2)) // beat
	select {
	case st := <-w.ch:
		t.Fatalf("heartbeat sent a message: %+v", st)
	case <-time.After(150 * time.Millisecond):
	}
	start := time.Now()
	writeVoice(t, dir, voiceBody("bbbb2222-1794950000", "hearing", 3)) // switch + phase
	st := next("switch")
	if st == nil || st.Target.SessionID != "bbbb2222-1794950000" || st.Phase != "hearing" {
		t.Fatalf("after switch: %+v", st)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("switch reached the TUI after %s", d)
	}
	_ = os.Remove(filepath.Join(dir, "current.json"))
	if st := next("off"); st != nil {
		t.Fatalf("removed file still live: %+v", st)
	}
}
