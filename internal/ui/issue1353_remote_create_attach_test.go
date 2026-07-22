package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

type fakeRemoteCreateRunner struct {
	createResult session.RemoteCreateResult
	createErr    error
	attachErr    error

	createCalls int
	attachCalls int

	attachedResult session.RemoteCreateResult
}

func (f *fakeRemoteCreateRunner) FetchSessions(context.Context) ([]session.RemoteSessionInfo, error) {
	return nil, nil
}

func (f *fakeRemoteCreateRunner) FetchSessionOutput(context.Context, string) (string, error) {
	return "", nil
}

func (f *fakeRemoteCreateRunner) FetchSessionPane(context.Context, string) (string, error) {
	return "", nil
}

func (f *fakeRemoteCreateRunner) FetchCostSummary(context.Context) (*costs.RemoteCostSummary, error) {
	return nil, nil
}

func (f *fakeRemoteCreateRunner) MeasureLatency(context.Context) (time.Duration, error) {
	return 0, nil
}

func (f *fakeRemoteCreateRunner) CreateSession(context.Context, session.RemoteCreateOptions) (session.RemoteCreateResult, error) {
	f.createCalls++
	return f.createResult, f.createErr
}

func (f *fakeRemoteCreateRunner) DeleteSession(context.Context, string) error { return nil }
func (f *fakeRemoteCreateRunner) StopSession(context.Context, string) error   { return nil }
func (f *fakeRemoteCreateRunner) RestartSession(context.Context, string) error {
	return nil
}

func (f *fakeRemoteCreateRunner) RenameSession(context.Context, string, string) error {
	return nil
}

func (f *fakeRemoteCreateRunner) Attach(string) error {
	f.attachCalls++
	return f.attachErr
}

func (f *fakeRemoteCreateRunner) AttachCreatedResult(result session.RemoteCreateResult) error {
	f.attachedResult = result
	return nil
}

func TestIssue1353_RemoteCreateAndAttachCmd_UsesCreateResponseWhenAvailable(t *testing.T) {
	runner := &fakeRemoteCreateRunner{
		createResult: session.RemoteCreateResult{
			SessionID:          "ws-1",
			Attachable:         true,
			AttachCommand:      "ssh agentbox tmux attach -t ws-1",
			LocalAttachCommand: "tmux attach -t ws-1",
		},
		attachErr: errors.New("follow-up attach lookup failed"),
	}

	err := (remoteCreateAndAttachCmd{runner: runner}).Run()
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if runner.createCalls != 1 {
		t.Fatalf("CreateSession calls = %d, want 1", runner.createCalls)
	}
	if runner.attachCalls != 0 {
		t.Fatalf("Attach calls = %d, want 0 when create already returned attach commands", runner.attachCalls)
	}
	if runner.attachedResult.SessionID != "ws-1" {
		t.Fatalf("attached create result = %+v, want preserved create response", runner.attachedResult)
	}
}

func TestIssue1353_RemoteCreateAndAttachCmd_FallsBackToAttachForSSH(t *testing.T) {
	runner := &fakeRemoteCreateRunner{
		createResult: session.RemoteCreateResult{SessionID: "remote-session-1"},
	}

	err := (remoteCreateAndAttachCmd{runner: runner}).Run()
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if runner.attachCalls != 1 {
		t.Fatalf("Attach calls = %d, want 1 for non-agentbox create results", runner.attachCalls)
	}
}
