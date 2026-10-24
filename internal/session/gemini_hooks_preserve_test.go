package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ASK-102: Gemini hooks install/uninstall used to round-trip the user's
// settings.json hooks through typed structs that knew only matcher/type/command,
// dropping every other field, and replaced malformed hook values outright.

func TestInjectGeminiHooks_PreservesUnknownHookFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	seed := `{"hooks":{"SessionStart":[{"matcher":"*","sequential":true,"hooks":[{"name":"mine","type":"command","command":"x","timeout":5000,"description":"d"}]}]}}`
	if err := os.WriteFile(p, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	check := func(stage string) {
		t.Helper()
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Hooks map[string][]map[string]any `json:"hooks"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		var user map[string]any
		for _, m := range got.Hooks["SessionStart"] {
			if m["matcher"] == "*" {
				user = m
			}
		}
		if user == nil || user["sequential"] != true {
			t.Fatalf("%s: matcher sequential lost: %s", stage, data)
		}
		hooks, _ := user["hooks"].([]any)
		if len(hooks) != 1 {
			t.Fatalf("%s: user hooks changed: %s", stage, data)
		}
		h, _ := hooks[0].(map[string]any)
		if h["name"] != "mine" || h["timeout"] != float64(5000) || h["description"] != "d" {
			t.Fatalf("%s: hook fields lost: %s", stage, data)
		}
	}
	if ok, err := InjectGeminiHooks(dir); err != nil || !ok {
		t.Fatalf("inject: %v %v", ok, err)
	}
	check("after inject")
	if ok, err := RemoveGeminiHooks(dir); err != nil || !ok {
		t.Fatalf("remove: %v %v", ok, err)
	}
	check("after remove")
}

func TestInjectGeminiHooks_RefusesMalformedHooks(t *testing.T) {
	for name, seed := range map[string]string{
		"event not an array": `{"hooks":{"SessionStart":{"not":"an array"}}}`,
		"hooks not object":   `{"hooks":"oops","other":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(p, []byte(seed), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := InjectGeminiHooks(dir); err == nil {
				t.Error("expected an error for malformed hooks")
			}
			data, _ := os.ReadFile(p)
			if string(data) != seed {
				t.Errorf("settings.json modified: %s", data)
			}
		})
	}
}
