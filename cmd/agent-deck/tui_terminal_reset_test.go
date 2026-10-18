package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func withTerminalResetSink(t *testing.T, tty, running bool) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevTTY, prevRunning := terminalResetOutput, terminalResetIsTTY, tuiRunning.Load()
	terminalResetOutput = &buf
	terminalResetIsTTY = func() bool { return tty }
	tuiRunning.Store(running)
	t.Cleanup(func() {
		terminalResetOutput, terminalResetIsTTY = prevOut, prevTTY
		tuiRunning.Store(prevRunning)
	})
	return &buf
}

func TestReportTUIRunErrorResetsModesWithoutLeavingTheAltScreen(t *testing.T) {
	buf := withTerminalResetSink(t, true, false)
	reportTUIRunError(errors.New("program was killed"))
	out := buf.String()
	if !strings.Contains(out, "\x1b[?1000l") || !strings.Contains(out, "\x1b[?2004l") {
		t.Errorf("Run-error path did not turn the modes off: %q", out)
	}
	if strings.Contains(out, "\x1b[?1049l") {
		t.Errorf("Run-error path sent ?1049l, which restores the cursor over the printed panic: %q", out)
	}
	if !strings.HasSuffix(out, "Error: program was killed\n") {
		t.Errorf("error line missing or not last: %q", out)
	}
}

func TestReportTUIRunErrorWritesNoEscapesWithoutATerminal(t *testing.T) {
	buf := withTerminalResetSink(t, false, false)
	reportTUIRunError(errors.New("boom"))
	if got := buf.String(); got != "Error: boom\n" {
		t.Errorf("non-terminal stdout got %q, want only the error line", got)
	}
}

func TestSignalTerminalCleanup(t *testing.T) {
	cases := []struct {
		name         string
		tty, running bool
		wantReset    bool
	}{
		{"headless or before Run", true, false, false},
		{"TUI running on a terminal", true, true, true},
		{"TUI running, stdout not a terminal", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := withTerminalResetSink(t, tc.tty, tc.running)
			signalTerminalCleanup()
			out := buf.String()
			if !tc.wantReset {
				if out != "" {
					t.Fatalf("wrote %d bytes of escapes: %q", len(out), out)
				}
				return
			}
			if !strings.Contains(out, "\x1b[?1000l") || !strings.HasSuffix(out, "\x1b[?1049l") {
				t.Fatalf("signal reset = %q, want modes off then ?1049l", out)
			}
		})
	}
}

// main() wires the resets: the signal goroutine calls signalTerminalCleanup,
// the Run-error branch calls reportTUIRunError, and tuiRunning is set right
// before p.Run. Removing any of them must fail a test.
func TestMainWiresTheTerminalResets(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := func(n ast.Node, name string) bool {
		found := false
		ast.Inspect(n, func(x ast.Node) bool {
			if c, ok := x.(*ast.CallExpr); ok {
				switch f := c.Fun.(type) {
				case *ast.Ident:
					found = found || f.Name == name
				case *ast.SelectorExpr:
					found = found || f.Sel.Name == name
				}
			}
			return !found
		})
		return found
	}
	var signalWired, runErrWired, runningBeforeRun bool
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.GoStmt:
			if lit, ok := x.Call.Fun.(*ast.FuncLit); ok && strings.Contains(nodeSource(t, fset, lit), "<-sigChan") {
				signalWired = signalWired || calls(lit, "signalTerminalCleanup")
			}
		case *ast.IfStmt:
			if strings.TrimSpace(nodeSource(t, fset, x.Cond)) == "runErr != nil" {
				runErrWired = runErrWired || calls(x.Body, "reportTUIRunError")
			}
		case *ast.BlockStmt:
			for i, st := range x.List {
				if i > 0 && strings.Contains(nodeSource(t, fset, st), "p.Run()") &&
					strings.Contains(nodeSource(t, fset, x.List[i-1]), "tuiRunning.Store(true)") {
					runningBeforeRun = true
				}
			}
		}
		return true
	})
	if !signalWired {
		t.Error("the signal handler goroutine does not call signalTerminalCleanup")
	}
	if !runErrWired {
		t.Error("the p.Run error branch does not call reportTUIRunError")
	}
	if !runningBeforeRun {
		t.Error("tuiRunning.Store(true) is not the statement right before p.Run()")
	}
}

func nodeSource(t *testing.T, fset *token.FileSet, n ast.Node) string {
	t.Helper()
	var b strings.Builder
	if err := printNode(&b, fset, n); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func printNode(b *strings.Builder, fset *token.FileSet, n ast.Node) error {
	return format.Node(b, fset, n)
}
