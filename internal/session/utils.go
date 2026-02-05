package session

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// findNewestFile returns the newest file matching a glob pattern along with its modification time.
// Returns empty string and zero time if no files match.
func findNewestFile(pattern string) (string, time.Time) {
	files, _ := filepath.Glob(pattern)
	if len(files) == 0 {
		return "", time.Time{}
	}

	var newestPath string
	var newestTime time.Time
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if info.ModTime().After(newestTime) {
			newestTime = info.ModTime()
			newestPath = f
		}
	}
	return newestPath, newestTime
}

// GetDirectoryCompletions returns a list of directories that match the input prefix.
// Supports absolute, relative, and tilde-prefixed (~) paths.
func GetDirectoryCompletions(input string) ([]string, error) {
	if input == "" {
		return nil, nil
	}

	// Handle tilde-prefixed paths
	originalInput := input
	if strings.HasPrefix(input, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		input = filepath.Join(home, input[1:])
	}

	// Determine the directory to scan and the prefix to match.
	// Trailing-slash check MUST come first: when input ends with '/', the user
	// wants to list directory contents, not echo the directory back. os.Stat
	// would match the directory and return early, making this branch unreachable.
	var dir, prefix string
	if strings.HasSuffix(originalInput, string(os.PathSeparator)) {
		dir = input
		prefix = ""
	} else if info, err := os.Stat(input); err == nil && info.IsDir() {
		// Exact directory match without trailing slash - return itself
		return []string{originalInput}, nil
	} else {
		dir = filepath.Dir(input)
		prefix = filepath.Base(input)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // Silently ignore errors (e.g. invalid parent dir)
	}

	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(name, prefix) {
			match := filepath.Join(dir, name)

			// If original input used tilde, convert back
			if strings.HasPrefix(originalInput, "~") {
				home, _ := os.UserHomeDir()
				rel, err := filepath.Rel(home, match)
				if err == nil && !strings.HasPrefix(rel, "..") {
					match = "~" + string(os.PathSeparator) + rel
				}
			}
			matches = append(matches, match)
		}
	}

	return matches, nil
}

// CompletionCycler manages the state of directory autocomplete.
type CompletionCycler struct {
	matches []string
	index   int
}

// IsActive returns true if the cycler has active matches.
func (c *CompletionCycler) IsActive() bool {
	return len(c.matches) > 0
}

// Reset clears the cycler state.
func (c *CompletionCycler) Reset() {
	c.matches = nil
	c.index = -1
}

// SetMatches sets the matches for the cycler and resets the index.
func (c *CompletionCycler) SetMatches(matches []string) {
	c.matches = matches
	c.index = -1
}

// Next returns the next match in the cycle.
// Wraps around to the beginning if the end is reached.
func (c *CompletionCycler) Next() string {
	if !c.IsActive() {
		return ""
	}
	c.index = (c.index + 1) % len(c.matches)
	return c.matches[c.index]
}
