package main

import (
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

func TestPortForwardFlags_Set(t *testing.T) {
	t.Parallel()
	var flags portForwardFlags
	if err := flags.Set("L:8444:localhost:8444"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	if err := flags.Set("R:3000:localhost:3000"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	if len(flags) != 2 {
		t.Fatalf("expected 2 flags, got %d", len(flags))
	}
	if flags[0] != "L:8444:localhost:8444" {
		t.Errorf("flags[0] = %q, want %q", flags[0], "L:8444:localhost:8444")
	}
	if flags[1] != "R:3000:localhost:3000" {
		t.Errorf("flags[1] = %q, want %q", flags[1], "R:3000:localhost:3000")
	}
}

func TestPortForwardFlags_String(t *testing.T) {
	t.Parallel()
	flags := portForwardFlags{"L:8080:localhost:8080", "D:1080"}
	got := flags.String()
	if got != "L:8080:localhost:8080,D:1080" {
		t.Errorf("String() = %q, want %q", got, "L:8080:localhost:8080,D:1080")
	}

	var empty portForwardFlags
	if s := empty.String(); s != "" {
		t.Errorf("empty String() = %q, want %q", s, "")
	}
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
