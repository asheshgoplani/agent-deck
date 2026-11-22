package session

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/termtext"
)

// TitleConflictError is the refusal of a rename onto a title another session
// already holds at the same location. Title plus location is how a session is
// addressed by name, so two sessions sharing both can no longer be told apart.
// The CLI, the TUI and the web API all report this one error.
type TitleConflictError struct {
	Title      string
	Location   Location
	ConflictID string
}

func (e *TitleConflictError) Error() string {
	return fmt.Sprintf("session with title %q already exists at %s (id: %s)", e.Title, e.Location.String(), e.ConflictID)
}

// TitleConflictAt returns the OTHER session holding newTitle at inst's
// location, or nil when the title is free there.
func TitleConflictAt(instances []*Instance, inst *Instance, newTitle string) *Instance {
	if inst == nil {
		return nil
	}
	loc := LocationOf(inst)
	for _, other := range instances {
		if other == nil || other.ID == inst.ID {
			continue
		}
		if other.GetTitleThreadSafe() == newTitle && LocationOf(other) == loc {
			return other
		}
	}
	return nil
}

// CheckTitleConflict is the one decision every rename path makes (`rename`,
// `session set <id> title`, the TUI rename and edit dialogs, the web PATCH):
// it returns a *TitleConflictError when newTitle is held by another session at
// inst's location and nil when the rename is free. Renaming a session to the
// title it already holds is free.
func CheckTitleConflict(instances []*Instance, inst *Instance, newTitle string) error {
	// The title is stored cleaned (SetTitleThreadSafe), so compare that form:
	// "foo\x1b" must collide with an existing "foo".
	newTitle = termtext.Clean(newTitle)
	if inst == nil || newTitle == inst.GetTitleThreadSafe() {
		return nil
	}
	conflict := TitleConflictAt(instances, inst, newTitle)
	if conflict == nil {
		return nil
	}
	return &TitleConflictError{Title: newTitle, Location: LocationOf(inst), ConflictID: conflict.ID}
}
