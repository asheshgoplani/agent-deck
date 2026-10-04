package session

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #2481 item 3 follow-up: a Claude session whose transcript is not at
// the exact project path made every resolve glob the whole projects
// directory (886 entries on the measured host). The notify-daemon resolves
// each session several times per pass, every 1 to 3 s, so two such sessions
// were most of its CPU. The glob result is now reused.

func countTranscriptGlobs(t *testing.T) *atomic.Int64 {
	t.Helper()
	calls := &atomic.Int64{}
	prev := transcriptGlob
	transcriptGlob = func(p string) ([]string, error) { calls.Add(1); return filepath.Glob(p) }
	t.Cleanup(func() { transcriptGlob = prev })
	return calls
}

func TestIssue2481_MissingTranscriptGlobIsReused(t *testing.T) {
	calls := countTranscriptGlobs(t)
	config := t.TempDir()
	projects := filepath.Join(config, "projects")
	for _, d := range []string{"-a", "-b", "-c"} {
		if err := os.MkdirAll(filepath.Join(projects, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const sid = "11111111-2222-3333-4444-555555555555"

	for i := 0; i < 50; i++ {
		if got := resolveClaudeTranscriptPath(config, "/work/missing", sid); got != "" {
			t.Fatalf("resolve = %q, want empty", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("50 resolves of a missing transcript globbed %d times, want 1", calls.Load())
	}

	// A transcript at the exact path is found at once, cached miss or not.
	exact := filepath.Join(projects, ConvertToClaudeDirName("/work/missing"), sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(exact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exact, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveClaudeTranscriptPath(config, "/work/missing", sid); got != exact {
		t.Fatalf("exact transcript not found despite a cached miss: %q", got)
	}
}

func TestIssue2481_GlobbedTranscriptIsReusedWhileItExists(t *testing.T) {
	calls := countTranscriptGlobs(t)
	config := t.TempDir()
	const sid = "21111111-2222-3333-4444-555555555555"
	odd := filepath.Join(config, "projects", "C--Users-x-proj", sid+".jsonl") // another encoding
	if err := os.MkdirAll(filepath.Dir(odd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(odd, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if got := resolveClaudeTranscriptPath(config, "/home/x/proj", sid); got != odd {
			t.Fatalf("resolve = %q, want %q", got, odd)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("20 resolves of a globbed transcript globbed %d times, want 1", calls.Load())
	}

	// Removed: the cached path is not trusted, the glob runs again.
	if err := os.Remove(odd); err != nil {
		t.Fatal(err)
	}
	if got := resolveClaudeTranscriptPath(config, "/home/x/proj", sid); got != "" {
		t.Fatalf("a removed transcript still resolves: %q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("globs after removal = %d, want 2", calls.Load())
	}
}

// A miss is reused only for transcriptGlobMissTTL: a transcript that shows up
// under another encoding later is still found.
func TestIssue2481_TranscriptGlobMissExpires(t *testing.T) {
	calls := countTranscriptGlobs(t)
	config := t.TempDir()
	const sid = "31111111-2222-3333-4444-555555555555"
	if got := resolveClaudeTranscriptPath(config, "/home/x/late", sid); got != "" {
		t.Fatalf("resolve = %q, want empty", got)
	}
	odd := filepath.Join(config, "projects", "C--Users-x-late", sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(odd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(odd, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Age the entry deterministically instead of sleeping or mutating the TTL.
	transcriptGlobMu.Lock()
	key := filepath.Join(config, "projects") + "\x00" + sid
	entry := transcriptGlobCache[key]
	entry.checked = time.Now().Add(-transcriptGlobMissTTL)
	transcriptGlobCache[key] = entry
	transcriptGlobMu.Unlock()
	if got := resolveClaudeTranscriptPath(config, "/home/x/late", sid); got != odd {
		t.Fatalf("after the miss TTL the transcript must be found, got %q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("globs = %d, want 2", calls.Load())
	}
}

func TestIssue2481_TranscriptGlobMovedAndRestarted(t *testing.T) {
	config := t.TempDir()
	const sid = "41111111-2222-3333-4444-555555555555"
	old := filepath.Join(config, "projects", "odd-old", sid+".jsonl")
	moved := filepath.Join(config, "projects", "odd-new", sid+".jsonl")
	for _, p := range []string{old, moved} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveClaudeTranscriptPath(config, "/work", sid); got != old {
		t.Fatalf("initial = %q", got)
	}
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	if got := resolveClaudeTranscriptPath(config, "/work", sid); got != moved {
		t.Fatalf("moved = %q", got)
	}
	const restarted = "51111111-2222-3333-4444-555555555555"
	if got := resolveClaudeTranscriptPath(config, "/work", restarted); got != "" {
		t.Fatalf("new ID reused old transcript: %q", got)
	}
	otherConfig := t.TempDir()
	if got := resolveClaudeTranscriptPath(otherConfig, "/work", sid); got != "" {
		t.Fatalf("another config reused transcript: %q", got)
	}
}

func TestIssue2481_TranscriptGlobConcurrent(t *testing.T) {
	config := t.TempDir()
	const sid = "61111111-2222-3333-4444-555555555555"
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if got := resolveClaudeTranscriptPath(config, "/work", sid); got != "" {
					t.Errorf("missing = %q", got)
				}
			}
		}()
	}
	wg.Wait()
}

func TestIssue2481_TranscriptGlobCacheBounded(t *testing.T) {
	transcriptGlobMu.Lock()
	previous := transcriptGlobCache
	transcriptGlobCache = make(map[string]transcriptGlobEntry, 4096)
	for i := 0; i < 4096; i++ {
		transcriptGlobCache[string(rune(i))] = transcriptGlobEntry{}
	}
	transcriptGlobMu.Unlock()
	t.Cleanup(func() { transcriptGlobMu.Lock(); transcriptGlobCache = previous; transcriptGlobMu.Unlock() })
	resolveClaudeTranscriptPath(t.TempDir(), "/work", "71111111-2222-3333-4444-555555555555")
	transcriptGlobMu.Lock()
	size := len(transcriptGlobCache)
	transcriptGlobMu.Unlock()
	if size > 4096 {
		t.Fatalf("cache grew to %d entries", size)
	}
}

func TestIssue2481_MultiRepoTranscriptImmediatelyVisible(t *testing.T) {
	for _, reader := range []string{"path", "response", "recall", "migration", "explicit-config"} {
		t.Run(reader, func(t *testing.T) {
			config := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			calls := countTranscriptGlobs(t)
			inst := &Instance{Tool: "claude", ProjectPath: t.TempDir(), MultiRepoEnabled: true,
				MultiRepoTempDir: t.TempDir(), ClaudeSessionID: "81111111-2222-3333-4444-555555555555"}
			if got := inst.GetJSONLPath(); got != "" {
				t.Fatalf("before creation = %q", got)
			}
			// Claude starts in the multi-repo directory, not ProjectPath.
			cwd, err := filepath.EvalSymlinks(inst.EffectiveWorkingDir())
			if err != nil {
				t.Fatal(err)
			}
			want := mkTranscript(t, config, ConvertToClaudeDirName(cwd), inst.ClaudeSessionID)
			if err := os.WriteFile(want, []byte(fxAssistantText("a1", "multi-repo reply")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			switch reader {
			case "path":
				if got := inst.GetJSONLPath(); got != want {
					t.Fatalf("next lookup = %q, want %q immediately", got, want)
				}
			case "recall":
				if got := recallInstanceTranscript(inst); got != want {
					t.Fatalf("recall lookup = %q, want %q", got, want)
				}
			case "migration":
				if err := VerifyConversationInDir(inst, config, 0); err != nil {
					t.Fatal(err)
				}
			case "explicit-config":
				if got := ResolveClaudeTranscriptPath(config, inst.ProjectPath, inst.ClaudeSessionID, inst.EffectiveWorkingDir()); got != want {
					t.Fatalf("explicit config lookup = %q, want %q", got, want)
				}
			case "response":
				response, err := inst.getClaudeLastResponse()
				if err != nil {
					t.Fatalf("next response lookup: %v", err)
				}
				if response.Content != "multi-repo reply" {
					t.Fatalf("response = %q", response.Content)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("exact multi-repo lookup triggered another glob: %d", calls.Load())
			}
			primary := mkTranscript(t, config, ConvertToClaudeDirName(inst.ProjectPath), inst.ClaudeSessionID)
			if got := inst.GetJSONLPath(); got != primary {
				t.Fatalf("ProjectPath precedence: got %q, want %q", got, primary)
			}
		})
	}
}

func TestIssue2481_TranscriptGlobLateMissKeepsNewerHit(t *testing.T) {
	config := t.TempDir()
	const sid = "91111111-2222-3333-4444-555555555555"
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	previous := transcriptGlob
	transcriptGlob = func(pattern string) ([]string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return nil, nil // An old miss finishes after a newer lookup finds the file.
		}
		return filepath.Glob(pattern)
	}
	t.Cleanup(func() { transcriptGlob = previous })
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }); <-done })
	go func() { defer close(done); resolveClaudeTranscriptPath(config, "/work", sid) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first glob did not start")
	}
	want := mkTranscript(t, config, "non-exact", sid)
	if got := resolveClaudeTranscriptPath(config, "/work", sid); got != want {
		t.Fatalf("newer lookup = %q, want %q", got, want)
	}
	unblock.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("old lookup did not finish")
	}
	if got := resolveClaudeTranscriptPath(config, "/work", sid); got != want {
		t.Fatalf("late miss replaced newer hit: %q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("cached hit was lost: %d globs", calls.Load())
	}
}
