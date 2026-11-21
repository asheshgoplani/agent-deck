package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
)

const (
	checkpointVersion    = 1
	checkpointPatchLimit = 8 << 20
	checkpointNoteLimit  = 16 << 10
	checkpointFileLimit  = 12 << 20
)

// TaskCheckpoint stores a net tracked patch relative to one exact Git commit.
type TaskCheckpoint struct {
	Version int    `json:"version"`
	Base    string `json:"base"`
	Patch   []byte `json:"patch"`
	Note    string `json:"note,omitempty"`
}

// CaptureTaskCheckpoint captures staged and unstaged tracked changes as one patch.
func CaptureTaskCheckpoint(ctx context.Context, dir, note string) (TaskCheckpoint, error) {
	root, err := checkpointRoot(ctx, dir)
	if err != nil {
		return TaskCheckpoint{}, err
	}
	if err := checkCheckpointFilters(ctx, root, false); err != nil {
		return TaskCheckpoint{}, err
	}
	if err := refuseCheckpointGitOperation(ctx, root); err != nil {
		return TaskCheckpoint{}, err
	}
	entries, err := checkpointGit(ctx, root, nil, "ls-files", "-v", "-z")
	if err != nil {
		return TaskCheckpoint{}, err
	}
	for entry := range bytes.SplitSeq(entries, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		switch entry[0] {
		case 'S', 's', 'h', 'M':
			return TaskCheckpoint{}, errors.New("checkpoint requires an unmerged index without sparse or assume-unchanged entries")
		}
	}
	head, err := checkpointGit(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return TaskCheckpoint{}, fmt.Errorf("resolve checkpoint base: %w", err)
	}
	base := strings.TrimSpace(string(head))
	patch, err := checkpointGit(ctx, root, nil, "diff", "--patch", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--no-relative", "--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/", base, "--")
	if err != nil {
		return TaskCheckpoint{}, fmt.Errorf("capture tracked changes: %w", err)
	}
	current, err := checkpointGit(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !bytes.Equal(head, current) {
		return TaskCheckpoint{}, errors.New("source HEAD changed while saving the checkpoint")
	}
	c := TaskCheckpoint{Version: checkpointVersion, Base: base, Patch: patch, Note: note}
	_, err = c.Paths(ctx)
	return c, err
}

// WriteTaskCheckpoint creates a private file and refuses to overwrite one.
func WriteTaskCheckpoint(filename string, c TaskCheckpoint) (err error) {
	f, err := os.CreateTemp(filepath.Dir(filename), ".agent-deck-checkpoint-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(f.Name())) }()
	writeErr := json.NewEncoder(f).Encode(c)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Link(f.Name(), filename)

}

// ReadTaskCheckpoint reads one bounded checkpoint and returns its file checksum.
func ReadTaskCheckpoint(filename string) (TaskCheckpoint, string, error) {
	var c TaskCheckpoint
	data, err := readCheckpointRegular(filename, checkpointFileLimit)
	if err != nil {
		return c, "", err
	}
	if !utf8.Valid(data) {
		return c, "", errors.New("checkpoint file must be UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decodeCheckpoint(decoder, &c); err != nil {
		return c, "", fmt.Errorf("decode checkpoint: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return c, "", errors.New("unexpected data after checkpoint")
	}
	return c, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// ReadTaskCheckpointNote reads a bounded regular file for a task handoff.
func ReadTaskCheckpointNote(filename string) (string, error) {
	if filename == "" {
		return "", nil
	}
	data, err := readCheckpointRegular(filename, checkpointNoteLimit)
	return string(data), err
}

func readCheckpointRegular(filename string, limit int64) ([]byte, error) {
	f, err := openScriptFileForHashing(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("checkpoint input exceeds %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("checkpoint input exceeds %d bytes", limit)
	}
	return data, nil
}

func decodeCheckpoint(d *json.Decoder, c *TaskCheckpoint) error {
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return errors.New("expected a checkpoint object")
	}
	fields := map[string]any{"version": &c.Version, "base": &c.Base, "patch": &c.Patch, "note": &c.Note}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return err
		}
		key, _ := token.(string)
		value, ok := fields[key]
		if !ok {
			return errors.New("unknown or duplicate checkpoint field")
		}
		if err := d.Decode(value); err != nil {
			return err
		}
		delete(fields, key)
	}
	if _, err := d.Token(); err != nil {
		return err
	}
	for _, key := range []string{"version", "base", "patch"} {
		if _, ok := fields[key]; ok {
			return fmt.Errorf("missing checkpoint field %q", key)
		}
	}
	return nil
}

// Paths validates the checkpoint using the same Git patch parser as restore.
func (c TaskCheckpoint) Paths(ctx context.Context) ([]string, error) {
	if c.Version != checkpointVersion {
		return nil, errors.New("unsupported checkpoint version")
	}
	if len(c.Base) != 40 && len(c.Base) != 64 {
		return nil, errors.New("checkpoint base must be a full Git commit ID")
	}
	if _, err := hex.DecodeString(c.Base); err != nil || strings.ToLower(c.Base) != c.Base {
		return nil, errors.New("invalid checkpoint base")
	}
	if len(c.Note) > checkpointNoteLimit || !utf8.ValidString(c.Note) {
		return nil, errors.New("checkpoint note must be UTF-8 and at most 16 KiB")
	}
	if len(c.Patch) == 0 {
		return nil, nil
	}
	if len(c.Patch) > checkpointPatchLimit || !utf8.Valid(c.Patch) || bytes.IndexByte(c.Patch, 0) >= 0 {
		return nil, errors.New("checkpoint patch must be UTF-8 text and at most 8 MiB")
	}
	stats, err := checkpointGit(ctx, "", c.Patch, "apply", "--numstat", "-z", "-p1", "-")
	if err != nil {
		return nil, fmt.Errorf("read checkpoint paths: %w", err)
	}
	if len(stats) == 0 || stats[len(stats)-1] != 0 {
		return nil, errors.New("checkpoint patch contains no recognized paths")
	}
	var paths []string
	seen := make(map[string]bool)
	for _, record := range bytes.Split(stats[:len(stats)-1], []byte{0}) {
		fields := strings.SplitN(string(record), "\t", 3)
		if len(fields) != 3 || !checkpointPathOK(fields[2]) {
			return nil, errors.New("unsupported checkpoint patch path or rename")
		}
		for _, count := range fields[:2] {
			if _, err := strconv.ParseUint(count, 10, 64); err != nil {
				return nil, errors.New("binary patches are not supported")
			}
		}
		if strings.EqualFold(path.Base(fields[2]), ".gitattributes") {
			return nil, errors.New("checkpoints do not support changes to Git attributes")
		}
		if seen[fields[2]] {
			return nil, errors.New("duplicate checkpoint patch path")
		}
		seen[fields[2]] = true
		paths = append(paths, fields[2])
	}
	summary, err := checkpointGit(ctx, "", c.Patch, "apply", "--summary", "-p1", "-")
	if err != nil {
		return nil, fmt.Errorf("read checkpoint patch summary: %w", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(summary)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "create mode 100644 ") && !strings.HasPrefix(line, "delete mode 100644 ") {
			return nil, errors.New("checkpoint supports regular text files without mode changes")
		}
	}
	// Git numstat reports submodule changes as text. Reject non-regular index
	// modes as well as modes already reported by Git's summary.
	for line := range bytes.SplitSeq(c.Patch, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("index ")) {
			parts := bytes.Fields(line)
			if len(parts) == 3 && string(parts[2]) != "100644" && string(parts[2]) != "100755" {
				return nil, errors.New("checkpoint does not support symbolic links or submodules")
			}
		}
	}
	return paths, nil
}

func checkpointPathOK(name string) bool {
	if name == "." || name == "" || !utf8.ValidString(name) || path.IsAbs(name) || path.Clean(name) != name ||
		strings.ContainsAny(name, "\\:") || strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) }) {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// RestoreTaskCheckpoint applies a validated checkpoint in a new worktree.
func RestoreTaskCheckpoint(ctx context.Context, dir, target string, c TaskCheckpoint) (err error) {
	paths, err := c.Paths(ctx)
	if err != nil {
		return err
	}
	root, err := checkpointRoot(ctx, dir)
	if err != nil {
		return err
	}
	base, err := checkpointGit(ctx, root, nil, "rev-parse", "--verify", c.Base+"^{commit}")
	if err != nil || strings.TrimSpace(string(base)) != c.Base {
		return errors.New("checkpoint base commit is not available locally")
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("resolve restore parent: %w", err)
	}
	target = filepath.Join(parent, filepath.Base(target))
	if err := checkCheckpointTarget(ctx, root, target); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return fmt.Errorf("reserve new restore target: %w", err)
	}
	reserved, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("inspect reserved restore target: %w", err)
	}
	added := false
	defer func() {
		if err == nil {
			return
		}
		current, statErr := os.Lstat(target)
		if errors.Is(statErr, os.ErrNotExist) && !added {
			return
		}
		if statErr != nil || !os.SameFile(reserved, current) {
			err = errors.Join(err, fmt.Errorf("restore target %q changed during operation; inspect it before cleanup", target))
			return
		}
		if added {
			// A canceled apply still needs bounded cleanup.
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointCleanupTimeout)
			defer cancel()
			if _, removeErr := checkpointGit(cleanup, root, nil, "worktree", "remove", "--force", "--", target); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("could not remove failed restore at %q: %w", target, removeErr))
			}
			return
		}
		// A failed add must not force-remove a Git registration. Remove
		// only the empty directory we reserved and report any residue.
		if removeErr := os.Remove(target); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("inspect incomplete restore at %q: %w", target, removeErr))
		}
	}()
	if _, err = checkpointGit(ctx, root, nil, "worktree", "add", "--detach", "--quiet", "--no-checkout", "--", target, c.Base); err != nil {
		return fmt.Errorf("create restore worktree: %w", err)
	}
	added = true
	// Materialize only the index first, then inspect the base tree's
	// attributes before any checkout can invoke a filter.
	if _, err = checkpointGit(ctx, target, nil, "read-tree", c.Base); err != nil {
		return fmt.Errorf("load restore index: %w", err)
	}
	if err = checkCheckpointFilters(ctx, target, true, paths...); err != nil {
		return err
	}
	if _, err = checkpointGit(ctx, target, nil, "checkout-index", "--all", "--index"); err != nil {
		return fmt.Errorf("check out restore base: %w", err)
	}
	for _, name := range paths {
		if err = checkCheckpointDestination(target, name); err != nil {
			return err
		}
	}
	if len(paths) == 0 {
		return nil
	}
	if _, err = checkpointGit(ctx, target, c.Patch, "apply", "--check", "--index", "--whitespace=nowarn", "-p1", "-"); err != nil {
		return fmt.Errorf("checkpoint does not apply to its base: %w", err)
	}
	if _, err = checkpointGit(ctx, target, c.Patch, "apply", "--whitespace=nowarn", "-p1", "-"); err != nil {
		return err
	}
	for _, name := range paths {
		if err = checkCheckpointDestination(target, name); err != nil {
			return err
		}
	}
	return nil
}

func checkCheckpointTarget(ctx context.Context, root, target string) error {
	list, err := checkpointGit(ctx, root, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return fmt.Errorf("check existing worktrees: %w", err)
	}
	for record := range strings.SplitSeq(string(list), "\x00") {
		worktree, ok := strings.CutPrefix(record, "worktree ")
		if !ok {
			continue
		}
		if filepath.Clean(worktree) == target {
			return errors.New("restore path is already registered as a worktree")
		}
		worktreeInfo, err := os.Stat(worktree)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect existing worktree: %w", err)
		}
		for ancestor := filepath.Dir(target); ; ancestor = filepath.Dir(ancestor) {
			info, err := os.Stat(ancestor)
			if err != nil {
				return fmt.Errorf("inspect restore parent: %w", err)
			}
			if os.SameFile(worktreeInfo, info) {
				return errors.New("restore target must be outside existing worktrees")
			}
			if filepath.Dir(ancestor) == ancestor {
				break
			}
		}
	}
	return nil
}

func refuseCheckpointGitOperation(ctx context.Context, root string) error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply"} {
		out, err := checkpointGit(ctx, root, nil, "rev-parse", "--path-format=absolute", "--git-path", name)
		if err != nil {
			return fmt.Errorf("check active Git operation: %w", err)
		}
		if _, err := os.Lstat(strings.TrimSuffix(string(out), "\n")); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return errors.New("finish the active Git operation before saving a checkpoint")
		}
	}
	return nil
}

func checkCheckpointDestination(root, name string) error {
	parts := strings.Split(name, "/")
	local := root
	for i, part := range parts {
		local = filepath.Join(local, part)
		info, err := os.Lstat(local)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if i == len(parts)-1 && info.Mode().IsRegular() || i < len(parts)-1 && info.IsDir() {
			// On case-insensitive filesystems a successfully opened path
			// can spell a different tracked file or directory.
			names, err := os.ReadDir(filepath.Dir(local))
			if err != nil {
				return err
			}
			exact := false
			for _, entry := range names {
				if entry.Name() == part {
					exact = true
					break
				}
			}
			if exact {
				continue
			}
		}
		return fmt.Errorf("checkpoint path %q conflicts with a non-regular entry", name)
	}
	return nil
}

func checkpointRoot(ctx context.Context, dir string) (string, error) {
	out, err := checkpointGit(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree: %w", err)
	}
	return filepath.EvalSymlinks(strings.TrimSuffix(string(out), "\n"))
}

func checkCheckpointFilters(ctx context.Context, dir string, cached bool, extraPaths ...string) error {
	// check-attr uses these literal labels for absent attributes. A
	// driver with either reserved name would be indistinguishable.
	reserved, err := checkpointGit(ctx, dir, nil, "config", "--name-only", "--get-regexp", "^filter\\.(unspecified|unset)\\.")
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
		return fmt.Errorf("check Git filter names: %w", err)
	}
	if len(reserved) != 0 {
		return errors.New("checkpoint does not support checkout filters named unset or unspecified")
	}
	files, err := checkpointGit(ctx, dir, nil, "ls-files", "-z")
	if err != nil {
		return fmt.Errorf("list tracked paths for Git attribute check: %w", err)
	}
	for _, path := range extraPaths {
		files = append(files, path...)
		files = append(files, 0)
	}
	if len(files) == 0 {
		return nil
	}
	args := []string{"check-attr", "-z", "--stdin"}
	if cached {
		args = append(args, "--cached")
	}
	out, err := checkpointGit(ctx, dir, files, append(args, "filter")...)
	if err != nil {
		return fmt.Errorf("check tracked Git attributes: %w", err)
	}
	fields := bytes.Split(out, []byte{0})
	if len(fields)%3 != 1 || len(fields[len(fields)-1]) != 0 || len(fields) != bytes.Count(files, []byte{0})*3+1 {
		return errors.New("unexpected Git attribute output")
	}
	for i := 2; i < len(fields); i += 3 {
		switch string(fields[i]) {
		case "unspecified", "unset":
		default:
			return errors.New("checkpoint does not support checkout filters on tracked files")
		}
	}
	return nil
}

const checkpointCleanupTimeout = 10 * time.Second

func checkpointGit(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	opts := []string{"--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "submodule.recurse=false"}
	// G204: only fixed Git operations reach this helper. The base is a full
	// hex object ID, worktree paths follow --, and patch data goes to stdin.
	cmd := exec.CommandContext(ctx, "git", append(opts, args...)...) //nolint:gosec // G204: fixed Git operations and validated arguments, no shell
	if dir == "" {
		dir = os.TempDir()
	}
	cmd.Dir = dir
	for _, entry := range childenv.ForLaunch("") {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "LC_ALL=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	stdout := cmdOutputLimit{}
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.buf.Bytes(), nil
}

type cmdOutputLimit struct{ buf bytes.Buffer }

func (b *cmdOutputLimit) Write(p []byte) (int, error) {
	if len(p) > checkpointPatchLimit-b.buf.Len() {
		return 0, errors.New("Git output exceeds checkpoint size limit")
	}
	return b.buf.Write(p)
}
