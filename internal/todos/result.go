package todos

// The stable result shapes shared by `agent-deck todo --json` and the web
// API (/api/todos), documented in docs/todos.md.

// Statuses of a Result.
const (
	StatusAdded       = "added"
	StatusDone        = "done"
	StatusAlreadyDone = "already_done"
	StatusReopened    = "reopened"
	StatusAlreadyOpen = "already_open"
	StatusRemoved     = "removed"
	StatusNotFound    = "not_found"
	StatusAmbiguous   = "ambiguous"
	StatusCloseMatch  = "close_match"
	StatusError       = "error"
)

// Result is the answer to a change (add, done, undo, remove), a failed
// match (not_found, ambiguous, close_match) or an error.
type Result struct {
	Status     string `json:"status"`
	Todo       *Item  `json:"todo,omitempty"`
	Open       *int   `json:"open,omitempty"`
	Query      string `json:"query,omitempty"`
	Candidates []Item `json:"candidates,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ListResult is the answer to a list. Todos is never null.
type ListResult struct {
	Todos []Item `json:"todos"`
	Open  int    `json:"open"`
	Done  int    `json:"done"`
}

// OpenCount returns the number of open items, or nil when the list cannot
// be read.
func (s *Store) OpenCount() *int {
	open, _, err := s.Counts()
	if err != nil {
		return nil
	}
	return &open
}

// ListResult returns the list (open items, or all with all) with counts.
func (s *Store) ListResult(all bool) (ListResult, error) {
	items, err := s.List(all)
	if err != nil {
		return ListResult{}, err
	}
	open, done, err := s.Counts()
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Todos: items, Open: open, Done: done}, nil
}
