package session

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The rebind gate's size and mtime must see every key Claude may have keyed
// the project by, including the macOS /private form (ASK-92).
func TestConversationKeyPaths_IncludesMacOSPrivateForm(t *testing.T) {
	const cfg = "/cfg"
	noResolve := func(string) (string, error) { return "", errors.New("not found") }
	keyPath := func(dir string) string {
		return filepath.Join(cfg, "projects", ConvertToClaudeDirName(dir), "s.jsonl")
	}
	for _, tc := range []struct {
		name, project, goos string
		resolve             func(string) (string, error)
		want                []string
	}{
		{"darwin /tmp", "/tmp/proj", "darwin", noResolve,
			[]string{keyPath("/tmp/proj"), keyPath("/private/tmp/proj")}},
		{"darwin /private/var", "/private/var/folders/x/proj", "darwin", noResolve,
			[]string{keyPath("/private/var/folders/x/proj"), keyPath("/var/folders/x/proj")}},
		{"darwin /var with realpath", "/var/folders/x/proj", "darwin",
			func(string) (string, error) { return "/private/var/folders/x/proj", nil },
			[]string{keyPath("/var/folders/x/proj"), keyPath("/private/var/folders/x/proj")}},
		{"linux symlink", "/home/u/link", "linux",
			func(string) (string, error) { return "/srv/real", nil },
			[]string{keyPath("/home/u/link"), keyPath("/srv/real")}},
		{"linux /tmp has no alias", "/tmp/proj", "linux", noResolve,
			[]string{keyPath("/tmp/proj")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, conversationKeyPathsWith(cfg, tc.project, "s.jsonl", tc.goos, tc.resolve))
		})
	}
}
