package tmux

// latestCandidate is a person's client that could take a window's latest slot.
type latestCandidate struct {
	pid      int
	activity int64
	cols     int
	rows     int
}

func parseLatestCandidates(out, windowID string) []latestCandidate { return nil }

func pickLatestViewer(candidates []latestCandidate, cols, rows int) (latestCandidate, bool) {
	return latestCandidate{}, false
}
