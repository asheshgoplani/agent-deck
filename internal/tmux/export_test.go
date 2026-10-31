package tmux

// SetCachedSubstateForTest seeds the cached substate so tests can exercise
// layers that read it without classifying a real pane. It lives in a _test.go
// file so it never ships in the binary (issue #2986 item 6).
func (s *Session) SetCachedSubstateForTest(sub Substate, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSubstate, s.lastSubstateDetail = sub, detail
}
