package tmux

import (
	"go/ast"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Helper utilities for TestNoRawTmuxExec_OutsideAllowlist. Kept in a
// separate _test.go so the main lint file stays readable.

func walkGoFiles(root string, visit func(path string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable entries; lint is advisory
		}
		if d.IsDir() {
			name := d.Name()
			// Prune noise + nested worktrees so CI scanning a worktree
			// does not trip on the sibling checkouts.
			switch name {
			case ".git", "node_modules", "vendor", ".worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		return visit(path)
	})
}

func stringLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func statFile(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func itoa(i int) string { return strconv.Itoa(i) }

// matchesArgvPrefix returns true when any combo in the allowlist matches
// argv[0..len(combo)-1]. An empty element in the combo acts as a wildcard
// for that position (used for session_cmd.go's multi-line format string
// whose literal is assembled across source lines).
func matchesArgvPrefix(argv []string, combos [][]string) bool {
	for _, combo := range combos {
		if len(combo) > len(argv) {
			continue
		}
		match := true
		for i, want := range combo {
			if want == "" {
				continue
			}
			if !strings.HasPrefix(argv[i], want) && argv[i] != want {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
