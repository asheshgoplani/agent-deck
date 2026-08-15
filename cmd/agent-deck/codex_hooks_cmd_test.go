package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestMapCodexNotifyToStatus(t *testing.T) {
	tests := []struct {
		event  string
		expect string
	}{
		{"agent-turn-complete", "waiting"},
		{"agent-turn-start", "running"},
		{"AGENT-TURN-COMPLETE", "waiting"},
		{"turn/completed", "waiting"},
		{"turn/started", "running"},
		{"turn.completed", "waiting"},
		{"turn.started", "running"},
		{"turn.failed", "waiting"},
		{"thread.started", "waiting"},
		{"foo turn start bar", "running"},
		{"foo turn complete bar", "waiting"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got := mapCodexNotifyToStatus(tt.event)
			if got != tt.expect {
				t.Fatalf("mapCodexNotifyToStatus(%q) = %q, want %q", tt.event, got, tt.expect)
			}
		})
	}
}

func TestHandleCodexNotify_WritesStatus(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-1")
	t.Setenv("CODEX_SESSION_ID", "")

	origArgs := os.Args
	defer func() { os.Args = origArgs }()
	os.Args = []string{"agent-deck", "codex-notify"}

	origStdin := os.Stdin
	defer func() { os.Stdin = origStdin }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	_, _ = w.WriteString(`{"type":"agent-turn-complete","session_id":"abc-123"}`)
	_ = w.Close()
	os.Stdin = r

	handleCodexNotify()

	hookPath := filepath.Join(getHooksDir(), "inst-1.json")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var hook hookStatusFile
	if err := json.Unmarshal(data, &hook); err != nil {
		t.Fatalf("unmarshal hook: %v", err)
	}
	if hook.Status != "waiting" {
		t.Fatalf("hook status = %q, want waiting", hook.Status)
	}
	if hook.SessionID != "abc-123" {
		t.Fatalf("hook session_id = %q, want abc-123", hook.SessionID)
	}
}

func TestHandleCodexNotify_ArgPayload(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-arg")
	t.Setenv("CODEX_SESSION_ID", "")

	origArgs := os.Args
	defer func() { os.Args = origArgs }()
	os.Args = []string{"agent-deck", "codex-notify", `{"event":"turn/completed","thread_id":"thr-1"}`}

	handleCodexNotify()

	hookPath := filepath.Join(getHooksDir(), "inst-arg.json")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var hook hookStatusFile
	if err := json.Unmarshal(data, &hook); err != nil {
		t.Fatalf("unmarshal hook: %v", err)
	}
	if hook.Status != "waiting" {
		t.Fatalf("hook status = %q, want waiting", hook.Status)
	}
	if hook.SessionID != "thr-1" {
		t.Fatalf("hook session_id = %q, want thr-1", hook.SessionID)
	}
}

func TestHandleCodexNotify_JSONRPCMethodPayload(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-method")
	t.Setenv("CODEX_SESSION_ID", "")

	origArgs := os.Args
	defer func() { os.Args = origArgs }()
	os.Args = []string{"agent-deck", "codex-notify", `{"method":"turn/completed","params":{"thread_id":"thr-42"}}`}

	handleCodexNotify()

	hookPath := filepath.Join(getHooksDir(), "inst-method.json")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var hook hookStatusFile
	if err := json.Unmarshal(data, &hook); err != nil {
		t.Fatalf("unmarshal hook: %v", err)
	}
	if hook.Status != "waiting" {
		t.Fatalf("hook status = %q, want waiting", hook.Status)
	}
	if hook.SessionID != "thr-42" {
		t.Fatalf("hook session_id = %q, want thr-42", hook.SessionID)
	}
}

func TestHandleCodexNotify_EmptyTailEventKeepsJSONEmptyAndPersistsAnchor(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-sticky")
	t.Setenv("CODEX_SESSION_ID", "")

	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	// Seed sticky mapping with a thread_id-bearing event.
	os.Args = []string{"agent-deck", "codex-notify", `{"event":"turn/started","thread_id":"thr-sticky","turn_id":"turn-main"}`}
	handleCodexNotify()

	// Tail event has no session_id/thread_id; should backfill from sticky store.
	os.Args = []string{"agent-deck", "codex-notify", `{"event":"turn/completed"}`}
	handleCodexNotify()

	hookPath := filepath.Join(getHooksDir(), "inst-sticky.json")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var hook hookStatusFile
	if err := json.Unmarshal(data, &hook); err != nil {
		t.Fatalf("unmarshal hook: %v", err)
	}
	if hook.SessionID != "" {
		t.Fatalf("hook session_id = %q, want empty for compatibility", hook.SessionID)
	}
	if got := session.ReadHookSessionAnchor("inst-sticky"); got != "thr-sticky" {
		t.Fatalf("session anchor = %q, want thr-sticky", got)
	}
	if hook.CodexStartedGeneration == "" || hook.CodexCompletedGeneration != "" {
		t.Fatalf("identity-less completion must fail closed: %#v", hook)
	}
}

func TestWriteCodexHookStatus_NewerStartSupersedesCompletedGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeCodexHookStatus("inst-generation", "running", "thread-1", "turn.started", "turn-1")
	writeCodexHookStatus("inst-generation", "waiting", "thread-1", "turn.completed", "turn-1")
	path := filepath.Join(getHooksDir(), "inst-generation.json")
	var completed hookStatusFile
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.CodexStartedGeneration != completed.CodexCompletedGeneration {
		t.Fatal("matching completion was not retained")
	}
	writeCodexHookStatus("inst-generation", "running", "thread-1", "turn.started", "turn-2")
	var superseded hookStatusFile
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &superseded); err != nil {
		t.Fatal(err)
	}
	if superseded.CodexStartedGeneration == superseded.CodexCompletedGeneration {
		t.Fatal("new start must supersede old completion evidence")
	}
}

func TestWriteCodexHookStatus_CompletionMustMatchTurnIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_HOOKS_DIR", filepath.Join(t.TempDir(), "hooks"))

	writeCodexHookStatus("turn-match", "running", "thread-1", "turn.started", "turn-2")
	writeCodexHookStatus("turn-match", "waiting", "thread-1", "turn.completed", "turn-1")
	data, err := os.ReadFile(filepath.Join(getHooksDir(), "turn-match.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got hookStatusFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.CodexCompletedGeneration != "" {
		t.Fatalf("out-of-order completion converged live turn: %#v", got)
	}

	writeCodexHookStatus("turn-match", "waiting", "thread-1", "turn.completed", "turn-2")
	data, err = os.ReadFile(filepath.Join(getHooksDir(), "turn-match.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.CodexCompletedGeneration == "" || got.CodexCompletedGeneration != got.CodexStartedGeneration {
		t.Fatalf("matching completion did not converge: %#v", got)
	}
}

func TestWriteCodexHookStatus_IdentityLessCompletionFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_HOOKS_DIR", filepath.Join(t.TempDir(), "hooks"))
	writeCodexHookStatus("no-turn", "running", "thread-1", "turn.started", "turn-1")
	writeCodexHookStatus("no-turn", "waiting", "thread-1", "agent-turn-complete", "")
	data, err := os.ReadFile(filepath.Join(getHooksDir(), "no-turn.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got hookStatusFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.CodexCompletedGeneration != "" {
		t.Fatalf("identity-less completion converged: %#v", got)
	}
}

func TestWriteCodexHookStatus_LegacyCompletionConvergesWithoutStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_HOOKS_DIR", filepath.Join(t.TempDir(), "hooks"))
	writeCodexHookStatus("legacy-complete", "waiting", "thread-1", "agent-turn-complete", "turn-1")
	data, err := os.ReadFile(filepath.Join(getHooksDir(), "legacy-complete.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got hookStatusFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.CodexStartedGeneration == "" || got.CodexStartedGeneration != got.CodexCompletedGeneration {
		t.Fatalf("completion-only notify did not converge: %#v", got)
	}
	if got.CodexStartedSessionID != "thread-1" || got.CodexCompletedSessionID != "thread-1" {
		t.Fatalf("completion-only notify lost session identity: %#v", got)
	}
}

func TestWriteCodexHookStatus_IdentityLessStartClearsPriorCompletion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_HOOKS_DIR", filepath.Join(t.TempDir(), "hooks"))
	writeCodexHookStatus("stale-pair", "waiting", "thread-1", "agent-turn-complete", "turn-1")
	writeCodexHookStatus("stale-pair", "running", "", "turn.started", "")
	data, err := os.ReadFile(filepath.Join(getHooksDir(), "stale-pair.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got hookStatusFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.CodexStartedGeneration != "" || got.CodexCompletedGeneration != "" ||
		got.CodexStartedSessionID != "" || got.CodexCompletedSessionID != "" {
		t.Fatalf("identity-less start retained stale completion evidence: %#v", got)
	}
}

func TestWriteCodexHookStatus_ConcurrentFleetPersistence(t *testing.T) {
	for _, size := range []int{1, 20, 100} {
		t.Run(fmt.Sprintf("fleet-%d", size), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("AGENTDECK_HOOKS_DIR", filepath.Join(t.TempDir(), "hooks"))
			for n := 0; n < size; n++ {
				id := fmt.Sprintf("instance-%03d", n)
				turn := fmt.Sprintf("turn-%03d", n)
				writeCodexHookStatus(id, "running", id, "turn.started", turn)
			}
			var wg sync.WaitGroup
			wg.Add(size)
			for n := 0; n < size; n++ {
				n := n
				go func() {
					defer wg.Done()
					id := fmt.Sprintf("instance-%03d", n)
					writeCodexHookStatus(id, "waiting", id, "turn.completed", fmt.Sprintf("turn-%03d", n))
				}()
			}
			wg.Wait()
			for n := 0; n < size; n++ {
				id := fmt.Sprintf("instance-%03d", n)
				data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
				if err != nil {
					t.Fatalf("read %s: %v", id, err)
				}
				var got hookStatusFile
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatalf("decode %s: %v", id, err)
				}
				if got.CodexStartedGeneration == "" || got.CodexStartedGeneration != got.CodexCompletedGeneration {
					t.Fatalf("%s did not converge: %#v", id, got)
				}
			}
		})
	}
}

func TestCleanStaleHookFilesPreservesCodexWriterLockForLiveStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(getHooksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	instanceID := "codex-live-writer"
	statusPath := filepath.Join(getHooksDir(), instanceID+".json")
	lockPath := filepath.Join(getHooksDir(), instanceID+".codex-writer.lock")
	if err := os.WriteFile(statusPath, []byte(`{"status":"running"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer closeChecked(lock)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	cleanStaleHookFiles()
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("writer lock for live status was reaped: %v", err)
	}
}

func TestCodexHooksInstallUninstall(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	handleCodexHooksInstall()

	configPath := getCodexConfigPath()
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, codexNotifyMarkerBegin) {
		t.Fatalf("config missing marker begin")
	}
	if !strings.Contains(text, codexNotifyLine) {
		t.Fatalf("config missing notify line")
	}

	handleCodexHooksUninstall()

	content, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after uninstall: %v", err)
	}
	text = string(content)
	if strings.Contains(text, codexNotifyMarkerBegin) {
		t.Fatalf("expected codex notify block removed, got: %q", text)
	}
}

func TestCodexHooksInstall_UpgradesLegacyTableWithoutMarkers(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configPath := getCodexConfigPath()
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := "model = \"gpt-5\"\n\n[notify]\nprogram = [\"agent-deck\", \"codex-notify\"]\n"
	if err := os.WriteFile(configPath, []byte(legacy), 0644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	handleCodexHooksInstall()

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, codexNotifyMarkerBegin) || !strings.Contains(text, codexNotifyLine) {
		t.Fatalf("expected agent-deck notify block after upgrade, got: %q", text)
	}
	if strings.Contains(text, "[notify]") || strings.Contains(text, "program =") {
		t.Fatalf("expected legacy notify table removed, got: %q", text)
	}
}

func TestCodexHooksInstall_UpgradesLegacyMarkerBlock(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configPath := getCodexConfigPath()
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := codexNotifyMarkerBegin + "\n[notify]\nprogram = [\"agent-deck\", \"codex-notify\"]\n" + codexNotifyMarkerEnd + "\n"
	if err := os.WriteFile(configPath, []byte(legacy), 0644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	handleCodexHooksInstall()

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, codexNotifyLine) {
		t.Fatalf("expected upgraded notify line, got: %q", text)
	}
	if strings.Contains(text, "[notify]") || strings.Contains(text, "program =") {
		t.Fatalf("expected legacy notify format removed, got: %q", text)
	}
}

func TestGetCodexConfigPath_UsesCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex-home"))

	got := getCodexConfigPath()
	if !strings.HasSuffix(got, filepath.Join("codex-home", "config.toml")) {
		t.Fatalf("getCodexConfigPath() = %q, expected suffix codex-home/config.toml", got)
	}
}

func TestCodexSharedStatusWriteNeverMutatesAnchor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "codex-anchor-owner"
	session.WriteHookSessionAnchor(id, "thread-current")
	if !writeHookStatusFile(id, hookStatusFile{Status: "dead", SessionID: "thread-stale", Event: "SessionEnd", Timestamp: 1}, false) {
		t.Fatal("status write failed")
	}
	if got := session.ReadHookSessionAnchor(id); got != "thread-current" {
		t.Fatalf("shared writer mutated Codex anchor: %q", got)
	}
}
