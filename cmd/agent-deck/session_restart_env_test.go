package main

import "testing"

func TestEnvVarFlags(t *testing.T) {
	env := make(envVarFlags)

	for _, value := range []string{"API_URL=https://example.com?a=b", "EMPTY=", "API_URL=override"} {
		if err := env.Set(value); err != nil {
			t.Fatalf("Set(%q): %v", value, err)
		}
	}

	if got := env["API_URL"]; got != "override" {
		t.Fatalf("duplicate key should use the last value, got %q", got)
	}
	if got, ok := env["EMPTY"]; !ok || got != "" {
		t.Fatalf("empty value should be retained, got %q (present=%v)", got, ok)
	}
}

func TestEnvVarFlagsRejectsInvalidInput(t *testing.T) {
	for _, value := range []string{"MISSING_EQUALS", "=value", "BAD-KEY=value", "1KEY=value"} {
		env := make(envVarFlags)
		if err := env.Set(value); err == nil {
			t.Errorf("Set(%q) unexpectedly succeeded", value)
		}
	}
}
