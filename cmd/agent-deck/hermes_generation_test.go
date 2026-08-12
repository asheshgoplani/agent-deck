package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func writeHermesControl(t *testing.T, id, generation string) {
	t.Helper()
	if err := os.MkdirAll(getHooksDir(), 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"generation": generation, "next_sequence": 0})
	if err := os.WriteFile(filepath.Join(getHooksDir(), id+".generation.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHermesStaleGenerationCannotMutateAnchor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "hermes-anchor"
	writeHermesControl(t, id, "g2")
	session.WriteHookSessionAnchor(id, "current-session")
	t.Setenv("AGENTDECK_HOOK_GENERATION", "g1")
	writeHookStatus(id, "dead", "old-session", "on_session_finalize", "")
	if got := session.ReadHookSessionAnchor(id); got != "current-session" {
		t.Fatalf("stale hook changed anchor to %q", got)
	}
}

func TestCleanStaleHookFilesPreservesLiveGenerationControlAndLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "hermes-long-lived"
	writeHermesControl(t, id, "g")
	status := filepath.Join(getHooksDir(), id+".json")
	lock := filepath.Join(getHooksDir(), id+".lock")
	control := filepath.Join(getHooksDir(), id+".generation.json")
	if err := os.WriteFile(status, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{status, lock, control} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	cleanStaleHookFiles()
	if _, err := os.Stat(status); !os.IsNotExist(err) {
		t.Fatalf("old status survived: %v", err)
	}
	for _, path := range []string{lock, control} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("live generation artifact reaped: %s: %v", path, err)
		}
	}
}

func TestCleanStaleHookFilesReapsOnlyUnlockedAbandonedHermesLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(getHooksDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(getHooksDir(), "hermes-abandoned.lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	cleanStaleHookFiles()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("abandoned lock survived: %v", err)
	}
}

func TestHermesDelayedOldGenerationWriteRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "hermes-delayed"
	writeHermesControl(t, id, "g2")
	t.Setenv("AGENTDECK_HOOK_GENERATION", "g1")
	writeHookStatus(id, "dead", "", "on_session_finalize", "")
	if _, err := os.Stat(filepath.Join(getHooksDir(), id+".json")); !os.IsNotExist(err) {
		t.Fatalf("stale write accepted: %v", err)
	}
	t.Setenv("AGENTDECK_HOOK_GENERATION", "g2")
	writeHookStatus(id, "waiting", "", "post_llm_call", "")
	var got hookStatusFile
	b, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(b, &got) != nil || got.HookGeneration != "g2" || got.Sequence != 1 {
		t.Fatalf("bad accepted write: %+v", got)
	}
}

func TestHermesConcurrentWritersUseMonotonicSequenceAndNoTemps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_HOOK_GENERATION", "g")
	id := "hermes-concurrent"
	writeHermesControl(t, id, "g")
	var wg sync.WaitGroup
	for n := 0; n < 24; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			writeHookStatus(id, "running", "", fmt.Sprintf("pre_tool_call_%d", n), "")
		}(n)
	}
	wg.Wait()
	var got hookStatusFile
	b, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(b, &got) != nil || got.Sequence != 24 {
		t.Fatalf("sequence=%d want 24", got.Sequence)
	}
	if temps, _ := filepath.Glob(filepath.Join(getHooksDir(), "."+id+"*.tmp-*")); len(temps) != 0 {
		t.Fatalf("orphan temps: %v", temps)
	}
}

func TestHermesAttachClearPreservesInitialMessagePending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_HOOK_GENERATION", "g")
	id := "hermes-pending"
	b, _ := json.Marshal(hookGenerationControl{Generation: "g", InitialMessagePending: true})
	if err := os.MkdirAll(getHooksDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(getHooksDir(), id+".generation.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if writeHookStatusFile(id, hookStatusFile{Status: "waiting", Event: "on_session_start", Timestamp: 1}, true) {
		t.Fatal("on_session_start downgraded pending initial message")
	}
	if !writeHookStatusFile(id, hookStatusFile{Status: "waiting", Event: "post_llm_call", Timestamp: 2}, true) {
		t.Fatal("post_llm_call rejected")
	}
	controlData, err := os.ReadFile(filepath.Join(getHooksDir(), id+".generation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var control hookGenerationControl
	if json.Unmarshal(controlData, &control) != nil || control.InitialMessagePending {
		t.Fatalf("completion did not clear pending: %+v", control)
	}
}
