package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func review2115Activate(t *testing.T) {
	t.Helper()
	dir, err := resolveLocksDirForSpawnLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"version": 1, "quiescent_upgrade_declared": true, "locks_namespace": canonical})
	if err := os.WriteFile(filepath.Join(dir, "completion-protocol.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReview2115Activation(t *testing.T) {
	for _, scenario := range []string{"missing", "malformed", "unsupported", "wrong-namespace", "valid"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			review2115Activate(t)
			dir, _ := resolveLocksDirForSpawnLock()
			path := filepath.Join(dir, "completion-protocol.json")
			switch scenario {
			case "missing":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				os.WriteFile(path, []byte("null"), 0600)
			case "unsupported":
				os.WriteFile(path, []byte("{\"version\":2}"), 0600)
			case "wrong-namespace":
				os.WriteFile(path, []byte("{\"version\":1,\"quiescent_upgrade_declared\":true,\"locks_namespace\":\"/wrong\"}"), 0600)
			}
			if got := completionProtocolActive(); got != (scenario == "valid") {
				t.Fatalf("activation %s: %v", scenario, got)
			}
		})
	}
}

// A real separate controller holds the guard until released or killed.
func TestReview2115GuardChild(t *testing.T) {
	if os.Getenv("REVIEW2115_CHILD") != "1" {
		return
	}
	t.Setenv("HOME", os.Getenv("REVIEW2115_HOME"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	release, err := defaultAcquireInstanceSpawnLock("process-test")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ready := os.Getenv("REVIEW2115_READY")
	if err := os.WriteFile(ready, []byte(os.Getenv("HOME")), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(ready + ".release"); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("child release timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReview2115GuardProcesses(t *testing.T) {
	for _, scenario := range []string{"release", "killed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			review2115Activate(t)
			childTemp := t.TempDir()
			ready := filepath.Join(childTemp, "ready")
			cmd := exec.Command(os.Args[0], "-test.run=^TestReview2115GuardChild$")
			cmd.Env = append(os.Environ(), "TMPDIR="+childTemp, "REVIEW2115_CHILD=1", "REVIEW2115_READY="+ready, "REVIEW2115_HOME="+os.Getenv("HOME"))
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child not ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			childHome, err := os.ReadFile(ready)
			if err != nil || string(childHome) != os.Getenv("HOME") {
				t.Fatal("controllers did not share sandbox HOME")
			}
			before := time.Now()
			if release, ok := tryTerminatedStatusCommit("process-test"); ok {
				release()
				t.Fatal("two controllers acquired ownership")
			}
			if time.Since(before) > time.Second {
				t.Fatal("status blocked")
			}
			path, _ := instanceSpawnLockPath("process-test")
			guardBefore, err := os.Stat(path + ".guard")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "killed" {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
			} else {
				if err := os.WriteFile(ready+".release", nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			}
			release, ok := tryTerminatedStatusCommit("process-test")
			if !ok {
				t.Fatal("upgraded released/dead owner did not recover")
			}
			release()
			guardAfter, err := os.Stat(path + ".guard")
			if err != nil || !os.SameFile(guardBefore, guardAfter) {
				t.Fatal("guard inode replaced")
			}
		})
	}
}

func TestReview2115GuardPreservesReplacement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	release, ok := tryTerminatedStatusCommit("replacement")
	if !ok {
		t.Fatal("acquire")
	}
	path, _ := instanceSpawnLockPath("replacement")
	replacement := []byte("ambiguous replacement")
	if err := os.WriteFile(path+".replacement", replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".replacement", path); err != nil {
		t.Fatal(err)
	}
	release()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(replacement) {
		t.Fatal("release removed replacement")
	}
}

func TestReview2115GuardAmbiguousOwner(t *testing.T) {
	for _, scenario := range []string{"legacy-dead", "live-old", "eperm"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			review2115Activate(t)
			path, _ := instanceSpawnLockPath("ambiguous")
			data := []byte("2147483647")
			if scenario != "legacy-dead" {
				data, _ = json.Marshal(spawnGuardOwner{Version: 1, PID: os.Getpid(), Nonce: "0123456789abcdef0123456789abcdef"})
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-time.Hour)
			os.Chtimes(path, old, old)
			if scenario == "eperm" {
				original := spawnOwnerSignal
				spawnOwnerSignal = func(int, syscall.Signal) error { return syscall.EPERM }
				defer func() { spawnOwnerSignal = original }()
			}
			if release, ok := tryTerminatedStatusCommit("ambiguous"); ok {
				release()
				t.Fatal("ambiguous owner reclaimed")
			}
			retained, err := os.ReadFile(path)
			if err != nil || string(retained) != string(data) {
				t.Fatal("owner changed")
			}
		})
	}
}

func TestReview2115PreactivationClassification(t *testing.T) {
	for _, scenario := range []string{"empty", "occupied", "active"} {
		t.Run(scenario, func(t *testing.T) {
			observer, scope := issue2091ProofObserver(t, false)
			if scenario != "active" {
				dir, _ := resolveLocksDirForSpawnLock()
				if err := os.Rename(filepath.Join(dir, "completion-protocol.json"), filepath.Join(dir, "completion-protocol.json.saved")); err != nil {
					t.Fatal(err)
				}
			}
			generation, _ := hookGenerationForInstance(observer.ID)
			data, _ := json.Marshal(map[string]any{"generation": generation, "next_sequence": 2, "launch_at": time.Now().Add(-2 * time.Second)})
			if err := os.WriteFile(filepath.Join(scope, observer.ID+".generation.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if scenario == "active" {
				review2115Activate(t)
			}
			if scenario == "occupied" {
				path, _ := instanceSpawnLockPath(observer.ID)
				if err := os.WriteFile(path, []byte("legacy"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			observer.mu.Lock()
			observer.applyTerminatedPaneStatus()
			got := observer.Status
			observer.mu.Unlock()
			want := StatusError
			if scenario == "occupied" {
				want = StatusWaiting
			}
			if scenario == "active" {
				want = StatusStopped
			}
			if got != want {
				t.Fatalf("%s: got %s want %s", scenario, got, want)
			}
		})
	}
}
