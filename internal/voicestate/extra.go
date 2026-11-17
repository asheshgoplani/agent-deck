package voicestate

// RawPhase is the daemon's own phase word (or the one derived from mode for
// an older daemon), before the fold to the six words. "" for no state.
func (s *State) RawPhase() string {
	if s == nil {
		return ""
	}
	return s.phase()
}
