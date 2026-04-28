package samp

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSanitizeAlias(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"alice", "alice"},
		{"work.alice", "work.alice"},
		{"work/alice", "work-alice"},
		{"work alice", "work-alice"},
		{"my project: phase 2", "my-project-phase-2"},
		{"  --leading", "leading"},
		{"trailing-.", "trailing"},
		{"", ""},
		{"!!!", ""},
		{strings.Repeat("a", 80), strings.Repeat("a", 64)},
	}
	for _, c := range cases {
		got := SanitizeAlias(c.in)
		if got != c.want {
			t.Errorf("SanitizeAlias(%q) = %q, want %q", c.in, got, c.want)
		}
		if got != "" && ValidateAlias(got) != nil {
			t.Errorf("SanitizeAlias(%q) = %q which fails ValidateAlias", c.in, got)
		}
	}
}

func TestResolveAlias_FromCwdBasename(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "msg-agent-2")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAlias(sub); got != "msg-agent-2" {
		t.Errorf("ResolveAlias(%q) = %q, want %q", sub, got, "msg-agent-2")
	}
}

func TestResolveAlias_AgentMessageFileWins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".agent-message"), []byte("custom-alias\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAlias(dir); got != "custom-alias" {
		t.Errorf("ResolveAlias = %q, want %q", got, "custom-alias")
	}
}

func TestResolveAlias_AgentMessageFileSkipsBlanks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".agent-message"), []byte("\n\n  real-alias  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAlias(dir); got != "real-alias" {
		t.Errorf("ResolveAlias = %q, want %q", got, "real-alias")
	}
}

func TestResolveAlias_EmptyCwd(t *testing.T) {
	if got := ResolveAlias(""); got != "" {
		t.Errorf("ResolveAlias(\"\") = %q, want \"\"", got)
	}
}

func TestResolveAlias_DirtyBasenameSanitized(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "my project")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAlias(sub); got != "my-project" {
		t.Errorf("ResolveAlias = %q, want %q", got, "my-project")
	}
}

// TestResolveAlias_InvalidLinesFallThrough pins the contract: when
// .agent-message contains only sanitize-empty lines, fall through to
// basename rather than returning "".
func TestResolveAlias_InvalidLinesFallThrough(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "msg-agent-2")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".agent-message"), []byte("!!!\n@@@\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAlias(sub); got != "msg-agent-2" {
		t.Errorf("ResolveAlias = %q, want %q (basename fallback)", got, "msg-agent-2")
	}
}

// TestResolveAlias_CRLFLineEndings pins CRLF tolerance — \r is stripped
// by TrimSpace so a Windows-edited .agent-message file still resolves.
func TestResolveAlias_CRLFLineEndings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".agent-message"), []byte("custom-alias\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAlias(dir); got != "custom-alias" {
		t.Errorf("ResolveAlias with CRLF = %q, want %q", got, "custom-alias")
	}
}

// TestDefaultDir_AgentMessageDirOverrides pins SPEC.md §1: when set,
// $AGENT_MESSAGE_DIR wins over XDG and HOME fallbacks.
func TestDefaultDir_AgentMessageDirOverrides(t *testing.T) {
	t.Setenv("AGENT_MESSAGE_DIR", "/tmp/explicit")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg") // ignored
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/explicit" {
		t.Errorf("DefaultDir = %q, want /tmp/explicit", got)
	}
}

// TestDefaultDir_XDGStateHome pins XDG honoring when AGENT_MESSAGE_DIR
// is unset.
func TestDefaultDir_XDGStateHome(t *testing.T) {
	t.Setenv("AGENT_MESSAGE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/state")
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/state/agent-message" {
		t.Errorf("DefaultDir = %q, want /tmp/state/agent-message", got)
	}
}

// TestDefaultDir_HomeFallback pins XDG default ($HOME/.local/state)
// when neither AGENT_MESSAGE_DIR nor XDG_STATE_HOME is set.
func TestDefaultDir_HomeFallback(t *testing.T) {
	t.Setenv("AGENT_MESSAGE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "state", "agent-message")
	if got != want {
		t.Errorf("DefaultDir = %q, want %q", got, want)
	}
}

func TestPoller_TickEmitsCounts(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)

	for i, to := range []string{"bob", "bob", "carol"} {
		writeLine(t, dir, &Message{
			From: "alice", To: to, Body: "hi", Thread: "t",
			TS: now.Unix() + int64(i),
		})
	}

	var got map[string]int
	p := &Poller{
		Dir: dir,
		Sessions: func() []SessionAlias {
			return []SessionAlias{
				{SessionID: "s-bob", Alias: "bob"},
				{SessionID: "s-carol", Alias: "carol"},
				{SessionID: "s-empty", Alias: ""}, // skipped
			}
		},
		OnUpdate: func(m map[string]int) { got = m },
	}
	p.tick()

	if got["s-bob"] != 2 {
		t.Errorf("s-bob unread = %d, want 2", got["s-bob"])
	}
	if got["s-carol"] != 1 {
		t.Errorf("s-carol unread = %d, want 1", got["s-carol"])
	}
	if _, present := got["s-empty"]; present {
		t.Errorf("empty-alias session leaked into counts")
	}
}

func TestPoller_OmitsZeroCounts(t *testing.T) {
	dir := t.TempDir()
	var got map[string]int
	p := &Poller{
		Dir: dir,
		Sessions: func() []SessionAlias {
			return []SessionAlias{{SessionID: "s1", Alias: "alice"}}
		},
		OnUpdate: func(m map[string]int) { got = m },
	}
	p.tick()
	if _, present := got["s1"]; present {
		t.Errorf("zero-unread session should be omitted from map")
	}
}

func TestPoller_DropsStaleCacheEntries(t *testing.T) {
	dir := t.TempDir()
	p := &Poller{
		Dir: dir,
		Sessions: func() []SessionAlias {
			return []SessionAlias{{SessionID: "s1", Alias: "alice"}}
		},
		OnUpdate: func(map[string]int) {},
	}
	p.tick()

	// Switch to a different alias set — the old cache entry for "alice"
	// must be evicted to keep memory bounded.
	p.Sessions = func() []SessionAlias {
		return []SessionAlias{{SessionID: "s2", Alias: "bob"}}
	}
	p.tick()

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.cache["alice"]; ok {
		t.Errorf("stale cache entry for alice not evicted")
	}
	if _, ok := p.cache["bob"]; !ok {
		t.Errorf("new cache entry for bob not created")
	}
}

func TestPoller_StartStop(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	writeLine(t, dir, &Message{From: "alice", To: "bob", Body: "hi", Thread: "t", TS: now.Unix()})

	var (
		mu   sync.Mutex
		hits int
		last map[string]int
	)
	p := &Poller{
		Dir:      dir,
		Interval: 20 * time.Millisecond,
		Sessions: func() []SessionAlias {
			return []SessionAlias{{SessionID: "s1", Alias: "bob"}}
		},
		OnUpdate: func(m map[string]int) {
			mu.Lock()
			hits++
			last = m
			mu.Unlock()
		},
	}
	p.Start()
	defer p.Stop()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := hits >= 2 && last["s1"] == 1
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if hits < 2 {
		t.Fatalf("OnUpdate fired %d times, want >= 2", hits)
	}
	if last["s1"] != 1 {
		t.Fatalf("last unread for s1 = %d, want 1", last["s1"])
	}
}

func TestPoller_StopBeforeStartIsSafe(t *testing.T) {
	p := &Poller{}
	p.Stop() // must not panic on never-started poller
	p.Stop() // double-stop must not panic
}
