package model

import "testing"

func TestLabelFallsBackToUntitled(t *testing.T) {
	if got := (Session{}).Label(); got != "(untitled)" {
		t.Fatalf("empty title: got %q want %q", got, "(untitled)")
	}
	if got := (Session{Title: "Fix S3 cache"}).Label(); got != "Fix S3 cache" {
		t.Fatalf("with title: got %q", got)
	}
}
