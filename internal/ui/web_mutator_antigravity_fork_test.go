package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The web fork endpoint goes through WebMutator.ForkSession, whose dispatch
// must refuse Antigravity sessions that cannot fork before spawning anything:
// an unbound session has no conversation, and an SSH or sandboxed parent would
// run the child on the controller.
func TestWebMutatorForkSessionRejectsUnforkableAntigravity(t *testing.T) {
	bind := func(inst *session.Instance) {
		inst.AntigravitySessionID = "conv-web-parent"
		inst.AntigravityDetectedAt = time.Now()
	}
	for _, tt := range []struct {
		name   string
		mutate func(*session.Instance)
	}{
		{"unbound", func(*session.Instance) {}},
		{"ssh", func(inst *session.Instance) {
			bind(inst)
			inst.SSHHost = "devbox"
		}},
		{"sandbox", func(inst *session.Instance) {
			bind(inst)
			inst.Sandbox = &session.SandboxConfig{Enabled: true}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inst := session.NewInstanceWithGroupAndTool("agy-web-fork", t.TempDir(), "default", "antigravity")
			inst.Command = "agy"
			tt.mutate(inst)
			m := &WebMutator{h: &Home{instanceByID: map[string]*session.Instance{inst.ID: inst}}}
			if _, err := m.ForkSession(inst.ID); err == nil || !strings.Contains(err.Error(), "fork") {
				t.Fatalf("ForkSession(%s) error = %v, want a fork refusal", tt.name, err)
			}
		})
	}
}

// The HTTP endpoint and the TUI both call this mutator, so the projection and
// the refusal agree on the same CanFork gate.
func TestWebMutatorForkSessionUnknownSession(t *testing.T) {
	m := &WebMutator{h: &Home{instanceByID: map[string]*session.Instance{}}}
	if _, err := m.ForkSession("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ForkSession(missing) error = %v, want not found", err)
	}
}
