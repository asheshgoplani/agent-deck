package ui

// Sandbox lifecycle in the TUI: destroy a provider sandbox from its host
// row, see the sandbox kind on that row, and follow a "new sandbox" create
// through a progress line that stays visible and a failure said in plain
// words.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	// Deliver the result, but not the follow-up remote fetch it asks for:
	// that would reach real ssh for the fake hosts.
	result, ok := cmd().(sandboxDestroyedMsg)
	if !ok {
		t.Fatal("the destroy command did not report a sandboxDestroyedMsg")
	}
	model, refetch := h.Update(result)
	h = model.(*Home)
	if refetch == nil {
		t.Fatal("a finished destroy must refetch the remotes so the rows drop out")
	}

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

// holdSandboxLock takes remoteName's sandbox lock the way another
// agent-deck process holds it (an flock on its own open of the lock file)
// until the test ends.
func holdSandboxLock(t *testing.T, remoteName string) {
	t.Helper()
	dir, err := provider.SandboxDir(remoteName)
	if err != nil {
		t.Fatal(err)
	}
	locks := filepath.Join(filepath.Dir(dir), ".locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(locks, remoteName+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
}

// While a destroy of a sandbox runs, a second one of the same remote is
// refused: d shows a notice instead of a second confirm, and a destroy
// command started anyway changes nothing.
func TestSandboxDestroy_SecondDestroyOfTheSameRemoteIsRefused(t *testing.T) {
	h := sandboxHostHome(t, nil)
	rec := &destroyRecorder{}
	h.sandboxDestroyDeps = rec.deps()
	h, _ = pressSandboxKey(h, "d")
	h, first := pressSandboxKey(h, "y")
	if first == nil {
		t.Fatal("confirming the destroy returned no command")
	}
	// The first destroy has not finished (its command has not run).
	h, _ = pressSandboxKey(h, "d")
	if h.confirmDialog.IsVisible() && h.confirmDialog.GetConfirmType() == ConfirmDestroySandbox {
		t.Fatal("d opened a second destroy confirm while the first destroy runs")
	}
	assertNotice(t, h, "sb1", "already being destroyed")

	second, ok := h.destroySandboxCmd("sb1", "abc123def456", 0)().(sandboxDestroyedMsg)
	if !ok || !second.refused || second.err == nil || !strings.Contains(second.err.Error(), "already being destroyed") {
		t.Fatalf("second destroy = %+v, want a refusal saying it is already being destroyed", second)
	}
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("calls = %v before the first destroy ran: the second one must call nothing", got)
	}
	if _, ok := first().(sandboxDestroyedMsg); !ok {
		t.Fatal("the first destroy did not report a sandboxDestroyedMsg")
	}
	if got := rec.got(); strings.Join(got, "|") != "list sb1|destroy sb1" {
		t.Fatalf("calls = %v, want one list and one destroy", got)
	}
}

// Another agent-deck (a CLI `remote destroy`, or a create of the same
// name) holds the remote: the TUI destroy is refused and changes nothing.
func TestSandboxDestroy_RefusedWhileAnotherAgentDeckHoldsTheRemote(t *testing.T) {
	h := sandboxHostHome(t, []session.RemoteSessionInfo{{ID: "s1", Status: "running"}})
	rec := &destroyRecorder{sessions: []session.RemoteSessionInfo{{ID: "s1", Status: "running"}}}
	h.sandboxDestroyDeps = rec.deps()
	holdSandboxLock(t, "sb1")
	h, _ = pressSandboxKey(h, "d")
	h, cmd := pressSandboxKey(h, "y")
	h = drainCmd(t, h, cmd)
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("calls = %v, want none while another agent-deck holds the remote", got)
	}
	assertNotice(t, h, "Sandbox Not Destroyed", "busy", "Nothing was changed")
}

// The user confirmed destroying sandbox abc123def456. If the remote points
// at another sandbox by the time the destroy runs (destroyed and made again
// under the same name), nothing is changed.
func TestSandboxDestroy_RefusesWhenTheSandboxIsNotTheConfirmedOne(t *testing.T) {
	h := sandboxHostHome(t, nil)
	rec := &destroyRecorder{}
	h.sandboxDestroyDeps = rec.deps()
	h, _ = pressSandboxKey(h, "d")
	if err := session.SaveUserConfig(&session.UserConfig{Remotes: map[string]session.RemoteConfig{
		"sb1": {Host: "agentdeck-sb-sb1", Provider: "docker", SandboxID: "fff999eee888"},
	}}); err != nil {
		t.Fatal(err)
	}
	h, cmd := pressSandboxKey(h, "y")
	h = drainCmd(t, h, cmd)
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("calls = %v, want none: the remote is not the sandbox the user confirmed", got)
	}
	assertNotice(t, h, "Sandbox Not Destroyed", "abc123def456", "fff999eee888", "Nothing was changed")
}

// The provider destroyed the sandbox but agent-deck could not finish its
// local cleanup: the notice says the sandbox is gone, not that it is still
// there.
func TestSandboxDestroy_NoticeSaysSandboxGoneWhenOnlyLocalCleanupFailed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores the read-only dir this test relies on")
	}
	fake, err := filepath.Abs(filepath.Join("..", "provider", "testdata", "fake-provider.sh"))
	if err != nil {
		t.Fatal(err)
	}
	h := sandboxHostHome(t, nil)
	dir, err := provider.SandboxDir("sb1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sshConfig := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(sshConfig, []byte("Host x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveUserConfig(&session.UserConfig{
		Providers: map[string]session.ProviderConfig{"fake": {Command: fake, TimeoutSeconds: 10}},
		Remotes: map[string]session.RemoteConfig{
			"sb1": {Host: "agentdeck-sb-sb1", Provider: "fake", SandboxID: "abc123def456", SSHConfig: sshConfig},
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PROVIDER_STATE", t.TempDir())
	t.Setenv("FAKE_PROVIDER_MODE", "ok")
	t.Setenv("FAKE_PROVIDER_DESTROY", "ok")
	// The sandbox dir cannot be removed: the local cleanup fails after the
	// provider destroyed the sandbox.
	if err := os.Chmod(filepath.Dir(dir), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(dir), 0o700) })

	rec := &destroyRecorder{}
	deps := rec.deps()
	deps.destroy = provider.DestroyRemote
	h.sandboxDestroyDeps = deps
	h, _ = pressSandboxKey(h, "d")
	h, cmd := pressSandboxKey(h, "y")
	result, ok := cmd().(sandboxDestroyedMsg)
	if !ok {
		t.Fatal("the destroy command did not report a sandboxDestroyedMsg")
	}
	model, _ := h.Update(result)
	h = model.(*Home)
	assertNotice(t, h, "Sandbox Not Destroyed", "The sandbox itself was destroyed")
	if view := stripAnsi(h.confirmDialog.View()); strings.Contains(view, "are still there") {
		t.Fatalf("the notice says the sandbox is still there although it was destroyed:\n%s", view)
	}
}
