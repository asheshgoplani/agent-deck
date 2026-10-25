package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// Review fixes for FX-11: launch env files must not pile up, the state-root
// tighten must reach both roots, a failed os/user lookup fails closed, and
// a skipped dir-local config is reported.

func launchEnvFileNames(t *testing.T) []string {
	t.Helper()
	dir, err := runtimeDataPath(launchEnvDirName)
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// ageLaunchEnvFiles moves every launch env file's time past the prune grace.
func ageLaunchEnvFiles(t *testing.T, by time.Duration) {
	t.Helper()
	dir, err := runtimeDataPath(launchEnvDirName)
	require.NoError(t, err)
	old := time.Now().Add(-by)
	for _, n := range launchEnvFileNames(t) {
		require.NoError(t, os.Chtimes(filepath.Join(dir, n), old, old))
	}
}

func TestLaunchEnvFiles_SupersededRestartEnvRemoved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := &Instance{ID: "fx11-rot", restartEnv: map[string]string{"FX11_TOKEN": "first-secret"}}
	_ = inst.buildRestartEnvPrefix()
	ageLaunchEnvFiles(t, launchEnvPruneGrace+time.Minute)
	inst.restartEnv = map[string]string{"FX11_TOKEN": "second-secret"}
	_ = inst.buildRestartEnvPrefix()
	require.Len(t, launchEnvFileNames(t), 1, "an older restart env file of the same instance must be removed")

	// A launch with no restart overrides drops the one-shot restart file.
	ageLaunchEnvFiles(t, launchEnvPruneGrace+time.Minute)
	inst.restartEnv = nil
	_ = inst.buildEnvSourceCommand()
	require.Empty(t, launchEnvFileNames(t), "restart env file must not outlive the restart that used it")
}

// LOW-3: a command saved before a rebuild (systemd Restart=, a recreated
// session, a racing TUI/CLI build) still sources its file: superseded files
// survive the grace, and a missing file does not stop the launch chain.
func TestLaunchEnvFiles_SupersededFileKeptWithinGrace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := &Instance{ID: "fx11-grace", restartEnv: map[string]string{"FX11_TOKEN": "first"}}
	first := inst.buildRestartEnvPrefix()
	inst.restartEnv = map[string]string{"FX11_TOKEN": "second"}
	_ = inst.buildRestartEnvPrefix()
	inst.restartEnv = nil
	_ = inst.buildEnvSourceCommand()
	require.Len(t, launchEnvFileNames(t), 2, "files still within the grace were removed")
	require.Equal(t, "first", loc4RunPrefix(t, first, "FX11_TOKEN"), "the earlier saved command lost its env")
}

func TestLaunchEnvFiles_MissingFileDoesNotStopLaunch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := &Instance{ID: "fx11-missing", restartEnv: map[string]string{"FX11_TOKEN": "gone"}}
	prefix := inst.buildRestartEnvPrefix()
	inst.restartEnv = nil
	removeLaunchEnvFiles(inst.ID)
	require.Empty(t, launchEnvFileNames(t))

	out, err := exec.Command("bash", "-c", prefix+`printf 'launched:%s' "$FX11_TOKEN"`).CombinedOutput()
	require.NoError(t, err, "launch chain failed on a missing env file: %s", out)
	require.Contains(t, string(out), "launched:")
	require.Contains(t, string(out), "launch env file missing")
}

// LOW-5: the file name (in argv) carries nothing derived from the values.
func TestLaunchEnvFiles_NameNotDerivedFromContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const exports = "export FX11_TOKEN='same-secret'"
	a, err := writeLaunchEnvFile("fx11-a", launchEnvKindTool, exports)
	require.NoError(t, err)
	b, err := writeLaunchEnvFile("fx11-b", launchEnvKindTool, exports)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(exports + "\n"))
	digest := hex.EncodeToString(sum[:8])
	for _, p := range []string{a, b} {
		require.NotContains(t, filepath.Base(p), digest, "file name carries a digest of the exports")
	}
	require.NotEqual(t, strings.TrimPrefix(filepath.Base(a), "fx11-a-"), strings.TrimPrefix(filepath.Base(b), "fx11-b-"),
		"same content gave the same name suffix for two instances")
	again, err := writeLaunchEnvFile("fx11-a", launchEnvKindTool, exports)
	require.NoError(t, err)
	require.Equal(t, a, again, "same content was not reused, so a rebuilt command differs")
}

// LOW-4: files of instances that no longer exist (deleted while agent-deck
// was down, dropped by a reconcile, or written without an id) are swept.
func TestLaunchEnvFiles_OrphanSweepRemovesUnknownInstances(t *testing.T) {
	s := hookCleanupStorage(t)
	require.NoError(t, s.SaveWithGroups([]*Instance{{ID: "live-1", Title: "live", ProjectPath: t.TempDir(), Tool: "shell"}}, nil))
	for _, id := range []string{"live-1", "gone-1", ""} {
		_, err := writeLaunchEnvFile(id, launchEnvKindTool, "export A='"+id+"'")
		require.NoError(t, err)
	}
	_, err := writeLaunchEnvFile("gone-fresh", launchEnvKindTool, "export A='fresh'")
	require.NoError(t, err)
	dir, err := runtimeDataPath(launchEnvDirName)
	require.NoError(t, err)
	old := time.Now().Add(-48 * time.Hour)
	for _, n := range launchEnvFileNames(t) {
		if !strings.HasPrefix(n, "gone-fresh-") {
			require.NoError(t, os.Chtimes(filepath.Join(dir, n), old, old))
		}
	}

	require.NoError(t, PruneHookArtifacts())
	names := launchEnvFileNames(t)
	require.Len(t, names, 2, "left: %v", names)
	for _, n := range names {
		require.True(t, strings.HasPrefix(n, "live-1-") || strings.HasPrefix(n, "gone-fresh-"), "unexpected survivor %s", n)
	}
}

func TestLaunchEnvFiles_KindsKeptSideBySide(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := writeLaunchEnvFile("fx11-kinds", launchEnvKindTool, "export A='1'")
	require.NoError(t, err)
	_, err = writeLaunchEnvFile("fx11-kinds", launchEnvKindConductor, "export B='2'")
	require.NoError(t, err)
	ageLaunchEnvFiles(t, launchEnvPruneGrace+time.Minute)
	_, err = writeLaunchEnvFile("fx11-kinds", launchEnvKindTool, "export A='3'")
	require.NoError(t, err)
	require.Len(t, launchEnvFileNames(t), 2, "one file per kind: a launch sources several kinds at once")
}

func TestLaunchEnvFiles_RemovedWhenSessionDeleted(t *testing.T) {
	s := hookCleanupStorage(t)
	for _, id := range []string{"gone", "kept"} {
		_, err := writeLaunchEnvFile(id, launchEnvKindTool, "export A='"+id+"'")
		require.NoError(t, err)
		_, err = writeLaunchEnvFile(id, launchEnvKindRestart, "export R='"+id+"'")
		require.NoError(t, err)
	}
	require.NoError(t, s.DeleteInstance("gone"))
	names := launchEnvFileNames(t)
	require.Len(t, names, 2, "files left: %v", names)
	for _, n := range names {
		require.Contains(t, n, "kept-", "deleting one session removed another session's file")
	}
}

func TestStateRootPerms_LegacyAndXDGRootsBothTightened(t *testing.T) {
	home := setupDirLocalHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	xdg, err := agentpaths.DataDir()
	require.NoError(t, err)
	legacy, err := agentpaths.LegacyDir()
	require.NoError(t, err)
	require.NotEqual(t, xdg, legacy)
	for _, root := range []string{xdg, legacy} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "logs"), 0o700))
		require.NoError(t, os.Chmod(root, 0o755))
	}
	// Diverged layout: the profiles live in the legacy root.
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, ProfilesDirName, "default"), 0o700))

	prev := stateRootOnce
	stateRootOnce = &sync.Once{}
	t.Cleanup(func() { stateRootOnce = prev })

	var buf bytes.Buffer
	EnsureStateRootPrivate(&buf)
	assertPrivateMode(t, xdg, 0o700)
	assertPrivateMode(t, legacy, 0o700)
}

func TestDirLocalTrust_GroupLookupFailureFailsClosed(t *testing.T) {
	home := setupDirLocalHome(t)
	withPermWarnings(t)
	prevUser := lookupUserByID
	lookupUserByID = func(string) (*user.User, error) {
		return nil, errors.New("user: unknown userid (no cgo, sssd account)")
	}
	t.Cleanup(func() { lookupUserByID = prevUser })

	// The file carries the process's primary gid. With the lookup failed,
	// that group may be shared (domain users), so group write is untrusted.
	target, path := writeTrustDirLocal(t, home, "upg", "planted", 0o664, 0o775)

	settings, _, _, err := ResolveWorktreeSettingsForDir(target)
	require.NoError(t, err)
	require.NotEqual(t, "planted", settings.SparseCheckout, "group-writable dir-local config applied although the group lookup failed")
	_, notice, err := GetWorktreeSettingsForDirWithNotice(target)
	require.NoError(t, err)
	require.Contains(t, notice, path)
	require.Contains(t, notice, "group-writable")
}

func TestDirLocalTrust_GroupNameLookupFailureFailsClosed(t *testing.T) {
	home := setupDirLocalHome(t)
	withPermWarnings(t)
	prevUser, prevGroup := lookupUserByID, lookupGroupByID
	lookupUserByID = func(uid string) (*user.User, error) {
		return &user.User{Uid: uid, Gid: strconv.Itoa(os.Getgid()), Username: "me"}, nil
	}
	lookupGroupByID = func(string) (*user.Group, error) { return nil, errors.New("group: unknown groupid") }
	t.Cleanup(func() { lookupUserByID, lookupGroupByID = prevUser, prevGroup })

	target, _ := writeTrustDirLocal(t, home, "upg2", "planted", 0o664, 0o775)
	settings, _, _, err := ResolveWorktreeSettingsForDir(target)
	require.NoError(t, err)
	require.NotEqual(t, "planted", settings.SparseCheckout, "group-writable dir-local config applied although the group name lookup failed")
}

func TestPluginPinRefresh_RewritesMcpJSONOwnerOnly(t *testing.T) {
	profileDir := t.TempDir()
	projectDir := t.TempDir()
	pluginRoot := filepath.Join(profileDir, "plugins", "cache", "mkt", "plug")
	v2Server := filepath.Join(pluginRoot, "2.0.0", "server.js")
	require.NoError(t, os.MkdirAll(filepath.Dir(v2Server), 0o700))
	require.NoError(t, os.WriteFile(v2Server, []byte("v2"), 0o600))
	stale := filepath.Join(pluginRoot, "1.0.0", "server.js")
	mcpFile := filepath.Join(projectDir, ".mcp.json")
	body := `{"mcpServers":{"plug":{"type":"stdio","command":"node","args":["` + stale + `"],"env":{"TOKEN":"x"}}}}`
	require.NoError(t, os.WriteFile(mcpFile, []byte(body), 0o600))

	n, err := RefreshStalePluginPins(mcpFile, []string{profileDir})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assertPrivateMode(t, mcpFile, 0o600)
}

func TestProjectMCPGitExclude_NewlineInPathNotSplitIntoPatterns(t *testing.T) {
	root := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, root, "init", "-q")
	sub := filepath.Join(root, "a\n*")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	mcpFile := filepath.Join(sub, ".mcp.json")
	require.NoError(t, os.WriteFile(mcpFile, []byte("{}\n"), 0o600))

	excludeProjectMCPFromGit(root, mcpFile)
	for _, line := range []string{"*", "*/.mcp.json", "/a"} {
		require.Zero(t, countLine(readExclude(t, root), line), "exclude gained pattern %q:\n%s", line, readExclude(t, root))
	}
}

func TestDirLocalTrust_SkippedFileReportedInRejectionsAndNotice(t *testing.T) {
	home := setupDirLocalHome(t)
	withPermWarnings(t)
	target, path := writeTrustDirLocal(t, home, "skipped", "planted", 0o600, 0o700)
	withTrustUID(t, os.Getuid()+4242)

	_, _, rejections, err := ResolveWorktreeSettingsForDir(target)
	require.NoError(t, err)
	for _, key := range []string{WorktreeKeyDefaultLocation, WorktreeKeyPathTemplate, WorktreeKeySparseCheckout} {
		require.Len(t, rejections[key], 1, "key %s", key)
		require.Contains(t, rejections[key][0], path)
		require.Contains(t, rejections[key][0], "file ignored")
	}

	_, notice, err := GetWorktreeSettingsForDirWithNotice(target)
	require.NoError(t, err)
	require.Contains(t, notice, path)
	require.Contains(t, notice, "owned by uid")
}
