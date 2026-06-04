package buildinfo

import "testing"

func TestCommitPrefersOverride(t *testing.T) {
	if got := Commit("ab44d360"); got != "ab44d360" {
		t.Fatalf("Commit override: got %q, want %q", got, "ab44d360")
	}
}

func TestCommitTrimsOverride(t *testing.T) {
	if got := Commit("  ab44d360\n"); got != "ab44d360" {
		t.Fatalf("Commit should trim whitespace: got %q", got)
	}
}

func TestCommitFallbackNeverEmpty(t *testing.T) {
	// With no override, the result is either the embedded short VCS revision
	// or the "unknown" sentinel — never empty, and never longer than the
	// short width.
	got := Commit("")
	if got == "" {
		t.Fatal("Commit(\"\") returned empty string; want hash or \"unknown\"")
	}
	if got != "unknown" && len(got) > shortLen {
		t.Fatalf("fallback hash %q exceeds short width %d", got, shortLen)
	}
}
