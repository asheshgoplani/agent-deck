package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// This file implements the read-only `agent-deck status --stale` candidate
// view (issue #1704). It is the accepted first slice of proactive session
// lifecycle management: a heuristic-driven list of sessions that LOOK
// abandoned, shown with the evidence behind each classification, so a human
// or a conductor agent can decide what to do about them.
//
// HARD CONSTRAINT (per the issue's accepted design + the standing
// never-auto-stop/never-auto-delete rule): this view is suggest-only. It
// never stops or removes a session, and never writes session state back to
// storage — there is no code path from this file that calls Stop/Remove/
// Save. It does call the same status-refresh (RefreshInstancesForCLIStatus +
// UpdateStatus) that plain `status`/`status -v` already run, which can
// create/update/clear an auth-hold sidecar file exactly as those commands
// do today; this file introduces no new mutation surface, but "read-only"
// above refers specifically to the session record, not every byte on disk.

// defaultStaleThreshold is the default staleness window applied when
// --threshold is not given. 24h matches "sat around overnight with nobody
// looking at it" — long enough that a lunch break doesn't false-positive,
// short enough to catch the "forgot about this worker for a day" case the
// issue was filed over.
const defaultStaleThreshold = 24 * time.Hour

// staleReason is a single named heuristic that fired for a candidate.
// Multiple heuristics are evaluated but only one applies per session (see
// classifyStale) so each candidate has exactly one primary reason — the
// three named in the accepted scope: never-started, bash-idle, and
// last-activity age for everything else sitting waiting/stopped.
type staleReason string

const (
	reasonNeverStarted staleReason = "never-started" // added but Start() was never called
	reasonBashIdle     staleReason = "bash-idle"     // tool=="shell" session sitting at an idle prompt
	reasonLastActivity staleReason = "last-activity" // waiting/idle/stopped with no observed activity past the threshold
)

// staleCandidate is one session flagged by the heuristic view, carrying the
// evidence behind the classification so nobody has to trust the verdict
// blindly (per Ashesh's #1704 review comment: "no single heuristic proves
// that work is safely landed").
type staleCandidate struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	Tool                string   `json:"tool"`
	Status              string   `json:"status"`
	Substate            string   `json:"substate,omitempty"`
	Path                string   `json:"path"`
	GroupPath           string   `json:"group_path,omitempty"`
	ParentSessionID     string   `json:"parent_session_id,omitempty"`
	Reasons             []string `json:"reasons"`
	NeverStarted        bool     `json:"never_started"`
	CreatedAt           string   `json:"created_at"`
	LastStartedAt       string   `json:"last_started_at,omitempty"`
	LastActivityAt      string   `json:"last_activity_at"`
	LastActivityAgeSecs int64    `json:"last_activity_age_seconds"`
}

// staleEligibleStatus reports whether a status is even considered for
// staleness. Running (active work) and error (needs attention, not cleanup)
// are never candidates. Starting/queued are transient launch states, not
// idle abandonment. Only waiting, idle, and stopped sessions can be stale.
func staleEligibleStatus(status session.Status) bool {
	switch status {
	case session.StatusWaiting, session.StatusIdle, session.StatusStopped:
		return true
	default:
		return false
	}
}

// classifyStale evaluates the three in-scope heuristics against one instance
// and returns the candidate reasons (empty when the session is not stale).
// now and threshold are passed in explicitly so tests can exercise the pure
// logic without depending on wall-clock time.
func classifyStale(inst *session.Instance, now time.Time, threshold time.Duration) []staleReason {
	if !staleEligibleStatus(inst.Status) {
		return nil
	}

	// LastStartedAt is persisted via the tool_data extras zone (see
	// internal/session/last_started_persist.go, added alongside this
	// heuristic) so this reads a real cross-process value, not an
	// always-zero in-memory-only field. But zero is still ambiguous: it
	// means either "genuinely never started" or "started before this
	// field's persistence landed" (last_started_at is a brand-new tool_data
	// key — every row saved before this fix shipped reads back as zero even
	// though the session ran fine, and that describes literally every
	// pre-existing session in the fleet on first deploy). Reporting the
	// stronger "never-started" claim on an unproven zero is exactly the
	// false-positive class this view exists to avoid (see the maintainer's
	// PR #1826 review comment: "make the unknown case render as unknown
	// rather than collapsing into a definite claim"). Corroborate with
	// independent evidence collected by staleActivityEvidence — confirmed
	// tmux activity, a TUI attach, or a durable hook-status sample — before
	// asserting never-started: if any of those fired after CreatedAt,
	// something DID happen, so fall through to the activity-based
	// classification below instead.
	evidence := staleActivityEvidence(inst)
	if inst.LastStartedAt.IsZero() && evidence.Equal(inst.CreatedAt) {
		if now.Sub(inst.CreatedAt) >= threshold {
			return []staleReason{reasonNeverStarted}
		}
		return nil
	}

	activityAge := now.Sub(evidence)
	if activityAge < threshold {
		return nil
	}
	// StatusIdle is overloaded: for tool=="shell" it means a genuine bash
	// prompt sitting idle (see UpdateStatus's "idle"/"waiting" tmux-status
	// handling for shell), but for Claude/Codex/Gemini/etc it means
	// "waiting, user-acknowledged" (Instance.UpdateStatus's IsAcknowledged
	// branch) — a different situation that belongs under last-activity, not
	// under a reason literally named bash-idle.
	if inst.Status == session.StatusIdle && inst.Tool == "shell" {
		return []staleReason{reasonBashIdle}
	}
	return []staleReason{reasonLastActivity}
}

// staleActivityEvidence returns the most credible "last active" timestamp
// available for classification. DisplayLastActivityTime() alone is not
// enough here: it only counts LastObservedActivity, a process-local tmux
// tracker that a short-lived `status --stale` CLI invocation never runs
// long enough to populate, so it falls straight through to the persisted
// LastAccessedAt/CreatedAt fallback. That fallback can be stale for a
// worker that just finished real work but was never attached in the TUI.
// UpdateStatus's cold-load path (called by runStatusStale before this runs)
// reads the on-disk hook status sidecar regardless of process age, so
// LastHookActivityTime is durable, real evidence of recent activity — take
// whichever timestamp is more recent (never the reverse: this can only
// shrink the reported age, never inflate it into a false negative).
func staleActivityEvidence(inst *session.Instance) time.Time {
	activity := inst.DisplayLastActivityTime()
	if hookTime, ok := inst.LastHookActivityTime(); ok && hookTime.After(activity) {
		return hookTime
	}
	return activity
}

// computeStaleCandidates scans instances (already status-refreshed by the
// caller) and returns the stale subset, sorted oldest-last-activity-first so
// the most obviously-abandoned sessions surface at the top.
func computeStaleCandidates(instances []*session.Instance, now time.Time, threshold time.Duration) []staleCandidate {
	var candidates []staleCandidate
	for _, inst := range instances {
		reasons := classifyStale(inst, now, threshold)
		if len(reasons) == 0 {
			continue
		}
		reasonStrs := make([]string, 0, len(reasons))
		neverStartedConfirmed := false
		for _, r := range reasons {
			reasonStrs = append(reasonStrs, string(r))
			if r == reasonNeverStarted {
				neverStartedConfirmed = true
			}
		}
		lastActivity := staleActivityEvidence(inst)
		c := staleCandidate{
			ID:              inst.ID,
			Title:           inst.Title,
			Tool:            inst.Tool,
			Status:          StatusString(inst.Status),
			Substate:        string(inst.Substate()),
			Path:            inst.ProjectPath,
			GroupPath:       inst.GroupPath,
			ParentSessionID: inst.ParentSessionID,
			Reasons:         reasonStrs,
			// NeverStarted mirrors the actual classification reason (not a
			// raw LastStartedAt.IsZero() check) so this boolean can never
			// contradict Reasons — a zero LastStartedAt that was
			// corroborated by other activity evidence and classified under
			// last-activity/bash-idle instead must not also claim
			// never-started here.
			NeverStarted:        neverStartedConfirmed,
			CreatedAt:           inst.CreatedAt.Format(time.RFC3339),
			LastActivityAt:      lastActivity.Format(time.RFC3339),
			LastActivityAgeSecs: int64(now.Sub(lastActivity).Seconds()),
		}
		if !inst.LastStartedAt.IsZero() {
			c.LastStartedAt = inst.LastStartedAt.Format(time.RFC3339)
		}
		candidates = append(candidates, c)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LastActivityAgeSecs > candidates[j].LastActivityAgeSecs
	})
	return candidates
}

// staleCandidatesJSON is the top-level --stale --json envelope.
type staleCandidatesJSON struct {
	ThresholdSeconds int64            `json:"threshold_seconds"`
	Total            int              `json:"total"`
	StaleCount       int              `json:"stale_count"`
	Candidates       []staleCandidate `json:"candidates"`
	Note             string           `json:"note"`
}

// runStatusStale is the entry point invoked from handleStatus when --stale is
// set. It is READ-ONLY: it loads, classifies, and prints — nothing here calls
// Stop, Remove, or Save. profile/threshold/jsonOutput come from the parsed
// flags in handleStatus.
func runStatusStale(profile string, threshold time.Duration, jsonOutput bool) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Printf("Error: failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Printf("Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	// Refresh status the same way `status`/`status -v` does (issue #610 parity)
	// so staleness is judged against live state, not a stale on-disk snapshot.
	// This only reads tmux/hook state into the in-memory Instance objects; it
	// does not write anything back to storage (no SaveWithGroups call below).
	session.RefreshInstancesForCLIStatus(instances)
	for _, inst := range instances {
		_ = inst.UpdateStatus()
	}

	now := time.Now()
	candidates := computeStaleCandidates(instances, now, threshold)

	if jsonOutput {
		resp := staleCandidatesJSON{
			ThresholdSeconds: int64(threshold.Seconds()),
			Total:            len(instances),
			StaleCount:       len(candidates),
			Candidates:       candidates,
			Note:             "read-only candidate view; nothing was stopped or removed",
		}
		if resp.Candidates == nil {
			resp.Candidates = []staleCandidate{}
		}
		out, _ := json.Marshal(resp)
		fmt.Println(string(out))
		return
	}

	if len(candidates) == 0 {
		fmt.Printf("No stale candidates in profile '%s' (threshold %s). %d session(s) checked.\n",
			storage.Profile(), threshold, len(instances))
		return
	}

	fmt.Printf("STALE CANDIDATES (%d of %d sessions, threshold %s) — read-only, nothing stopped or removed:\n\n",
		len(candidates), len(instances), threshold)
	for _, c := range candidates {
		age := time.Duration(c.LastActivityAgeSecs) * time.Second
		fmt.Printf("  %-16s %-10s %-8s %-16s last-active %s ago  %s\n",
			truncate(c.Title, 16), c.Tool, c.Status, strings.Join(c.Reasons, ","), age.Round(time.Minute), c.Path)
	}
	fmt.Println()
	fmt.Println("Suggest-only: review each candidate, then use `session stop`/`session remove` yourself. This command never stops or removes a session.")
}
