package session

// HistoricalSubstate retains diagnostics that do not require a live pane.
func (i *Instance) HistoricalSubstate() Substate {
	if held, _ := i.IsAuthHeld(); held {
		return SubstateAuth401
	}
	if i.usageLimited() {
		return SubstateUsageLimit
	}
	return SubstateNone
}
