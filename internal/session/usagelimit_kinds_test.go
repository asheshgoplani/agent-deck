package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Field records, verbatim in shape from transcripts on the box (model
// "<synthetic>", content a text-block array). Only the timestamp is varied.
func recFieldAPIErrorAt(ts string, status int, kind, text string) string {
	statusField := ""
	if status != 0 {
		statusField = fmt.Sprintf(`"apiErrorStatus":%d,`, status)
	}
	return fmt.Sprintf(`{"type":"assistant","isApiErrorMessage":true,%s"error":%q,"message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":%q}]},"timestamp":%q}`,
		statusField, kind, text, ts)
}

const (
	fieldOrgDisabledText  = "Your organization has disabled Claude subscription access for Claude Code · Use an Anthropic API key instead, or ask your admin to enable access"
	fieldWeeklyText       = "You've hit your weekly limit · resets 12pm (Europe/Berlin)"
	fieldWeeklyDatedText  = "You've hit your weekly limit · resets Oct 1 at 12pm (Europe/Berlin)"
	fieldModelLimitText   = "You've reached your Fable limit. Run /usage-credits to continue or switch models with /model."
	fieldOutOfCreditsText = "You're out of usage credits. Run /usage-credits to keep using Fable 5.1 or /model to switch models."
)

func TestLatestAssistantTurnLimit_FieldRecords(t *testing.T) {
	recent := stampAt(-time.Minute)
	tests := []struct {
		name       string
		record     string
		wantKind   Substate
		wantDetail string
	}{
		{
			name:       "org disabled subscription access is account-blocked",
			record:     recFieldAPIErrorAt(recent, 403, "oauth_org_not_allowed", fieldOrgDisabledText),
			wantKind:   SubstateAccountBlocked,
			wantDetail: accountBlockedDetail,
		},
		{
			name:       "oauth_org_not_allowed without a status is still account-blocked",
			record:     recFieldAPIErrorAt(recent, 0, "oauth_org_not_allowed", fieldOrgDisabledText),
			wantKind:   SubstateAccountBlocked,
			wantDetail: accountBlockedDetail,
		},
		{
			name:       "weekly limit is usage-limit with the window as detail",
			record:     recFieldAPIErrorAt(recent, 429, "rate_limit", fieldWeeklyText),
			wantKind:   SubstateUsageLimit,
			wantDetail: "weekly limit · resets 12pm (Europe/Berlin)",
		},
		{
			name:       "dated weekly limit keeps the full reset text",
			record:     recFieldAPIErrorAt(recent, 429, "rate_limit", fieldWeeklyDatedText),
			wantKind:   SubstateUsageLimit,
			wantDetail: "weekly limit · resets Oct 1 at 12pm (Europe/Berlin)",
		},
		{
			name:       "reached your <Model> limit is model-limit",
			record:     recFieldAPIErrorAt(recent, 429, "rate_limit", fieldModelLimitText),
			wantKind:   SubstateModelLimit,
			wantDetail: "Fable",
		},
		{
			name:       "keep using <Model> or /model is model-limit",
			record:     recFieldAPIErrorAt(recent, 429, "rate_limit", fieldOutOfCreditsText),
			wantKind:   SubstateModelLimit,
			wantDetail: "Fable 5.1",
		},
		{
			name:       "reached your weekly limit is a plan window, not a model",
			record:     recFieldAPIErrorAt(recent, 429, "rate_limit", "You've reached your weekly limit · resets 12pm (UTC)"),
			wantKind:   SubstateUsageLimit,
			wantDetail: "weekly limit · resets 12pm (UTC)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeUsageLimitTranscript(t, recUserAt(recent), tt.record)
			v, ok := latestAssistantTurnLimit(path, refNow)
			if !ok || v.Kind != tt.wantKind || v.Detail != tt.wantDetail {
				t.Fatalf("latestAssistantTurnLimit = (%+v, %v), want ({Kind:%s Detail:%s}, true)",
					v, ok, tt.wantKind, tt.wantDetail)
			}
		})
	}
}

// account-blocked does not clear on its own like a plan window, so it is
// believed for 24h while the 429 kinds keep the 5h bound.
func TestLatestAssistantTurnLimit_StaleBounds(t *testing.T) {
	tests := []struct {
		name     string
		age      time.Duration
		record   func(ts string) string
		wantKind Substate
	}{
		{"account-blocked at 6h is still believed", 6 * time.Hour,
			func(ts string) string {
				return recFieldAPIErrorAt(ts, 403, "oauth_org_not_allowed", fieldOrgDisabledText)
			},
			SubstateAccountBlocked},
		{"account-blocked at 23h is still believed", 23 * time.Hour,
			func(ts string) string {
				return recFieldAPIErrorAt(ts, 403, "oauth_org_not_allowed", fieldOrgDisabledText)
			},
			SubstateAccountBlocked},
		{"account-blocked at 25h is stale", 25 * time.Hour,
			func(ts string) string {
				return recFieldAPIErrorAt(ts, 403, "oauth_org_not_allowed", fieldOrgDisabledText)
			},
			SubstateNone},
		{"usage-limit at 4h is believed", 4 * time.Hour,
			func(ts string) string { return recFieldAPIErrorAt(ts, 429, "rate_limit", fieldWeeklyText) },
			SubstateUsageLimit},
		{"usage-limit at 6h is stale", 6 * time.Hour,
			func(ts string) string { return recFieldAPIErrorAt(ts, 429, "rate_limit", fieldWeeklyText) },
			SubstateNone},
		{"model-limit at 6h is stale", 6 * time.Hour,
			func(ts string) string { return recFieldAPIErrorAt(ts, 429, "rate_limit", fieldModelLimitText) },
			SubstateNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := stampAt(-tt.age)
			v, ok := latestAssistantTurnLimit(writeUsageLimitTranscript(t, recUserAt(ts), tt.record(ts)), refNow)
			if !ok || v.Kind != tt.wantKind {
				t.Fatalf("latestAssistantTurnLimit = (%+v, %v), want kind %q, ok true", v, ok, tt.wantKind)
			}
		})
	}
}

func TestLatestAssistantTurnLimit_AuthAndServerErrorsDoNotMatch(t *testing.T) {
	recent := stampAt(-time.Minute)
	for _, rec := range []string{
		recFieldAPIErrorAt(recent, 401, "authentication_failed", "Please run /login · API Error: 401"),
		recFieldAPIErrorAt(recent, 0, "authentication_failed", "Please run /login"),
		recFieldAPIErrorAt(recent, 500, "server_error", "API Error: 500 Internal server error"),
		recFieldAPIErrorAt(recent, 0, "server_error", "API Error: Internal server error"),
		// 403 alone is not the org-disabled signal: the error kind is the key.
		recFieldAPIErrorAt(recent, 403, "permission_error", "forbidden"),
	} {
		v, ok := latestAssistantTurnLimit(writeUsageLimitTranscript(t, recUserAt(recent), rec), refNow)
		if !ok || v.Kind != SubstateNone || v.Detail != "" {
			t.Fatalf("latestAssistantTurnLimit(%s) = (%+v, %v), want no verdict kind, ok true", rec, v, ok)
		}
	}
}

// Pins the Substate/SubstateDetail wiring for the transcript kinds with a
// seeded memo, mirroring TestSubstate_ReportsUsageLimitWhenLimited.
func TestSubstate_ReportsTranscriptLimitKindsWithDetail(t *testing.T) {
	for _, v := range []limitVerdict{
		{Kind: SubstateUsageLimit, Detail: "weekly limit · resets 12pm (Europe/Berlin)"},
		{Kind: SubstateModelLimit, Detail: "Fable 5.1"},
		{Kind: SubstateAccountBlocked, Detail: accountBlockedDetail},
	} {
		inst := NewInstanceWithTool("limit-kind-substate", t.TempDir(), "claude")
		inst.ClaudeSessionID = "bound-session-id"
		inst.mu.Lock()
		inst.lastUsageLimitScanAt = time.Now()
		inst.usageLimitedCached = true
		inst.usageLimitVerdict = v
		inst.usageLimitSessionID = inst.ClaudeSessionID
		inst.mu.Unlock()

		if got := inst.Substate(); got != v.Kind {
			t.Fatalf("Substate() = %q, want %q", got, v.Kind)
		}
		if got := inst.SubstateDetail(); got != v.Detail {
			t.Fatalf("SubstateDetail() after %s = %q, want %q", v.Kind, got, v.Detail)
		}
	}
}

// Drives the real scan-and-publish path: the verdict kind and detail formed
// from the transcript must reach Substate and SubstateDetail.
func TestSubstate_AccountBlockedFromTranscript(t *testing.T) {
	const sid = "account-blocked-session"
	project := t.TempDir()
	inst := NewInstanceWithTool("account-blocked", project, "claude")
	inst.ClaudeSessionID = sid

	dir := filepath.Join(GetClaudeConfigDirForInstance(inst), "projects", ConvertToClaudeDirName(project))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	recent := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	body := recUserAt(recent) + "\n" + recFieldAPIErrorAt(recent, 403, "oauth_org_not_allowed", fieldOrgDisabledText) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := locateHandoffTranscript(inst); got != path {
		t.Skipf("resolver did not find the fixture (got %q, want %q); environment-dependent", got, path)
	}

	if got := inst.Substate(); got != SubstateAccountBlocked {
		t.Fatalf("Substate() = %q, want %q", got, SubstateAccountBlocked)
	}
	if got := inst.SubstateDetail(); got != accountBlockedDetail {
		t.Fatalf("SubstateDetail() = %q, want %q", got, accountBlockedDetail)
	}
}
