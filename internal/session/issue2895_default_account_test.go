// Issue #2895 — [claude].default_account: the account slot new Claude
// sessions start on until it is changed.
//
// The default is a creation-time prefill, not a level of the config-dir
// chain: it is copied into Instance.Account when a session is created, so
// changing it never moves an existing session. A group or conductor
// config_dir the user scoped more narrowly keeps winning, and a slot that is
// no longer configured falls back to inherit instead of failing the create.
//
// Suggested by @godlen4332.
package session

import (
	"path/filepath"
	"testing"
)

func TestDefaultAccountForNewSession(t *testing.T) {
	slots := map[string]ProfileSettings{
		"ai":   {Claude: ProfileClaudeSettings{ConfigDir: "/tmp/claude-ai"}},
		"work": {Claude: ProfileClaudeSettings{ConfigDir: "/tmp/claude-work"}},
	}
	opsConductor := &Instance{ID: "c1", Title: "conductor-ops", IsConductor: true, ConductorName: "ops"}
	plainParent := &Instance{ID: "p1", Title: "worker"}

	tests := []struct {
		name      string
		cfg       *UserConfig
		groupPath string
		parent    *Instance
		want      string
	}{
		{name: "no config", cfg: nil, want: ""},
		{name: "unset keeps inherit", cfg: &UserConfig{Profiles: slots}, want: ""},
		{name: "configured slot", cfg: &UserConfig{Profiles: slots, Claude: ClaudeSettings{DefaultAccount: "work"}}, want: "work"},
		{name: "surrounding space trimmed", cfg: &UserConfig{Profiles: slots, Claude: ClaudeSettings{DefaultAccount: " work "}}, want: "work"},
		{name: "removed slot falls back to inherit", cfg: &UserConfig{Profiles: slots, Claude: ClaudeSettings{DefaultAccount: "gone"}}, want: ""},
		{
			name:      "group config_dir wins",
			cfg:       &UserConfig{Profiles: slots, Claude: ClaudeSettings{DefaultAccount: "work"}, Groups: map[string]GroupSettings{"pinned": {Claude: GroupClaudeSettings{ConfigDir: "/tmp/claude-pinned"}}}},
			groupPath: "pinned",
			want:      "",
		},
		{
			name:      "parent group config_dir wins for a subgroup",
			cfg:       &UserConfig{Profiles: slots, Claude: ClaudeSettings{DefaultAccount: "work"}, Groups: map[string]GroupSettings{"pinned": {Claude: GroupClaudeSettings{ConfigDir: "/tmp/claude-pinned"}}}},
			groupPath: "pinned/child",
			want:      "",
		},
		{
			name:      "group without config_dir keeps the default",
			cfg:       &UserConfig{Profiles: slots, Claude: ClaudeSettings{DefaultAccount: "work"}},
			groupPath: "projects",
			want:      "work",
		},
		{
			name:   "conductor parent config_dir wins",
			cfg:    &UserConfig{Profiles: slots, Claude: ClaudeSettings{DefaultAccount: "work"}, Conductors: map[string]ConductorOverrides{"ops": {Claude: ConductorClaudeSettings{ConfigDir: "/tmp/claude-ops"}}}},
			parent: opsConductor,
			want:   "",
		},
		{
			name:   "conductor parent without config_dir keeps the default",
			cfg:    &UserConfig{Profiles: slots, Claude: ClaudeSettings{DefaultAccount: "work"}},
			parent: opsConductor,
			want:   "work",
		},
		{
			name:   "non-conductor parent keeps the default",
			cfg:    &UserConfig{Profiles: slots, Claude: ClaudeSettings{DefaultAccount: "work"}},
			parent: plainParent,
			want:   "work",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultAccountForNewSession(tt.cfg, tt.groupPath, tt.parent); got != tt.want {
				t.Fatalf("DefaultAccountForNewSession() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Changing the default must never move a session that already exists: an
// "inherit" session keeps resolving through the conductor/group/env chain and
// a session stored on a slot keeps that slot. Moving one is switch-account's
// job, because the conversation has to move with it.
func TestDefaultAccountChangeLeavesExistingSessionsUntouched(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[claude]
default_account = "work"

[profiles.ai.claude]
config_dir = "~/.claude-ai"

[profiles.work.claude]
config_dir = "~/.claude-work"
`)

	inherit := NewInstanceWithTool("inherit-session", home, "claude")
	inherit.Account = ""
	onAI := NewInstanceWithTool("ai-session", home, "claude")
	onAI.Account = "ai"

	path, source := GetClaudeConfigDirSourceForInstance(inherit)
	if source == "account" || path == filepath.Join(home, ".claude-work") {
		t.Fatalf("inherit session resolved to %q (source %q); default_account must not be a resolver level", path, source)
	}
	if inherit.Account != "" {
		t.Fatalf("inherit session account = %q, want it untouched", inherit.Account)
	}
	if path, source := GetClaudeConfigDirSourceForInstance(onAI); source != "account" || path != filepath.Join(home, ".claude-ai") {
		t.Fatalf("ai session resolved to %q (source %q), want its own slot", path, source)
	}
	if onAI.Account != "ai" {
		t.Fatalf("ai session account = %q, want ai", onAI.Account)
	}
}
