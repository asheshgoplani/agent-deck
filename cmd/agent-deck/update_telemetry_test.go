package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grantTelemetryForTest isolates telemetry state, pins a person at a TTY,
// and grants consent, so recorded events land in the spool.
func grantTelemetryForTest(t *testing.T, version string) {
	t.Helper()
	isolateTelemetryHome(t)
	telemetry.SetTerminalForTest(t, true)
	telemetry.SetProcess(version, telemetry.SurfaceCLI)
	t.Cleanup(func() { telemetry.SetProcess("dev", telemetry.SurfaceCLI) })
	s := telemetry.LoadState()
	require.NoError(t, telemetry.Grant(s, version, time.Now()))
	require.NoError(t, telemetry.SaveState(s))
}

type spooledEvent struct {
	E string         `json:"e"`
	V string         `json:"v"`
	P map[string]any `json:"p"`
}

// spooledEvents returns the spooled events named name.
func spooledEvents(t *testing.T, name string) []spooledEvent {
	t.Helper()
	statePath, err := telemetry.StatePath()
	require.NoError(t, err)
	f, err := os.Open(filepath.Join(filepath.Dir(statePath), telemetry.SpoolFileName))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	defer f.Close()
	var out []spooledEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev spooledEvent
		require.NoError(t, json.Unmarshal(sc.Bytes(), &ev))
		if ev.E == name {
			out = append(out, ev)
		}
	}
	require.NoError(t, sc.Err())
	return out
}

// Every unattended install records one update event whose kind follows the
// run's trigger and whose outcome follows the install result.
func TestRunUnattendedUpdate_RecordsUpdateEvent(t *testing.T) {
	cases := []struct {
		trigger    string
		installErr error
		kind       string
		outcome    string
		exit       int
	}{
		{"timer", nil, "timer", "ok", exitUpdateOK},
		{"tui", nil, "auto", "ok", exitUpdateOK},
		{"nudge", nil, "remote_sweep", "ok", exitUpdateOK},
		{"nudge-fallback", nil, "remote_sweep", "ok", exitUpdateOK},
		{"manual", nil, "manual", "ok", exitUpdateOK},
		{"timer", errors.New("checksum mismatch"), "timer", "error", exitUpdateFailed},
	}
	for _, tc := range cases {
		t.Run(tc.trigger+"_"+tc.outcome, func(t *testing.T) {
			grantTelemetryForTest(t, "1.16.5")
			h := newUnattendedHarness(t, availableInfo())
			h.deps.trigger = tc.trigger
			h.deps.install = func(string) error { return tc.installErr }
			require.Equal(t, tc.exit, runUnattendedUpdate(h.deps))

			evs := spooledEvents(t, "update")
			require.Len(t, evs, 1, "one update event per install attempt")
			assert.Equal(t, "1.16.5", evs[0].V, "event carries the running version")
			assert.Equal(t, tc.kind, evs[0].P["kind"])
			assert.Equal(t, tc.outcome, evs[0].P["outcome"])
			assert.Equal(t, "1.16", evs[0].P["from_minor"])
			assert.Equal(t, "1.17", evs[0].P["to_minor"])
			if tc.installErr != nil {
				assert.Len(t, spooledEvents(t, "error"), 1, "a failed install also records the update error")
			}
		})
	}
}

// A run that installs nothing records no update event.
func TestRunUnattendedUpdate_NoInstallNoUpdateEvent(t *testing.T) {
	grantTelemetryForTest(t, "1.16.5")
	h := newUnattendedHarness(t, availableInfo())
	h.deps.autoInstall = false
	require.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.Empty(t, spooledEvents(t, "update"))
}

func TestRecordUpdateAttempt(t *testing.T) {
	grantTelemetryForTest(t, "1.16.25")
	require.NoError(t, recordUpdateAttempt("1.16.25", "1.16.26", telemetry.UpdateManual, func() error { return nil }))
	boom := errors.New("boom")
	require.ErrorIs(t, recordUpdateAttempt("1.16.25", "1.16.26", telemetry.UpdateManual, func() error { return boom }), boom)

	evs := spooledEvents(t, "update")
	require.Len(t, evs, 2)
	assert.Equal(t, "ok", evs[0].P["outcome"])
	assert.Equal(t, "error", evs[1].P["outcome"])
	for _, ev := range evs {
		assert.Equal(t, "manual", ev.P["kind"])
		assert.Equal(t, "1.16.25", ev.V)
	}
	assert.Len(t, spooledEvents(t, "error"), 1)
}

// Every install of a release in this package goes through
// recordUpdateAttempt, so no install path can skip the update event again
// (the startup prompt and `update <version>` once did). The unattended
// install is the one exception by construction: realUnattendedDeps hands it
// to runUnattendedUpdate, which wraps d.install (tested above).
func TestEveryInstallPathRecordsTelemetry(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var unwrapped []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var stack []ast.Node
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				if isPkgFuncCall(n, "update", "PerformVerifiedUpdate") && !insideRecordedInstall(fn.Name.Name, stack) {
					unwrapped = append(unwrapped, fset.Position(n.Pos()).String()+" in "+fn.Name.Name)
				}
				return true
			})
		}
	}
	assert.Empty(t, unwrapped, "update.PerformVerifiedUpdate called outside recordUpdateAttempt")
}

func isPkgFuncCall(n ast.Node, pkg, fn string) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != fn {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// insideRecordedInstall reports whether the innermost enclosing func literal
// of the call is an argument of recordUpdateAttempt, or is the install
// collaborator built by realUnattendedDeps.
func insideRecordedInstall(funcName string, stack []ast.Node) bool {
	for i := len(stack) - 1; i > 0; i-- {
		if _, ok := stack[i].(*ast.FuncLit); !ok {
			continue
		}
		switch parent := stack[i-1].(type) {
		case *ast.CallExpr:
			if id, ok := parent.Fun.(*ast.Ident); ok && id.Name == "recordUpdateAttempt" {
				return true
			}
		case *ast.KeyValueExpr:
			if key, ok := parent.Key.(*ast.Ident); ok && key.Name == "install" && funcName == "realUnattendedDeps" {
				return true
			}
		}
		return false
	}
	return false
}
