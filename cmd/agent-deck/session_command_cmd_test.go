package main

import (
	"os"
	"path/filepath"
	"strings"
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
	guardDefaultModel(&res, dir, before, "claude-opus-5-5")
	if res.OK || res.Code != "default_changed" {
		t.Fatalf("a changed default must fail the command, got %+v", res)
	}
	if len(res.Evidence) == 0 || res.Evidence[len(res.Evidence)-1] != `settings.json "model": "claude-opus-4-8" -> "claude-opus-5-5" (`+path+`)` {
		t.Fatalf("evidence must name the file, key and both values: %v", res.Evidence)
	}
	if strings.Contains(strings.ToLower(res.Error), "restore") {
		t.Fatalf("the error must never ask to restore a value: %q", res.Error)
	}
}

func TestDefaultModelGuard_UnchangedOrMissingPasses(t *testing.T) {
	dir := t.TempDir()
	res := slashcmd.Result{OK: true}
	guardDefaultModel(&res, dir, readDefaultModel(dir), "haiku") // no settings.json at all
	if !res.OK {
		t.Fatalf("missing settings must not fail: %+v", res)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"claude-opus-4-8"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := readDefaultModel(dir)
	guardDefaultModel(&res, dir, before, "haiku")
	if !res.OK || res.Evidence[len(res.Evidence)-1] != `settings.json "model" unchanged ("claude-opus-4-8")` {
		t.Fatalf("unchanged default must pass and say so: %+v", res)
	}
}

// Review 2736 item 3: a change of the saved default to something other than
// the requested model was made elsewhere (another session sharing the config
// dir); it does not fail the switch.
func TestDefaultModelGuard_ChangeToAnotherModelIsNotOurs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"claude-opus-4-8"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := readDefaultModel(dir)
	if err := os.WriteFile(path, []byte(`{"model":"claude-sonnet-5-5"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res := slashcmd.Result{OK: true, Command: "/model haiku", After: "Haiku 5.5"}
	guardDefaultModel(&res, dir, before, "haiku")
	if !res.OK || res.Code != "" {
		t.Fatalf("a change to another model is not ours, got %+v", res)
	}
	if len(res.Evidence) == 0 || !strings.Contains(res.Evidence[len(res.Evidence)-1], "changed elsewhere") {
		t.Fatalf("the change must be noted: %v", res.Evidence)
	}
	// An alias request that saved its resolved id is ours.
	if err := os.WriteFile(path, []byte(`{"model":"claude-haiku-5-5"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res = slashcmd.Result{OK: true, Command: "/model haiku", After: "Haiku 5.5"}
	guardDefaultModel(&res, dir, before, "haiku")
	if res.OK || res.Code != "default_changed" {
		t.Fatalf("the switch saving the requested model must fail, got %+v", res)
	}
}

// Low: a half-written settings.json is read once more.
func TestReadDefaultModel_RetriesAHalfWrittenFile(t *testing.T) {
	reads := 0
	settingsReadFile = func(string) ([]byte, error) {
		reads++
		if reads == 1 {
			return []byte(`{"model":"claude-op`), nil
		}
		return []byte(`{"model":"claude-opus-4-8"}`), nil
	}
	t.Cleanup(func() { settingsReadFile = os.ReadFile })
	if got := readDefaultModel(t.TempDir()); !got.found || got.value != "claude-opus-4-8" || reads != 2 {
		t.Fatalf("readDefaultModel = %+v after %d reads, want the second read", got, reads)
	}
}
