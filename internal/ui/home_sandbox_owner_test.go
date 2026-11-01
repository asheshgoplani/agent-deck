package ui

// ASK-172: whose sandbox it is shows on its host row and in its preview,
// and a start prompt typed in the New Session dialog reaches a session on
// a new sandbox (or a remote picked in the Where row).

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func ownedSandboxHome(t *testing.T) *Home {
	t.Helper()
	remotes := map[string]session.RemoteConfig{
		"sb1":   {Host: "agentdeck-sb-sb1", Provider: "docker", SandboxID: "abc123def456", Owner: "alice"},
		"plain": {Host: "dev@example.com"},
	}
	if err := session.SaveUserConfig(&session.UserConfig{Remotes: remotes}); err != nil {
		t.Fatal(err)
	}
	items := []session.Item{
		{Type: session.ItemTypeRemoteGroup, RemoteName: "sb1", Path: "remotes/sb1", Level: 0},
		{Type: session.ItemTypeRemoteGroup, RemoteName: "plain", Path: "remotes/plain", Level: 0},
	}
	h := newTestHomeWithItems(120, 40, items)
	h.remotePollConfig = remotes
	h.cursor = 0
	return h
}

func TestSandboxHostRowShowsOwner(t *testing.T) {
	h := ownedSandboxHome(t)
	var b strings.Builder
	h.renderRemoteGroupItem(&b, h.flatItems[0], false, 140)
	if row := stripAnsi(b.String()); !strings.Contains(row, "sandbox (provider docker)") || !strings.Contains(row, "owner: alice") {
		t.Fatalf("sandbox host row does not show its owner: %q", row)
	}
	// A narrow list cuts the row; the owner must survive the cut.
	b.Reset()
	h.renderRemoteGroupItem(&b, h.flatItems[0], false, 56)
	if row := stripAnsi(b.String()); !strings.Contains(row, "owner: alice") {
		t.Fatalf("narrow sandbox host row lost its owner: %q", row)
	}
	b.Reset()
	h.renderRemoteGroupItem(&b, h.flatItems[1], false, 140)
	if row := stripAnsi(b.String()); strings.Contains(row, "owner") {
		t.Fatalf("plain remote row must not show an owner: %q", row)
	}
}

func TestSandboxPreviewShowsOwner(t *testing.T) {
	h := ownedSandboxHome(t)
	if out := stripAnsi(h.renderRemotePreview(h.flatItems[0], 100, 30)); !strings.Contains(out, "owner: alice") {
		t.Fatalf("sandbox preview does not show its owner:\n%s", out)
	}
	if out := stripAnsi(h.renderRemotePreview(h.flatItems[1], 100, 30)); strings.Contains(out, "owner") {
		t.Fatalf("plain remote preview must not show an owner:\n%s", out)
	}
}

// A start prompt is typed by the user, never filled from this machine's
// config, so it travels with a Where pick (#2918 keeps the rest at the
// remote's defaults). Only a Claude session has one.
func TestNewDialogWhereSubmit_CarriesTypedStartPrompt(t *testing.T) {
	cfg := whereTestConfig(map[string]session.RemoteConfig{"build": {Host: "b"}}, "docker")
	tests := []struct {
		name   string
		choose int // 1 build, 2 new sandbox (docker)
		claude bool
		query  string
		want   string
	}{
		{name: "new sandbox gets the typed prompt", choose: 2, claude: true, query: "run the tests and fix the failing one", want: "run the tests and fix the failing one"},
		{name: "existing remote gets the typed prompt", choose: 1, claude: true, query: "explain this repo", want: "explain this repo"},
		{name: "no prompt typed sends none", choose: 2, claude: true, query: "", want: ""},
		{name: "a shell session has no prompt", choose: 2, claude: false, query: "ignored", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, capture := whereSubmitHome(t, cfg, tt.choose)
			h.sandboxProvisioner = func(context.Context, provider.ProvisionOptions) (*provider.Provisioned, error) {
				return &provider.Provisioned{RemoteName: "docker-abc123"}, nil
			}
			if tt.claude {
				h.newDialog.commandCursor = 1 // claude
			} else {
				h.newDialog.commandCursor = 0 // shell
			}
			h.newDialog.updateToolOptions()
			h.newDialog.claudeOptions.SetStartQuery(tt.query)

			model, cmd := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyCtrlS})
			h = drainCmd(t, model.(*Home), cmd)
			if capture.calls != 1 {
				t.Fatalf("remote create calls = %d, want 1 (dialog error %q)", capture.calls, h.newDialog.validationErr)
			}
			if capture.opts.StartQuery != tt.want {
				t.Fatalf("StartQuery = %q, want %q", capture.opts.StartQuery, tt.want)
			}
		})
	}
}

// For a Claude session the Where note says the start prompt travels; the
// rest still comes from the remote's defaults.
func TestNewDialogWhereRow_NoteSaysStartPromptTravels(t *testing.T) {
	cfg := whereTestConfig(nil, "docker")
	h, _ := whereSubmitHome(t, cfg, 1)
	h.newDialog.commandCursor = 1 // claude
	h.newDialog.updateToolOptions()
	view := stripAnsi(h.newDialog.View())
	if !strings.Contains(view, "Start prompt: sent") || !strings.Contains(view, "remote's own defaults") {
		t.Fatalf("Where note does not say the start prompt travels:\n%s", view)
	}
}
