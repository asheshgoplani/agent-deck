package ui

// A forked Claude session inherits the parent's session name (e.g. an
// auto-assigned plan title), so the #572 name sync clobbers the title the
// user typed in the fork dialog on the fork's first hook event. Dialog forks
// therefore lock the title; quick forks keep the auto-generated
// "<title> (fork)" name sync-enabled.

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func forkTitleLockDeps(fake *session.Instance) forkInstanceDeps {
	return forkInstanceDeps{
		createInstance: func(_ *session.Instance, _, _ string, _ *session.ClaudeOptions) (*session.Instance, error) {
			return fake, nil
		},
		createMultiRepoDir: func(_, _ *session.Instance) error { return nil },
		startInstance:      func(_ *session.Instance) error { return nil },
		rollback:           func(_, _, _ string) {},
	}
}

func TestCompleteFork_LockTitleSetsTitleLocked(t *testing.T) {
	fake := &session.Instance{}
	inst, err := completeFork(&session.Instance{}, "my fork", "group", forkToggles{LockTitle: true}, nil, "", "", false, forkTitleLockDeps(fake))
	if err != nil {
		t.Fatalf("completeFork: %v", err)
	}
	if !inst.TitleLocked {
		t.Error("TitleLocked = false for dialog fork, want true")
	}
}

func TestCompleteFork_NoLockKeepsTitleSyncEnabled(t *testing.T) {
	fake := &session.Instance{}
	inst, err := completeFork(&session.Instance{}, "parent (fork)", "group", forkToggles{}, nil, "", "", false, forkTitleLockDeps(fake))
	if err != nil {
		t.Fatalf("completeFork: %v", err)
	}
	if inst.TitleLocked {
		t.Error("TitleLocked = true for quick fork, want false (name sync stays enabled)")
	}
}
