package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/ui"
)

func TestExecSelfRestartDisablesModifyOtherKeys(t *testing.T) {
	var out bytes.Buffer
	err := execSelfRestart(&out, filepath.Join(t.TempDir(), "missing"), ui.RestartHandoff{})
	if err == nil {
		t.Fatal("expected exec to fail for a missing executable")
	}
	if got, want := out.String(), "\x1b[>4;0m"; got != want {
		t.Fatalf("wrote %q before exec, want %q", got, want)
	}
}

func TestEnableModifyOtherKeysHonoursExtendedKeysOff(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]string
		want    string
	}{
		{name: "default", want: "\x1b[>4;1m"},
		{name: "explicit on", options: map[string]string{"extended-keys": "on"}, want: "\x1b[>4;1m"},
		{name: "explicit off", options: map[string]string{"extended-keys": "off"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			enableModifyOtherKeys(&out, session.TmuxSettings{Options: tt.options})
			if got := out.String(); got != tt.want {
				t.Fatalf("wrote %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnableModifyOtherKeysNormalisesValue(t *testing.T) {
	for _, v := range []string{"off", "OFF", "Off", " off ", "off\t"} {
		var out bytes.Buffer
		enableModifyOtherKeys(&out, session.TmuxSettings{Options: map[string]string{"extended-keys": v}})
		if out.Len() != 0 {
			t.Errorf("extended-keys=%q wrote %q, want nothing", v, out.String())
		}
	}
	for _, v := range []string{"on", "", "always", "csi-u"} {
		var out bytes.Buffer
		enableModifyOtherKeys(&out, session.TmuxSettings{Options: map[string]string{"extended-keys": v}})
		if out.String() != "\x1b[>4;1m" {
			t.Errorf("extended-keys=%q wrote %q, want default enable", v, out.String())
		}
	}
}

// TestMainWiresModifyOtherKeysLifecycle fails if main() goes back to the
// unconditional ui.EnableModifyOtherKeys, restarts in place without going
// through execSelfRestart (which resets modifyOtherKeys first), or if
// enableModifyOtherKeys stops judging the value through ExtendedKeysOff.
func TestMainWiresModifyOtherKeysLifecycle(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			funcs[fd.Name.Name] = fd
		}
	}
	mainFn := funcs["main"]
	if mainFn == nil {
		t.Fatal("main() not found")
	}
	var helperCalls, restartCalls, directEnable, directExec int
	ast.Inspect(mainFn, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := c.Fun.(type) {
		case *ast.Ident:
			switch f.Name {
			case "enableModifyOtherKeys":
				helperCalls++
			case "execSelfRestart":
				restartCalls++
			}
		case *ast.SelectorExpr:
			switch f.Sel.Name {
			case "EnableModifyOtherKeys":
				directEnable++
			case "ExecSelf":
				directExec++
			}
		}
		return true
	})
	if directEnable != 0 {
		t.Errorf("main() calls ui.EnableModifyOtherKeys directly %d time(s); it must go through enableModifyOtherKeys", directEnable)
	}
	if helperCalls != 1 {
		t.Errorf("main() calls enableModifyOtherKeys %d times, want 1", helperCalls)
	}
	if directExec != 0 {
		t.Errorf("main() calls ui.ExecSelf directly %d time(s); it must go through execSelfRestart so modifyOtherKeys is reset first", directExec)
	}
	if restartCalls != 1 {
		t.Errorf("main() calls execSelfRestart %d times, want 1", restartCalls)
	}
	helper := funcs["enableModifyOtherKeys"]
	if helper == nil {
		t.Fatal("enableModifyOtherKeys not found")
	}
	if !strings.Contains(nodeSource(t, fset, helper), "ExtendedKeysOff") {
		t.Error("enableModifyOtherKeys does not judge extended-keys through TmuxSettings.ExtendedKeysOff")
	}
}
