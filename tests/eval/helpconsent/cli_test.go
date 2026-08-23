//go:build eval_smoke

// Package helpconsent evaluates the user-visible contract that asking a CLI
// command family for help exits successfully without creating state.
package helpconsent_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_HooksBareHelpIsDetailedAndReadOnly(t *testing.T) {
	sb := harness.NewSandbox(t)
	before := tree(t, sb.Home)
	cmd := exec.Command(sb.BinPath, "hooks", "help")
	cmd.Env = sb.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent-deck hooks help failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Usage: agent-deck hooks <install|uninstall|status>") {
		t.Fatalf("hooks help lost detailed usage:\n%s", out)
	}
	after := tree(t, sb.Home)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("hooks help changed the sandbox HOME\nbefore: %v\nafter: %v", before, after)
	}
}

func tree(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}
