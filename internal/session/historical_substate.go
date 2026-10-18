package session

// HistoricalSubstate retains diagnostics that do not require a live pane,
// including the terminated-pane verdict CLIStatusCandidates records for an
// absent error row (process-exited, unknown-exit).
func (i *Instance) HistoricalSubstate() Substate {
	if held, _ := i.IsAuthHeld(); held {
		return SubstateAuth401
	}
	if i.usageLimited() {
		return SubstateUsageLimit
	}
	return i.getTerminatedPaneSubstate()
}
