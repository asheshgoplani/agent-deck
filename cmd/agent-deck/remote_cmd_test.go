package main

import (
	"flag"
	"io"
	"testing"
)

func TestIsValidRemoteName(t *testing.T) {
	t.Parallel()

	valid := []string{"dev", "prod_us", "us-west-2"}
	invalid := []string{
		"",
		"dev env",
		"dev/env",
		"dev\\env",
		"dev.env",
		"dev:env",
	}

	for _, name := range valid {
		if !isValidRemoteName(name) {
			t.Fatalf("expected %q to be valid", name)
		}
	}

	for _, name := range invalid {
		if isValidRemoteName(name) {
			t.Fatalf("expected %q to be invalid", name)
		}
	}
}

func TestReorderRemoteArgs(t *testing.T) {
	t.Parallel()

	t.Run("single_dash_profile_value_consumed", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		_ = fs.String("profile", "", "")
		_ = fs.String("agent-deck-path", "", "")

		result := reorderRemoteArgs(fs, []string{"mac-studio", "chuck@localhost", "-profile", "dev"})
		// flags should come first, then positional
		expected := []string{"-profile", "dev", "mac-studio", "chuck@localhost"}
		if len(result) != len(expected) {
			t.Fatalf("expected %v, got %v", expected, result)
		}
		for i, exp := range expected {
			if result[i] != exp {
				t.Errorf("result[%d]: expected %q, got %q", i, exp, result[i])
			}
		}
	})

	t.Run("double_dash_profile_value_consumed", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		_ = fs.String("profile", "", "")

		result := reorderRemoteArgs(fs, []string{"srv", "user@host", "--profile", "work"})
		expected := []string{"--profile", "work", "srv", "user@host"}
		if len(result) != len(expected) {
			t.Fatalf("expected %v, got %v", expected, result)
		}
		for i, exp := range expected {
			if result[i] != exp {
				t.Errorf("result[%d]: expected %q, got %q", i, exp, result[i])
			}
		}
	})
}

func TestShouldProceedWithRemoteUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		readErr  error
		want     bool
	}{
		{name: "default yes on empty line", response: "\n", readErr: nil, want: true},
		{name: "yes lower", response: "y\n", readErr: nil, want: true},
		{name: "yes word", response: "yes\n", readErr: nil, want: true},
		{name: "no lower", response: "n\n", readErr: nil, want: false},
		{name: "other value", response: "nope\n", readErr: nil, want: false},
		{name: "eof empty fails closed", response: "", readErr: io.EOF, want: false},
		{name: "eof with explicit yes", response: "y", readErr: io.EOF, want: true},
		{name: "read error fails closed", response: "", readErr: io.ErrClosedPipe, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldProceedWithRemoteUpdate(tc.response, tc.readErr)
			if got != tc.want {
				t.Fatalf("shouldProceedWithRemoteUpdate(%q, %v) = %v, want %v", tc.response, tc.readErr, got, tc.want)
			}
		})
	}
}
