package tmux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestTmuxAttachSpawnsForceUTF8 guards every production attach path against
// inheriting a non-UTF-8 locale from launchd, systemd, or another supervisor.
func TestTmuxAttachSpawnsForceUTF8(t *testing.T) {
	root := moduleRoot(t)

	var violations []string
	err := walkGoFiles(root, func(path string) error {
		if strings.HasSuffix(filepath.Base(path), "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// This package is a test-only multi-client fixture, not an agent-deck
		// attach surface. Its explicit -S socket is controlled by each test.
		if rel == "internal/testutil/multiclienttmux/multiclienttmux.go" {
			return nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			uIdx, attachIdx := -1, -1
			for i, arg := range call.Args {
				value, ok := stringLiteral(arg)
				if !ok {
					continue
				}
				switch value {
				case "-u":
					uIdx = i
				case "attach-session":
					attachIdx = i
				}
			}

			if attachIdx == -1 || (uIdx != -1 && uIdx < attachIdx) {
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
		t.Errorf("%d tmux attach spawn(s) do not force UTF-8; pass the global `-u` "+
			"flag before `attach-session`:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
