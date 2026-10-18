package session

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
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
// Hidden directories are included only when the typed segment starts with a dot.
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
		// Preserve a trailing "/." so it can complete hidden directories.
		input = home + string(os.PathSeparator) + strings.TrimPrefix(input[1:], string(os.PathSeparator))
	}

	// Split without cleaning so a symlink followed by ".." keeps its meaning.
	dir, prefix := filepath.Split(input)
	if dir == "" {
		dir = "."
	}

	// Resolve parent traversal before joining names. Lexically cleaning a
	// symlink followed by ".." would produce paths outside the scanned directory.
	resolvedParent := false
	for _, segment := range strings.Split(dir, string(os.PathSeparator)) {
		if segment == ".." {
			resolved, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return nil, nil
			}
			dir = resolved
			input = filepath.Join(dir, prefix)
			resolvedParent = true
			break
		}
	}
	// A terminal ".." is the typed segment, not part of dir. Resolve it too
	// so later project-path normalization cannot clean away a symlink traversal.
	if prefix == ".." {
		resolved, err := filepath.EvalSymlinks(input)
		if err != nil {
			return nil, nil
		}
		input = resolved
		resolvedParent = true
	}

	// A trailing slash lists children; an exact directory returns itself.
	if strings.HasSuffix(originalInput, string(os.PathSeparator)) {
		dir = input
		prefix = ""
	} else if info, err := os.Stat(input); err == nil && info.IsDir() && prefix != "." {
		if resolvedParent {
			return []string{input}, nil
		}
		return []string{originalInput}, nil
	}

	entries, err := readDirBounded(dir, maxCompletionScan)
	if err != nil {
		return nil, nil // Silently ignore errors (e.g. invalid parent dir)
	}

	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
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

// maxCompletionScan bounds how many directory entries one completion request
// reads, so a directory with millions of entries (or a slow network mount)
// cannot make a single keystroke, or a web request, do unbounded work.
const maxCompletionScan = 10000

// readDirBounded reads at most limit entries of dir and returns them sorted by
// name, like os.ReadDir. Entries past the limit are not considered.
func readDirBounded(dir string, limit int) ([]os.DirEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []os.DirEntry
	for len(entries) < limit {
		batch, err := f.ReadDir(min(512, limit-len(entries)))
		entries = append(entries, batch...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
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

// Matches returns the current completion matches (nil when inactive).
func (c *CompletionCycler) Matches() []string {
	return c.matches
}

// Index returns the index of the currently selected match, or -1 when
// inactive or before the first Next() call.
func (c *CompletionCycler) Index() int {
	if !c.IsActive() || c.index < 0 || c.index >= len(c.matches) {
		return -1
	}
	return c.index
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
