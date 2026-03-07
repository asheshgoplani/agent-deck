package vcs

import (
	"testing"
)

func TestGenerateWorktreePath(t *testing.T) {
	tests := []struct {
		name     string
		repoDir  string
		branch   string
		location string
		want     string
	}{
		{
			name:     "sibling default",
			repoDir:  "/home/user/myrepo",
			branch:   "feature-x",
			location: "",
			want:     "/home/user/myrepo-feature-x",
		},
		{
			name:     "sibling explicit",
			repoDir:  "/home/user/myrepo",
			branch:   "feature-x",
			location: "sibling",
			want:     "/home/user/myrepo-feature-x",
		},
		{
			name:     "subdirectory",
			repoDir:  "/home/user/myrepo",
			branch:   "feature-x",
			location: "subdirectory",
			want:     "/home/user/myrepo/.worktrees/feature-x",
		},
		{
			name:     "slash sanitization",
			repoDir:  "/home/user/myrepo",
			branch:   "feature/foo",
			location: "",
			want:     "/home/user/myrepo-feature-foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateWorktreePath(tt.repoDir, tt.branch, tt.location)
			if got != tt.want {
				t.Errorf("GenerateWorktreePath(%q, %q, %q) = %q, want %q",
					tt.repoDir, tt.branch, tt.location, got, tt.want)
			}
		})
	}
}

func TestWorktreePath_WithTemplate(t *testing.T) {
	got := WorktreePath(WorktreePathOptions{
		Branch:   "feature/test",
		RepoDir:  "/home/user/myrepo",
		Template: "/tmp/worktrees/{repo-name}/{branch}",
	})
	want := "/tmp/worktrees/myrepo/feature-test"
	if got != want {
		t.Errorf("WorktreePath with template = %q, want %q", got, want)
	}
}

func TestWorktreePath_FallbackWithoutTemplate(t *testing.T) {
	got := WorktreePath(WorktreePathOptions{
		Branch:   "dev",
		RepoDir:  "/home/user/myrepo",
		Location: "sibling",
	})
	want := "/home/user/myrepo-dev"
	if got != want {
		t.Errorf("WorktreePath fallback = %q, want %q", got, want)
	}
}

func TestGeneratePathID(t *testing.T) {
	id := GeneratePathID()
	if len(id) != 8 {
		t.Errorf("GeneratePathID() length = %d, want 8", len(id))
	}

	// Check uniqueness
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		pid := GeneratePathID()
		if seen[pid] {
			t.Errorf("GeneratePathID() produced duplicate: %s", pid)
		}
		seen[pid] = true
	}
}
