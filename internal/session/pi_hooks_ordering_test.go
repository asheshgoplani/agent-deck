package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPiHookExtension_OrderedEmit is the regression test for #2222/P1-2: the
// extension used to fire-and-forget spawn a child per event with no await,
// so two events fired back-to-back (pi always fires session_shutdown
// immediately followed by session_start on /new, /resume, /fork, /clone,
// /reload) could land at hook-handler out of order and leave a healthy
// session's status file at the wrong terminal value. The fix awaits each
// emit on the child's close/error/timeout and awaits it in the handler, so
// pi's own in-order handler execution now serialises delivery too.
//
// This drives the real shipped source (PiHookExtensionSource) under node, the
// same way pi's runtime awaits extension handlers in order, and asserts the
// on-disk status always matches the last event fired - not a race.
func TestPiHookExtension_OrderedEmit(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available in PATH, skipping extension ordering test")
	}

	agentDeckBin, err := exec.LookPath("agent-deck")
	if err != nil {
		// Build a throwaway binary for the test if the dev binary isn't on PATH.
		agentDeckBin = filepath.Join(t.TempDir(), "agent-deck")
		build := exec.Command("go", "build", "-o", agentDeckBin, "github.com/asheshgoplani/agent-deck/cmd/agent-deck")
		build.Dir = repoRootForTest(t)
		if out, err := build.CombinedOutput(); err != nil {
			t.Skipf("could not build agent-deck for ordering test: %v\n%s", err, out)
		}
	}

	extDir := t.TempDir()
	extPath := filepath.Join(extDir, "ext.ts")
	if err := os.WriteFile(extPath, []byte(PiHookExtensionSource()), 0o644); err != nil {
		t.Fatalf("write extension source: %v", err)
	}

	driverPath := filepath.Join(extDir, "drive.mjs")
	driver := `import ext from "` + extPath + `";
const handlers = {};
ext({ on(ev, h) { handlers[ev] = h; } });
const seq = process.argv.slice(2);
for (const ev of seq) { await handlers[ev](); }
await new Promise(r => setTimeout(r, 1500));
`
	if err := os.WriteFile(driverPath, []byte(driver), 0o644); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	binDir := t.TempDir()
	if err := os.Symlink(agentDeckBin, filepath.Join(binDir, "agent-deck")); err != nil {
		t.Fatalf("symlink agent-deck: %v", err)
	}

	// Point this process at a throwaway HOME/profile so GetHooksDir resolves the
	// same status file the spawned hook-handler writes (the child inherits both
	// through os.Environ below).
	const instanceID = "ordering-test-instance"
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_PROFILE", "pi-ordering-test")
	statusFile := filepath.Join(GetHooksDir(), instanceID+".json")

	env := append(os.Environ(),
		"AGENTDECK_INSTANCE_ID="+instanceID,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)

	cases := []struct {
		name   string
		events []string
		want   string
	}{
		{"new_flow", []string{"session_shutdown", "session_start"}, "waiting"},
		{"fast_turn", []string{"turn_start", "turn_end"}, "waiting"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const runs = 20
			var wrong int
			for i := 0; i < runs; i++ {
				_ = os.Remove(statusFile)

				cmd := exec.Command(nodePath, "--experimental-strip-types", "--no-warnings", driverPath)
				cmd.Args = append(cmd.Args, tc.events...)
				cmd.Env = env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("run %d: driver failed: %v\n%s", i, err, out)
				}

				data, err := os.ReadFile(statusFile)
				if err != nil {
					t.Fatalf("run %d: read status file: %v", i, err)
				}
				var got struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatalf("run %d: unmarshal status: %v", i, err)
				}
				if got.Status != tc.want {
					wrong++
				}
			}
			if wrong != 0 {
				t.Errorf("%s: %d/%d runs landed at the wrong final status (want %q every time)", tc.name, wrong, runs, tc.want)
			}
		})
	}
}

// repoRootForTest walks up from the current package to the module root so
// `go build ./cmd/agent-deck` resolves regardless of the test binary's cwd.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}
