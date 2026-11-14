package ui

// #2918 follow-ups: what a Where pick cannot carry is said in the dialog,
// and quitting the TUI waits for a sandbox provisioning to clean up.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Multi-repo and the Docker sandbox checkbox cannot travel to a remote or a
// new sandbox: submit refuses while the dialog is still open, instead of
// creating the session without them.
func TestNewDialogWhereSubmit_RefusesWhatCannotTravel(t *testing.T) {
	cfg := whereTestConfig(map[string]session.RemoteConfig{"build": {Host: "b"}}, "docker")
	for _, choose := range []int{1, 2} {
		for _, field := range []string{"docker", "multi-repo"} {
			h, capture := whereSubmitHome(t, cfg, choose)
			prov := &provisionCapture{}
			h.sandboxProvisioner = func(ctx context.Context, opts provider.ProvisionOptions) (*provider.Provisioned, error) {
				prov.mu.Lock()
				prov.calls = append(prov.calls, opts)
				prov.mu.Unlock()
				return &provider.Provisioned{RemoteName: "docker-abc123"}, nil
			}
			want := "Docker sandbox"
			if field == "docker" {
				h.newDialog.sandboxEnabled = true
			} else {
				h.newDialog.multiRepoEnabled = true
				h.newDialog.multiRepoPaths = []string{"/srv/a", "/srv/b"}
				want = "Multi-repo"
			}
			model, cmd := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyCtrlS})
			h = model.(*Home)
			if cmd != nil {
				h = drainCmd(t, h, cmd)
			}
			if !h.newDialog.IsVisible() || capture.calls != 0 || prov.count() != 0 {
				t.Fatalf("choose=%d %s: submitted anyway (visible=%v creates=%d provisions=%d)",
					choose, field, h.newDialog.IsVisible(), capture.calls, prov.count())
			}
			if !strings.Contains(h.newDialog.validationErr, want) {
				t.Fatalf("choose=%d %s: dialog error %q, want it to mention %q", choose, field, h.newDialog.validationErr, want)
			}
		}
	}
}

// The model, effort and Claude options are not carried (the remote applies
// its own defaults); the dialog says so when a remote or sandbox is picked.
func TestNewDialogWhereRow_SaysRemoteDefaultsApply(t *testing.T) {
	cfg := whereTestConfig(map[string]session.RemoteConfig{"build": {Host: "b"}}, "docker")
	const note = "remote's own defaults"
	for choose, want := range map[int]bool{0: false, 1: true, 2: true} {
		h, _ := whereSubmitHome(t, cfg, choose)
		view := stripAnsi(h.newDialog.View())
		if got := strings.Contains(view, note); got != want {
			t.Fatalf("choose=%d: note shown = %v, want %v:\n%s", choose, got, want, view)
		}
	}
}

// Quitting while a sandbox is being provisioned cancels it and waits for
// the rollback, so the sandbox is not left running.
func TestHomeWaitsForSandboxProvisioningOnQuit(t *testing.T) {
	h := NewHome()
	var started, rolledBack atomic.Bool
	h.sandboxProvisioner = func(ctx context.Context, opts provider.ProvisionOptions) (*provider.Provisioned, error) {
		started.Store(true)
		<-ctx.Done()
		time.Sleep(300 * time.Millisecond) // the rollback's destroy
		rolledBack.Store(true)
		return nil, ctx.Err()
	}
	batch, ok := h.provisionSandboxCmd("docker", session.RemoteAddOptions{})().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatal("provisionSandboxCmd did not return a batch")
	}
	go batch[0]()
	deadline := time.Now().Add(5 * time.Second)
	for !started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("provisioning did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.cancel()
	if !h.waitSandboxProvisions(10 * time.Second) {
		t.Fatal("waitSandboxProvisions timed out")
	}
	if !rolledBack.Load() {
		t.Fatal("quit returned before the provisioning finished its rollback")
	}
}
