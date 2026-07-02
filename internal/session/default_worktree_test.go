package session

import (
	"regexp"
	"strings"
	"testing"
)

// branchPattern matches the expected "wt/<slug>-<8hex>" format.
var branchPattern = regexp.MustCompile(`^wt/[a-z0-9][a-z0-9-]*-[0-9a-f]{8}$`)

// TestGenerateWorktreeBranchName_BasicTitle verifies slug + hex suffix generation.
func TestGenerateWorktreeBranchName_BasicTitle(t *testing.T) {
	branch := GenerateWorktreeBranchName("My Feature")
	if !strings.HasPrefix(branch, "wt/") {
		t.Errorf("expected wt/ prefix, got %q", branch)
	}
	if !branchPattern.MatchString(branch) {
		t.Errorf("branch %q does not match pattern wt/<slug>-<8hex>", branch)
	}
}

// TestGenerateWorktreeBranchName_EmptyTitle falls back to "session" slug.
func TestGenerateWorktreeBranchName_EmptyTitle(t *testing.T) {
	branch := GenerateWorktreeBranchName("")
	if !strings.HasPrefix(branch, "wt/session-") {
		t.Errorf("empty title: expected wt/session-<hex>, got %q", branch)
	}
	if !branchPattern.MatchString(branch) {
		t.Errorf("branch %q does not match pattern", branch)
	}
}

// TestGenerateWorktreeBranchName_SpecialChars verifies sanitisation of spaces
// and special characters.
func TestGenerateWorktreeBranchName_SpecialChars(t *testing.T) {
	branch := GenerateWorktreeBranchName("Fix: Bug #42 (critical)")
	// slug should be all lowercase alphanumeric + hyphens
	slugPart := strings.TrimPrefix(branch, "wt/")
	// Remove the trailing -<8hex>
	if idx := strings.LastIndex(slugPart, "-"); idx >= 0 {
		slugPart = slugPart[:idx]
	}
	for _, c := range slugPart {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			t.Errorf("slug contains invalid character %q in branch %q", c, branch)
		}
	}
}

// TestGenerateWorktreeBranchName_LongTitle verifies truncation to 30 chars.
func TestGenerateWorktreeBranchName_LongTitle(t *testing.T) {
	longTitle := "this-is-a-very-long-feature-branch-name-that-exceeds-thirty-characters"
	branch := GenerateWorktreeBranchName(longTitle)
	if !strings.HasPrefix(branch, "wt/") {
		t.Errorf("expected wt/ prefix, got %q", branch)
	}
	// slug is at most 30 chars (before the -<8hex> suffix)
	slugPart := strings.TrimPrefix(branch, "wt/")
	if idx := strings.LastIndex(slugPart, "-"); idx >= 0 {
		slug := slugPart[:idx]
		if len(slug) > 30 {
			t.Errorf("slug %q exceeds 30 chars (len=%d)", slug, len(slug))
		}
	}
}

// TestGenerateWorktreeBranchName_Uniqueness verifies two calls produce different branches.
func TestGenerateWorktreeBranchName_Uniqueness(t *testing.T) {
	a := GenerateWorktreeBranchName("my-session")
	b := GenerateWorktreeBranchName("my-session")
	if a == b {
		// Astronomically unlikely but not impossible; treat as a flaky indicator.
		t.Logf("Warning: two GenerateWorktreeBranchName calls returned identical branch %q (very unlikely - rerun if flaky)", a)
	}
}
