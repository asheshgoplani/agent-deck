package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// This file exists for one screen.
//
// agent-deck refuses to invent a context-window size for a model identifier it
// has not been taught (internal/ctxinspect/claude/window.go): the window
// resolves to "unknown", the gauge drops its percentage, and the header prints
// the bare word "unknown". That is the honest outcome and it is also the first
// thing this feature's user ever hit — he opened the inspector, saw no
// percentage, and had to ask what it meant.
//
// No recorded case can cover it. The corpus was captured on models this build
// knows, and a case recorded on an untaught model would stop being untaught the
// moment the table learned it. So the world is DERIVED: exactly one field of
// the materialized transcript — the model identifier — is rewritten, inside the
// sandbox, and the rewrite is reported back to the caller so the row's
// transcript can say out loud what was changed.
//
// The line this holds is the same one variant.go holds: no conversation content
// is invented and no token figure is touched. The model identifier is not a
// measurement; it is the key the window table is looked up by, and it is the
// only input that produces this screen.

// modelField matches the model identifier a Claude transcript record carries,
// capturing the identifier it is about to replace so the row can report it.
var modelField = regexp.MustCompile(`"model"\s*:\s*"([^"]*)"`)

// OverrideModel rewrites the model identifier in every JSONL transcript under
// root, which must be the caller-owned sandbox.
//
// It returns the files it changed and the identifiers it replaced. A tree with
// no transcript in it is not an error here — a world materializes its
// transcript in one place and its HOME in another, and only the caller knows
// how many of those it asked about. What is NOT allowed is a world that
// rewrote nothing at all, and that check lives in the caller, which knows what
// "nothing" means. A fixture that silently no-ops would produce a row looking
// like it covered the untaught-model screen when it did not, which is the
// precise failure mode this framework exists to make impossible.
func OverrideModel(root, model string) (files int, replaced []string, err error) {
	if strings.TrimSpace(model) == "" {
		return 0, nil, fmt.Errorf("fixture: OverrideModel needs a model identifier")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return 0, nil, err
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(abs, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		if !withinDir(abs, p) { // belt and braces: never rewrite outside the sandbox
			return fmt.Errorf("fixture: refusing to rewrite %s, which is not under %s", p, abs)
		}
		raw, readErr := os.ReadFile(p) //nolint:gosec // p is inside the caller-owned sandbox
		if readErr != nil {
			return readErr
		}
		hits := modelField.FindAllSubmatch(raw, -1)
		if len(hits) == 0 {
			return nil
		}
		for _, h := range hits {
			id := strings.TrimSpace(string(h[1]))
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			replaced = append(replaced, id)
		}
		out := modelField.ReplaceAll(raw, []byte(`"model":"`+model+`"`))
		if writeErr := os.WriteFile(p, out, 0o644); writeErr != nil { //nolint:gosec // sandbox fixture
			return writeErr
		}
		files++
		return nil
	})
	return files, replaced, err
}
