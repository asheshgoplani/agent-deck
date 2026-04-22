package tmux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestNoRawTmuxExec_OutsideAllowlist is the v1.7.55 guard against the #687
// follow-up regression @jcordasco found: v1.7.50 shipped socket isolation
// (config, CLI flag, SQLite persistence) but Session.Attach and several
// other pty.go call sites built their tmux argv by hand — bypassing the
// factory and silently connecting to the user's default server. This lint
// scans every non-test Go file in the module for exec.Command("tmux", ...)
// or exec.CommandContext(ctx, "tmux", ...) and fails the build if any
// appears outside the allowlist.
//
// Adding a new source call site? You almost never want raw exec.Command —
// use tmux.Exec / tmux.ExecContext (package-level) or s.tmuxCmd /
// s.tmuxCmdContext (per-Session). If the call legitimately needs to bypass
// (binary existence check, explicit -S sandbox, internal wrapper that
// already threads the socket itself), add an entry to allowedBypassSites
// below with a one-line reason.
func TestNoRawTmuxExec_OutsideAllowlist(t *testing.T) {
	root := moduleRoot(t)

	// Reason strings are retained in test output for rapid audit: if a new
	// tmux-exec site shows up on PATH and you can justify it, you must add
	// a reason here. No unexplained bypasses.
	//
	// Paths are module-relative. Keep sorted for diffability.
	allowedBypassFiles := map[string]string{
		// The factory itself must call raw exec.Command — it is the ONE
		// place in the codebase authorized to do so.
		"internal/tmux/socket.go": "tmux argv factory — the sanctioned single source of truth",

		// terminal_bridge has its own socket-aware wrapper (tmuxCommand)
		// that handles TMUX env -S fallback — a pattern the package-level
		// tmux.Exec does not cover. Its raw exec.Command calls sit INSIDE
		// that wrapper, not bypassing it.
		"internal/web/terminal_bridge.go": "self-contained socket-aware wrapper (tmuxCommand) with TMUX env -S fallback",

		// Eval harness sandbox deliberately passes `-S <socket-path>` to
		// target its per-test temp socket. That is explicit isolation, not
		// a bypass — and it deliberately does NOT use the factory because
		// the factory emits -L <name>, not -S <path>.
		"tests/eval/harness/sandbox.go": "test harness — explicit -S <socket-path> for per-test sandbox server",
	}

	// Specific (file, call-argv) combos that are legitimate bypasses in
	// otherwise-guarded files. Format: file path (module-relative) ->
	// list of prefix strings that must match the argv literal sequence
	// (first N args) to be allowed.
	allowedBypassCalls := map[string][][]string{
		// `tmux -V` is a binary-existence check. It never touches a tmux
		// server, so the socket name is irrelevant. Keep plain.
		"internal/tmux/tmux.go": {
			{"tmux", "-V"},
		},

		// The CLI "who am I" helpers read $TMUX env to identify the
		// current tmux session. tmux auto-routes via TMUX env when no -L
		// is passed; adding -L based on DefaultSocketName would over-
		// restrict and break users who run `agent-deck session current`
		// from a non-agent-deck tmux pane. Documented at each call site.
		"cmd/agent-deck/cli_utils.go": {
			{"tmux", "display-message", "-p", "#S"},
		},
		"cmd/agent-deck/session_cmd.go": {
			{"tmux", "display-message", "-p", "#{session_name}\t#{pane_current_path}"},
			{"tmux", "display-message", "-p", ""}, // multi-field variant (empty sentinel; matched loosely below)
			{"tmux", "display-message", "-p", "#{session_name}"},
		},
	}

	violations := scanForRawTmuxExec(t, root)

	var unallowed []string
	for _, v := range violations {
		rel, err := filepath.Rel(root, v.file)
		if err != nil {
			rel = v.file
		}
		rel = filepath.ToSlash(rel)

		if _, ok := allowedBypassFiles[rel]; ok {
			continue
		}
		if combos, ok := allowedBypassCalls[rel]; ok && matchesArgvPrefix(v.argv, combos) {
			continue
		}

		unallowed = append(unallowed,
			rel+":"+itoa(v.line)+": exec."+v.fn+"(\"tmux\", "+strings.Join(v.argv[1:], ", ")+")")
	}

	sort.Strings(unallowed)

	if len(unallowed) > 0 {
		t.Fatalf(
			"%d unauthorized raw tmux exec call site(s) — use s.tmuxCmd / tmux.Exec / tmux.ExecContext instead, "+
				"or add to allowedBypassFiles / allowedBypassCalls with justification:\n  %s\n\n"+
				"Context: #687 follow-up. A bypass silently defeats socket isolation.",
			len(unallowed), strings.Join(unallowed, "\n  "))
	}
}

// tmuxExecSite is a single raw-exec.Command-of-tmux site found by the AST walk.
type tmuxExecSite struct {
	file string
	line int
	fn   string   // "Command" or "CommandContext"
	argv []string // argv[0] = "tmux"; rest are the literal string args (non-literals render as "<expr>")
}

func scanForRawTmuxExec(t *testing.T, root string) []tmuxExecSite {
	t.Helper()

	var sites []tmuxExecSite
	err := walkGoFiles(root, func(path string) error {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			// Tests are allowed to drive tmux directly.
			return nil
		}
		// Skip vendored and .git paths; walkGoFiles already filters, belt-
		// and-suspenders.
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(filepath.ToSlash(rel), ".worktrees/") ||
			strings.HasPrefix(filepath.ToSlash(rel), "vendor/") {
			return nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			// Parser errors are a real problem but not this test's concern.
			return nil
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "exec" {
				return true
			}

			var argStart int
			switch sel.Sel.Name {
			case "Command":
				argStart = 0
			case "CommandContext":
				argStart = 1 // skip ctx
			default:
				return true
			}

			if len(call.Args) <= argStart {
				return true
			}
			first, ok := stringLiteral(call.Args[argStart])
			if !ok || first != "tmux" {
				return true
			}

			argv := make([]string, 0, len(call.Args)-argStart)
			for _, a := range call.Args[argStart:] {
				if s, ok := stringLiteral(a); ok {
					argv = append(argv, s)
				} else {
					argv = append(argv, "<expr>")
				}
			}

			pos := fset.Position(call.Pos())
			sites = append(sites, tmuxExecSite{
				file: path,
				line: pos.Line,
				fn:   sel.Sel.Name,
				argv: argv,
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

// moduleRoot climbs parent dirs from this test file looking for go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(self)
	for {
		if _, err := statFile(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + self)
		}
		dir = parent
	}
}
