package main

import (
	"strings"
	"testing"
)

// These cover the `launch -g <selector>` group resolver.
//
// Regression: `launch -g baba` took the selector as a literal path and created a
// stray top-level `baba` group next to the existing `doozyx/baba`, detaching the
// child from its siblings. `add -g baba` already resolved the leaf name, so the
// two commands disagreed.

func TestResolveGroupSelectorToPath_BareLeafNameResolvesToNestedGroup(t *testing.T) {
	got, err := resolveGroupSelectorToPath([]string{"doozyx/baba"}, "baba")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "doozyx/baba" {
		t.Fatalf("expected leaf selector to resolve to doozyx/baba, got %q", got)
	}
}

func TestResolveGroupSelectorToPath_ExactPathWinsOverLeafMatch(t *testing.T) {
	got, err := resolveGroupSelectorToPath([]string{"doozyx/baba", "baba"}, "baba")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "baba" {
		t.Fatalf("expected exact path to win, got %q", got)
	}
}

// A selector matching nothing must stay literal: that is how -g deliberately
// creates a new group.
func TestResolveGroupSelectorToPath_UnknownSelectorStaysLiteral(t *testing.T) {
	got, err := resolveGroupSelectorToPath([]string{"doozyx/baba"}, "brand-new")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "brand-new" {
		t.Fatalf("expected unknown selector to stay literal, got %q", got)
	}
}

func TestResolveGroupSelectorToPath_NormalizedNameResolves(t *testing.T) {
	got, err := resolveGroupSelectorToPath([]string{"my-team"}, "My Team")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "my-team" {
		t.Fatalf("expected normalized selector to resolve to my-team, got %q", got)
	}
}

// Two groups sharing a leaf make a bare leaf selector ambiguous. Guessing would
// silently drop the session in the wrong group, so this must error.
func TestResolveGroupSelectorToPath_AmbiguousLeafErrors(t *testing.T) {
	_, err := resolveGroupSelectorToPath([]string{"work/api", "personal/api"}, "api")
	if err == nil {
		t.Fatal("expected ambiguous leaf selector to error")
	}
	for _, want := range []string{"work/api", "personal/api"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to list candidate %q, got %q", want, err.Error())
		}
	}
}

// An ambiguous leaf that is also an exact path is not ambiguous.
func TestResolveGroupSelectorToPath_ExactPathBeatsAmbiguity(t *testing.T) {
	got, err := resolveGroupSelectorToPath([]string{"work/api", "personal/api", "api"}, "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "api" {
		t.Fatalf("expected exact path to win over ambiguous leaves, got %q", got)
	}
}
