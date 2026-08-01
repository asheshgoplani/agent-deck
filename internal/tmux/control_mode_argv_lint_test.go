package tmux

// Repo-wide lint: no tmux control-mode client may be spawned without an
// explicit command.
//
// `tmux -C` with nothing after it is two bugs wearing one coat.
//
// It creates something. A control-mode client with no command falls back to
// `new-session`, so every spawn mints a session with a live shell pane holding
// a pty. `internal/tmux/keysender.go` did this on every insert-mode entry and
// never killed the result; ~26 of those orphans helped drive the host to
// 507/511 ptys on 2026-07-18, after which no process could attach to anything.
//
// And it lies about what it is. On macOS a process keeps the argv it was
// exec'd with, so the default socket's server — auto-started by such a client —
// is itself named exactly `tmux -C`. On 2026-07-26 an hourly maintenance job
// matching `pgrep -fx "tmux -C"` hit that server and all ~65 live agent-deck
// sessions died at once.
//
// The second failure is the reason this lint scans argv shape rather than
// trusting the reaper to be careful: scripts/reap-stale-tmux.sh now matches by
// socket path only, but nothing this repo spawns should be ambiguous in the
// first place. Pass `-u attach-session -t <target>` for a UTF-8 client bound
// to the session.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoBareControlModeTmuxSpawn(t *testing.T) {
	root := moduleRoot(t)

	var violations []string
	err := walkGoFiles(root, func(path string) error {
		if strings.HasSuffix(filepath.Base(path), "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "vendor/") || strings.HasPrefix(rel, ".worktrees/") {
			return nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 || call.Ellipsis.IsValid() {
				return true
			}
			// A trailing "-C" is the signature of a bare control-mode spawn:
			// the flag is there and no command follows it. This is
			// call-shape-agnostic on purpose — tmuxExec, tmuxExecContext,
			// s.tmuxCmd and exec.Command("tmux", ...) all read the same way.
			last, ok := stringLiteral(call.Args[len(call.Args)-1])
			if !ok || last != "-C" {
				return true
			}
			violations = append(violations,
				rel+":"+itoa(fset.Position(call.Pos()).Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("%d bare `tmux -C` spawn site(s) — a control-mode client with no "+
			"command implicitly creates a session (leaking a pty), and its argv is "+
			"indistinguishable from the server it may auto-start. Pass an explicit "+
			"command, e.g. `\"-C\", \"-u\", \"attach-session\", \"-t\", target`:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
