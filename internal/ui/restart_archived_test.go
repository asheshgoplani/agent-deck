package ui

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRestartWithArchiveTransitionActiveSession(t *testing.T) {
	inst := &session.Instance{ID: "active"}
	persisted := false
	restarted := false

	unarchived, err := restartWithArchiveTransition(inst, func(*session.Instance) error {
		persisted = true
		return nil
	}, func() error {
		restarted = true
		return nil
	})
	if err != nil {
		t.Fatalf("restartWithArchiveTransition: %v", err)
	}
	if unarchived || persisted || !restarted {
		t.Fatalf("active restart: unarchived=%v persisted=%v restarted=%v", unarchived, persisted, restarted)
	}
}

func TestRestartWithArchiveTransitionUnarchivesBeforeRestart(t *testing.T) {
	archivedAt := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	inst := &session.Instance{ID: "archived", ArchivedAt: archivedAt}
	var events []string

	unarchived, err := restartWithArchiveTransition(inst, func(current *session.Instance) error {
		if current.IsArchived() {
			events = append(events, "persist-archived")
		} else {
			events = append(events, "persist-unarchived")
		}
		return nil
	}, func() error {
		events = append(events, "restart")
		return nil
	})
	if err != nil {
		t.Fatalf("restartWithArchiveTransition: %v", err)
	}
	if !unarchived || inst.IsArchived() {
		t.Fatalf("successful restart should leave session unarchived: unarchived=%v archivedAt=%v", unarchived, inst.ArchivedAt)
	}
	want := []string{"persist-unarchived", "restart"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestRestartWithArchiveTransitionRestoresArchiveOnFailure(t *testing.T) {
	archivedAt := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	inst := &session.Instance{ID: "archived", ArchivedAt: archivedAt}
	var persisted []time.Time
	restartErr := errors.New("spawn failed")

	unarchived, err := restartWithArchiveTransition(inst, func(current *session.Instance) error {
		persisted = append(persisted, current.ArchivedAt)
		return nil
	}, func() error {
		return restartErr
	})
	if !errors.Is(err, restartErr) {
		t.Fatalf("error = %v, want %v", err, restartErr)
	}
	if unarchived || !inst.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("failed restart should restore archive: unarchived=%v archivedAt=%v", unarchived, inst.ArchivedAt)
	}
	if len(persisted) != 2 || !persisted[0].IsZero() || !persisted[1].Equal(archivedAt) {
		t.Fatalf("persisted timestamps = %v, want [zero, %v]", persisted, archivedAt)
	}
}

func TestRestartWithArchiveTransitionDoesNotRestartWhenUnarchivePersistenceFails(t *testing.T) {
	archivedAt := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	inst := &session.Instance{ID: "archived", ArchivedAt: archivedAt}
	restarted := false

	unarchived, err := restartWithArchiveTransition(inst, func(*session.Instance) error {
		return errors.New("database unavailable")
	}, func() error {
		restarted = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "failed to unarchive session before restart") {
		t.Fatalf("unexpected error: %v", err)
	}
	if unarchived || restarted || !inst.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("persistence failure: unarchived=%v restarted=%v archivedAt=%v", unarchived, restarted, inst.ArchivedAt)
	}
}

func TestRestartFreshWithArchiveTransitionUnarchivesBeforeRestart(t *testing.T) {
	archivedAt := time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC)
	inst := &session.Instance{ID: "archived-fresh", ArchivedAt: archivedAt}
	home := NewHome()
	home.instanceByID[inst.ID] = inst
	var events []string

	cmd := home.restartSessionFreshWith(inst, func(current *session.Instance) error {
		if current.IsArchived() {
			events = append(events, "persist-archived")
		} else {
			events = append(events, "persist-unarchived")
		}
		return nil
	}, func(*session.Instance) error {
		events = append(events, "restart-fresh")
		return nil
	})
	msg, ok := cmd().(sessionRestartedMsg)
	if !ok {
		t.Fatalf("expected sessionRestartedMsg")
	}
	if msg.err != nil {
		t.Fatalf("fresh restart archive transition: %v", msg.err)
	}
	if !msg.fresh || !msg.unarchived || inst.IsArchived() {
		t.Fatalf("successful fresh restart: fresh=%v unarchived=%v archivedAt=%v", msg.fresh, msg.unarchived, inst.ArchivedAt)
	}
	want := []string{"persist-unarchived", "restart-fresh"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestRestartFreshWithArchiveTransitionRestoresArchiveOnFailure(t *testing.T) {
	archivedAt := time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC)
	inst := &session.Instance{ID: "archived-fresh", ArchivedAt: archivedAt}
	home := NewHome()
	home.instanceByID[inst.ID] = inst
	var persisted []time.Time
	restartErr := errors.New("fresh spawn failed")

	cmd := home.restartSessionFreshWith(inst, func(current *session.Instance) error {
		persisted = append(persisted, current.ArchivedAt)
		return nil
	}, func(*session.Instance) error {
		return restartErr
	})
	msg, ok := cmd().(sessionRestartedMsg)
	if !ok {
		t.Fatalf("expected sessionRestartedMsg")
	}
	if !errors.Is(msg.err, restartErr) {
		t.Fatalf("error = %v, want %v", msg.err, restartErr)
	}
	if !msg.fresh || msg.unarchived || !inst.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("failed fresh restart: fresh=%v unarchived=%v archivedAt=%v", msg.fresh, msg.unarchived, inst.ArchivedAt)
	}
	if len(persisted) != 2 || !persisted[0].IsZero() || !persisted[1].Equal(archivedAt) {
		t.Fatalf("persisted timestamps = %v, want [zero, %v]", persisted, archivedAt)
	}
}

func TestRestartFreshWithArchiveTransitionActiveSessionSkipsPersistence(t *testing.T) {
	inst := &session.Instance{ID: "active-fresh"}
	home := NewHome()
	home.instanceByID[inst.ID] = inst
	persisted := false
	restarted := false

	cmd := home.restartSessionFreshWith(inst, func(*session.Instance) error {
		persisted = true
		return nil
	}, func(*session.Instance) error {
		restarted = true
		return nil
	})
	msg, ok := cmd().(sessionRestartedMsg)
	if !ok {
		t.Fatalf("expected sessionRestartedMsg")
	}
	if msg.err != nil {
		t.Fatalf("fresh restart archive transition: %v", msg.err)
	}
	if !msg.fresh || msg.unarchived || persisted || !restarted {
		t.Fatalf("active fresh restart: fresh=%v unarchived=%v persisted=%v restarted=%v", msg.fresh, msg.unarchived, persisted, restarted)
	}
}

func TestSessionRestartedMsgMovesUnarchivedSessionOutOfArchivedView(t *testing.T) {
	home := NewHome()
	home.storage = nil
	home.statusFilter = FilterModeArchived
	inst := &session.Instance{
		ID:         "archived",
		Title:      "agent deck dev",
		Tool:       "shell",
		Status:     session.StatusStopped,
		GroupPath:  session.DefaultGroupPath,
		CreatedAt:  time.Now(),
		ArchivedAt: time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
	}
	home.instances = []*session.Instance{inst}
	home.instanceByID = map[string]*session.Instance{inst.ID: inst}
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()
	if got := visibleSessionIDsFromFlat(home); !sliceContainsString(got, inst.ID) {
		t.Fatalf("setup: archived session not visible: %v", got)
	}

	inst.ArchivedAt = time.Time{}
	model, _ := home.Update(sessionRestartedMsg{sessionID: inst.ID, unarchived: true})
	h := model.(*Home)
	if got := visibleSessionIDsFromFlat(h); sliceContainsString(got, inst.ID) {
		t.Fatalf("restarted session remained in archived view: %v", got)
	}
	if h.err == nil || !strings.Contains(h.err.Error(), "unarchived and restarted 'agent deck dev'") {
		t.Fatalf("missing restart confirmation: %v", h.err)
	}
}
