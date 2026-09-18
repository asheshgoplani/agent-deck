package session

import (
	"context"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// viewerTarget names the tmux session the viewer listings ask about; ok is
// false when the instance has no tmux session to ask.
func (i *Instance) viewerTarget() (socketName, sessionName string, ok bool) {
	ts := i.GetTmuxSession()
	if ts == nil || ts.Name == "" {
		return "", "", false
	}
	return ts.SocketName, ts.Name, true
}

// Viewers lists the people attached to this session's tmux session (its
// interactive clients, never agent-deck's own control pipes), most recently
// active first. ok is false when there is no tmux session to ask or tmux
// could not answer; callers render that as "unknown", not "nobody".
func (i *Instance) Viewers(ctx context.Context) (viewers []tmux.Viewer, ok bool) {
	socketName, sessionName, ok := i.viewerTarget()
	if !ok {
		return nil, false
	}
	viewers, err := tmux.ListViewers(ctx, socketName, sessionName)
	if err != nil {
		return nil, false
	}
	return nonNilViewers(viewers), true
}

// ViewersCached is the non-blocking form for render paths: it answers from
// the per-socket viewer cache (internal/tmux) and refreshes it in the
// background. known is false until the first listing completes.
func (i *Instance) ViewersCached() (viewers []tmux.Viewer, known bool) {
	socketName, sessionName, ok := i.viewerTarget()
	if !ok {
		return nil, false
	}
	return tmux.ViewersCached(socketName, sessionName)
}

// ViewersByTmuxSession lists the viewers of every instance's tmux session
// with one tmux call per distinct socket, for listings that must not pay a
// subprocess per row. Sessions whose socket could not be asked are absent
// from the result (unknown); sessions nobody views map to an empty list.
func ViewersByTmuxSession(ctx context.Context, instances []*Instance) map[string][]tmux.Viewer {
	result := map[string][]tmux.Viewer{}
	// Per socket: the server's listing, or nil when it could not be asked.
	bySocket := map[string]map[string][]tmux.Viewer{}
	for _, inst := range instances {
		socketName, sessionName, ok := inst.viewerTarget()
		if !ok {
			continue
		}
		all, listed := bySocket[socketName]
		if !listed {
			var err error
			if all, err = tmux.ListAllViewers(ctx, socketName); err != nil {
				all = nil
			}
			bySocket[socketName] = all
		}
		if all == nil {
			continue
		}
		result[sessionName] = nonNilViewers(all[sessionName])
	}
	return result
}

// nonNilViewers turns "nobody" into an empty list so it encodes as [] and
// stays distinct from an absent (unknown) field.
func nonNilViewers(viewers []tmux.Viewer) []tmux.Viewer {
	if viewers == nil {
		return []tmux.Viewer{}
	}
	return viewers
}
