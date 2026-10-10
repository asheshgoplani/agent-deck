package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/slashcmd"
)

func TestDefaultModelGuard_FailsWhenTheSavedDefaultChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"claude-opus-4-8","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := readDefaultModel(dir)
	if err := os.WriteFile(path, []byte(`{"model":"claude-opus-5-5","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res := slashcmd.Result{OK: true, Command: "/model claude-opus-5-5"}
	guardDefaultModel(&res, dir, before)
	if res.OK || res.Code != "default_changed" {
		t.Fatalf("a changed default must fail the command, got %+v", res)
	}
	if len(res.Evidence) == 0 || res.Evidence[len(res.Evidence)-1] != `settings.json "model": "claude-opus-4-8" -> "claude-opus-5-5" (`+path+`)` {
		t.Fatalf("evidence must name the file, key and both values: %v", res.Evidence)
	}
}

func TestDefaultModelGuard_UnchangedOrMissingPasses(t *testing.T) {
	dir := t.TempDir()
	res := slashcmd.Result{OK: true}
	guardDefaultModel(&res, dir, readDefaultModel(dir)) // no settings.json at all
	if !res.OK {
		t.Fatalf("missing settings must not fail: %+v", res)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"claude-opus-4-8"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := readDefaultModel(dir)
	guardDefaultModel(&res, dir, before)
	if !res.OK || res.Evidence[len(res.Evidence)-1] != `settings.json "model" unchanged ("claude-opus-4-8")` {
		t.Fatalf("unchanged default must pass and say so: %+v", res)
	}
}
