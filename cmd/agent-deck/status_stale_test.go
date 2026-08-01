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
			name: "started_then_idle_shell_past_threshold_is_bash_idle",
			inst: &session.Instance{
				Status:         session.StatusIdle,
				Tool:           "shell", // reasonBashIdle only applies to genuine bash-idle tools
				CreatedAt:      now.Add(-72 * time.Hour),
				LastStartedAt:  now.Add(-72 * time.Hour), // was started, so NOT never-started
				LastAccessedAt: now.Add(-48 * time.Hour),
			},
			want: []staleReason{reasonBashIdle},
		},
		{
			// Regression guard (#1826 re-review BLOCKING finding): a zero
			// LastStartedAt is ambiguous — it also describes every row
			// persisted before last_started_at existed as a tool_data key,
			// i.e. every live session in the fleet on first deploy of this
			// fix. LastAccessedAt here proves the session WAS used after
			// creation, so classifyStale must not assert the stronger,
			// unproven never-started claim; it must fall through to the
			// ordinary activity-based classification instead.
			name: "zero_last_started_at_with_corroborating_activity_is_not_never_started",
			inst: &session.Instance{
				Status:         session.StatusWaiting,
				CreatedAt:      now.Add(-72 * time.Hour),
				LastAccessedAt: now.Add(-48 * time.Hour), // proves activity after creation; LastStartedAt left zero
			},
			want: []staleReason{reasonLastActivity},
		},
		{
			// Regression guard (#1826 review finding #3): StatusIdle on a
			// non-shell tool means "waiting, user-acknowledged" (see
			// UpdateStatus's IsAcknowledged branch), not a bash prompt sitting
			// idle. It must classify as last-activity, never bash-idle.
			name: "started_then_idle_claude_past_threshold_is_last_activity_not_bash_idle",
			inst: &session.Instance{
				Status:         session.StatusIdle,
				Tool:           "claude",
				CreatedAt:      now.Add(-72 * time.Hour),
				LastStartedAt:  now.Add(-72 * time.Hour),
				LastAccessedAt: now.Add(-48 * time.Hour),
			},
			want: []staleReason{reasonLastActivity},
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

// TestStaleActivityEvidence_PrefersDurableHookTimestamp is a regression
// guard for the #1826 review's second P1 (chatgpt-codex-connector,
// status_stale.go:110): DisplayLastActivityTime() alone only counts
// process-local LastObservedActivity, which a short-lived `status --stale`
// CLI invocation never runs long enough to populate, so it silently falls
// back to the (possibly very old) CreatedAt/LastAccessedAt. A session that
// just finished real work — proven by a fresh on-disk hook status sample —
// must not be misclassified as stale against its creation time.
func TestStaleActivityEvidence_PrefersDurableHookTimestamp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old := time.Now().Add(-72 * time.Hour)
	recent := time.Now().Add(-30 * time.Second)

	inst := &session.Instance{
		ID:            "stale-evidence-test",
		Status:        session.StatusWaiting,
		Tool:          "claude",
		CreatedAt:     old,
		LastStartedAt: old, // started, so classifyStale takes the activity-age branch
		// No LastAccessedAt: never attached in the TUI, so
		// DisplayLastActivityTime() would otherwise fall all the way back
		// to the 72h-old CreatedAt.
	}

	// A hook sample with no SessionID/Event is a no-op past setting the
	// hookStatus/hookLastUpdate fields themselves (see UpdateHookStatus:
	// empty SessionID + no session-anchor file on this fresh HOME resolves
	// to an early return before any session-id binding logic runs).
	inst.UpdateHookStatus(&session.HookStatus{Status: "waiting", UpdatedAt: recent})

	got := staleActivityEvidence(inst)
	if !got.Equal(recent) {
		t.Fatalf("staleActivityEvidence() = %v, want the durable hook timestamp %v (got CreatedAt-derived instead)", got, recent)
	}

	// End-to-end: with a 1h threshold, this session must NOT be flagged
	// stale, even though CreatedAt is 72h old — the hook proves it was
	// active 30s ago.
	if got := classifyStale(inst, time.Now(), time.Hour); len(got) != 0 {
		t.Fatalf("classifyStale() = %v, want nil: durable hook activity 30s ago must not read as stale under a 1h threshold", got)
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

// TestStatusStale_CLI_StartedSessionIsNotNeverStarted closes the #1826
// review's blind spot: the original CLI test only ever exercised a session
// that was genuinely never started, so a heuristic that (bug) classified
// EVERYTHING as never-started would still pass it. This test actually starts
// a real session, lets tmux settle it to idle, and asserts through the
// SQLite-backed CLI subprocess (a real process boundary, not an in-memory
// Instance literal) that:
//   - never_started is false and last_started_at is populated
//   - the reason is bash-idle (tool=="shell"), not never-started
//   - the candidate's status field is asserted, not just its reasons
func TestStatusStale_CLI_StartedSessionIsNotNeverStarted(t *testing.T) {
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

	binPath := filepath.Join(t.TempDir(), "agent-deck-stale-started-test")
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
		"AGENTDECK_PROFILE=test-1704-stale-started",
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

	addOut, addErr, err := run("add", projectDir, "-t", "started-probe", "-c", "shell", "--no-parent", "--json")
	if err != nil {
		t.Fatalf("add failed: %v\nstdout=%s\nstderr=%s", err, addOut, addErr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(addOut), &added); err != nil || added.ID == "" {
		t.Fatalf("unmarshal add --json: %v\nraw=%s", err, addOut)
	}
	// Stop the tmux pane at the end regardless of outcome — this test spawns
	// a real tmux session on the isolated per-test profile/socket.
	t.Cleanup(func() {
		_, _, _ = run("session", "stop", added.ID)
	})

	startOut, startErr, err := run("session", "start", added.ID, "--json")
	if err != nil {
		t.Fatalf("session start failed: %v\nstdout=%s\nstderr=%s", err, startOut, startErr)
	}

	// Poll list --json until tmux settles the shell to a terminal status
	// (idle, given no foreground process) or the deadline expires.
	type listRow struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	deadline := time.Now().Add(20 * time.Second)
	var lastStatus string
	for time.Now().Before(deadline) {
		listOut, listErrOut, err := run("list", "--json")
		if err != nil {
			t.Fatalf("list --json failed: %v\nstdout=%s\nstderr=%s", err, listOut, listErrOut)
		}
		var rows []listRow
		if err := json.Unmarshal([]byte(listOut), &rows); err != nil {
			t.Fatalf("unmarshal list --json: %v\nraw=%s", err, listOut)
		}
		for _, r := range rows {
			if r.ID == added.ID {
				lastStatus = r.Status
			}
		}
		if lastStatus == "idle" || lastStatus == "waiting" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastStatus != "idle" && lastStatus != "waiting" {
		t.Fatalf("started shell session never settled to idle/waiting (last seen %q) — cannot exercise the started-then-idle path", lastStatus)
	}

	staleOut, staleErrOut, err := run("status", "--stale", "--threshold", "0s", "--json")
	if err != nil {
		t.Fatalf("status --stale --threshold 0s failed: %v\nstdout=%s\nstderr=%s", err, staleOut, staleErrOut)
	}
	var resp staleCandidatesJSON
	if err := json.Unmarshal([]byte(staleOut), &resp); err != nil {
		t.Fatalf("unmarshal --stale --json: %v\nraw=%s", err, staleOut)
	}
	var cand *staleCandidate
	for i := range resp.Candidates {
		if resp.Candidates[i].ID == added.ID {
			cand = &resp.Candidates[i]
		}
	}
	if cand == nil {
		t.Fatalf("started session missing from --stale candidates: %+v", resp)
	}
	if cand.NeverStarted {
		t.Fatalf("started session reported NeverStarted=true (the #1826 blocker regressed): %+v", cand)
	}
	if cand.LastStartedAt == "" {
		t.Fatalf("started session missing last_started_at: %+v", cand)
	}
	if cand.Status != lastStatus {
		t.Fatalf("candidate.Status = %q, want %q (the observed list --json status)", cand.Status, lastStatus)
	}
	if len(cand.Reasons) != 1 {
		t.Fatalf("expected exactly one reason, got %v", cand.Reasons)
	}
	switch lastStatus {
	case "idle":
		if cand.Reasons[0] != string(reasonBashIdle) {
			t.Fatalf("idle shell session reason = %q, want %q", cand.Reasons[0], reasonBashIdle)
		}
	case "waiting":
		if cand.Reasons[0] != string(reasonLastActivity) {
			t.Fatalf("waiting session reason = %q, want %q", cand.Reasons[0], reasonLastActivity)
		}
	}
	if cand.Reasons[0] == string(reasonNeverStarted) {
		t.Fatalf("started session classified as never-started: %+v", cand)
	}
}
