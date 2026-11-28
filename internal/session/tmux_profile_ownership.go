package session

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// tmuxOwnedByOtherProfile checks the live session before discovery changes it.
// Older sessions may have only an instance ID, so consult other profiles' live
// registries in that case as well.
func tmuxOwnedByOtherProfile(sess *tmux.Session, profile string) (bool, error) {
	owner, id, err := sess.ReadAgentDeckIdentity()
	if err != nil {
		return false, err
	}
	if owner != "" {
		return owner != profile, nil
	}
	if id == "" {
		return false, nil
	}

	profiles, err := ListProfiles()
	if err != nil {
		return false, err
	}
	for _, other := range profiles {
		if other == profile {
			continue
		}
		path, err := GetDBPathForProfile(other)
		if err != nil {
			return false, err
		}
		db, err := statedb.OpenReadOnlyLive(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // A legacy profile may have only sessions.json.
		}
		if err != nil {
			return false, err
		}
		row, err := db.LoadInstanceByID(id)
		_ = db.Close()
		if err != nil {
			return false, err
		}
		if row != nil {
			return true, nil
		}
	}
	return false, nil
}

func (i *Instance) checkRecoveredTmuxOwnership() error {
	if !i.CreatedAt.IsZero() {
		return nil
	}
	profile := i.storageProfile
	if profile == "" {
		profile = sessionProfileEnvValue()
	}
	return checkRecoveredTmuxOwnership(i.tmuxSession, i.CreatedAt, profile)
}

// checkRecoveredTmuxOwnership protects copies created by older imports.
// Imported rows have no creation timestamp and may refer to a live session
// registered in another profile, even after the local row changes groups.
func checkRecoveredTmuxOwnership(sess *tmux.Session, createdAt time.Time, profile string) error {
	if sess == nil || !createdAt.IsZero() || !strings.HasPrefix(sess.TmuxName(), tmux.SessionPrefix) {
		return nil
	}
	foreign, err := tmuxOwnedByOtherProfile(sess, profile)
	if err != nil {
		// A missing session leaves no live tmux owner to protect.
		if exists, probeErr := sess.ProbeExists(); probeErr == nil && !exists {
			return nil
		}
		return fmt.Errorf("could not verify ownership of tmux session %q: %w", sess.TmuxName(), err)
	}
	if foreign {
		return fmt.Errorf("tmux session %q belongs to another profile", sess.TmuxName())
	}
	return nil
}
