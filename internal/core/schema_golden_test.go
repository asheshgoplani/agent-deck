package core

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/core/schema"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files under testdata/")

// TestCommandSchemasGolden pins the reflected input and output schema of
// every built-in command. A struct change shows up as a golden diff; rerun
// with -update to accept it.
func TestCommandSchemasGolden(t *testing.T) {
	r := NewRegistry()
	if err := RegisterBuiltins(r, Deps{}); err != nil {
		t.Fatal(err)
	}
	for _, d := range r.Defs() {
		for _, side := range []string{"in", "out"} {
			d, side := d, side
			t.Run(d.ID+"/"+side, func(t *testing.T) {
				var s *schema.Schema
				var err error
				if side == "in" {
					s, err = d.InputSchema()
				} else {
					s, err = d.OutputSchema()
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := schema.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				checkGolden(t, filepath.Join("testdata", "schema", d.ID+"."+side+".json"), got)
			})
		}
	}
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create it)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from reflected schema (run with -update to accept)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
