package ui

// Submitting the new-session dialog with Where = a remote or a new sandbox.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

type provisionCapture struct {
	mu    sync.Mutex
	calls []provider.ProvisionOptions
}

func (p *provisionCapture) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// drainCmd runs cmd and every command its messages produce through h.Update,
// as Bubble Tea would, up to a bound.
func drainCmd(t *testing.T, h *Home, cmd tea.Cmd) *Home {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 50 {
			t.Fatal("command chain did not settle")
		}
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		msg := next()
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		if msg == nil {
			continue
		}
		model, follow := h.Update(msg)
		h = model.(*Home)
		queue = append(queue, follow)
	}
	return h
}

func whereSubmitHome(t *testing.T, cfg *session.UserConfig, choose int) (*Home, *remoteCreateCapture) {
	t.Helper()
	h := NewHome()
	h.width, h.height = 120, 50
	capture := &remoteCreateCapture{}
	h.remoteCreateSink = capture.sink
	h.newDialog.SetSize(120, 50)
	h.newDialog.Show()
	h.newDialog.SetWhereChoices(newDialogWhereChoices(cfg))
	h.newDialog.nameInput.SetValue("demo")
	h.newDialog.pathInput.SetValue("/srv/work")
	h.newDialog.whereCursor = choose
	return h, capture
}

func TestNewDialogWhereSubmit(t *testing.T) {
	cfg := whereTestConfig(map[string]session.RemoteConfig{"build": {Host: "b"}}, "docker")
	// choices: 0 this machine, 1 build, 2 new sandbox (docker)
	tests := []struct {
		name          string
		choose        int
		provisionErr  error
		wantProvision bool
		wantRemote    string // "" = nothing created on a remote
		wantErrSubstr string
	}{
		{name: "existing remote uses the remote create path", choose: 1, wantRemote: "build"},
		{name: "new sandbox provisions then creates on it", choose: 2, wantProvision: true, wantRemote: "docker-abc123"},
		{name: "provisioning error is shown and nothing is created", choose: 2, provisionErr: errors.New("create failed: no capacity"),
			wantProvision: true, wantErrSubstr: "no capacity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, capture := whereSubmitHome(t, cfg, tt.choose)
			h.profile = "work"
			prov := &provisionCapture{}
			h.sandboxProvisioner = func(ctx context.Context, opts provider.ProvisionOptions) (*provider.Provisioned, error) {
				prov.mu.Lock()
				prov.calls = append(prov.calls, opts)
				prov.mu.Unlock()
				if opts.Progress != nil {
					opts.Progress("Creating sandbox...")
				}
				if tt.provisionErr != nil {
					return nil, tt.provisionErr
				}
				return &provider.Provisioned{RemoteName: "docker-abc123"}, nil
			}

			model, cmd := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyCtrlS})
			h = model.(*Home)
			// Provisioning never runs on the UI loop: the key handler only
			// returns a command.
			if prov.count() != 0 {
				t.Fatal("provisioning ran synchronously inside the key handler")
			}
			if h.newDialog.IsVisible() {
				t.Fatal("dialog stayed open after submit")
			}
			if tt.wantProvision && h.err == nil {
				t.Fatal("no status line while the sandbox is being provisioned")
			}
			h = drainCmd(t, h, cmd)

			if got := prov.count() > 0; got != tt.wantProvision {
				t.Fatalf("provision called = %v, want %v", got, tt.wantProvision)
			}
			if tt.wantProvision {
				if p := prov.calls[0]; p.Provider != "docker" || p.Profile != "work" {
					t.Fatalf("provision opts = %+v, want provider docker, profile work", p)
				}
			}
			if tt.wantRemote == "" {
				if capture.calls != 0 {
					t.Fatalf("a session was created on %q after a provisioning error", capture.remoteName)
				}
			} else {
				if capture.calls != 1 || capture.remoteName != tt.wantRemote {
					t.Fatalf("remote create calls=%d remote=%q, want 1 on %q", capture.calls, capture.remoteName, tt.wantRemote)
				}
				if capture.opts.Title != "demo" || capture.opts.Path != "/srv/work" {
					t.Fatalf("forwarded opts = %+v, want title demo and path /srv/work", capture.opts)
				}
			}
			if tt.wantErrSubstr != "" {
				if h.err == nil || !strings.Contains(h.err.Error(), tt.wantErrSubstr) {
					t.Fatalf("footer error = %v, want it to mention %q", h.err, tt.wantErrSubstr)
				}
			}
		})
	}
}

func TestRemoteHostLabel(t *testing.T) {
	tests := []struct {
		rc   session.RemoteConfig
		want string
	}{
		{session.RemoteConfig{Host: "user@box"}, "user@box"},
		{session.RemoteConfig{Host: "agentdeck-sb-x", Provider: "docker", SandboxID: "id1"}, "sandbox (provider docker)"},
	}
	for _, tt := range tests {
		if got := remoteHostLabel(tt.rc); got != tt.want {
			t.Fatalf("remoteHostLabel(%+v) = %q, want %q", tt.rc, got, tt.want)
		}
	}
}

// ASK-110 x ASK-120: Enter creates from the Where row too (default mode),
// and its footer says so.
func TestNewDialogWhereRow_EnterCreates(t *testing.T) {
	cfg := whereTestConfig(map[string]session.RemoteConfig{"build": {Host: "b"}}, "docker")
	for _, choose := range []int{0, 1} {
		h, capture := whereSubmitHome(t, cfg, choose)
		h.newDialog.pathInput.SetValue(t.TempDir())
		flowFocus(t, h.newDialog, focusWhere)
		if view := stripAnsi(h.newDialog.View()); !strings.Contains(view, "choose where") || !strings.Contains(view, "Enter create") {
			t.Fatalf("choose=%d: Where row footer should offer Enter create:\n%s", choose, view)
		}
		model, cmd := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
		h = model.(*Home)
		// The remote sink returns no command, so a remote create is seen
		// through the capture; a local create returns its create command.
		if h.newDialog.IsVisible() || (choose == 0 && cmd == nil) {
			t.Fatalf("choose=%d: Enter on the Where row did not create (error %q)", choose, h.newDialog.validationErr)
		}
		if choose == 1 {
			if capture.calls != 1 || capture.remoteName != "build" {
				t.Fatalf("remote create calls=%d remote=%q, want 1 on build", capture.calls, capture.remoteName)
			}
		}
	}
}

// ASK-110's folder-name default is local only: a remote or new sandbox
// picked in the Where row still needs a typed name, and the empty field does
// not preview the folder name for it.
func TestNewDialogWhereRow_OffMachineNeedsName(t *testing.T) {
	cfg := whereTestConfig(map[string]session.RemoteConfig{"build": {Host: "b"}}, "docker")
	for _, choose := range []int{1, 2} {
		h, capture := whereSubmitHome(t, cfg, choose)
		h.newDialog.nameInput.SetValue("")
		prov := &provisionCapture{}
		h.sandboxProvisioner = func(ctx context.Context, opts provider.ProvisionOptions) (*provider.Provisioned, error) {
			prov.mu.Lock()
			prov.calls = append(prov.calls, opts)
			prov.mu.Unlock()
			return &provider.Provisioned{RemoteName: "docker-abc123"}, nil
		}
		_ = h.newDialog.View()
		if got := h.newDialog.nameInput.Placeholder; got != "session-name" {
			t.Fatalf("choose=%d: empty name previews %q for an off-machine create, want session-name", choose, got)
		}
		model, cmd := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
		h = model.(*Home)
		if cmd != nil {
			h = drainCmd(t, h, cmd)
		}
		if !h.newDialog.IsVisible() || capture.calls != 0 || prov.count() != 0 {
			t.Fatalf("choose=%d: empty name created off-machine (visible=%v creates=%d provisions=%d)",
				choose, h.newDialog.IsVisible(), capture.calls, prov.count())
		}
		if !strings.Contains(h.newDialog.validationErr, "name cannot be empty") || h.newDialog.currentTarget() != focusName {
			t.Fatalf("choose=%d: error %q focus %v, want the name error on the Name row",
				choose, h.newDialog.validationErr, h.newDialog.currentTarget())
		}
	}
}
