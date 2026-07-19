package main

import "testing"

// TestShouldLockTitle codifies the #1615-class chokepoint: an explicit,
// user-provided title (-t/--title) must be TitleLocked so Claude's folder-name
// sync can never silently clobber it — matching the TUI dialog and `launch`
// paths. The explicit lock flags remain independent triggers; an auto-derived
// (folder-name) title stays unlocked so name-sync keeps working.
func TestShouldLockTitle(t *testing.T) {
	tests := []struct {
		name              string
		userProvidedTitle bool
		titleLockFlag     bool
		noTitleSyncFlag   bool
		want              bool
	}{
		{name: "explicit -t title locks", userProvidedTitle: true, want: true},
		{name: "--title-lock locks", titleLockFlag: true, want: true},
		{name: "--no-title-sync locks", noTitleSyncFlag: true, want: true},
		{name: "auto folder-name title stays unlocked", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldLockTitle(tt.userProvidedTitle, tt.titleLockFlag, tt.noTitleSyncFlag)
			if got != tt.want {
				t.Fatalf("shouldLockTitle(%v,%v,%v) = %v, want %v",
					tt.userProvidedTitle, tt.titleLockFlag, tt.noTitleSyncFlag, got, tt.want)
			}
		})
	}
}
