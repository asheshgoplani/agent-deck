package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSwitchTUIPresentationStatus(t *testing.T) {
	tests := []struct {
		name                      string
		committed, ready, pending bool
		want                      string
	}{
		{name: "ready success", committed: true, ready: true, want: "success"},
		{name: "native pending", committed: true, pending: true, want: "pending"},
		{name: "failure", want: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := switchTUIStatus(tt.committed, tt.ready, tt.pending); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAccountSwitchPendingDoesNotAttachCrossHarnessTarget(t *testing.T) {
	h := NewHome()
	source := session.NewInstanceWithTool("source", t.TempDir(), "claude")
	source.Account = "personal"
	target := session.NewInstanceWithTool("target", source.ProjectPath, "codex")
	target.Account = "work"
	h.instancesMu.Lock()
	h.instances = []*session.Instance{source}
	h.instanceByID[source.ID] = source
	h.switchGenerations[source.ID] = 1
	h.resumingSessions[source.ID] = time.Now()
	h.instancesMu.Unlock()

	_, _ = h.Update(accountSwitchedMsg{
		sessionID: source.ID, generation: 1,
		sourceTool: source.Tool, sourceAccount: source.Account,
		sourceProject: source.ProjectPath, sourceTitle: source.Title, sourceGroup: source.GroupPath,
		sourceCommand: source.Command, sourceStatus: source.Status,
		sourceClaudeID: source.ClaudeSessionID, sourceCodexID: source.CodexSessionID, sourceCWD: source.EffectiveWorkingDir(),
		targetHarness: "codex", account: "work", target: target,
		pending: true, summary: "distinct codex target created; status=pending; readiness=pending",
		err: session.ErrCrossHarnessPending,
	})

	if h.getInstanceByID(target.ID) != nil {
		t.Fatal("pending cross-harness target was auto-attached")
	}
	if target.Tool != "codex" || target.Account != "work" {
		t.Fatalf("pending target was not preserved: %#v", target)
	}
	if h.getInstanceByID(source.ID) != source || source.Tool != "claude" || source.Account != "personal" {
		t.Fatalf("source changed while pending: %#v", source)
	}
	if _, stillResuming := h.resumingSessions[source.ID]; stillResuming {
		t.Fatal("pending completion did not clear the source spinner")
	}
	if !h.confirmDialog.IsVisible() || h.confirmDialog.noticeTitle != "Account switch pending" {
		t.Fatalf("pending result notice = visible:%t title:%q, want Account switch pending", h.confirmDialog.IsVisible(), h.confirmDialog.noticeTitle)
	}
	if !strings.Contains(h.confirmDialog.noticeBody, "status=pending") {
		t.Fatalf("pending result body = %q, want explicit status", h.confirmDialog.noticeBody)
	}
}

func TestNativeAccountSwitchReadyUsesSuccessMessage(t *testing.T) {
	h := NewHome()
	source := session.NewInstanceWithTool("source", t.TempDir(), "codex")
	source.Account = "work"
	h.storage = nil // this presentation test must not persist the synthetic row
	h.instancesMu.Lock()
	h.instances = []*session.Instance{source}
	h.instanceByID[source.ID] = source
	h.switchGenerations[source.ID] = 1
	h.instancesMu.Unlock()

	_, _ = h.Update(accountSwitchedMsg{
		sessionID: source.ID, generation: 1,
		sourceTool: source.Tool, sourceAccount: "personal",
		sourceProject: source.ProjectPath, sourceTitle: source.Title, sourceGroup: source.GroupPath,
		sourceCommand: source.Command, sourceStatus: source.Status,
		sourceClaudeID: source.ClaudeSessionID, sourceCodexID: source.CodexSessionID, sourceCWD: source.EffectiveWorkingDir(),
		targetHarness: "codex", account: "work", committed: true,
		summary: "status=success; readiness=ready; native destination observed",
	})
	if h.err == nil || !strings.Contains(h.err.Error(), "status=success; readiness=ready") {
		t.Fatalf("ready switch presentation = %v, want explicit success and ready", h.err)
	}
}

func TestNativeAccountSwitchPendingUsesPendingNotice(t *testing.T) {
	h := NewHome()
	// The backend has durably assigned the new native account, but no fresh
	// destination event has made it ready. The current snapshot therefore
	// matches the target while the message retains the original source account.
	source := session.NewInstanceWithTool("source", t.TempDir(), "codex")
	source.Account = "work"
	h.storage = nil // this presentation test must not persist the synthetic row
	h.instancesMu.Lock()
	h.instances = []*session.Instance{source}
	h.instanceByID[source.ID] = source
	h.switchGenerations[source.ID] = 1
	h.instancesMu.Unlock()

	_, _ = h.Update(accountSwitchedMsg{
		sessionID: source.ID, generation: 1,
		sourceTool: source.Tool, sourceAccount: "personal",
		sourceProject: source.ProjectPath, sourceTitle: source.Title, sourceGroup: source.GroupPath,
		sourceCommand: source.Command, sourceStatus: source.Status,
		sourceClaudeID: source.ClaudeSessionID, sourceCodexID: source.CodexSessionID, sourceCWD: source.EffectiveWorkingDir(),
		targetHarness: "codex", account: "work", committed: true, pending: true,
		summary: "status=pending; readiness=pending; native target start requested",
	})

	if !h.confirmDialog.IsVisible() || h.confirmDialog.noticeTitle != "Account switch pending" {
		t.Fatalf("native pending notice = visible:%t title:%q, want Account switch pending", h.confirmDialog.IsVisible(), h.confirmDialog.noticeTitle)
	}
	if strings.Contains(h.confirmDialog.noticeTitle, "switched") || !strings.Contains(h.confirmDialog.noticeBody, "readiness=pending") {
		t.Fatalf("native pending presentation = %q / %q", h.confirmDialog.noticeTitle, h.confirmDialog.noticeBody)
	}
}

func TestAccountSwitchErrorWithVerifiedTargetDoesNotAttach(t *testing.T) {
	h := NewHome()
	source := session.NewInstanceWithTool("source", t.TempDir(), "claude")
	target := session.NewInstanceWithTool("target", source.ProjectPath, "codex")
	h.instancesMu.Lock()
	h.instances = []*session.Instance{source}
	h.instanceByID[source.ID] = source
	h.switchGenerations[source.ID] = 1
	h.resumingSessions[source.ID] = time.Now()
	h.instancesMu.Unlock()

	_, _ = h.Update(accountSwitchedMsg{
		sessionID: source.ID, generation: 1,
		sourceTool: source.Tool, sourceAccount: source.Account,
		sourceProject: source.ProjectPath, sourceTitle: source.Title, sourceGroup: source.GroupPath,
		sourceCommand: source.Command, sourceStatus: source.Status,
		sourceClaudeID: source.ClaudeSessionID, sourceCodexID: source.CodexSessionID, sourceCWD: source.EffectiveWorkingDir(),
		targetHarness: "codex", account: "work", target: target, targetReady: true,
		err: errors.New("injected post-commit persistence failure"),
	})

	if h.getInstanceByID(target.ID) != nil {
		t.Fatal("verified target with an error and pending=false was attached")
	}
	if !h.confirmDialog.IsVisible() || h.confirmDialog.noticeTitle != "Account switch failed" {
		t.Fatalf("error result notice = visible:%t title:%q, want Account switch failed", h.confirmDialog.IsVisible(), h.confirmDialog.noticeTitle)
	}
}

func TestAccountSwitchVerifiedCrossHarnessTargetAttaches(t *testing.T) {
	h := NewHome()
	source := session.NewInstanceWithTool("source", t.TempDir(), "claude")
	target := session.NewInstanceWithTool("target", source.ProjectPath, "codex")
	h.instancesMu.Lock()
	h.instances = []*session.Instance{source}
	h.instanceByID[source.ID] = source
	h.switchGenerations[source.ID] = 1
	h.resumingSessions[source.ID] = time.Now()
	h.instancesMu.Unlock()

	_, _ = h.Update(accountSwitchedMsg{
		sessionID: source.ID, generation: 1,
		sourceTool: source.Tool, sourceAccount: source.Account,
		sourceProject: source.ProjectPath, sourceTitle: source.Title, sourceGroup: source.GroupPath,
		sourceCommand: source.Command, sourceStatus: source.Status,
		sourceClaudeID: source.ClaudeSessionID, sourceCodexID: source.CodexSessionID, sourceCWD: source.EffectiveWorkingDir(),
		targetHarness: "codex", account: "work", target: target, targetReady: true,
		summary: "status=success; readiness=ready",
	})

	if h.getInstanceByID(target.ID) != target {
		t.Fatal("positively verified target was not attached")
	}
}

func TestNativeHarnessSwitchMessageClassifiesResultAndError(t *testing.T) {
	tests := []struct {
		name                   string
		result                 *session.HarnessSwitchResult
		err                    error
		wantCommitted          bool
		wantReady, wantPending bool
		wantStatus             string
		wantRecoveryRequired   bool
	}{
		{
			name:       "nil result",
			err:        errors.New("executor failed before a result"),
			wantStatus: "unchanged",
		},
		{
			name:                 "committed journal error with readiness pending",
			result:               &session.HarnessSwitchResult{Committed: true, DestinationReady: false, Conversation: "journal transition failed"},
			err:                  errors.New("persist completed journal"),
			wantCommitted:        true,
			wantStatus:           "failed",
			wantRecoveryRequired: true,
		},
		{
			name:                 "committed journal error with readiness ready",
			result:               &session.HarnessSwitchResult{Committed: true, DestinationReady: true, Conversation: "journal transition failed"},
			err:                  errors.New("persist completed journal"),
			wantCommitted:        true,
			wantReady:            true,
			wantStatus:           "failed",
			wantRecoveryRequired: true,
		},
		{
			name:          "ordinary native pending",
			result:        &session.HarnessSwitchResult{Committed: true, Conversation: "native readiness pending"},
			wantCommitted: true,
			wantPending:   true,
			wantStatus:    "pending",
		},
		{
			name:          "ready success",
			result:        &session.HarnessSwitchResult{Committed: true, DestinationReady: true, Conversation: "native readiness observed"},
			wantCommitted: true,
			wantReady:     true,
			wantStatus:    "success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := nativeHarnessSwitchMessage(accountSwitchedMsg{summary: "unchanged"}, tt.result, tt.err)
			if msg.err != tt.err || msg.committed != tt.wantCommitted || msg.targetReady != tt.wantReady || msg.pending != tt.wantPending {
				t.Fatalf("mapped message = %#v", msg)
			}
			if tt.result == nil {
				if msg.summary != "unchanged" {
					t.Fatalf("nil result summary = %q, want unchanged", msg.summary)
				}
				return
			}
			if !strings.Contains(msg.summary, "status="+tt.wantStatus) {
				t.Fatalf("summary = %q, want status=%s", msg.summary, tt.wantStatus)
			}
			if strings.Contains(msg.summary, "recovery_required=true") != tt.wantRecoveryRequired {
				t.Fatalf("summary = %q, recovery_required=%t", msg.summary, tt.wantRecoveryRequired)
			}
			if tt.err != nil && (strings.Contains(msg.summary, "status=pending") || strings.Contains(msg.summary, "status=success")) {
				t.Fatalf("error was presented as pending or success: %q", msg.summary)
			}
		})
	}
}

func TestCrossHarnessSwitchMessageMapsNilTargetFailure(t *testing.T) {
	msg := crossHarnessSwitchMessage(accountSwitchedMsg{sessionID: "source"}, &session.SwitchPreview{TargetHarness: "codex"}, &session.CrossHarnessSwitchResult{TargetCreated: false}, errors.New("load target failed"))
	if msg.target != nil || msg.targetReady || msg.summary == "" || !strings.Contains(msg.summary, "status=failed") {
		t.Fatalf("nil target result was not mapped safely: %#v", msg)
	}
}

func TestAccountSwitchFailureUsesFailureNotice(t *testing.T) {
	h := NewHome()
	source := session.NewInstanceWithTool("source", t.TempDir(), "claude")
	h.instancesMu.Lock()
	h.instances = []*session.Instance{source}
	h.instanceByID[source.ID] = source
	h.switchGenerations[source.ID] = 1
	h.instancesMu.Unlock()

	_, _ = h.Update(accountSwitchedMsg{
		sessionID: source.ID, generation: 1,
		sourceTool: source.Tool, sourceAccount: source.Account,
		sourceProject: source.ProjectPath, sourceTitle: source.Title, sourceGroup: source.GroupPath,
		sourceCommand: source.Command, sourceStatus: source.Status,
		sourceClaudeID: source.ClaudeSessionID, sourceCodexID: source.CodexSessionID, sourceCWD: source.EffectiveWorkingDir(),
		targetHarness: "codex", account: "work", err: errors.New("injected switch failure"),
	})
	if !h.confirmDialog.IsVisible() || h.confirmDialog.noticeTitle != "Account switch failed" {
		t.Fatalf("failure result notice = visible:%t title:%q, want Account switch failed", h.confirmDialog.IsVisible(), h.confirmDialog.noticeTitle)
	}
}
