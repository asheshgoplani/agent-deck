// issue1715_launch_title_lock_test.go — `agent-deck launch -t <title>` must
// lock the title against Claude's session-name sync, matching `add -t` and the
// TUI New Session dialog.
//
// Reported in #1715: conductor-launched workers were created as
// `booct-adopt-contracts` and silently renamed to `adopt-platform-contracts-34`
// by Claude's name sync, after which every `agent-deck session send
// booct-adopt-contracts` missed its target and a whole sequence of
// conductor->worker instructions was lost. `launch` was the one creation entry
// point that still required --title-lock/--no-title-sync on top of -t.
//
// Why structural assertions on handleLaunch: it creates a real tmux session and
// calls os.Exit on every error path, and this package has no subprocess harness
// for the launch verb. Precedent: rename_title_lock_test.go,
// session_remove_kill_test.go. The decision itself is unit-tested behaviourally
// through shouldLockTitle below.

package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestHandleLaunch_LocksExplicitTitle: the launch path must route the lock
// decision through the shared shouldLockTitle chokepoint (which locks on an
// explicit -t/--title), not through the old flags-only condition.
func TestHandleLaunch_LocksExplicitTitle(t *testing.T) {
	body := foldSpaces(mustExtractFuncBody(t, "launch_cmd.go", "handleLaunch"))

	if !strings.Contains(body, "if shouldLockTitle(userProvidedTitle, *titleLock, *noTitleSync) { newInstance.TitleLocked = true }") {
		t.Error("handleLaunch must set newInstance.TitleLocked via shouldLockTitle(userProvidedTitle, *titleLock, *noTitleSync) so an explicit -t/--title locks (#1715)")
	}
	if strings.Contains(body, "if *titleLock || *noTitleSync {") {
		t.Error("handleLaunch still uses the flags-only lock condition — an explicit -t/--title would stay unlocked and drift (#1715)")
	}
	// The decision must be made after userProvidedTitle is derived from the
	// flags, otherwise it would always read false.
	lockIdx := strings.Index(body, "shouldLockTitle(")
	declIdx := strings.Index(body, `userProvidedTitle := (mergeFlags(*title, *titleShort) != "")`)
	if declIdx < 0 {
		t.Fatal("handleLaunch must derive userProvidedTitle from the -t/--title flags before locking")
	}
	if lockIdx < declIdx {
		t.Error("handleLaunch evaluates shouldLockTitle before userProvidedTitle is derived — the lock would never trigger for -t (#1715)")
	}
}

// TestHandleAdd_LocksExplicitTitle keeps the sibling entry point pinned: `add`
// and `launch` must not drift apart again (#1615/#1715).
func TestHandleAdd_LocksExplicitTitle(t *testing.T) {
	body := foldSpaces(mustExtractFuncBody(t, "main.go", "handleAdd"))

	if !strings.Contains(body, "if shouldLockTitle(userProvidedTitle, *titleLock, *noTitleSync) { newInstance.TitleLocked = true }") {
		t.Error("handleAdd must set newInstance.TitleLocked via shouldLockTitle so an explicit -t/--title locks (#1615)")
	}
}

// TestLaunchTitleLockDecision covers the flag combinations a `launch` caller
// can produce, applied to a real instance so the persisted field is asserted
// and not just the predicate:
//
//   - conductor style `launch -t booct-db-strategy` -> locked (#1715)
//   - explicit --title-lock / --no-title-sync on an auto-named session -> locked
//   - plain `launch .` (folder-name title) -> unlocked, sync keeps naming it
func TestLaunchTitleLockDecision(t *testing.T) {
	tests := []struct {
		name              string
		userProvidedTitle bool
		titleLock         bool
		noTitleSync       bool
		wantLocked        bool
	}{
		{name: "launch -t locks", userProvidedTitle: true, wantLocked: true},
		{name: "launch -t with --title-lock locks", userProvidedTitle: true, titleLock: true, wantLocked: true},
		{name: "auto title with --title-lock locks", titleLock: true, wantLocked: true},
		{name: "auto title with --no-title-sync locks", noTitleSync: true, wantLocked: true},
		{name: "auto folder-name title stays syncable", wantLocked: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &session.Instance{Title: "booct-db-strategy"}
			if shouldLockTitle(tt.userProvidedTitle, tt.titleLock, tt.noTitleSync) {
				inst.TitleLocked = true
			}
			if inst.TitleLocked != tt.wantLocked {
				t.Fatalf("TitleLocked = %v, want %v", inst.TitleLocked, tt.wantLocked)
			}
		})
	}
}
