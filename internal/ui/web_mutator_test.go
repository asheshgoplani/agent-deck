package ui

import (
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestParseWebEnvList(t *testing.T) {
	got, err := parseWebEnvList("FOO=bar\n  BAZ=qux  \n\nHTTPS_PROXY=http://h:8080")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"FOO=bar", "BAZ=qux", "HTTPS_PROXY=http://h:8080"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseWebEnvList = %v, want %v", got, want)
	}

	if out, err := parseWebEnvList(""); err != nil || out != nil {
		t.Fatalf("empty payload should clear env: got %v, err %v", out, err)
	}

	out, err := parseWebEnvList("1BAD=x")
	if err == nil {
		t.Fatalf("expected error for invalid key, got %v", out)
	}
	if _, ok := err.(*session.MutationError); !ok {
		t.Fatalf("expected *session.MutationError so the handler maps to 400, got %T", err)
	}
}

func TestSanitizeSessionEnv(t *testing.T) {
	got := sanitizeSessionEnv([]string{"FOO=bar", "  ", "1BAD=x", "OK_2=y"})
	want := []string{"FOO=bar", "OK_2=y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizeSessionEnv = %v, want %v", got, want)
	}
}
