package session

import (
	"errors"
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// refreshCommittedGroupTitles runs after the storage lock is released. Unchanged
// groups cost no tmux work. A failed save never reaches this publication step.
func (s *Storage) refreshCommittedGroupTitles(instances []*Instance, rows []*statedb.InstanceRow) {
	for idx, row := range rows {
		sess := instances[idx].GetTmuxSession()
		if sess == nil || sess.GetGroupPath() == row.GroupPath {
			continue
		}
		applied, err := reconcileGroupTitle(func() (*statedb.InstanceRow, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.db.LoadInstanceByID(row.ID)
		}, func(current *statedb.InstanceRow) error {
			target := &tmux.Session{Name: current.TmuxSession, SocketName: current.TmuxSocketName}
			return target.SetGroupTitleMetadata(current.GroupPath)
		})
		if err != nil {
			storageLog.Debug("group_title_refresh_failed", slog.String("instance_id", row.ID), slog.String("error", err.Error()))
			continue
		}
		if applied != nil && applied.TmuxSession == sess.Name && applied.TmuxSocketName == sess.SocketName {
			sess.SetGroupPath(applied.GroupPath)
		}
	}
}

// A late callback must publish current database truth, not its old save snapshot.
// Recheck after writing so a concurrent commit cannot leave an older callback's
// title as the last result. Bound contention; later mutations/attach can retry.
func reconcileGroupTitle(read func() (*statedb.InstanceRow, error), write func(*statedb.InstanceRow) error) (*statedb.InstanceRow, error) {
	for attempt := 0; attempt < 3; attempt++ {
		current, err := read()
		if err != nil || current == nil || current.TmuxSession == "" {
			return current, err
		}
		if err := write(current); err != nil {
			return nil, err
		}
		latest, err := read()
		if err != nil {
			return nil, err
		}
		if latest == nil {
			return nil, nil
		}
		if current.GroupPath == latest.GroupPath && current.TmuxSession == latest.TmuxSession && current.TmuxSocketName == latest.TmuxSocketName {
			return current, nil
		}
	}
	return nil, errors.New("group changed during three title refresh attempts")
}
