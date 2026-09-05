package session

import (
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountDoctorDirectories(t *testing.T) {
	if accountDoctorUnprivileged(t) {
		return
	}
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

func TestAccountDoctorEffectiveSearchPermission(t *testing.T) {
	if accountDoctorUnprivileged(t) {
		return
	}
	home := t.TempDir()
	dir := filepath.Join(home, "read-without-owner-search")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.Chmod(dir, 0401))
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	f, err := os.Open(dir)
	require.NoError(t, err, "fixture must permit directory read")
	require.NoError(t, f.Close())
	_, err = os.Stat(dir + string(os.PathSeparator) + ".")
	require.Error(t, err, "fixture must deny effective-identity search")
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{"owner": {Claude: ProfileClaudeSettings{ConfigDir: dir}}}}
	got := DiagnoseClaudeAccountDirectories(cfg)
	require.Equal(t, "unknown", got[0].State)
	require.Equal(t, "unknown", got[0].PathState)
}

// Run permission fixtures as an ordinary identity even when the suite is root.
func accountDoctorUnprivileged(t *testing.T) bool {
	t.Helper()
	if os.Geteuid() != 0 {
		if os.Getenv("DOCTOR_PERMISSION_CHILD") == "1" {
			require.Equal(t, 65534, os.Geteuid())
			require.Equal(t, 65534, os.Getegid())
			groups, err := os.Getgroups()
			require.NoError(t, err)
			require.Empty(t, groups)
			t.Log("permission child verified: uid=65534 gid=65534 supplemental groups empty")
		}
		return false
	}
	sandbox, err := os.MkdirTemp("/tmp", "doctor-permission-")
	require.NoError(t, err)
	t.Cleanup(func() {
		// Only walk this freshly created sandbox. Restore directory ownership before
		// descending, including leftovers from a timed-out unprivileged child.
		err := filepath.WalkDir(sandbox, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if err := os.Chown(path, 0, 0); err != nil {
					return err
				}
				return os.Chmod(path, 0700)
			}
			return nil
		})
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(sandbox))
	})
	binary := filepath.Join(sandbox, "session.test")
	executable, err := os.Executable()
	require.NoError(t, err)
	source, err := os.Open(executable)
	require.NoError(t, err)
	target, err := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	require.NoError(t, err)
	_, copyErr := io.Copy(target, source)
	require.NoError(t, source.Close())
	require.NoError(t, target.Close())
	require.NoError(t, copyErr)
	require.NoError(t, os.Chmod(binary, 0755))
	require.NoError(t, os.Chown(sandbox, 65534, 65534))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^"+t.Name()+"$", "-test.v", "-test.timeout=30s")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = sandbox
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "AGENTDECK_") || strings.HasPrefix(key, "XDG_") || key == "HOME" || key == "TMPDIR" || key == "CODEX_HOME" || key == "CLAUDE_CONFIG_DIR" || key == "TMUX" || key == "TMUX_TMPDIR" || key == "DOCTOR_PERMISSION_CHILD" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "HOME="+sandbox, "TMPDIR="+sandbox, "XDG_CONFIG_HOME="+sandbox, "XDG_DATA_HOME="+sandbox, "XDG_CACHE_HOME="+sandbox, "DOCTOR_PERMISSION_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}}}
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	require.NoError(t, err, "unprivileged permission controls must execute")
	require.Contains(t, string(output), "permission child verified:")
	return true
}
