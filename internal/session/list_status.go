package session

import "github.com/asheshgoplani/agent-deck/internal/tmux"

// CLIStatusCandidates avoids probing each stopped session separately. A stopped
// row with no tmux session in a complete socket listing retains its stored
// status; an indeterminate listing also retains it, marked as cached.
func CLIStatusCandidates(instances []*Instance) ([]*Instance, map[*Instance]bool) {
	refresh := make([]*Instance, 0, len(instances))
	cached := make(map[*Instance]bool)
	bySocket := make(map[string]map[string]struct{})
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if inst.Status != StatusStopped {
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
			names, err = tmux.ListSessionNamesOnSocket(sess.SocketName)
			if err != nil {
				names = nil
			}
			bySocket[sess.SocketName] = names
		}
		if _, exists := names[sess.Name]; !exists {
			cached[inst] = true
			continue
		}
		refresh = append(refresh, inst)
	}
	return refresh, cached
}
