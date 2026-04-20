package session

import (
	"reflect"
	"testing"
)

func TestParseZoxideOutput_StripsBlankAndWhitespace(t *testing.T) {

	raw := []byte("/Users/me/proj-a\n\n  /Users/me/proj-b  \n\t\n/Users/me/proj-c\n")

	got := parseZoxideOutput(raw)

	want := []string{"/Users/me/proj-a", "/Users/me/proj-b", "/Users/me/proj-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseZoxideOutput = %v, want %v", got, want)
	}
}

func TestParseZoxideOutput_EmptyInput(t *testing.T) {

	got := parseZoxideOutput(nil)

	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}
