package main

import (
	"strings"
	"testing"
)

// secweb #1: `agent-deck web` must refuse to start on a non-loopback bind
// when no auth token is set, before the TUI boots. See /tmp/sec-web-REPORT.md.

func TestBuildWebServer_RefusesNonLoopbackWithoutToken(t *testing.T) {
	_, err := buildWebServer("test-profile", []string{"--listen", "0.0.0.0:8420"}, nil, noopMutator{})
	if err == nil {
		t.Fatal("expected buildWebServer to refuse non-loopback bind without a token")
	}
	if !strings.Contains(err.Error(), "--token") || !strings.Contains(err.Error(), "--insecure-bind") {
		t.Fatalf("refusal error should be actionable (mention --token and --insecure-bind), got: %v", err)
	}
}

func TestBuildWebServer_AllowsNonLoopbackWithToken(t *testing.T) {
	srv, err := buildWebServer("test-profile", []string{"--listen", "0.0.0.0:0", "--token", "secret"}, nil, noopMutator{})
	if err != nil {
		t.Fatalf("non-loopback bind with token should be allowed, got %v", err)
	}
	if srv == nil {
		t.Fatal("expected a server")
	}
}

func TestBuildWebServer_AllowsNonLoopbackWithInsecureBind(t *testing.T) {
	srv, err := buildWebServer("test-profile", []string{"--listen", "0.0.0.0:0", "--insecure-bind"}, nil, noopMutator{})
	if err != nil {
		t.Fatalf("non-loopback bind with --insecure-bind should be allowed, got %v", err)
	}
	if srv == nil {
		t.Fatal("expected a server")
	}
}

func TestBuildWebServer_DefaultLoopbackUnchanged(t *testing.T) {
	srv, err := buildWebServer("test-profile", []string{"--listen", "127.0.0.1:0"}, nil, noopMutator{})
	if err != nil {
		t.Fatalf("default loopback bind without token should still work, got %v", err)
	}
	if srv == nil {
		t.Fatal("expected a server")
	}
}
