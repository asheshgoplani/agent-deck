package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// SparseCheckoutState is a snapshot of one worktree's sparse-checkout
// configuration, taken so a newly created worktree can be materialized with the
// same sparsity instead of checking out the whole repository first (issue #1708).
type SparseCheckoutState struct {
	// Enabled is true only when the captured worktree is genuinely sparse and
	// has at least one pattern to replay. The zero value therefore means
	// "nothing to inherit", which every caller treats as today's behavior.
	Enabled bool
	// Cone mirrors core.sparseCheckoutCone: cone mode takes directory names,
	// non-cone mode takes gitignore-style patterns, and the two are replayed
	// with --cone / --no-cone respectively.
	Cone bool
	// SparseIndex mirrors index.sparse. Git only supports a sparse index in
	// cone mode, so it is never captured for a non-cone source.
	SparseIndex bool
	// Patterns is `git sparse-checkout list` output, one entry per line.
	Patterns []string
}

// CaptureSparseCheckout snapshots the sparse-checkout state of dir.
//
// dir MUST be the worktree the operation was invoked from — never
// GetWorktreeBaseRoot(dir). Sparse-checkout state is worktree-specific: git
// enables extensions.worktreeConfig and stores core.sparseCheckout in
// <gitdir>/config.worktree, and keeps the patterns in
// <gitdir>/worktrees/<id>/info/sparse-checkout. Reading from the normalized base
// root therefore inherits the MAIN worktree's sparsity (usually none) rather
// than the invoking worktree's, which is exactly the bug that makes
// `git -C <base-root> worktree add` materialize the full tree for a session
// running in a sparse linked worktree.
//
// Every failure mode — empty dir, a bare project root with no working tree, a
// non-sparse worktree, an unreadable pattern list — yields the zero value, so
// inheritance silently degrades to git's normal checkout instead of breaking
// worktree creation.
func CaptureSparseCheckout(dir string) SparseCheckoutState {
	if strings.TrimSpace(dir) == "" {
		return SparseCheckoutState{}
	}
	if !gitConfigBool(dir, "core.sparseCheckout") {
		return SparseCheckoutState{}
	}
	// `sparse-checkout list` fails with "this worktree is not sparse" (and on a
	// bare repo with "must be run in a work tree"), which is the authoritative
	// answer for dirs whose config says otherwise.
	out, err := exec.Command("git", "-C", dir, "sparse-checkout", "list").Output()
	if err != nil {
		return SparseCheckoutState{}
	}
	var patterns []string
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			patterns = append(patterns, p)
		}
	}
	// An enabled-but-empty pattern set would produce a worktree with no files at
	// all. Refuse to inherit that: a normal checkout is the safer outcome.
	if len(patterns) == 0 {
		return SparseCheckoutState{}
	}
	cone := gitConfigBool(dir, "core.sparseCheckoutCone")
	return SparseCheckoutState{
		Enabled:     true,
		Cone:        cone,
		SparseIndex: cone && gitConfigBool(dir, "index.sparse"),
		Patterns:    patterns,
	}
}

// ApplySparseCheckout installs st into worktreePath — which must have just been
// created by `git worktree add --no-checkout` — and then materializes it.
//
// The trailing `git checkout` is load-bearing. After --no-checkout the new
// worktree's index is empty and `git sparse-checkout set` only writes the
// pattern file plus config, so without it the worktree would be left with no
// files at all (every tracked path staged as deleted).
//
// Callers must treat an error here as fatal for the worktree: it is at that
// point half-initialized and has to be cleaned up.
func ApplySparseCheckout(worktreePath string, st SparseCheckoutState) error {
	if !st.Enabled {
		return nil
	}
	args := []string{"-C", worktreePath, "sparse-checkout", "set"}
	if st.Cone {
		args = append(args, "--cone")
		// Only meaningful in cone mode (see SparseCheckoutState.SparseIndex);
		// passed explicitly in both directions so the new worktree matches the
		// source instead of whatever git's default happens to be.
		if st.SparseIndex {
			args = append(args, "--sparse-index")
		} else {
			args = append(args, "--no-sparse-index")
		}
	} else {
		args = append(args, "--no-cone")
	}
	args = append(args, "--stdin")

	cmd := exec.Command("git", args...)
	cmd.Stdin = strings.NewReader(strings.Join(st.Patterns, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sparse-checkout set: %s: %w", strings.TrimSpace(string(out)), err)
	}

	if out, err := exec.Command("git", "-C", worktreePath, "checkout").CombinedOutput(); err != nil {
		return fmt.Errorf("checkout sparse worktree: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// gitConfigBool reports whether dir's effective git config sets key to a true
// boolean. Unset keys, unreadable config, and non-boolean values are false.
func gitConfigBool(dir, key string) bool {
	out, err := exec.Command("git", "-C", dir, "config", "--type=bool", "--get", key).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}
