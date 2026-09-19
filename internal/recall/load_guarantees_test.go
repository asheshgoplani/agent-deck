package recall

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The load guarantees the design makes are structural: no daemon, no file
// watcher, nothing that outlives the command. This test reads the recall
// sources (every package under internal/recall and the CLI file) and fails
// on a watcher import or a goroutine launch in non-test code.
func TestRecall_NoDaemonNoWatcher(t *testing.T) {
	root := moduleRoot(t)
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "internal", "recall"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join(root, "cmd", "agent-deck", "recall_cmd.go"))
	fset := token.NewFileSet()
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(p, "fsnotify") || strings.HasSuffix(p, "/fswatch") {
				t.Errorf("%s imports a file watcher (%s): recall polls on demand, never watches", path, p)
			}
		}
		if strings.HasSuffix(path, "recall_cmd.go") {
			continue // the CLI may use signal.NotifyContext; it still exits with the command
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "go ") || strings.HasPrefix(trimmed, "go func") {
				t.Errorf("%s:%d launches a goroutine: the sweep runs in the caller's goroutine and finishes with it", path, i+1)
			}
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
