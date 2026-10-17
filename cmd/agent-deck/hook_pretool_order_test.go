package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func preToolHookHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("AGENTDECK_HOOK_GENERATION", "")
	t.Setenv(session.PreToolHookSyncMarkerEnv, "1")
	return home
}

func TestDelayedPreToolCannotOverwriteLifecycle(t *testing.T) {
	for _, generation := range []string{"", "current"} {
		for _, event := range []string{"PermissionRequest", "Stop", "SessionEnd"} {
			t.Run(event+"/generation="+generation, func(t *testing.T) {
				preToolHookHome(t)
				id := "pretool-ordered"
				if generation != "" {
					writeHermesControl(t, id, generation)
					t.Setenv("AGENTDECK_HOOK_GENERATION", generation)
				}
				// The tool was received first but is deliberately published last.
				// Nanoseconds in the same second guard against coarse timestamp ties.
				toolReceived := time.Unix(1700000000, 10)
				laterReceived := toolReceived.Add(time.Nanosecond)
				writeHookStatusReceivedAt(id, mapEventToStatus(event), "native", event, "", doneScanResult{}, laterReceived)
				path := filepath.Join(getHooksDir(), id+".json")
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				anchor := session.ReadHookSessionAnchor(id)
				writeHookStatusReceivedAt(id, "running", "old-native", "PreToolUse", "", doneScanResult{}, toolReceived)
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("delayed tool changed %s: %s -> %s (%v)", event, before, after, err)
				}
				if got := session.ReadHookSessionAnchor(id); got != anchor {
					t.Fatalf("rejected tool changed anchor: %q -> %q", anchor, got)
				}
				if generation != "" {
					var control hookGenerationControl
					data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".generation.json"))
					if err != nil || json.Unmarshal(data, &control) != nil || control.NextSequence != 1 {
						t.Fatalf("rejected event consumed sequence: %s (%v)", data, err)
					}
				}
			})
		}
	}
}

func TestPreToolNewToolAfterStopAndPermissionResolution(t *testing.T) {
	preToolHookHome(t)
	id := "pretool-resume"
	start := time.Now()
	for n, event := range []string{"UserPromptSubmit", "Stop", "PreToolUse", "PermissionRequest", "PreToolUse", "Stop"} {
		writeHookStatusReceivedAt(id, mapEventToStatus(event), "native", event, "", doneScanResult{}, start.Add(time.Duration(n)*time.Nanosecond))
		data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
		var state hookStatusFile
		if err != nil || json.Unmarshal(data, &state) != nil || state.Event != event || state.Status != mapEventToStatus(event) {
			t.Fatalf("new %s did not win: %s (%v)", event, data, err)
		}
	}
}

func TestPreToolEqualReceiveTimePreservesLifecycle(t *testing.T) {
	for _, event := range []string{"PermissionRequest", "Stop", "SessionEnd"} {
		t.Run(event, func(t *testing.T) {
			preToolHookHome(t)
			id := "pretool-receive-tie"
			received := time.Now()
			writeHookStatusReceivedAt(id, mapEventToStatus(event), "native", event, "", doneScanResult{}, received)
			path := filepath.Join(getHooksDir(), id+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Clock resolution must not let a tool win an ambiguous tie.
			writeHookStatusReceivedAt(id, "running", "native", "PreToolUse", "", doneScanResult{}, received)
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("receive-time tie changed %s: %s -> %s (%v)", event, before, after, err)
			}
		})
	}
}

func TestPreToolDoesNotOpenProfileStores(t *testing.T) {
	home := preToolHookHome(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "pretool-no-store")
	runHookHandlerWithStdin(t, fmt.Sprintf(`{"hook_event_name":"PreToolUse","session_id":"native","cwd":%q}`, home))
	data, err := os.ReadFile(filepath.Join(getHooksDir(), "pretool-no-store.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state hookStatusFile
	if err := json.Unmarshal(data, &state); err != nil || state.Status != "running" || state.ReceivedAt == 0 {
		t.Fatalf("missing received tool state: %s (%v)", data, err)
	}
	if err := filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "state.db" {
			t.Errorf("PreToolUse opened a profile store: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPreToolLegacyAsyncPreToolCannotPublish(t *testing.T) {
	preToolHookHome(t)
	t.Setenv(session.PreToolHookSyncMarkerEnv, "")
	id := "pretool-legacy"
	t.Setenv("AGENTDECK_INSTANCE_ID", id)
	writeHookStatusReceivedAt(id, "waiting", "native", "PermissionRequest", "", doneScanResult{}, time.Now())
	path := filepath.Join(getHooksDir(), id+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Even an old async process first scheduled AFTER the permission event
	// has no right to publish. A receive timestamp alone cannot fix this case.
	runHookHandlerWithStdin(t, `{"hook_event_name":"PreToolUse","session_id":"native"}`)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("legacy async hook published: %s -> %s (%v)", before, after, err)
	}
}

func TestPreToolLockWaitIsBounded(t *testing.T) {
	preToolHookHome(t)
	id := "pretool-contended"
	writeHookStatusReceivedAt(id, "waiting", "native", "PermissionRequest", "", doneScanResult{}, time.Now())
	lock, err := os.OpenFile(filepath.Join(getHooksDir(), id+".lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	done := make(chan struct{})
	go func() {
		writeHookStatusReceivedAt(id, "running", "native", "PreToolUse", "", doneScanResult{}, time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// Release and join before failing so the fixture cannot leak a writer.
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		<-done
		t.Fatal("tool handler did not bound its lock wait")
	}
	data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
	var state hookStatusFile
	if err != nil || json.Unmarshal(data, &state) != nil || state.Event != "PermissionRequest" {
		t.Fatalf("contended tool changed permission state: %s (%v)", data, err)
	}
}
