package tmux

// Regression tests for #1694: OpenCode renders clipped under agent-deck, but
// not in plain iTerm/WezTerm and not under native tmux.
//
// Root cause: `Session.startCommandSpec` created the session detached with no
// -x/-y, so the window was born at tmux's 80x24 default-size. agent-deck runs
// the tool as the pane's INITIAL process, so the tool's first frames land in an
// 80-column window; the attach client arrives later at the real terminal size
// and window-size=largest grows the window, but a TUI that fixed its layout on
// frame one keeps drawing the 80-column layout into the wider pane. Native
// tmux never shows it because `tmux new-session` without -d is born at the
// attached client's real size.
//
// #1167 fixed the sibling half of this (the ATTACH client's PTY size, see
// StartAttachPTY) and its comment already named the 80x24 birth default — the
// creation side was never given the same treatment.
//
// Coverage here, cheapest first:
//   - InitialWindowSize: TTY, floor, and headless-fallback branches.
//   - startCommandSpec: the argv contract (-x/-y present, values from the
//     probe) in both the plain and initial-process forms.
//   - a real tmux server: the window is BORN at the requested size, checked
//     before any client attaches. This is the test that fails on pre-fix code
//     with window_width == 80.
//   - a repo-wide lint so no future `new-session` spawn omits the size again.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinInitialWindowSize forces the terminal-size probe to a fixed answer for the
// duration of the test, so argv assertions do not depend on whether the test
// binary happens to own a terminal. hasTTY=false exercises the headless branch.
func pinInitialWindowSize(t *testing.T, cols, rows int, hasTTY bool) {
	t.Helper()
	prev := terminalSizeProbe
	terminalSizeProbe = func() (int, int, bool) { return cols, rows, hasTTY }
	t.Cleanup(func() { terminalSizeProbe = prev })
}

func TestInitialWindowSize_UsesRealTerminalSize(t *testing.T) {
	pinInitialWindowSize(t, 214, 57, true)

	cols, rows := InitialWindowSize()
	assert.Equal(t, 214, cols, "birth width must be the real terminal width so the first paint is not clipped (#1694)")
	assert.Equal(t, 57, rows)
}

// A host terminal smaller than tmux's own default must not birth an even more
// clipped pane: 80x24 is the floor. window-size=largest shrinks the window to
// the real client on attach anyway.
func TestInitialWindowSize_FloorsAtTmuxDefault(t *testing.T) {
	pinInitialWindowSize(t, 40, 10, true)

	cols, rows := InitialWindowSize()
	assert.Equal(t, tmuxDefaultCols, cols)
	assert.Equal(t, tmuxDefaultRows, rows)
}

// No controlling terminal (web/xterm.js sessions, watchers, cron, redirected
// output) must not silently fall back to 80x24 — that is the bug. The fallback
// is deliberately wider than any realistic client.
func TestInitialWindowSize_HeadlessFallbackIsGenerous(t *testing.T) {
	pinInitialWindowSize(t, 0, 0, false)

	cols, rows := InitialWindowSize()
	assert.Equal(t, headlessInitialCols, cols)
	assert.Equal(t, headlessInitialRows, rows)
	assert.Greater(t, cols, tmuxDefaultCols,
		"a headless birth size of 80 columns is the #1694 bug, not a fallback")
}

// TestStartCommandSpec_CarriesBirthSize pins the production argv contract in
// both spawn forms. On pre-fix code both subtests fail: no -x/-y at all.
func TestStartCommandSpec_CarriesBirthSize(t *testing.T) {
	for _, tc := range []struct {
		name             string
		initialProcess   bool
		command          string
		probeCols        int
		probeRows        int
		hasTTY           bool
		wantCols         string
		wantRows         string
		wantTrailingArgs []string
	}{
		{
			name:      "shell pane on a real terminal",
			probeCols: 214, probeRows: 57, hasTTY: true,
			wantCols: "214", wantRows: "57",
		},
		{
			name:           "tool as initial process, headless",
			initialProcess: true,
			command:        "opencode",
			hasTTY:         false,
			wantCols:       strconv.Itoa(headlessInitialCols),
			wantRows:       strconv.Itoa(headlessInitialRows),
			// The #1567/#1580 argv-token delivery must stay intact and stay LAST.
			wantTrailingArgs: []string{"bash", "-c", "opencode"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinInitialWindowSize(t, tc.probeCols, tc.probeRows, tc.hasTTY)

			s := &Session{
				Name:                       "agentdeck_test-1694_1234abcd",
				WorkDir:                    "/tmp/project",
				RunCommandAsInitialProcess: tc.initialProcess,
			}
			launcher, args := s.startCommandSpec("/tmp/project", tc.command)
			require.Equal(t, "tmux", launcher)

			x := indexOfArg(args, "-x")
			y := indexOfArg(args, "-y")
			require.NotEqual(t, -1, x, "new-session must carry -x; without it the window is born at tmux's 80-column default (#1694). argv: %v", args)
			require.NotEqual(t, -1, y, "new-session must carry -y (#1694). argv: %v", args)
			require.Less(t, x+1, len(args))
			require.Less(t, y+1, len(args))
			assert.Equal(t, tc.wantCols, args[x+1])
			assert.Equal(t, tc.wantRows, args[y+1])

			if len(tc.wantTrailingArgs) > 0 {
				assert.Equal(t, tc.wantTrailingArgs, args[len(args)-len(tc.wantTrailingArgs):],
					"the bash -c COMMAND argv tokens must remain the LAST args (#1567/#1580)")
			}
		})
	}
}

func indexOfArg(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

// TestStartCommandSpec_WindowIsBornAtRequestedSize runs the exact production
// arg vector against a private tmux server and reads the window width BEFORE
// any client attaches — i.e. the size the tool's first frame is painted into.
// Pre-fix this reports 80 (tmux's default-size) no matter how wide the
// terminal is; that 80 is what OpenCode locked its layout to.
func TestStartCommandSpec_WindowIsBornAtRequestedSize(t *testing.T) {
	skipIfNoTmuxBinary(t)

	socket := privateSocketName1694(t)
	pinInitialWindowSize(t, 173, 41, true)

	s := &Session{
		Name:       "birthsize",
		SocketName: socket,
		WorkDir:    "/tmp",
	}
	launcher, args := s.startCommandSpec("/tmp", "")
	require.Equal(t, "tmux", launcher, "direct mode expected; argv: %v", args)
	if out, err := exec.Command(launcher, args...).CombinedOutput(); err != nil {
		t.Fatalf("new-session failed: %v\n%s\nargs: %v", err, out, args)
	}

	assert.Equal(t, "173", tmuxDisplay1694(t, socket, "birthsize", "#{window_width}"),
		"the window must be BORN at the terminal width — a pane that starts at tmux's "+
			"80-column default keeps OpenCode's layout clipped even after "+
			"window-size=largest grows it (#1694)")
	assert.Equal(t, "41", tmuxDisplay1694(t, socket, "birthsize", "#{window_height}"))
}

// privateSocketName1694 returns a deterministic -L socket name for this test
// and registers teardown that kills the server on the SAME socket (an env
// mismatch between spawn and cleanup makes kill-server a silent no-op and
// leaks a server plus its ptys — the 2026-07-18 incident).
func privateSocketName1694(t *testing.T) string {
	t.Helper()
	socket := "ad1694-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	if len(socket) > 40 {
		socket = socket[:40]
	}
	kill := func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() }
	kill() // clear anything a previously aborted run stranded
	t.Cleanup(kill)
	return socket
}

func tmuxDisplay1694(t *testing.T, socket, target, format string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "display-message",
		"-p", "-t", target, format).Output()
	if err != nil {
		t.Fatalf("display-message %s: %v", format, err)
	}
	return strings.TrimSpace(string(out))
}

// TestNewSessionSpawnsCarryExplicitSize is the repo-wide lint that keeps #1694
// fixed. Every function in non-test code that spawns `new-session` must also
// name -x and -y; a detached new-session without them is born at 80x24 and any
// TUI that fixes its layout on the first paint stays clipped for the life of
// the pane.
//
// The check is per enclosing function rather than per call expression because
// production argv is assembled across several appends (startCommandSpec).
func TestNewSessionSpawnsCarryExplicitSize(t *testing.T) {
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

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var spawns bool
			var hasX, hasY bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok {
					return true
				}
				switch s, _ := stringLiteral(lit); s {
				case "new-session":
					spawns = true
				case "-x":
					hasX = true
				case "-y":
					hasY = true
				}
				return true
			})
			if spawns && (!hasX || !hasY) {
				violations = append(violations,
					rel+":"+itoa(fset.Position(fn.Pos()).Line)+" ("+fn.Name.Name+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("%d `new-session` spawn(s) without an explicit -x/-y size — the window "+
			"is born at tmux's 80x24 default and a TUI that fixes its layout on the first "+
			"paint renders clipped for the life of the pane (#1694). Pass the size from "+
			"tmux.InitialWindowSize():\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
