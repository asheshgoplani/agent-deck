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
