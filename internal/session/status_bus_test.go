package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

type busRecord struct {
	profile, kind, session string
	data                   []byte
}

// recordBus captures what the session taps publish, without the
// process-wide bus (closing it would silence later tests).
func recordBus(t *testing.T) *[]busRecord {
	t.Helper()
	var got []busRecord
	prev := busPublish
	busPublish = func(profile, kind, sessionID string, data any) {
		b, _ := json.Marshal(data)
		got = append(got, busRecord{profile, kind, sessionID, b})
	}
	t.Cleanup(func() { busPublish = prev })
	return &got
}

// enableStatusBus registers the status observer as [macapp] status_events
// would, and removes it afterwards.
func enableStatusBus(t *testing.T) {
	t.Helper()
	applyStatusBusGate(&UserConfig{Macapp: MacappSettings{StatusEvents: true}})
	t.Cleanup(func() { applyStatusBusGate(nil) })
}

func openStatusBusDB(t *testing.T, dbPath string) *statedb.StateDB {
	t.Helper()
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func saveStatusBusRow(t *testing.T, db *statedb.StateDB, id, status string) {
	t.Helper()
	now := time.Now()
	if err := db.SaveInstance(&statedb.InstanceRow{ID: id, Title: "t", ProjectPath: "/p", GroupPath: "g", Tool: "claude", Status: status, TmuxSession: "agentdeck_t_1", CreatedAt: now, LastAccessed: now}); err != nil {
		t.Fatal(err)
	}
}

// TestStatusBusOffByDefault: with no [macapp] status_events the observer is
// not registered, so a status transition publishes nothing (review of the
// macapp core surface: the observer was registered unconditionally in init).
func TestStatusBusOffByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if _, err := LoadUserConfig(); err != nil {
		t.Fatal(err)
	}
	rec := recordBus(t)
	db := openStatusBusDB(t, filepath.Join(t.TempDir(), "profiles", "statusbus", "state.db"))
	saveStatusBusRow(t, db, "sess-off", "idle")
	if err := db.WriteStatus("sess-off", "running", "claude"); err != nil {
		t.Fatal(err)
	}
	if len(*rec) != 0 {
		t.Fatalf("status frames published without [macapp] status_events: %+v", *rec)
	}

	// Turning the switch on in config.toml registers the tap on the next load.
	dir := filepath.Join(home, "config", "agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[macapp]\nstatus_events = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	if _, err := LoadUserConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { applyStatusBusGate(nil) })
	if err := db.WriteStatus("sess-off", "waiting", "claude"); err != nil {
		t.Fatal(err)
	}
	if len(*rec) == 0 || (*rec)[0].kind != "session.status" {
		t.Fatalf("status_events = true published %+v", *rec)
	}
}

func TestStatusBusProfile(t *testing.T) {
	for path, want := range map[string]string{
		"/h/.agent-deck/profiles/personal/state.db":            "personal",
		"/h/.local/share/agent-deck/profiles/default/state.db": "default",
		"/tmp/x/state.db": "",
		"/h/.agent-deck/profiles/personal/other.db": "",
		"": "",
	} {
		if got := statusBusProfile(path); got != want {
			t.Errorf("statusBusProfile(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestWriteStatusPublishesStatusAndTurnFrames: a status row transition
// written by any owner (TUI, daemon) lands on the profile's bus as
// session.status with from/to/tmux_session/changed_at, plus session.turn
// started/ended with the turn duration. An unchanged write publishes nothing.
func TestWriteStatusPublishesStatusAndTurnFrames(t *testing.T) {
	enableStatusBus(t)
	rec := recordBus(t)
	dbPath := filepath.Join(t.TempDir(), "profiles", "statusbus", "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.SaveInstance(&statedb.InstanceRow{ID: "sess-1", Title: "t", ProjectPath: "/p", GroupPath: "g", Tool: "claude", Status: "idle", TmuxSession: "agentdeck_t_1", CreatedAt: now, LastAccessed: now}); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"running", "running", "waiting"} {
		if err := db.WriteStatus("sess-1", st, "claude"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	frames := *rec
	var kinds []string
	for _, f := range frames {
		kinds = append(kinds, f.kind)
		if f.session != "sess-1" || f.profile != "statusbus" {
			t.Errorf("frame session/profile = %q/%q", f.session, f.profile)
		}
	}
	want := []string{"session.status", "session.turn", "session.status", "session.turn"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	var st StatusBusEvent
	if err := json.Unmarshal(frames[2].data, &st); err != nil {
		t.Fatal(err)
	}
	if st.From != "running" || st.To != "waiting" || st.TmuxSession != "agentdeck_t_1" || st.ChangedAt == "" {
		t.Fatalf("status data: %+v", st)
	}
	var first StatusBusEvent
	_ = json.Unmarshal(frames[0].data, &first)
	if first.From != "idle" || first.To != "running" {
		t.Fatalf("first transition: %+v", first)
	}
	var ended TurnBusEvent
	if err := json.Unmarshal(frames[3].data, &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Phase != "ended" || ended.DurationMs <= 0 || ended.To != "waiting" {
		t.Fatalf("turn ended: %+v", ended)
	}
}

func TestTranscriptGrowthFrames(t *testing.T) {
	rec := recordBus(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: "g1", Title: "g", Tool: "claude", ProjectPath: project, ClaudeSessionID: "22222222-2222-2222-2222-222222222222", Status: StatusRunning}
	path := ClaudeTranscriptPathForInstance(inst)
	if path == "" {
		t.Skip("transcript path not resolvable in this environment")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &transcriptSizes{sizes: map[string]int64{}}
	g.publish("growth", []*Instance{inst}) // first sight: record only
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("{\"a\":1}\n")
	f.Close()
	g.publish("growth", []*Instance{inst})
	g.publish("growth", []*Instance{inst}) // unchanged: nothing
	frames := *rec
	if len(frames) != 1 || frames[0].kind != "session.transcript" || frames[0].profile != "growth" {
		t.Fatalf("frames: %+v", frames)
	}
	var ev TranscriptBusEvent
	_ = json.Unmarshal(frames[0].data, &ev)
	if ev.Path != path || ev.BytesAppended != 8 || ev.Size != 11 {
		t.Fatalf("transcript frame: %+v", ev)
	}
}
