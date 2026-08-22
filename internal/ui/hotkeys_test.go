package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestAdvertisedOverviewBindingsHaveSingleMeaning walks the real handleMainKey
// dispatcher. Configurable bindings are assigned to the switch clause that
// consumes their canonical key, while fixed cases are read directly from that
// same switch. Thus adding a new literal route cannot bypass this invariant.
func TestAdvertisedOverviewBindingsHaveSingleMeaning(t *testing.T) {
	bindings := mainDispatcherBindings(t)
	seen := make(map[string]string)
	for _, binding := range bindings {
		for _, alias := range hotkeyAliases(binding.key) {
			if previous, ok := seen[alias]; ok && previous != binding.action {
				t.Errorf("overview dispatches %q to both %q and %q", alias, previous, binding.action)
				continue
			}
			seen[alias] = binding.action
		}
	}
}

type dispatcherBinding struct{ key, action string }

func mainDispatcherBindings(t *testing.T) []dispatcherBinding {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var pkgFiles []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if file.Name.Name == "ui" {
			pkgFiles = append(pkgFiles, file)
		}
	}
	if len(pkgFiles) == 0 {
		t.Fatal("ui package not found")
	}
	var bindings []dispatcherBinding
	add := func(key, action string) { bindings = append(bindings, dispatcherBinding{key: key, action: action}) }
	for _, file := range pkgFiles {
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "handleMainKey" {
				return true
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sw, ok := n.(*ast.SwitchStmt)
				if !ok {
					return true
				}
				tag, ok := sw.Tag.(*ast.Ident)
				if !ok || tag.Name != "key" {
					return true
				}
				for _, stmt := range sw.Body.List {
					clause, ok := stmt.(*ast.CaseClause)
					if !ok {
						continue
					}
					action := "dispatcher-clause@" + strconv.Itoa(int(clause.Pos()))
					for _, expr := range clause.List {
						switch e := expr.(type) {
						case *ast.BasicLit:
							if e.Kind == token.STRING {
								if key, err := strconv.Unquote(e.Value); err == nil {
									add(key, action)
								}
							}
						case *ast.IndexExpr:
							if id, ok := e.X.(*ast.Ident); ok && id.Name == "defaultHotkeyBindings" {
								if keyID, ok := e.Index.(*ast.Ident); ok {
									if actionName := dispatcherConstantValue(pkgFiles, keyID.Name); actionName != "" {
										if key := defaultHotkeyBindings[actionName]; key != "" {
											add(key, action)
										}
									}
								}
							}
						case *ast.Ident:
							// Resolve dispatcher constants without duplicating their values here.
							if key := dispatcherConstantValue(pkgFiles, e.Name); key != "" {
								add(key, action)
							}
						}
					}
				}
				return false
			})
			return false
		})
	}
	return bindings
}

func dispatcherConstantValue(pkgFiles []*ast.File, name string) string {
	for _, file := range pkgFiles {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range value.Names {
					if ident.Name != name || i >= len(value.Values) {
						continue
					}
					if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						key, _ := strconv.Unquote(lit.Value)
						return key
					}
				}
			}
		}
	}
	return ""
}

func TestCostAndErrorFilterKeysAlwaysHaveSeparateMeanings(t *testing.T) {
	home := NewHome()
	home.statusFilter = session.StatusRunning

	// With no cost store, $ remains reserved for Cost Dashboard and must not
	// fall back to changing the filter.
	home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(CostDashboardKey)})
	if home.statusFilter != session.StatusRunning {
		t.Fatalf("%s changed status filter to %q without a cost store", CostDashboardKey, home.statusFilter)
	}
	if home.err == nil || home.err.Error() != "Cost Dashboard unavailable: state database is missing; restart agent-deck with a writable config directory to enable it" {
		t.Fatalf("missing cost store feedback = %v", home.err)
	}

	errSession := session.NewInstance("errored", t.TempDir())
	errSession.Status = session.StatusError
	home.instances = []*session.Instance{errSession}
	home.groupTree = session.NewGroupTree(home.instances)
	home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(FilterKeyError)})
	if home.statusFilter != session.StatusError {
		t.Fatalf("%s status filter = %q, want error", FilterKeyError, home.statusFilter)
	}
}

func TestResolveHotkeysOverridesAndUnbinds(t *testing.T) {
	bindings := resolveHotkeys(map[string]string{
		"delete":        "backspace",
		"close_session": "",
		"unknown":       "x",
	})

	if got := bindings[hotkeyDelete]; got != "backspace" {
		t.Fatalf("delete binding = %q, want backspace", got)
	}

	if _, ok := bindings[hotkeyCloseSession]; ok {
		t.Fatalf("close_session should be unbound")
	}

	if got := bindings[hotkeyRestart]; got != defaultHotkeyBindings[hotkeyRestart] {
		t.Fatalf("restart binding = %q, want %q", got, defaultHotkeyBindings[hotkeyRestart])
	}

	if got := bindings[hotkeyRestartFresh]; got != defaultHotkeyBindings[hotkeyRestartFresh] {
		t.Fatalf("restart_fresh binding = %q, want %q", got, defaultHotkeyBindings[hotkeyRestartFresh])
	}
}

func TestResolveHotkeysPrefersCanonicalNameOverLegacyRename(t *testing.T) {
	bindings := resolveHotkeys(map[string]string{
		"toggle_gemini_yolo": "g",
		"toggle_yolo":        "y",
	})

	if got := bindings[hotkeyToggleYolo]; got != "y" {
		t.Fatalf("toggle_yolo binding = %q, want %q", got, "y")
	}
}

func TestResolveHotkeysMapsLegacyRenameWhenCanonicalAbsent(t *testing.T) {
	bindings := resolveHotkeys(map[string]string{
		"toggle_gemini_yolo": "g",
	})

	if got := bindings[hotkeyToggleYolo]; got != "g" {
		t.Fatalf("toggle_yolo binding = %q, want %q", got, "g")
	}
}

func TestBuildHotkeyLookupRemapAndUnbind(t *testing.T) {
	bindings := resolveHotkeys(map[string]string{
		"delete": "backspace",
		"quit":   "",
	})
	lookup, blocked := buildHotkeyLookup(bindings)

	if got := lookup["backspace"]; got != defaultHotkeyBindings[hotkeyDelete] {
		t.Fatalf("backspace maps to %q, want %q", got, defaultHotkeyBindings[hotkeyDelete])
	}

	if !blocked[defaultHotkeyBindings[hotkeyDelete]] {
		t.Fatalf("default delete key should be blocked when remapped")
	}

	if !blocked["q"] {
		t.Fatalf("q should be blocked when quit is unbound")
	}

	if !blocked["ctrl+c"] {
		t.Fatalf("ctrl+c should be blocked when quit is unbound")
	}
}

func TestHotkeyAliasesShiftAndSymbols(t *testing.T) {
	aliases := hotkeyAliases("shift+f")
	hasUpper := false
	for _, alias := range aliases {
		if alias == "F" {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		t.Fatalf("shift+f aliases should include F")
	}

	symbolAliases := hotkeyAliases("!")
	hasShiftNum := false
	for _, alias := range symbolAliases {
		if alias == "shift+1" {
			hasShiftNum = true
			break
		}
	}
	if !hasShiftNum {
		t.Fatalf("! aliases should include shift+1")
	}
}

func TestDetachByteFromBinding(t *testing.T) {
	tests := []struct {
		binding string
		want    byte
	}{
		{"ctrl+q", 17},     // 'q' - 'a' + 1 = 17
		{"ctrl+a", 1},      // 'a' - 'a' + 1 = 1
		{"ctrl+z", 26},     // 'z' - 'a' + 1 = 26
		{"ctrl+b", 2},      // 'b' - 'a' + 1 = 2
		{"Ctrl+Q", 17},     // case insensitive
		{"  ctrl+q  ", 17}, // whitespace trimmed
		{"ctrl+\\", 0x1C},
		{"ctrl+]", 0x1D},
		{"ctrl+^", 0x1E},
		{"ctrl+_", 0x1F},
		{"q", 17},       // non-ctrl binding defaults to Ctrl+Q
		{"", 17},        // empty defaults to Ctrl+Q
		{"shift+q", 17}, // non-ctrl prefix defaults to Ctrl+Q
		{"ctrl+1", 17},  // non-letter defaults to Ctrl+Q
	}

	for _, tt := range tests {
		t.Run(tt.binding, func(t *testing.T) {
			if got := DetachByteFromBinding(tt.binding); got != tt.want {
				t.Errorf("DetachByteFromBinding(%q) = %d, want %d", tt.binding, got, tt.want)
			}
		})
	}
}

func TestDetachByteLabel(t *testing.T) {
	tests := []struct {
		b    byte
		want string
	}{
		{17, "Ctrl+Q"},
		{1, "Ctrl+A"},
		{26, "Ctrl+Z"},
		{2, "Ctrl+B"},
		{0x1C, "Ctrl+\\"},
		{0x1D, "Ctrl+]"},
		{0x1E, "Ctrl+^"},
		{0x1F, "Ctrl+_"},
		{0, "Ctrl+Q"},  // out of range defaults
		{27, "Ctrl+Q"}, // ESC byte, not in letter range
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := DetachByteLabel(tt.b); got != tt.want {
				t.Errorf("DetachByteLabel(%d) = %q, want %q", tt.b, got, tt.want)
			}
		})
	}
}

func TestResolvedDetachByte(t *testing.T) {
	// Default (no overrides) should be Ctrl+Q
	if got := ResolvedDetachByte(nil); got != 17 {
		t.Fatalf("ResolvedDetachByte(nil) = %d, want 17", got)
	}

	// Override detach to ctrl+b
	if got := ResolvedDetachByte(map[string]string{"detach": "ctrl+b"}); got != 2 {
		t.Fatalf("ResolvedDetachByte(ctrl+b) = %d, want 2", got)
	}

	// Override detach to ctrl+a
	if got := ResolvedDetachByte(map[string]string{"detach": "ctrl+a"}); got != 1 {
		t.Fatalf("ResolvedDetachByte(ctrl+a) = %d, want 1", got)
	}

	// Unrelated overrides should not affect detach
	if got := ResolvedDetachByte(map[string]string{"quit": "x"}); got != 17 {
		t.Fatalf("ResolvedDetachByte with unrelated override = %d, want 17", got)
	}

	// Unbinding detach (empty string) should default to Ctrl+Q
	if got := ResolvedDetachByte(map[string]string{"detach": ""}); got != 17 {
		t.Fatalf("ResolvedDetachByte with empty override = %d, want 17", got)
	}
}

func TestNormalizeMainKeyWithConfiguredHotkeys(t *testing.T) {
	h := NewHome()
	h.setHotkeys(resolveHotkeys(map[string]string{
		"delete": "backspace",
		"quit":   "",
	}))

	if got := h.normalizeMainKey("backspace"); got != defaultHotkeyBindings[hotkeyDelete] {
		t.Fatalf("backspace normalized to %q, want %q", got, defaultHotkeyBindings[hotkeyDelete])
	}

	if got := h.normalizeMainKey(defaultHotkeyBindings[hotkeyDelete]); got != "" {
		t.Fatalf("default delete key should be blocked after remap, got %q", got)
	}

	if got := h.normalizeMainKey("ctrl+c"); got != "" {
		t.Fatalf("ctrl+c should be blocked when quit is unbound, got %q", got)
	}
}

// TestOpenShellHereHotkey verifies the new open_shell_here action is wired
// correctly: default key "H" preserves lowercase h navigation, is present in
// hotkeyActionOrder, and remains overridable.
// Issue #1470.
func TestOpenShellHereHotkey(t *testing.T) {
	// Default binding is "H" so lowercase h keeps its collapse/parent behavior.
	bindings := resolveHotkeys(nil)
	if got := bindings[hotkeyOpenShellHere]; got != "H" {
		t.Errorf("default open_shell_here binding = %q, want \"H\"", got)
	}

	// User can override to a different key.
	overridden := resolveHotkeys(map[string]string{"open_shell_here": "ctrl+h"})
	if got := overridden[hotkeyOpenShellHere]; got != "ctrl+h" {
		t.Errorf("overridden open_shell_here binding = %q, want \"ctrl+h\"", got)
	}

	// User can unbind it.
	unbound := resolveHotkeys(map[string]string{"open_shell_here": ""})
	if _, ok := unbound[hotkeyOpenShellHere]; ok {
		t.Errorf("open_shell_here should be unbound when set to empty string")
	}

	// Must be present in hotkeyActionOrder so it appears in the help panel.
	found := false
	for _, action := range hotkeyActionOrder {
		if action == hotkeyOpenShellHere {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hotkeyOpenShellHere is missing from hotkeyActionOrder")
	}
}
