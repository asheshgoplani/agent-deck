package vcs

import "testing"

func TestValidateBranchName(t *testing.T) {
	t.Run("valid names", func(t *testing.T) {
		validNames := []string{
			"main",
			"feature/test",
			"fix-123",
			"v1.0.0",
			"my-branch",
		}
		for _, name := range validNames {
			if err := ValidateBranchName(name); err != nil {
				t.Errorf("expected %q to be valid, got error: %v", name, err)
			}
		}
	})

	t.Run("invalid names", func(t *testing.T) {
		invalidNames := []string{
			"",
			" leading",
			"trailing ",
			"has..dots",
			".leading-dot",
			"name.lock",
			"has space",
			"has\ttab",
			"has~tilde",
			"has^caret",
			"has:colon",
			"has?question",
			"has*star",
			"has[bracket",
			"has\\backslash",
			"has@{sequence",
			"@",
		}
		for _, name := range invalidNames {
			if err := ValidateBranchName(name); err == nil {
				t.Errorf("expected %q to be invalid, but got no error", name)
			}
		}
	})
}

func TestSanitizeBranchName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello world", "hello-world"},
		{"has..dots", "has-dots"},
		{".leading-dot", "leading-dot"},
		{"name.lock", "name"},
		{"has~tilde", "has-tilde"},
		{"multiple---dashes", "multiple-dashes"},
		{"---leading-trailing---", "leading-trailing"},
	}
	for _, tt := range tests {
		got := SanitizeBranchName(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeBranchName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
