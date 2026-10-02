package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Claude Code names a project directory after the working directory string
// the process sees, so one directory can be keyed up to three ways: the path
// as typed (a /tmp symlink, say), its macOS /private form, and its realpath.
// A transcript written through one key is invisible to a lookup through
// another. Every reader that must find "the" conversation of a session (the
// account switch, the context export) therefore enumerates all of them.

// TranscriptCandidate is one copy of a session's transcript found under one
// project key in one account.
type TranscriptCandidate struct {
	Path string `json:"path"`
	// Side is "source" or "destination" (the account the copy was found in).
	Side       string `json:"side"`
	ProjectKey string `json:"project_key"`
	// NewestEvent is the latest "timestamp" field across the file's JSONL
	// events; zero when the file has none that parse.
	NewestEvent time.Time `json:"newest_event,omitempty"`
	Size        int64     `json:"size"`
	Lines       int       `json:"lines"`
	SHA256      string    `json:"sha256"`
}

// TranscriptChoice records which copy an account switch resumed from, every
// copy it considered, and what it wrote. The CLI prints it and emits it under
// --json so a wrong choice is diagnosable from the receipt alone.
type TranscriptChoice struct {
	Chosen     TranscriptCandidate   `json:"chosen"`
	Candidates []TranscriptCandidate `json:"candidates"`
	// Installed lists destination paths that received the chosen copy
	// (new files and replaced files); keys already holding identical bytes
	// are not listed.
	Installed []string `json:"installed,omitempty"`
	// BackedUp lists the backup path of every destination copy that was
	// replaced, one per replaced file. Nothing is ever deleted.
	BackedUp []string `json:"backed_up,omitempty"`
}

// Summary is the one-line receipt shown by the CLI and the TUI.
func (c *TranscriptChoice) Summary() string {
	if c == nil {
		return ""
	}
	age := "no timestamped events"
	if !c.Chosen.NewestEvent.IsZero() {
		age = "newest event " + c.Chosen.NewestEvent.UTC().Format(time.RFC3339)
	}
	candidates := len(c.Candidates)
	installedKeys := len(c.Installed) + c.alreadyInstalled()
	s := fmt.Sprintf("chose the %s copy %s (%s, %d lines) from %d %s; installed under %d %s",
		c.Chosen.Side, c.Chosen.Path, age, c.Chosen.Lines,
		candidates, pluralWord(candidates, "candidate", "candidates"),
		installedKeys, pluralWord(installedKeys, "project key", "project keys"))
	if n := len(c.BackedUp); n > 0 {
		s += fmt.Sprintf("; backed up %d replaced %s", n, pluralWord(n, "copy", "copies"))
	}
	return s
}

func pluralWord(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// alreadyInstalled counts destination candidates that already held the chosen
// bytes (including the chosen copy itself when it lives in the destination).
func (c *TranscriptChoice) alreadyInstalled() int {
	n := 0
	for _, cand := range c.Candidates {
		if cand.Side == "destination" && cand.SHA256 == c.Chosen.SHA256 {
			n++
		}
	}
	return n
}

// claudeProjectKeyCandidates lists the encoded project keys Claude Code may
// use for workingDir, typed form first. See claudeProjectKeyCandidatesWith.
func claudeProjectKeyCandidates(workingDir string) []string {
	return claudeProjectKeyCandidatesWith(workingDir, runtime.GOOS, filepath.EvalSymlinks)
}

// claudeProjectKeyCandidatesWith is the testable form: goos selects the
// macOS-only /private alias and resolve stands in for filepath.EvalSymlinks.
// The order is stable (typed, /private alias, realpath) and duplicates are
// removed, so the first key is always the one the session record names.
func claudeProjectKeyCandidatesWith(workingDir, goos string, resolve func(string) (string, error)) []string {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return nil
	}
	paths := []string{filepath.Clean(workingDir)}
	if goos == "darwin" {
		// macOS mounts /tmp, /var and /etc as symlinks into /private; a shell
		// started in /tmp/x reports either spelling depending on how it got
		// there, and Claude keys the directory by whichever it saw.
		switch {
		case strings.HasPrefix(paths[0], "/private/"):
			paths = append(paths, strings.TrimPrefix(paths[0], "/private"))
		case strings.HasPrefix(paths[0], "/tmp/"), strings.HasPrefix(paths[0], "/var/"), strings.HasPrefix(paths[0], "/etc/"),
			paths[0] == "/tmp", paths[0] == "/var", paths[0] == "/etc":
			paths = append(paths, "/private"+paths[0])
		}
	}
	if resolve != nil {
		if real, err := resolve(workingDir); err == nil && real != "" {
			paths = append(paths, filepath.Clean(real))
		}
	}
	seen := make(map[string]bool, len(paths))
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		key := ConvertToClaudeDirName(p)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys
}

// claudeTranscriptCopies finds every regular-file copy of sessionID's
// transcript under configDir for the given keys and measures each one. A
// symlink anywhere on a candidate path is an error, never followed.
func claudeTranscriptCopies(configDir string, keys []string, sessionID, side string) ([]TranscriptCandidate, error) {
	configDir = ExpandPath(strings.TrimSpace(configDir))
	if configDir == "" || sessionID == "" {
		return nil, nil
	}
	var found []TranscriptCandidate
	for _, key := range keys {
		dir, ok := claudeProjectDir(configDir, key)
		if !ok {
			continue
		}
		path := filepath.Join(dir, sessionID+".jsonl")
		if err := ensureNoSymlinkPath(path); err != nil {
			return nil, fmt.Errorf("unsafe exact Claude %s path: %w", side, err)
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect %s transcript %s: %w", side, path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s transcript is not a regular file: %s", side, path)
		}
		newest, lines, err := scanTranscriptEvents(path)
		if err != nil {
			return nil, fmt.Errorf("read %s transcript %s: %w", side, path, err)
		}
		hash, err := sha256File(path)
		if err != nil {
			return nil, err
		}
		found = append(found, TranscriptCandidate{Path: path, Side: side, ProjectKey: key, NewestEvent: newest, Size: info.Size(), Lines: lines, SHA256: hash})
	}
	return found, nil
}

// scanTranscriptEvents returns the newest "timestamp" across a JSONL file's
// events and the number of non-empty lines. Lines that are not JSON objects
// or lack a parseable timestamp count as lines but contribute no time.
func scanTranscriptEvents(path string) (newest time.Time, lines int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		lines++
		var event struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &event) != nil || event.Timestamp == "" {
			continue
		}
		ts, parseErr := time.Parse(time.RFC3339Nano, event.Timestamp)
		if parseErr != nil {
			continue
		}
		if ts.After(newest) {
			newest = ts
		}
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, 0, err
	}
	return newest, lines, nil
}

// chooseNewestTranscript orders candidates newest-event first, then longest,
// then source before destination, then by path, and returns the first. The
// order is total, so the same files always yield the same choice.
func chooseNewestTranscript(candidates []TranscriptCandidate) (TranscriptCandidate, bool) {
	if len(candidates) == 0 {
		return TranscriptCandidate{}, false
	}
	sorted := append([]TranscriptCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if !a.NewestEvent.Equal(b.NewestEvent) {
			return a.NewestEvent.After(b.NewestEvent)
		}
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		if a.Side != b.Side {
			return a.Side == "source"
		}
		return a.Path < b.Path
	})
	return sorted[0], true
}

// errNoTranscriptCopy is returned when a session with a recorded conversation
// id has no transcript under any candidate key in either account.
var errNoTranscriptCopy = errors.New("no transcript found under any project key")

// selectSwitchTranscript gathers every copy of inst's conversation in the
// source and destination account dirs, picks the newest, and refuses before
// any write when a destination copy that differs from the choice cannot be
// proven older. archiveDestination authorizes replacing such a copy anyway;
// it also makes the switch prefer the source side when the destination holds
// the newer copy, which is the pre-existing meaning of --archive-destination
// ("install my source conversation, archive whatever is there").
func selectSwitchTranscript(inst *Instance, sourceDir, targetDir string, archiveDestination bool) (*TranscriptChoice, error) {
	sid := strings.TrimSpace(inst.ClaudeSessionID)
	if sid == "" {
		return nil, nil
	}
	if err := validateExactSessionID(sid); err != nil {
		return nil, fmt.Errorf("invalid Claude source identity: %w", err)
	}
	keys := claudeProjectKeyCandidates(inst.EffectiveWorkingDir())
	if len(keys) == 0 {
		return nil, fmt.Errorf("source Claude effective working directory is empty")
	}
	source, err := claudeTranscriptCopies(sourceDir, keys, sid, "source")
	if err != nil {
		return nil, err
	}
	dest, err := claudeTranscriptCopies(targetDir, keys, sid, "destination")
	if err != nil {
		return nil, err
	}
	candidates := append(source, dest...)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: looked for %s.jsonl under %s in %s and %s", errNoTranscriptCopy, sid, strings.Join(keys, ", "), ExpandPath(sourceDir), ExpandPath(targetDir))
	}
	pool := candidates
	if archiveDestination && len(source) > 0 {
		pool = source
	}
	chosen, _ := chooseNewestTranscript(pool)
	choice := &TranscriptChoice{Chosen: chosen, Candidates: candidates}
	if !archiveDestination {
		for _, d := range dest {
			if d.SHA256 == chosen.SHA256 {
				continue
			}
			// Every differing destination copy is about to be replaced. That
			// is only safe when it is provably older than the choice, or the
			// same age and strictly shorter (the choice already won the tie),
			// or an exact byte prefix of it (the install path advances it in
			// place and keeps a .bak- snapshot).
			if d.Size < chosen.Size {
				if isPrefix, prefixErr := strictBytePrefix(d.Path, chosen.Path); prefixErr == nil && isPrefix {
					continue
				}
			}
			if d.NewestEvent.IsZero() || chosen.NewestEvent.IsZero() {
				return nil, fmt.Errorf("%w: %s has no timestamped events to compare with %s; preserving both files and refusing overwrite", ErrSwitchDestinationDivergent, d.Path, chosen.Path)
			}
			if d.NewestEvent.Equal(chosen.NewestEvent) && d.Size >= chosen.Size {
				return nil, fmt.Errorf("%w: %s is as new as and no shorter than %s; preserving both files and refusing overwrite", ErrSwitchDestinationDivergent, d.Path, chosen.Path)
			}
		}
	}
	return choice, nil
}

// switchSourceClaudeDir is the account config dir an account switch exports
// from: the configured dir of the session's named account, else the dir the
// resolver chain gives the instance.
func switchSourceClaudeDir(cfg *UserConfig, inst *Instance) (string, error) {
	dir := strings.TrimSpace(GetClaudeConfigDirForInstance(inst))
	if inst.Account != "" {
		if cfg == nil {
			var err error
			if cfg, err = LoadUserConfig(); err != nil {
				return "", fmt.Errorf("load source Claude account %q: %w", inst.Account, err)
			}
		}
		accountDir := ""
		if cfg != nil {
			accountDir = strings.TrimSpace(cfg.GetProfileClaudeConfigDir(inst.Account))
		}
		if accountDir == "" {
			return "", fmt.Errorf("source Claude account %q has no configured config_dir", inst.Account)
		}
		dir = accountDir
	}
	dir = ExpandPath(dir)
	if dir == "" {
		return "", fmt.Errorf("source Claude config dir is empty")
	}
	return dir, nil
}
