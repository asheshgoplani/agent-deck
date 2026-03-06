package vcs

import (
	"path/filepath"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

var (
	detectionCache sync.Map // map[string]Backend (nil stored as nilSentinel)
)

// nilSentinel is stored in the cache to represent "no backend detected".
type nilSentinel struct{}

// Detect walks up from dir looking for a VCS root.
// Returns nil if no VCS is detected.
func Detect(dir string) Backend {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	if val, ok := detectionCache.Load(absDir); ok {
		if _, isNil := val.(*nilSentinel); isNil {
			return nil
		}
		return val.(Backend)
	}

	backend := detect(absDir)
	if backend == nil {
		detectionCache.Store(absDir, &nilSentinel{})
	} else {
		detectionCache.Store(absDir, backend)
	}
	return backend
}

func detect(dir string) Backend {
	if git.IsGitRepo(dir) {
		return &GitBackend{}
	}
	return nil
}

// ClearCache clears the detection cache. Useful for testing.
func ClearCache() {
	detectionCache.Range(func(key, _ any) bool {
		detectionCache.Delete(key)
		return true
	})
}
