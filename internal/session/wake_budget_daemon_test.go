package session

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

// The daemon half of the maintainer's R5 ("the CLI send and the daemon wake
// both refuse and both publish session.budget_exceeded"). The CLI half is
// TestBudgetGate_CLISendRefusesAndPublishes in cmd/agent-deck.
//
// The wake goes through the production code: sendWakeNudgeNoWait builds the
// argv, and only its exec seam (wakeNudgeExec) is pointed at a freshly built
// agent-deck instead of the installed one. Nothing about the command line is
// copied into the test.

// daemonLegBuildTimeout bounds the go build of the CLI under test.
const daemonLegBuildTimeout = 5 * time.Minute

// daemonLegBinary is the CLI built from this tree (or the one a parent test
// run already built, AGENTDECK_TEST_CLI_BIN). The build lands in t.TempDir(),
// so it is removed with the test.
func daemonLegBinary(t *testing.T) string {
	t.Helper()
	if inherited := os.Getenv("AGENTDECK_TEST_CLI_BIN"); inherited != "" {
		if info, err := os.Stat(inherited); err == nil && !info.IsDir() {
			return inherited
		}
	}
	bin := filepath.Join(t.TempDir(), "agent-deck-test")
	ctx, cancel := context.WithTimeout(context.Background(), daemonLegBuildTimeout)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/agent-deck")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func budgetFramesUnder(t *testing.T, home string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ndjson") || !strings.Contains(p, "bus") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			var f map[string]any
			if strings.Contains(line, "session.budget_exceeded") && json.Unmarshal([]byte(line), &f) == nil && f["kind"] == "session.budget_exceeded" {
				frames = append(frames, f)
			}
		}
		return nil
	})
	return frames
}

func TestBudgetGate_DaemonWakeRefusesAndPublishes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the CLI")
	}
	bin := daemonLegBinary(t)
	const profile = "wakeleg"

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("AGENTDECK_PROFILE", profile)
	// TestMain turns the bus off for the package; the CLI must publish the frame.
	t.Setenv("AGENTDECK_EVENTS_BUS", "1")
	cfgPath := filepath.Join(home, "config", "agent-deck", "config.toml")
	writeConfig := func(body string) {
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ClearUserConfigCache()
	}
	t.Cleanup(ClearUserConfigCache)
	writeConfig(enforcedDaily1)

	// A stopped target: a send that passes the gate fails at the typing
	// stage with "not running"; a refused one never gets there.
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	added, err := exec.Command(bin, "add", "-t", "budget-target", "-c", "claude", "--no-parent", "--json", project).Output()
	if err != nil {
		t.Fatalf("add: %v %s", err, added)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(added, &created); err != nil || created.ID == "" {
		t.Fatalf("add JSON: %v %s", err, added)
	}
	seed, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := costs.NewStore(seed.GetDB().DB()).WriteCostEvent(costs.CostEvent{
		ID: "wake-ev", SessionID: created.ID, Timestamp: time.Now(), Model: "claude-test",
		InputTokens: 1000, CostMicrodollars: 5e6,
	}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	orig := wakeNudgeExec
	t.Cleanup(func() { wakeNudgeExec = orig })
	var output string
	wakeNudgeExec = func(ctx context.Context, _ string, args ...string) error {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), MachineSendEnv+"=1") // as the production seam sets it
		out, err := cmd.CombinedOutput()
		output = string(out)
		return err
	}
	exitCode := func(err error) int {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("wake: want an exit status, got %v", err)
		}
		return ee.ExitCode()
	}

	err = sendWakeNudgeNoWait(profile, created.ID, wakeNudgeMessage)
	if code := exitCode(err); code != 1 ||
		!strings.Contains(output, "budget exceeded: daily global limit exceeded ($5.00 used of $1.00)") ||
		!strings.Contains(output, "nothing was sent to 'budget-target'") {
		t.Fatalf("wake over budget: exit %d output %q", code, output)
	}
	if strings.Contains(output, "not running") {
		t.Fatalf("wake reached the typing stage: %q", output)
	}
	frames := budgetFramesUnder(t, home)
	if len(frames) != 1 {
		t.Fatalf("%d session.budget_exceeded frames, want exactly 1: %v", len(frames), frames)
	}
	data, _ := frames[0]["data"].(map[string]any)
	if frames[0]["session_id"] != created.ID || data["via"] != "send" || data["reason"] != "daily global limit exceeded" ||
		data["used_usd"] != float64(5) || data["limit_usd"] != float64(1) {
		t.Fatalf("frame = %v, want session %s via send, 5 of 1 USD", frames[0], created.ID)
	}

	// Control: the same production wake with the budget raised gets past the
	// gate (and fails at the stopped target) and publishes nothing.
	writeConfig("[costs.budgets]\nenforce = true\ndaily_limit = 100.0\n")
	err = sendWakeNudgeNoWait(profile, created.ID, wakeNudgeMessage)
	if code := exitCode(err); code != 1 || !strings.Contains(output, "not running") || strings.Contains(output, "budget exceeded") {
		t.Fatalf("control wake: exit %d output %q, want the not-running failure", code, output)
	}
	if n := len(budgetFramesUnder(t, home)); n != 1 {
		t.Fatalf("control wake published a frame: %d frames", n)
	}
}
