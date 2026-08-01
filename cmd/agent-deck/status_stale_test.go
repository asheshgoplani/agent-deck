package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestClassifyStale_Heuristics is a pure unit test over the three in-scope
// #1704 heuristics (never-started, bash-idle, last-activity) plus the
// exclusion rules (running/error/starting/queued are never candidates). No
// subprocess, no tmux, no wall clock — now/threshold are injected.
func TestClassifyStale_Heuristics(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	const threshold = 24 * time.Hour

	tests := []struct {
		name string
		inst *session.Instance
		want []staleReason
	}{
		{
			name: "never_started_past_threshold",
			inst: &session.Instance{
				Status:    session.StatusIdle,
				CreatedAt: now.Add(-25 * time.Hour), // LastStartedAt zero, CreatedAt old
			},
			want: []staleReason{reasonNeverStarted},
		},
		{
			name: "never_started_within_threshold_not_stale",
			inst: &session.Instance{
				Status:    session.StatusIdle,
				CreatedAt: now.Add(-1 * time.Hour), // just added, not stale yet
			},
			want: nil,
		},
		{
			name: "running_never_a_candidate_even_if_old",
			inst: &session.Instance{
				Status:    session.StatusRunning,
				CreatedAt: now.Add(-72 * time.Hour),
			},
			want: nil,
		},
		{
			name: "error_never_a_candidate",
			inst: &session.Instance{
				Status:    session.StatusError,
				CreatedAt: now.Add(-72 * time.Hour),
			},
			want: nil,
		},
		{
			name: "starting_never_a_candidate",
			inst: &session.Instance{
				Status:    session.StatusStarting,
				CreatedAt: now.Add(-72 * time.Hour),
			},
			want: nil,
		},
		{
			name: "queued_never_a_candidate",
			inst: &session.Instance{
				Status:    session.StatusQueued,
				CreatedAt: now.Add(-72 * time.Hour),
			},
			want: nil,
		},
		{
			name: "waiting_within_threshold_not_stale",
			inst: &session.Instance{
				Status:         session.StatusWaiting,
				CreatedAt:      now.Add(-72 * time.Hour),
				LastStartedAt:  now.Add(-72 * time.Hour),
				LastAccessedAt: now.Add(-1 * time.Hour), // recent activity via DisplayLastActivityTime fallback
			},
			want: nil,
		},
		{
			name: "waiting_past_threshold_is_last_activity",
			inst: &session.Instance{
				Status:         session.StatusWaiting,
				CreatedAt:      now.Add(-72 * time.Hour),
				LastStartedAt:  now.Add(-72 * time.Hour),
				LastAccessedAt: now.Add(-48 * time.Hour),
			},
			want: []staleReason{reasonLastActivity},
		},
		{
			name: "stopped_past_threshold_is_last_activity",
			inst: &session.Instance{
				Status:         session.StatusStopped,
				CreatedAt:      now.Add(-72 * time.Hour),
				LastStartedAt:  now.Add(-72 * time.Hour),
				LastAccessedAt: now.Add(-48 * time.Hour),
			},
			want: []staleReason{reasonLastActivity},
		},
		{
			name: "started_then_idle_past_threshold_is_bash_idle",
			inst: &session.Instance{
				Status:         session.StatusIdle,
				CreatedAt:      now.Add(-72 * time.Hour),
				LastStartedAt:  now.Add(-72 * time.Hour), // was started, so NOT never-started
				LastAccessedAt: now.Add(-48 * time.Hour),
			},
			want: []staleReason{reasonBashIdle},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStale(tc.inst, now, threshold)
			if len(got) != len(tc.want) {
				t.Fatalf("classifyStale() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("classifyStale() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestStatusStale_CLI_CandidateViewAndMutatesNothing is the end-to-end gate:
// builds the real binary, adds a never-started session (nothing else could
// legitimately reach candidate status without tmux in a test sandbox), and
// asserts:
//   - status --stale --json with a 0s threshold flags it as a candidate with
//     reason "never-started" and correct counts.
//   - status --stale --json with the (long) default threshold reports ZERO
//     candidates for the same fresh session — no false positives.
//   - `list --json` before and after --stale is byte-identical on the fields
//     that matter (status/count unchanged) — proving the read-only view
//     mutated nothing.
func TestStatusStale_CLI_CandidateViewAndMutatesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in short mode")
	}

	tmpHome := t.TempDir()
	xdgConfigHome := filepath.Join(tmpHome, ".config")
	xdgDataHome := filepath.Join(tmpHome, ".local", "share")
	xdgCacheHome := filepath.Join(tmpHome, ".cache")
	projectDir := filepath.Join(tmpHome, "project")
	for _, dir := range []string{xdgConfigHome, xdgDataHome, xdgCacheHome, projectDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	binPath := filepath.Join(t.TempDir(), "agent-deck-stale-test")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\noutput: %s", err, out)
	}

	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX") ||
			strings.HasPrefix(kv, "AGENTDECK_") ||
			strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+tmpHome,
		"XDG_CONFIG_HOME="+xdgConfigHome,
		"XDG_DATA_HOME="+xdgDataHome,
		"XDG_CACHE_HOME="+xdgCacheHome,
		"AGENTDECK_PROFILE=test-1704-stale",
		"TERM=dumb",
	)

	run := func(args ...string) (string, string, error) {
		cmd := exec.Command(binPath, args...)
		cmd.Env = env
		cmd.Dir = projectDir
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	// Add a session but never start it — the one candidate type reachable
	// without a live tmux server in a test sandbox.
	addOut, addErr, err := run("add", projectDir, "-t", "stale-probe", "-c", "shell", "--no-parent", "--json")
	if err != nil {
		t.Fatalf("add failed: %v\nstdout=%s\nstderr=%s", err, addOut, addErr)
	}

	// Baseline: with the (long) default threshold, a session added moments
	// ago must NOT be a candidate. Guards against a threshold-comparison bug
	// that would flag everything unconditionally.
	baselineOut, baselineErr, err := run("status", "--stale", "--json")
	if err != nil {
		t.Fatalf("status --stale (default threshold) failed: %v\nstdout=%s\nstderr=%s", err, baselineOut, baselineErr)
	}
	var baseline staleCandidatesJSON
	if err := json.Unmarshal([]byte(baselineOut), &baseline); err != nil {
		t.Fatalf("unmarshal baseline --stale --json: %v\nraw=%s", err, baselineOut)
	}
	if baseline.StaleCount != 0 {
		t.Fatalf("fresh session flagged stale under default threshold (false positive): %+v", baseline)
	}
	if baseline.Total != 1 {
		t.Fatalf("expected total=1, got %d", baseline.Total)
	}

	// Snapshot list --json before forcing candidacy, to prove --stale never
	// mutates session state.
	listBefore, _, err := run("list", "--json")
	if err != nil {
		t.Fatalf("list --json (before) failed: %v", err)
	}

	// Force candidacy with a zero threshold and assert the shape + reason.
	staleOut, staleErrOut, err := run("status", "--stale", "--threshold", "0s", "--json")
	if err != nil {
		t.Fatalf("status --stale --threshold 0s failed: %v\nstdout=%s\nstderr=%s", err, staleOut, staleErrOut)
	}
	var resp staleCandidatesJSON
	if err := json.Unmarshal([]byte(staleOut), &resp); err != nil {
		t.Fatalf("unmarshal --stale --json: %v\nraw=%s", err, staleOut)
	}
	if resp.StaleCount != 1 || len(resp.Candidates) != 1 {
		t.Fatalf("expected exactly 1 stale candidate, got %+v", resp)
	}
	cand := resp.Candidates[0]
	if cand.Title != "stale-probe" {
		t.Fatalf("candidate title = %q, want %q", cand.Title, "stale-probe")
	}
	if !cand.NeverStarted {
		t.Fatalf("candidate NeverStarted = false, want true: %+v", cand)
	}
	if len(cand.Reasons) != 1 || cand.Reasons[0] != string(reasonNeverStarted) {
		t.Fatalf("candidate Reasons = %v, want [%s]", cand.Reasons, reasonNeverStarted)
	}
	if cand.LastStartedAt != "" {
		t.Fatalf("never-started candidate must not carry LastStartedAt, got %q", cand.LastStartedAt)
	}

	// The critical mutation check: list --json after --stale must match
	// list --json before it, byte for byte. If --stale silently stopped,
	// removed, or otherwise rewrote the session this diverges.
	listAfter, _, err := run("list", "--json")
	if err != nil {
		t.Fatalf("list --json (after) failed: %v", err)
	}
	if listBefore != listAfter {
		t.Fatalf("status --stale mutated session state!\nbefore=%s\nafter=%s", listBefore, listAfter)
	}

	// Also confirm plain-text mode doesn't crash and mentions the candidate.
	textOut, textErr, err := run("status", "--stale", "--threshold", "0s")
	if err != nil {
		t.Fatalf("status --stale (text) failed: %v\nstdout=%s\nstderr=%s", err, textOut, textErr)
	}
	if !strings.Contains(textOut, "stale-probe") || !strings.Contains(textOut, "never-started") {
		t.Fatalf("text --stale output missing expected candidate info: %s", textOut)
	}
	if !strings.Contains(textOut, "read-only") {
		t.Fatalf("text --stale output should reassert read-only/suggest-only framing: %s", textOut)
	}
}
