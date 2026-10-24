package main

import (
	"strings"
	"testing"
)

func TestEnvVarFlags(t *testing.T) {
	env := newEnvVarFlags()

	for _, value := range []string{"API_URL=https://example.com?a=b", "EMPTY=", "API_URL=override"} {
		if err := env.Set(value); err != nil {
			t.Fatalf("Set(%q): %v", value, err)
		}
	}
	if err := env.Err(); err != nil {
		t.Fatalf("valid entries recorded an error: %v", err)
	}

	if got := env.Map()["API_URL"]; got != "override" {
		t.Fatalf("duplicate key should use the last value, got %q", got)
	}
	if got, ok := env.Map()["EMPTY"]; !ok || got != "" {
		t.Fatalf("empty value should be retained, got %q (present=%v)", got, ok)
	}
}

func TestEnvVarFlagsRejectsInvalidInput(t *testing.T) {
	for _, value := range []string{"MISSING_EQUALS", "=value", "BAD-KEY=value", "1KEY=value"} {
		env := newEnvVarFlags()
		if err := env.Set(value); err != nil {
			t.Errorf("Set(%q) returned %v; rejections must be deferred to Err so the flag package never echoes the value", value, err)
		}
		if env.Err() == nil {
			t.Errorf("Set(%q) unexpectedly accepted", value)
		}
		if len(env.Map()) != 0 {
			t.Errorf("Set(%q) stored a rejected entry: %v", value, env.Map())
		}
	}
}

// A malformed -env entry is often the secret itself ('API_KEY sk-…' with no
// '=', or a value with a bad key). Neither the deferred error nor the flag
// package's own parse output may carry it.
func TestEnvVarFlagsErrorsNeverCarryValue(t *testing.T) {
	const sentinel = "sk-test-SENTINEL-1234"
	for _, value := range []string{
		"AGENTDECK_TEST_SECRET " + sentinel,
		"BAD-KEY=" + sentinel,
		sentinel,
		"export AGENTDECK_TEST_SECRET=" + sentinel,
	} {
		env := newEnvVarFlags()
		_ = env.Set(value)
		if env.Err() == nil {
			t.Fatalf("Set(%q) unexpectedly accepted", value)
		}
		if strings.Contains(env.Err().Error(), sentinel) || strings.Contains(env.Err().Error(), "SENTINEL") {
			t.Errorf("rejection of %q leaks the value: %v", value, env.Err())
		}
	}
}
