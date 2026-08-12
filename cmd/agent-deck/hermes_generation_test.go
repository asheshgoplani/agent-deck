package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
