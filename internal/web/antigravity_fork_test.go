package web

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestToMenuSession_AntigravityCanForkTransition(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	inst := session.NewInstanceWithGroupAndTool("agy-web", home, "default", "antigravity")
	inst.Command = "agy"

	if toMenuSession(inst).CanFork {
		t.Fatal("unbound Antigravity session should report CanFork=false")
	}

	inst.AntigravitySessionID = "conv-web-1"
	inst.AntigravityDetectedAt = time.Now()
	if !toMenuSession(inst).CanFork {
		t.Fatal("bound Antigravity session should report CanFork=true")
	}
}

// The web fork gate must match the CLI: an Antigravity session on another host
// or in a sandbox cannot fork locally, so the projection reports CanFork=false.
func TestToMenuSession_AntigravityRejectsRemoteFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	remote := session.NewInstanceWithGroupAndTool("agy-remote", home, "default", "antigravity")
	remote.Command = "agy"
	remote.SSHHost = "devbox"
	remote.AntigravitySessionID = "conv-web-remote"
	remote.AntigravityDetectedAt = time.Now()
	if toMenuSession(remote).CanFork {
		t.Fatal("SSH Antigravity session should report CanFork=false")
	}

	sandboxed := session.NewInstanceWithGroupAndTool("agy-sandbox", home, "default", "antigravity")
	sandboxed.Command = "agy"
	sandboxed.Sandbox = &session.SandboxConfig{Enabled: true}
	sandboxed.AntigravitySessionID = "conv-web-sandbox"
	sandboxed.AntigravityDetectedAt = time.Now()
	if toMenuSession(sandboxed).CanFork {
		t.Fatal("sandboxed Antigravity session should report CanFork=false")
	}
}
