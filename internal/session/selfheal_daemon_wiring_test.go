package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// The transition daemon is self-heal's production scheduler. Unit tests for
// runSelfHealPass do not protect this call site: deleting it leaves the engine
// fully tested but unreachable in a real daemon.
func TestSyncProfile_InvokesSelfHealPass(t *testing.T) {
	src, err := os.ReadFile("transition_daemon.go")
	if err != nil {
		t.Fatalf("read transition_daemon.go: %v", err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "transition_daemon.go", src, 0)
	if err != nil {
		t.Fatalf("parse transition_daemon.go: %v", err)
	}

	var syncProfile *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "syncProfile" {
			syncProfile = fn
			break
		}
	}
	if syncProfile == nil {
		t.Fatal("TransitionDaemon.syncProfile not found")
	}

	calls := 0
	ast.Inspect(syncProfile.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "runSelfHealPass" {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if ok && recv.Name == "d" {
			calls++
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("syncProfile must invoke d.runSelfHealPass exactly once, got %d calls", calls)
	}
}
