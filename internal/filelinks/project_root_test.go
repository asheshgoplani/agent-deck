package filelinks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithProjectRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	project := t.TempDir()
	cases := []struct {
		name, project string
		added         bool
	}{
		{"plain project", project, true},
		{"relative", "work/proj", false},
		{"empty", "", false},
		{"filesystem root", "/", false},
		{"home itself", home, false},
		{"inside ~/.ssh", filepath.Join(home, ".ssh", "keys"), false},
	}
	for _, tc := range cases {
		got := WithProjectRoot([]string{"/srv/reports"}, tc.project)
		added := len(got) == 2 && got[1] == filepath.Clean(tc.project)
		if added != tc.added || got[0] != "/srv/reports" {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	if got := WithProjectRoot([]string{project}, project); len(got) != 1 {
		t.Errorf("duplicate: %v", got)
	}
}
