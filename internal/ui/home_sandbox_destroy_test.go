package ui

// Sandbox lifecycle in the TUI: destroy a provider sandbox from its host
// row, see the sandbox kind on that row, and follow a "new sandbox" create
// through a progress line that stays visible and a failure said in plain
// words.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// destroyRecorder is a fake for the remote and provider calls a TUI
// destroy makes, recording them in order.
type destroyRecorder struct {
	mu       sync.Mutex
	calls    []string
	sessions []session.RemoteSessionInfo
	listErr  error
}

func (r *destroyRecorder) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *destroyRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *destroyRecorder) deps() *sandboxDestroyDeps {
	return &sandboxDestroyDeps{
		listSessions: func(_ context.Context, name string, _ session.RemoteConfig) ([]session.RemoteSessionInfo, error) {
			r.record("list " + name)
			return r.sessions, r.listErr
		},
		stopSession: func(_ context.Context, name string, _ session.RemoteConfig, id string) error {
			r.record("stop " + name + " " + id)
			return nil
		},
		destroy: func(_ context.Context, name string) error {
			r.record("destroy " + name)
			return nil
		},
	}
}

// sandboxHostHome is a Home whose list shows the host header of remote
// "sb1", a docker sandbox, with the given sessions known for it.
func sandboxHostHome(t *testing.T, known []session.RemoteSessionInfo) *Home {
	t.Helper()
	cfg := &session.UserConfig{Remotes: map[string]session.RemoteConfig{
		"sb1":   {Host: "agentdeck-sb-sb1", Provider: "docker", SandboxID: "abc123def456"},
		"plain": {Host: "dev@example.com"},
	}}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	items := []session.Item{
		{Type: session.ItemTypeRemoteGroup, RemoteName: "sb1", Path: "remotes/sb1", Level: 0},
		{Type: session.ItemTypeRemoteGroup, RemoteName: "plain", Path: "remotes/plain", Level: 0},
	}
	h := newTestHomeWithItems(120, 40, items)
	h.remoteSessions = map[string][]session.RemoteSessionInfo{"sb1": known}
	h.cursor = 0
	return h
}

func pressSandboxKey(h *Home, k string) (*Home, tea.Cmd) {
	model, cmd := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	return model.(*Home), cmd
}

func TestSandboxDestroy_DOnSandboxHostHeaderAsksAndCountsSessions(t *testing.T) {
	h := sandboxHostHome(t, []session.RemoteSessionInfo{
		{ID: "s1", Title: "api", Status: "running"},
		{ID: "s2", Title: "docs", Status: "waiting"},
	})
	h, _ = pressSandboxKey(h, "d")
	if !h.confirmDialog.IsVisible() || h.confirmDialog.GetConfirmType() != ConfirmDestroySandbox {
		t.Fatalf("d on a sandbox host header must open the destroy-sandbox confirm; visible=%v type=%v",
			h.confirmDialog.IsVisible(), h.confirmDialog.GetConfirmType())
	}
	view := stripAnsi(h.confirmDialog.View())
	for _, want := range []string{"Destroy Sandbox?", "sb1", "2 sessions", "will be stopped", "abc123def456"} {
		if !strings.Contains(view, want) {
			t.Fatalf("destroy confirm does not say %q:\n%s", want, view)
		}
	}

	// A plain SSH remote has no sandbox to destroy: d stays a no-op there.
	h.confirmDialog.Hide()
	h.cursor = 1
	h, _ = pressSandboxKey(h, "d")
	if h.confirmDialog.IsVisible() {
		t.Fatal("d on a plain remote's host header must not open any dialog")
	}
}

func TestSandboxDestroy_ConfirmStopsSessionsThenDestroys(t *testing.T) {
	h := sandboxHostHome(t, []session.RemoteSessionInfo{{ID: "s1", Title: "api", Status: "running"}})
	rec := &destroyRecorder{sessions: []session.RemoteSessionInfo{
		{ID: "s1", Title: "api", Status: "running"},
		{ID: "s0", Title: "old", Status: "stopped"},
	}}
	h.sandboxDestroyDeps = rec.deps()
	h, _ = pressSandboxKey(h, "d")
	h, cmd := pressSandboxKey(h, "y")
	if cmd == nil {
		t.Fatal("confirming the destroy returned no command")
	}
	if len(rec.got()) != 0 {
		t.Fatal("the destroy ran inside the key handler instead of a command")
	}
	if view := stripAnsi(h.View()); !strings.Contains(view, "destroying sandbox sb1") {
		t.Fatalf("no visible progress line while the sandbox is destroyed:\n%s", view)
	}
	h = drainCmd(t, h, cmd)

	want := []string{"list sb1", "stop sb1 s1", "destroy sb1"}
	if got := rec.got(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v (stopped sessions are not stopped again)", got, want)
	}
	if view := stripAnsi(h.View()); !strings.Contains(view, "destroyed sandbox sb1") {
		t.Fatalf("no visible result line after the destroy:\n%s", view)
	}
}

func TestSandboxDestroy_RefusesWhenMoreSessionsRunThanConfirmed(t *testing.T) {
	h := sandboxHostHome(t, []session.RemoteSessionInfo{{ID: "s1", Title: "api", Status: "running"}})
	rec := &destroyRecorder{sessions: []session.RemoteSessionInfo{
		{ID: "s1", Title: "api", Status: "running"},
		{ID: "s2", Title: "new", Status: "running"},
	}}
	h.sandboxDestroyDeps = rec.deps()
	h, _ = pressSandboxKey(h, "d")
	h, cmd := pressSandboxKey(h, "y")
	h = drainCmd(t, h, cmd)

	if got := rec.got(); len(got) != 1 || got[0] != "list sb1" {
		t.Fatalf("calls = %v, want only the list: nothing may be stopped or destroyed", got)
	}
	assertNotice(t, h, "Sandbox Not Destroyed", "2 sessions", "Nothing was changed")
}

func TestSandboxDestroy_RefusesWhenSessionsCannotBeListed(t *testing.T) {
	h := sandboxHostHome(t, nil)
	rec := &destroyRecorder{listErr: errors.New("ssh: connect to host: connection refused")}
	h.sandboxDestroyDeps = rec.deps()
	h, _ = pressSandboxKey(h, "d")
	h, cmd := pressSandboxKey(h, "y")
	h = drainCmd(t, h, cmd)

	if got := rec.got(); len(got) != 1 {
		t.Fatalf("calls = %v, want only the list", got)
	}
	assertNotice(t, h, "Sandbox Not Destroyed", "connection refused", "Nothing was changed", "--force")
}

func TestSandboxDestroy_CancelChangesNothing(t *testing.T) {
	h := sandboxHostHome(t, []session.RemoteSessionInfo{{ID: "s1", Status: "running"}})
	rec := &destroyRecorder{}
	h.sandboxDestroyDeps = rec.deps()
	h, _ = pressSandboxKey(h, "d")
	h, cmd := pressSandboxKey(h, "n")
	if cmd != nil {
		h = drainCmd(t, h, cmd)
	}
	if h.confirmDialog.IsVisible() || len(rec.got()) != 0 {
		t.Fatalf("cancel must close the dialog and call nothing; visible=%v calls=%v", h.confirmDialog.IsVisible(), rec.got())
	}
}

func TestSandboxHostRowShowsKind(t *testing.T) {
	h := sandboxHostHome(t, nil)
	h.remotePollConfig = map[string]session.RemoteConfig{
		"sb1":   {Host: "agentdeck-sb-sb1", Provider: "docker", SandboxID: "abc123def456"},
		"plain": {Host: "dev@example.com"},
	}
	var b strings.Builder
	h.renderRemoteGroupItem(&b, h.flatItems[0], false, 120)
	if row := stripAnsi(b.String()); !strings.Contains(row, "remotes/sb1") || !strings.Contains(row, "sandbox (provider docker)") {
		t.Fatalf("sandbox host row does not show its kind: %q", row)
	}
	b.Reset()
	h.renderRemoteGroupItem(&b, h.flatItems[1], false, 120)
	if row := stripAnsi(b.String()); strings.Contains(row, "sandbox") {
		t.Fatalf("plain remote row must not claim to be a sandbox: %q", row)
	}
}

// startBlockedProvision submits a "new sandbox (docker)" create whose
// provisioner reports one progress line and then waits for release.
func startBlockedProvision(t *testing.T, result error) (*Home, tea.Cmd, chan struct{}) {
	t.Helper()
	cfg := whereTestConfig(nil, "docker")
	h, _ := whereSubmitHome(t, cfg, 1) // choices: 0 this machine, 1 new sandbox (docker)
	h.initialLoading = false
	release := make(chan struct{})
	h.sandboxProvisioner = func(ctx context.Context, opts provider.ProvisionOptions) (*provider.Provisioned, error) {
		opts.Progress("Waiting for ssh...")
		<-release
		if result != nil {
			return nil, result
		}
		return &provider.Provisioned{RemoteName: "docker-abc123"}, nil
	}
	model, cmd := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	return model.(*Home), cmd, release
}

func TestSandboxProvision_ProgressStaysVisible(t *testing.T) {
	h, cmd, release := startBlockedProvision(t, nil)
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("provision command = %T, want a batch of run + progress reader", batch)
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- batch[0]() }()
	model, _ := h.Update(batch[1]()) // the "Waiting for ssh..." line
	h = model.(*Home)

	// The footer error line auto-dismisses after 5 s and sits below the
	// help bar; neither may hide the progress of a create that takes a
	// minute.
	h.clearError()
	view := stripAnsi(h.View())
	if !strings.Contains(view, "new sandbox (docker): Waiting for ssh...") {
		t.Fatalf("provisioning progress is not visible on screen:\n%s", view)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("provisioning did not finish")
	}
}

func TestSandboxProvision_FailureIsANoticeInPlainWords(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want []string
	}{
		{"rolled back", fmt.Errorf("waiting for ssh on sandbox abc: timed out (sandbox abc destroyed)"),
			[]string{"New Sandbox Failed", "docker", "Waiting for ssh", "timed out", "Nothing was left behind"}},
		{"rollback failed", fmt.Errorf("waiting for ssh on sandbox abc: timed out; rollback failed: destroying sandbox abc: boom (destroy it by hand)"),
			[]string{"New Sandbox Failed", "boom", "may still exist"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, cmd, release := startBlockedProvision(t, tt.err)
			close(release)
			h = drainCmd(t, h, cmd)
			assertNotice(t, h, tt.want...)
			if view := stripAnsi(h.View()); strings.Contains(view, "Waiting for ssh...  (") {
				t.Fatalf("progress line still shown after the failure:\n%s", view)
			}
		})
	}
}

func TestSandboxInstallStepReportsProgress(t *testing.T) {
	var lines []string
	opts := withInstallProgress(provider.ProvisionOptions{Progress: func(s string) { lines = append(lines, s) }},
		func(context.Context, string, session.RemoteConfig) error { return nil })
	if err := opts.Install(context.Background(), "sb1", session.RemoteConfig{}); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "Installing agent-deck") {
		t.Fatalf("install step progress = %q, want one \"Installing agent-deck\" line", lines)
	}
}

func assertNotice(t *testing.T, h *Home, want ...string) {
	t.Helper()
	if !h.confirmDialog.IsVisible() || h.confirmDialog.GetConfirmType() != ConfirmNotice {
		t.Fatalf("want a notice dialog; visible=%v type=%v", h.confirmDialog.IsVisible(), h.confirmDialog.GetConfirmType())
	}
	view := stripAnsi(h.confirmDialog.View())
	// Join wrapped lines: drop the box border and collapse whitespace.
	flat := strings.Join(strings.Fields(strings.NewReplacer("│", " ", "╭", " ", "╰", " ", "─", " ", "╮", " ", "╯", " ").Replace(view)), " ")
	for _, w := range want {
		if !strings.Contains(flat, w) {
			t.Fatalf("notice does not say %q:\n%s", w, view)
		}
	}
}
