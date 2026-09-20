package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEmitListStats_Format pins the wire shape SSHRunner.FetchSessions
// (internal/session/list_stats.go) parses: a single prefixed JSON line on
// stderr.
func TestEmitListStats_Format(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	emitListStats(46812*time.Millisecond, 312, 76)
	os.Stderr = orig
	w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, session.ListStatsPrefix) {
		t.Fatalf("emitListStats output = %q, want prefix %q", line, session.ListStatsPrefix)
	}
	if !strings.Contains(line, `"status_pass_ms":46812`) || !strings.Contains(line, `"tmux_calls":312`) || !strings.Contains(line, `"sessions":76`) {
		t.Fatalf("emitListStats output missing expected fields: %q", line)
	}
}

// TestListJSON_StatsFlag_Opt covers the wire contract end to end through the
// real CLI binary (#2331): `list --json --stats` emits the stats line on
// stderr while stdout stays the unchanged, pure session-list JSON; plain
// `list --json` (what every human and every pre-#2331 caller runs) emits no
// stats line, so default behavior for everyone else is byte-for-byte
// unaffected.
func TestListJSON_StatsFlag_Opt(t *testing.T) {
	sb, env, _ := listPerfFixture(t)

	withStats := runListCapture(t, sb, env, "list", "--json", session.ListStatsFlag)
	if !strings.Contains(withStats.stderr, session.ListStatsPrefix) {
		t.Fatalf("list --json %s: stderr missing stats line: %q", session.ListStatsFlag, withStats.stderr)
	}

	plain := runListCapture(t, sb, env, "list", "--json")
	if strings.Contains(plain.stderr, session.ListStatsPrefix) {
		t.Fatalf("list --json (no flag): stderr unexpectedly carries a stats line: %q", plain.stderr)
	}
	if withStats.stdout != plain.stdout {
		t.Fatalf("--stats changed stdout:\nwith:    %q\nwithout: %q", withStats.stdout, plain.stdout)
	}
}

type listCaptureResult struct{ stdout, stderr string }

func runListCapture(t *testing.T, sb *harness.Sandbox, env []string, args ...string) listCaptureResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, sb.BinPath, args...)
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %v\nstderr: %s", args, err, stderr.String())
	}
	return listCaptureResult{stdout: stdout.String(), stderr: stderr.String()}
}
