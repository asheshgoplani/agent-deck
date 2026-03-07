package vcs

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ValidateBranchName validates that a branch/bookmark name follows naming rules.
// The rules are the same for git branches and jj bookmarks.
func ValidateBranchName(name string) error {
	if name == "" {
		return errors.New("branch name cannot be empty")
	}

	if strings.TrimSpace(name) != name {
		return errors.New("branch name cannot have leading or trailing spaces")
	}

	if strings.Contains(name, "..") {
		return errors.New("branch name cannot contain '..'")
	}

	if strings.HasPrefix(name, ".") {
		return errors.New("branch name cannot start with '.'")
	}

	if strings.HasSuffix(name, ".lock") {
		return errors.New("branch name cannot end with '.lock'")
	}

	invalidChars := []string{" ", "\t", "~", "^", ":", "?", "*", "[", "\\"}
	for _, char := range invalidChars {
		if strings.Contains(name, char) {
			return fmt.Errorf("branch name cannot contain '%s'", char)
		}
	}

	if strings.Contains(name, "@{") {
		return errors.New("branch name cannot contain '@{'")
	}

	if name == "@" {
		return errors.New("branch name cannot be just '@'")
	}

	return nil
}

// SanitizeBranchName converts a string to a valid branch/bookmark name.
func SanitizeBranchName(name string) string {
	replacer := strings.NewReplacer(
		" ", "-",
		"..", "-",
		"~", "-",
		"^", "-",
		":", "-",
		"?", "-",
		"*", "-",
		"[", "-",
		"\\", "-",
		"@{", "-",
	)

	sanitized := replacer.Replace(name)

	for strings.HasPrefix(sanitized, ".") {
		sanitized = strings.TrimPrefix(sanitized, ".")
	}

	for strings.HasSuffix(sanitized, ".lock") {
		sanitized = strings.TrimSuffix(sanitized, ".lock")
	}

	re := regexp.MustCompile(`-+`)
	sanitized = re.ReplaceAllString(sanitized, "-")

	sanitized = strings.Trim(sanitized, "-")

	return sanitized
}
