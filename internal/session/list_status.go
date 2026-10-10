package session

import "github.com/asheshgoplani/agent-deck/internal/tmux"

var listStatusSessionNames = tmux.ListSessionNamesOnSocket

// CLIStatusCandidates avoids probing each historical session separately. A
// stopped or error row absent from a complete socket listing retains its stored
// status. Failed inventory retains stopped history but refreshes error rows,
// which may still have a live pane.
func CLIStatusCandidates(instances []*Instance) ([]*Instance, map[*Instance]bool) {
	refresh := make([]*Instance, 0, len(instances))
	cached := make(map[*Instance]bool)
	bySocket := make(map[string]map[string]struct{})
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if inst.Status != StatusStopped && inst.Status != StatusError {
			refresh = append(refresh, inst)
			continue
		}
		sess := inst.GetTmuxSession()
		if sess == nil {
			cached[inst] = true
			continue
		}
		names, ok := bySocket[sess.SocketName]
		if !ok {
			var err error
			names, err = listStatusSessionNames(sess.SocketName)
			if err != nil {
				names = nil
			}
			bySocket[sess.SocketName] = names
		}
		if names == nil && inst.Status == StatusError {
			refresh = append(refresh, inst)
			continue
		}
		if _, exists := names[sess.Name]; !exists {
			cached[inst] = true
			continue
		}
		refresh = append(refresh, inst)
	}
	return refresh, cached
}
