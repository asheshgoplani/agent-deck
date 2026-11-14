package provider

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	userPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)
	labelKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}$`)
	dnsLabelPattern = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?$`)
	hostKeyType     = regexp.MustCompile(`^[a-z0-9][a-z0-9@._-]{0,63}$`)
)

const (
	maxLabels       = 32
	maxLabelValue   = 256
	maxHostKeys     = 16
	maxHostLength   = 253
	maxHostKeyBytes = 16 << 10
)

// Sandbox states a provider may report.
var validStates = map[string]bool{"": true, "running": true, "starting": true, "stopped": true, "unknown": true}

func validID(id string) bool { return idPattern.MatchString(id) }

// Validate checks a provider's sandbox before agent-deck writes any file or
// runs ssh from it (provider output is untrusted), and fills the default
// port (22). Key auth also checks the key file on disk.
func (s *Sandbox) Validate() error {
	if !validID(s.ID) {
		return fmt.Errorf("invalid sandbox id %q", sanitize(s.ID))
	}
	if err := validateHost(s.Host); err != nil {
		return err
	}
	if s.Port == 0 {
		s.Port = 22
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("invalid sandbox port %d (want 1-65535)", s.Port)
	}
	if !userPattern.MatchString(s.User) {
		return fmt.Errorf("invalid sandbox user %q", sanitize(s.User))
	}
	switch s.Auth.Type {
	case "key":
		if err := validateKeyPath(s.Auth.KeyPath); err != nil {
			return err
		}
	case "agent":
		if s.Auth.KeyPath != "" {
			return fmt.Errorf("invalid sandbox auth: key_path is only valid with type \"key\"")
		}
	default:
		return fmt.Errorf("invalid sandbox auth type %q (want \"key\" or \"agent\")", sanitize(s.Auth.Type))
	}
	if err := validateLabels(s.Labels); err != nil {
		return err
	}
	if !validStates[s.State] {
		return fmt.Errorf("invalid sandbox state %q (want running, starting, stopped or unknown)", sanitize(s.State))
	}
	if len(s.HostKeys) > maxHostKeys {
		return fmt.Errorf("invalid sandbox host_keys: %d keys (max %d)", len(s.HostKeys), maxHostKeys)
	}
	for i, k := range s.HostKeys {
		if err := validateHostKey(k); err != nil {
			return fmt.Errorf("invalid sandbox host_keys[%d]: %w", i, err)
		}
	}
	return nil
}

// validateHost accepts a DNS name, an IPv4 literal or an IPv6 literal (no
// zone) and nothing ssh or an ssh_config could read as more than a host.
func validateHost(host string) error {
	switch {
	case host == "":
		return fmt.Errorf("invalid sandbox host: empty")
	case len(host) > maxHostLength:
		return fmt.Errorf("invalid sandbox host: longer than %d bytes", maxHostLength)
	case strings.HasPrefix(host, "-"):
		return fmt.Errorf("invalid sandbox host %q: must not begin with '-'", sanitize(host))
	case strings.ContainsAny(host, "@%/\\\"'` \t\r\n"):
		return fmt.Errorf("invalid sandbox host %q: only a DNS name or IP literal is allowed", sanitize(host))
	}
	if ip := net.ParseIP(host); ip != nil {
		return nil
	}
	for _, label := range strings.Split(host, ".") {
		if !dnsLabelPattern.MatchString(label) {
			return fmt.Errorf("invalid sandbox host %q: only a DNS name or IP literal is allowed", sanitize(host))
		}
	}
	return nil
}

// validateKeyPath checks a key-auth private key: an absolute, clean path
// with no characters ssh_config would interpret, naming a regular file (not
// a symlink) that only its owner, the current user, can read.
func validateKeyPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("invalid sandbox auth key_path: empty for key auth")
	case !filepath.IsAbs(p):
		return fmt.Errorf("invalid sandbox auth key_path %q: must be absolute", sanitize(p))
	case filepath.Clean(p) != p || containsDotDot(p):
		return fmt.Errorf("invalid sandbox auth key_path %q: must be a clean path without \"..\"", sanitize(p))
	case !safeConfigPath(p):
		return fmt.Errorf("invalid sandbox auth key_path %q: contains a quote, %%, or control character", sanitize(p))
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("invalid sandbox auth key_path: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid sandbox auth key_path %q: is a symlink", p)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("invalid sandbox auth key_path %q: not a regular file", p)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("invalid sandbox auth key_path %q: mode %o gives group or other access (want 0600 or 0400)", p, fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("invalid sandbox auth key_path %q: owned by uid %d, not the current user", p, st.Uid)
	}
	return nil
}

func containsDotDot(p string) bool {
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}

// safeConfigPath reports whether p can go in a quoted ssh_config value:
// no quotes, no % (ssh token expansion), no control characters.
func safeConfigPath(p string) bool {
	if !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if r == '"' || r == '%' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validateLabels(labels map[string]string) error {
	if len(labels) > maxLabels {
		return fmt.Errorf("invalid sandbox labels: %d labels (max %d)", len(labels), maxLabels)
	}
	for k, v := range labels {
		if !labelKeyPattern.MatchString(k) {
			return fmt.Errorf("invalid sandbox label key %q", sanitize(k))
		}
		if utf8.RuneCountInString(v) > maxLabelValue || !utf8.ValidString(v) {
			return fmt.Errorf("invalid sandbox label %q: value longer than %d characters or not UTF-8", k, maxLabelValue)
		}
		for _, r := range v {
			if !unicode.IsPrint(r) {
				return fmt.Errorf("invalid sandbox label %q: value contains a control or non-printable character", k)
			}
		}
	}
	return nil
}

// validateHostKey accepts one "<type> <base64>" public key line.
func validateHostKey(k string) error {
	if len(k) > maxHostKeyBytes {
		return errors.New("key too long")
	}
	fields := strings.Split(k, " ")
	if len(fields) != 2 || !hostKeyType.MatchString(fields[0]) || fields[1] == "" {
		return errors.New("want one line of the form \"<type> <base64>\"")
	}
	if _, err := base64.StdEncoding.DecodeString(fields[1]); err != nil {
		return fmt.Errorf("key is not base64: %w", err)
	}
	return nil
}
