package session

import "github.com/asheshgoplani/agent-deck/internal/tmux"

// SetTmuxSessionForTest assigns the unexported tmuxSession field. Tests in
// other packages (notably internal/ui) need this to construct an Instance with
// a *tmux.Session attached without going through storage hydration or a real
// tmux server.
//
// Do not call from non-test code; production paths populate tmuxSession via
// Start (instance.go) or storage.LoadWithGroups (storage.go).
func (i *Instance) SetTmuxSessionForTest(s *tmux.Session) {
	i.tmuxSession = s
}
