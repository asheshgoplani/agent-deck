package session

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"strconv"
	"strings"
	"testing"
)

// hostileTitles carry a newline, a carriage return and an ANSI escape: the
// characters that let a session title forge or corrupt log records (CodeQL
// go/log-injection on the conductor_title_drift and TELEGRAM_STATE_DIR strip
// warnings).
var hostileTitles = []string{
	"evil\nlevel=ERROR msg=forged",
	"evil\r\nforged",
	"evil\x1b[2Jcleared",
}

// captureSessionLogJSON swaps sessionLog for a JSON handler and returns its
// buffer. The handler escapes control characters itself, so the check that
// matters is on the decoded attribute values: the sanitizer must have removed
// them before the value reached the log call (other sinks, such as a text
// handler or a log viewer, would otherwise print them raw).
func captureSessionLogJSON(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := sessionLog
	sessionLog = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { sessionLog = orig })
	return &buf
}

// assertOneCleanRecord checks buf holds exactly one log record and that no
// attribute value in it carries a raw control character once decoded.
func assertOneCleanRecord(t *testing.T, buf *bytes.Buffer, title string) {
	t.Helper()
	out := strings.TrimRight(buf.String(), "\n")
	if out == "" {
		t.Fatalf("title %q: nothing logged", title)
	}
	if n := strings.Count(out, "\n"); n != 0 {
		t.Fatalf("title %q: log spans %d lines, want 1:\n%s", title, n+1, out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("title %q: record is not one JSON object: %v\n%s", title, err, out)
	}
	for k, v := range rec {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if strings.ContainsAny(s, "\n\r\x1b") {
			t.Errorf("title %q: attribute %q carries a raw control character: %q", title, k, s)
		}
	}
}

func TestConductorTitleDriftLog_ControlCharsInTitle(t *testing.T) {
	for n, title := range hostileTitles {
		buf := captureSessionLogJSON(t)
		inst := &Instance{ID: "drift-inject-" + string(rune('a'+n)) + "-" + t.Name(), Title: title, IsConductor: true, ConductorName: "personal"}
		t.Cleanup(func() { conductorTitleDriftWarned.Delete(inst.ID) })
		if got := inst.ResolveConductorName(); got != "personal" {
			t.Fatalf("ResolveConductorName = %q, want personal", got)
		}
		if !strings.Contains(buf.String(), "conductor_title_drift") {
			t.Fatalf("title %q: drift warning not logged:\n%s", title, buf.String())
		}
		assertOneCleanRecord(t, buf, title)
	}
}

func TestTelegramStripWarningLog_ControlCharsInTitle(t *testing.T) {
	withTempAgentDeckHome(t, "")
	for _, title := range hostileTitles {
		inst := NewInstanceWithTool("plain-child", t.TempDir(), "claude")
		inst.Title = title
		inst.Wrapper = "env TELEGRAM_STATE_DIR=/tmp/x {command}"
		buf := captureSessionLogJSON(t)
		if got := inst.telegramStripSources(); len(got) != 2 {
			t.Fatalf("title %q: want warning + strip clauses, got %q", title, got)
		}
		if !strings.Contains(buf.String(), "TELEGRAM_STATE_DIR stripped") {
			t.Fatalf("title %q: strip warning not logged:\n%s", title, buf.String())
		}
		assertOneCleanRecord(t, buf, title)
	}
}

// TestLogInjectionSinks_InlineNewlineBarrier pins the shape CodeQL
// go/log-injection needs at the two title log sinks: it does not treat
// logging.SanitizeValue as a barrier, so the logged value must be wrapped in
// strings.ReplaceAll calls that remove "\n" and "\r" right at the slog.String
// call (alerts 458/459 reopened when only SanitizeValue was used).
func TestLogInjectionSinks_InlineNewlineBarrier(t *testing.T) {
	cases := []struct{ file, fn, key string }{
		{"conductor_identity.go", "warnConductorTitleDrift", "title"},
		{"env.go", "telegramStripSources", "session"},
	}
	for _, c := range cases {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, c.file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", c.file, err)
		}
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Name.Name != c.fn {
				return true
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isSelector(call.Fun, "slog", "String") || len(call.Args) != 2 {
					return true
				}
				if lit, ok := call.Args[0].(*ast.BasicLit); !ok || lit.Value != strconv.Quote(c.key) {
					return true
				}
				found = true
				removed := map[string]bool{}
				for e := call.Args[1]; ; {
					inner, ok := e.(*ast.CallExpr)
					if !ok || !isSelector(inner.Fun, "strings", "ReplaceAll") || len(inner.Args) != 3 {
						break
					}
					if lit, ok := inner.Args[1].(*ast.BasicLit); ok {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							removed[v] = true
						}
					}
					e = inner.Args[0]
				}
				if !removed["\n"] || !removed["\r"] {
					t.Errorf("%s:%s: slog.String(%q, ...) lacks inline strings.ReplaceAll barriers for \\n and \\r", c.file, c.fn, c.key)
				}
				return true
			})
			return false
		})
		if !found {
			t.Errorf("%s:%s: no slog.String(%q, ...) log call found", c.file, c.fn, c.key)
		}
	}
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}
