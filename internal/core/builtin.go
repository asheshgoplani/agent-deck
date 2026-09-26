package core

// Built-in command ids.
const (
	IDSessionStart   = "session.start"
	IDSessionStop    = "session.stop"
	IDSessionRestart = "session.restart"
	IDSessionList    = "session.list"
	IDGroupList      = "group.list"
)

// Builtins returns the slice-1 command definitions wired to deps.
func Builtins(deps Deps) []Def {
	return []Def{
		{
			ID: IDSessionStart, CLI: []string{"session", "start"},
			Summary: "Start a session's tmux process",
			Class:   Mutate, Remote: RemoteAllow,
			In: SessionStartIn{}, Out: SessionStartOut{},
			Exec: Typed(deps.sessionStart),
		},
		{
			ID: IDSessionStop, CLI: []string{"session", "stop"},
			Summary: "Stop a session's process",
			Class:   Mutate, Remote: RemoteAllow,
			In: SessionStopIn{}, Out: SessionStopOut{},
			Exec: Typed(deps.sessionStop),
		},
		{
			ID: IDSessionRestart, CLI: []string{"session", "restart"},
			Summary: "Restart a session, or every active session",
			Class:   Mutate, Remote: RemoteAllow,
			In: SessionRestartIn{}, Out: SessionRestartOut{},
			Exec: Typed(deps.sessionRestart),
		},
		{
			ID: IDSessionList, CLI: []string{"list"},
			Summary: "List sessions",
			Class:   Query, Remote: RemoteAllow,
			In: SessionListIn{}, Out: SessionListOut{},
			Exec: Typed(sessionList),
		},
		{
			ID: IDGroupList, CLI: []string{"group", "list"},
			Summary: "List groups with session counts",
			Class:   Query, Remote: RemoteAllow,
			In: GroupListIn{}, Out: GroupListOut{},
			Exec: Typed(groupList),
		},
	}
}

// RegisterBuiltins registers every built-in command on r.
func RegisterBuiltins(r *Registry, deps Deps) error {
	for _, d := range Builtins(deps) {
		if err := r.Register(d); err != nil {
			return err
		}
	}
	return nil
}
