// Package macapp holds the wire contract between agent-deck sessions and the
// macOS app's Browser panel: the frames a session writes to its own events
// log and the answers the app writes back (docs/macapp-browser.md).
package macapp

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrNotURL reports a target with no URL scheme. Callers that also accept
// file paths treat it as "this is a path", not as a refusal.
var ErrNotURL = errors.New("target is not a URL")

// ValidateURL applies the open command's URL rules: an absolute http or
// https URL with a host and without credentials or control characters. It
// returns the normalized URL. A target without a scheme (and without "://")
// returns ErrNotURL.
func ValidateURL(target string) (string, error) {
	if strings.ContainsAny(target, "\x00\r\n") || strings.TrimSpace(target) == "" {
		return "", errors.New("empty target or control characters in target")
	}
	u, parseErr := url.Parse(target)
	if parseErr == nil && u.Scheme != "" {
		if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
			return "", errors.New("URL must be an absolute http or https URL without credentials")
		}
		return u.String(), nil
	}
	if strings.Contains(target, "://") {
		return "", fmt.Errorf("invalid URL: %q", target)
	}
	return "", ErrNotURL
}
