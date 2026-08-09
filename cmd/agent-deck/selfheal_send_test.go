package main

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestSendForSelfHeal_UsesGuardedDefaultSendContract(t *testing.T) {
	inst := &session.Instance{ID: "worker-id", Title: "worker", Tool: "claude"}
	tmuxSess := tmux.ReconnectSessionLazy("worker-tmux", inst.Title, t.TempDir(), "claude", "idle")
	inst.SetTmuxSessionForTest(tmuxSess)
	message := "Transport connectivity may have recovered; continue from the interrupted step."
	wantErr := errors.New("delivery sentinel")

	orig := selfHealExecuteSend
	t.Cleanup(func() { selfHealExecuteSend = orig })
	selfHealExecuteSend = func(target sendRetryTarget, tool, gotMessage string, force bool, tuning sendExecTuning) (sendDeliveryResult, error) {
		if target != tmuxSess {
			t.Errorf("target = %p, want instance tmux session %p", target, tmuxSess)
		}
		if tool != inst.Tool {
			t.Errorf("tool = %q, want %q", tool, inst.Tool)
		}
		if gotMessage != message {
			t.Errorf("message = %q, want %q", gotMessage, message)
		}
		if force {
			t.Error("self-heal must use force=false so the shared composer-draft refusal remains active")
		}
		if want := defaultSendTuning(); tuning != want {
			t.Errorf("tuning = %+v, want default %+v", tuning, want)
		}
		return sendDeliveryResult{delivery: deliveryTypedNotSubmitted}, wantErr
	}

	delivery, err := sendForSelfHeal(inst, message)
	if delivery != deliveryTypedNotSubmitted {
		t.Fatalf("delivery = %q, want wrapper to return %q verbatim", delivery, deliveryTypedNotSubmitted)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapper to return %v verbatim", err, wantErr)
	}
}
