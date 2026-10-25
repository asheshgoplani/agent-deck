package ui

// New Session dialog "Where" row: this machine (default), each configured
// remote, and "new sandbox (<provider>)" for each configured provider.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func whereTestConfig(remotes map[string]session.RemoteConfig, providers ...string) *session.UserConfig {
	cfg := &session.UserConfig{Remotes: remotes}
	if len(providers) > 0 {
		cfg.Providers = map[string]session.ProviderConfig{}
		for _, p := range providers {
			cfg.Providers[p] = session.ProviderConfig{Command: "/bin/true"}
		}
	}
	return cfg
}

func whereLabels(choices []whereChoice) []string {
	out := make([]string, 0, len(choices))
	for _, c := range choices {
		out = append(out, c.label)
	}
	return out
}

func TestNewDialogWhereChoicesFromConfig(t *testing.T) {
	sandboxRemote := session.RemoteConfig{Host: "agentdeck-sb-sb1", Provider: "docker", SandboxID: "c0ffee"}
	tests := []struct {
		name string
		cfg  *session.UserConfig
		want []string
	}{
		{name: "nil config", cfg: nil, want: []string{"this machine"}},
		{name: "nothing configured", cfg: whereTestConfig(nil), want: []string{"this machine"}},
		{
			name: "remotes only, no sandbox entries",
			cfg:  whereTestConfig(map[string]session.RemoteConfig{"zeta": {Host: "z"}, "build": {Host: "b"}}),
			want: []string{"this machine", "build", "zeta"},
		},
		{
			name: "providers only",
			cfg:  whereTestConfig(nil, "fake", "docker"),
			want: []string{"this machine", "new sandbox (docker)", "new sandbox (fake)"},
		},
		{
			name: "sandbox remote labelled with its provider",
			cfg:  whereTestConfig(map[string]session.RemoteConfig{"build": {Host: "b"}, "sb1": sandboxRemote}, "docker"),
			want: []string{"this machine", "build", "sb1 · sandbox (provider docker)", "new sandbox (docker)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := whereLabels(newDialogWhereChoices(tt.cfg))
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("choices = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewDialogWhereRow(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *session.UserConfig
		wantRow bool
	}{
		{name: "nothing configured hides the row", cfg: whereTestConfig(nil), wantRow: false},
		{name: "a remote shows the row", cfg: whereTestConfig(map[string]session.RemoteConfig{"build": {Host: "b"}}), wantRow: true},
		{name: "a provider shows the row", cfg: whereTestConfig(nil, "docker"), wantRow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := flowDialog(t, "shell")
			d.SetWhereChoices(newDialogWhereChoices(tt.cfg))
			if got := d.indexOf(focusWhere) >= 0; got != tt.wantRow {
				t.Fatalf("Where row focusable = %v, want %v", got, tt.wantRow)
			}
			if got := strings.Contains(d.View(), "Where:"); got != tt.wantRow {
				t.Fatalf("Where row rendered = %v, want %v", got, tt.wantRow)
			}
			// Default stays this machine whatever is configured.
			if w := d.GetWhere(); w.kind != whereLocal {
				t.Fatalf("default Where = %+v, want this machine", w)
			}
		})
	}
}

func TestNewDialogWhereCycles(t *testing.T) {
	d := flowDialog(t, "shell")
	d.SetWhereChoices(newDialogWhereChoices(whereTestConfig(map[string]session.RemoteConfig{"build": {Host: "b"}}, "docker")))
	flowFocus(t, d, focusWhere)

	steps := []struct {
		key  tea.KeyMsg
		kind whereKind
		name string
	}{
		{tea.KeyMsg{Type: tea.KeyRight}, whereRemote, "build"},
		{tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}, whereSandbox, "docker"},
		{tea.KeyMsg{Type: tea.KeyRight}, whereLocal, ""},
		{tea.KeyMsg{Type: tea.KeyLeft}, whereSandbox, "docker"},
	}
	for i, s := range steps {
		d, _ = d.Update(s.key)
		if w := d.GetWhere(); w.kind != s.kind || w.name != s.name {
			t.Fatalf("step %d (%s): Where = %+v, want kind %v name %q", i, s.key.String(), w, s.kind, s.name)
		}
		if d.currentTarget() != focusWhere {
			t.Fatalf("step %d: focus left the Where row", i)
		}
	}
	if !strings.Contains(d.View(), "new sandbox (docker)") {
		t.Fatal("selected sandbox choice not rendered")
	}

	// Reopening resets to this machine and drops the choices until the
	// caller sets them again.
	d.Show()
	if w := d.GetWhere(); w.kind != whereLocal {
		t.Fatalf("Where after reopen = %+v, want this machine", w)
	}
	if d.indexOf(focusWhere) >= 0 {
		t.Fatal("Where row survived a reopen without SetWhereChoices")
	}
}

func TestNewDialogWhereHiddenForRemoteTarget(t *testing.T) {
	d := flowDialog(t, "shell")
	d.SetWhereChoices(newDialogWhereChoices(whereTestConfig(nil, "docker")))
	d.ResetRemoteDefaults()
	if d.indexOf(focusWhere) >= 0 {
		t.Fatal("a dialog opened on a remote must not offer the Where row")
	}
	if w := d.GetWhere(); w.kind != whereLocal {
		t.Fatalf("remote-target Where = %+v, want local (the remote path owns the target)", w)
	}
}
