package main

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

// detectAndCreateBackend detects the VCS type for the given directory and
// creates the appropriate backend. Returns the backend and the WorktreeType
// string to store on the session Instance.
func detectAndCreateBackend(dir string) (vcs.Backend, error) {
	var b vcs.Backend
	b, err := git.NewGitBackend(dir)
	if err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("failed to initialize backend: %w", err)
}
