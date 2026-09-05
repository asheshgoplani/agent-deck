package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountDoctorDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOCTOR_DIR", filepath.Join(home, "shared"))
	t.Setenv("DOCTOR_EMPTY", "")
	shared := filepath.Join(home, "shared")
	distinct := filepath.Join(home, "distinct")
	locked := filepath.Join(home, "locked")
	for _, p := range []string{shared, distinct, locked} {
		require.NoError(t, os.Mkdir(p, 0700))
	}
	require.NoError(t, os.Symlink(shared, filepath.Join(home, "alias")))
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0700) })
	f, err := os.Open(locked)
	if err == nil {
		_ = f.Close()
		t.Fatal("permission preflight invalid: mode000 directory accessible")
	}
	require.NoError(t, os.WriteFile(filepath.Join(home, "file"), []byte("not credentials"), 0600))
	for _, tc := range []struct{ name, a, b, state, pathState string }{
		{"tilde_env", "~/shared", "$DOCTOR_DIR", "warning", "accessible"},
		{"symlink", "~/shared", "~/alias", "warning", "accessible"},
		{"clean", "~/shared/../shared", "~/shared", "warning", "accessible"},
		{"distinct", "~/shared", "~/distinct", "ok", "accessible"},
		{"same_missing", "~/missing", "~/missing", "warning", "unknown"},
		{"different_missing", "~/missing", "~/other-missing", "unknown", "unknown"},
		{"same_unreadable", "~/locked", "~/locked", "warning", "unknown"},
		{"file", "~/file", "~/distinct", "unknown", "unknown"},
		{"empty_expansion", "$DOCTOR_EMPTY", "$DOCTOR_EMPTY", "unknown", "unknown"},
		{"relative", "shared", "shared", "unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &UserConfig{Profiles: map[string]ProfileSettings{"a": {Claude: ProfileClaudeSettings{ConfigDir: tc.a}}, "b": {Claude: ProfileClaudeSettings{ConfigDir: tc.b}}, "codex-only": {Codex: ProfileCodexSettings{ConfigDir: "~/shared"}}, "empty": {}}}
			got := DiagnoseClaudeAccountDirectories(cfg)
			require.Len(t, got, 2)
			require.Equal(t, "a", got[0].Name)
			require.Equal(t, tc.state, got[0].State)
			require.Equal(t, tc.pathState, got[0].PathState)
			if tc.state == "warning" {
				require.Equal(t, []string{"b"}, got[0].SharedWith)
				require.Equal(t, []string{"a"}, got[1].SharedWith)
			} else {
				require.Empty(t, got[0].SharedWith)
			}
		})
	}
	require.Empty(t, DiagnoseClaudeAccountDirectories(nil))
	require.Empty(t, DiagnoseClaudeAccountDirectories(&UserConfig{}))
}

func TestAccountDoctorSymlinkParentSemantics(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "other", "nested")
	for _, p := range []string{target, filepath.Join(home, "shared"), filepath.Join(home, "other", "shared")} {
		require.NoError(t, os.MkdirAll(p, 0700))
	}
	require.NoError(t, os.Symlink(target, filepath.Join(home, "link")))
	throughLink := home + "/link/../shared"
	for _, tc := range []struct{ name, other, want string }{
		{"not_lexical_parent", filepath.Join(home, "shared"), "ok"},
		{"actual_parent", filepath.Join(home, "other", "shared"), "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &UserConfig{Profiles: map[string]ProfileSettings{"a": {Claude: ProfileClaudeSettings{ConfigDir: throughLink}}, "b": {Claude: ProfileClaudeSettings{ConfigDir: tc.other}}}}
			require.Equal(t, throughLink, cfg.GetProfileClaudeConfigDir("a"), "runtime must preserve absolute symlink-parent spelling")
			got := DiagnoseClaudeAccountDirectories(cfg)
			require.Equal(t, tc.want, got[0].State)
		})
	}
}
