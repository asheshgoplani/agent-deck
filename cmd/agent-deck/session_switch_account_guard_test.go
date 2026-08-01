package main

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// #1815 Guard 2: switch-account already produced the right diagnosis ("no
// conversation to migrate, fresh session") and then let the restart proceed.
// A failed transcript verification must STOP the sequence.
func TestSwitchAccountRestartUnsafe(t *testing.T) {
	ranBefore := func() *session.Instance {
		inst := session.NewInstanceWithTool("switch-guard", t.TempDir(), "claude")
		inst.ClaudeSessionID = ""
		inst.ClaudeDetectedAt = time.Now() // this session HAS held a conversation
		return inst
	}

	t.Run("aborts when a session that has run cannot be located", func(t *testing.T) {
		blocked, why := switchAccountRestartUnsafe(ranBefore(), "", true, false)
		if !blocked {
			t.Fatal("#1815: verification failed (no conversation located, no recorded id) — the restart must be aborted, not annotated")
		}
		if why == "" {
			t.Fatal("the abort must surface a reason")
		}
	})

	t.Run("allows the switch when the conversation was located", func(t *testing.T) {
		if blocked, _ := switchAccountRestartUnsafe(ranBefore(), "/some/config/dir", true, false); blocked {
			t.Fatal("a located conversation is the verified path and must proceed")
		}
	})

	t.Run("allows a session with a recorded conversation id", func(t *testing.T) {
		inst := ranBefore()
		inst.ClaudeSessionID = "aaaaaaaa-1111-4222-8333-444444444444"
		if blocked, _ := switchAccountRestartUnsafe(inst, "", true, false); blocked {
			t.Fatal("a recorded conversation id is enough for the resume-time guard to verify identity")
		}
	})

	t.Run("allows a session that never held a conversation", func(t *testing.T) {
		inst := ranBefore()
		inst.ClaudeDetectedAt = time.Time{}
		if blocked, _ := switchAccountRestartUnsafe(inst, "", true, false); blocked {
			t.Fatal("a never-bound session has nothing to lose and nothing to hijack")
		}
	})

	t.Run("no restart to block", func(t *testing.T) {
		if blocked, _ := switchAccountRestartUnsafe(ranBefore(), "", true, true); blocked {
			t.Fatal("--no-restart is the documented way through: nothing is restarted, nothing to guard")
		}
		if blocked, _ := switchAccountRestartUnsafe(ranBefore(), "", false, false); blocked {
			t.Fatal("a session that was not running is not restarted by switch-account")
		}
	})
}
