package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeVaultScript is a stand-in for /usr/local/bin/deck-secret. It keeps one
// file per name under its store dir (baked in: the child env is clean, so it
// cannot be passed by env), records every argv and env it ran with, and
// prints deck-vault's fixed "stored ... (replaced=...)" line on stderr. With
// a .fail file in the store, set echoes its stdin to stderr and fails, so a
// test can prove deliver never repeats what the vault printed.
const fakeVaultScript = `#!/bin/sh
store='%s'
printf '%%s\n' "$*" >> "$store/.argv"
env >> "$store/.env"
echo '--' >> "$store/.env"
n=$(printf '%%s' "$2" | tr '/' '_')
case "$1" in
exists)
	if [ -f "$store/$n" ]; then echo yes; exit 0; fi
	echo no; exit 3 ;;
set)
	if [ -f "$store/.fail" ]; then cat >&2; exit 7; fi
	existed=no
	[ -f "$store/$n" ] && existed=yes
	cat > "$store/$n"
	printf '%%s' "$AGENTDECK_SESSION_ID" > "$store/$n.session"
	echo "stored $2 (replaced=$existed)" >&2
	exit 0 ;;
esac
exit 2
`

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type testEnv struct {
	profile string
	home    string
	store   string
	clock   *fakeClock
}

// newTestEnv gives a test its own HOME (so its profile, ask dir, config and
// events bus are private), a fake vault swapped in through the unexported
// hooks, and a controllable clock. Not for parallel tests (t.Setenv).
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, "")
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	store := t.TempDir()
	script := filepath.Join(t.TempDir(), "deck-secret")
	if err := os.WriteFile(script, []byte(fmt.Sprintf(fakeVaultScript, store)), 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath, oldTrusted, oldNow, oldPoll := vaultPath, vaultTrusted, now, directPollEvery
	vaultPath = script
	vaultTrusted = func(string) error { return nil }
	clock := &fakeClock{t: time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)}
	now = clock.Now
	directPollEvery = 20 * time.Millisecond
	t.Cleanup(func() { vaultPath, vaultTrusted, now, directPollEvery = oldPath, oldTrusted, oldNow, oldPoll })
	return &testEnv{profile: "ashesh", home: home, store: store, clock: clock}
}

func (e *testEnv) writeConfig(t *testing.T, body string) {
	t.Helper()
	path, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
}

// stored returns the fake vault's value for name and whether it exists.
func (e *testEnv) stored(name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(e.store, strings.ReplaceAll(name, "/", "_")))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (e *testEnv) preset(t *testing.T, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.store, strings.ReplaceAll(name, "/", "_")), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func secretRequest(sessionID string) NewRequest {
	return NewRequest{
		Kind:         KindSecret,
		SessionID:    sessionID,
		SessionTitle: "Conductor",
		Host:         "deckbox",
		Name:         "conductor/GH_TOKEN_DECKBOX",
		Reason:       "GitHub key with workflow permission",
		PID:          os.Getpid(),
	}
}

func headerFor(rec Record, by, nonce string) Header {
	return Header{RequestID: rec.RequestID, Kind: rec.Kind, Name: rec.Name, Replace: rec.Replace, Nonce: nonce, AnsweredBy: by}
}

// seenAndHeader marks rec seen by a TUI and returns the header that UI sends.
func seenAndHeader(t *testing.T, e *testEnv, rec Record) Header {
	t.Helper()
	nonce, err := MarkSeen(e.profile, rec.RequestID, "tui")
	if err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	return headerFor(rec, "tui", nonce)
}

func mustCreate(t *testing.T, e *testEnv, req NewRequest) Record {
	t.Helper()
	rec, err := Create(e.profile, req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rec
}

// busFrames reads every retained frame of kind on the profile bus.
func busFrames(t *testing.T, profile, kind string) []events.Frame {
	t.Helper()
	bus := events.OpenProfile(profile)
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	want := bus.Stats().Cursor
	sub, err := bus.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Frame
	for f := range sub.Frames() {
		if f.Kind == kind {
			out = append(out, f)
		}
		if f.Cursor >= want {
			cancel()
		}
	}
	return out
}

func frameData(t *testing.T, f events.Frame) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(f.Data, &m); err != nil {
		t.Fatalf("frame data %s: %v", f.Data, err)
	}
	return m
}

func recordFileBytes(t *testing.T, e *testEnv, id string) []byte {
	t.Helper()
	dir, err := Dir(e.profile)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
