package tmux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// pollSubcommands are the read-only tmux queries agent-deck fires on a
// cadence. Every one of them was observed spinning at ~100% CPU in the
// 2026-07-21 incident: a tmux 3.0a client leaks an epoll fd per event-loop
// iteration, and once it hits RLIMIT_NOFILE (1024) epoll_create returns
// EMFILE forever. The client retries without backoff, so it burns a core in
// pure system time (utime=0, stime=30298 on a 22-minute-old capture-pane)
// and can never exit on its own.
//
// The server is NOT wedged when this happens — a fresh `tmux ls` returns in
// milliseconds. Only clients that have already leaked their fd table are
// stuck, which is why the poller kept spawning replacements that each got
// stuck in turn: 14 live spinners, ~590% CPU, load average 28 on 8 cores.
//
// A deadline is the only defense that works, because the stuck client is
// unreachable by any in-band means. exec.CommandContext SIGKILLs it at
// tmuxPollTimeout, capping the damage at 3 core-seconds per leak.
var pollSubcommands = map[string]struct{}{
	"capture-pane":     {},
	"display-message":  {},
	"has-session":      {},
	"list-clients":     {},
	"list-panes":       {},
	"list-sessions":    {},
	"show-environment": {},
}

// TestPollCommandsAreBounded fails the build if any cadence tmux query is
// spawned through an unbounded factory (tmuxExec / s.tmuxCmd / tmux.Exec)
// instead of a deadline-carrying one (runBoundedOutput / runBoundedRun /
// OutputBounded / tmuxExecContext / s.tmuxCmdContext).
//
// This is the companion to TestNoRawTmuxExec_OutsideAllowlist: that lint
// enforces WHICH SERVER a command talks to, this one enforces that the
// command is guaranteed to TERMINATE. v1.7.56 ("Part A") bounded some of
// these sites by hand and the rest silently stayed unbounded — hence a lint
// rather than a comment.
//
// Adding a new call site? Use the bounded helper. If a command genuinely
// must run unbounded (an interactive attach, a blocking pipe-pane), add it
// to allowedUnbounded with a one-line reason.
func TestPollCommandsAreBounded(t *testing.T) {
	root := moduleRoot(t)

	// Module-relative file path -> reason the file's poll calls may be
	// unbounded. Keep sorted for diffability.
	allowedUnbounded := map[string]string{
		// The bounded helpers themselves call the unbounded factory — that is
		// the whole point of the indirection.
		"internal/tmux/socket.go": "defines the bounded helpers; its factory calls are the sanctioned unbounded layer",
	}

	violations := scanForUnboundedPolls(t, root)

	var unallowed []string
	for _, v := range violations {
		rel, err := filepath.Rel(root, v.file)
		if err != nil {
			rel = v.file
		}
		rel = filepath.ToSlash(rel)

		if _, ok := allowedUnbounded[rel]; ok {
			continue
		}
		unallowed = append(unallowed, rel+":"+itoa(v.line)+": "+v.callee+"(… \""+v.sub+"\" …)")
	}

	sort.Strings(unallowed)

	if len(unallowed) > 0 {
		t.Fatalf(
			"%d unbounded cadence tmux poll(s) — a tmux client that leaks its fd table "+
				"spins at 100%% CPU forever and cannot be reaped in-band. Use "+
				"runBoundedOutput / runBoundedRun (in-package), OutputBounded (external), "+
				"or thread a context via tmuxExecContext / s.tmuxCmdContext:\n  %s",
			len(unallowed), strings.Join(unallowed, "\n  "))
	}
}

// unboundedPollSite is one cadence query spawned without a deadline.
type unboundedPollSite struct {
	file   string
	line   int
	callee string // "tmuxExec", "s.tmuxCmd", "tmux.Exec"
	sub    string // the tmux subcommand literal
}

func scanForUnboundedPolls(t *testing.T, root string) []unboundedPollSite {
	t.Helper()

	var sites []unboundedPollSite
	err := walkGoFiles(root, func(path string) error {
		if strings.HasSuffix(filepath.Base(path), "_test.go") {
			// Tests drive tmux directly and are torn down by the harness.
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		slash := filepath.ToSlash(rel)
		if strings.HasPrefix(slash, ".worktrees/") ||
			strings.HasPrefix(slash, "vendor/") ||
			strings.HasPrefix(slash, "tests/") ||
			strings.HasPrefix(slash, "internal/testutil/") {
			return nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			callee, subIdx, ok := unboundedTmuxCallee(call.Fun)
			if !ok || len(call.Args) <= subIdx {
				return true
			}
			// exec.Command is generic: only flag it when argv[0] is literally
			// "tmux", or every exec.Command whose second arg happens to collide
			// with a tmux subcommand name would be reported.
			if callee == "exec.Command" {
				if bin, isLit := stringLiteral(call.Args[0]); !isLit || bin != "tmux" {
					return true
				}
			}
			sub, ok := stringLiteral(call.Args[subIdx])
			if !ok {
				return true
			}
			if _, isPoll := pollSubcommands[sub]; !isPoll {
				return true
			}

			sites = append(sites, unboundedPollSite{
				file:   path,
				line:   fset.Position(call.Pos()).Line,
				callee: callee,
				sub:    sub,
			})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	return sites
}

// unboundedTmuxCallee reports whether fun is one of the deadline-free tmux
// command factories, and at which argument index the subcommand literal sits
// (the package-level factories take a socket name first; the Session method
// does not).
//
// The *Context variants (tmuxExecContext, s.tmuxCmdContext, tmux.ExecContext,
// exec.CommandContext, tmuxCommandContext) are all treated as bounded. That is
// a deliberate simplification: a context argument is assumed to carry a
// deadline. This lint does not verify that — a context.Background() passed
// straight through would slip by. Every such call site was audited by hand when
// this lint was written; if you add one, give it a real timeout.
func unboundedTmuxCallee(fun ast.Expr) (name string, subIdx int, ok bool) {
	switch fn := fun.(type) {
	case *ast.Ident:
		switch fn.Name {
		case "tmuxExec":
			// In-package: tmuxExec(socket, "list-sessions", …)
			return "tmuxExec", 1, true
		case "tmuxCommand":
			// internal/web's private socket-aware wrapper, which the
			// raw-exec lint allowlists wholesale:
			//   tmuxCommand(socketName, "has-session", …)
			return "tmuxCommand", 1, true
		}
	case *ast.SelectorExpr:
		switch fn.Sel.Name {
		case "tmuxCmd":
			// Per-Session: s.tmuxCmd("capture-pane", …)
			return "s.tmuxCmd", 0, true
		case "Exec":
			// External packages: tmux.Exec(socketName, "list-panes", …)
			if pkg, isIdent := fn.X.(*ast.Ident); isIdent && pkg.Name == "tmux" {
				return "tmux.Exec", 1, true
			}
		case "Command":
			// Raw exec.Command("tmux", "display-message", …). These bypass the
			// argv factory entirely — TestNoRawTmuxExec_OutsideAllowlist
			// permits a handful for socket-routing reasons, and that allowance
			// silently exempted them from the deadline rule too. It must not:
			// the fd-leak spin is a property of the tmux CLIENT, so a plain-argv
			// client wedges exactly like a -L one.
			if pkg, isIdent := fn.X.(*ast.Ident); isIdent && pkg.Name == "exec" {
				return "exec.Command", 1, true
			}
		}
	}
	return "", 0, false
}
