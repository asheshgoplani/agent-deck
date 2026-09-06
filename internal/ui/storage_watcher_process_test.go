package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

func TestStorageWatcherExternalProcessHelper(t *testing.T) {
	path := os.Getenv("WATCHER_PROCESS_DB")
	if path == "" {
		return
	}
	db, err := statedb.Open(path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.DB().Exec("UPDATE instances SET title = ? WHERE id = ?", "raw-process-title", os.Getenv("WATCHER_PROCESS_ID"))
	require.NoError(t, err) // Deliberately no Touch: legacy metadata is unchanged.
}
func TestStorageWatcherRealProcessesOneRefresh(t *testing.T) {
	require.NoError(t, watcherBuildCacheErr)
	t.Setenv("GOMODCACHE", "") // Reproduce CI with no explicit cache after HOME changes.
	h, storage, inst := newWatcherEffectsHome(t)
	w, err := NewStorageWatcher(storage.GetDB())
	require.NoError(t, err)
	defer w.Close()
	h.storageWatcher = w
	w.checkAndNotify()
	requireWatcherSignal(t, w)
	acknowledgeWatcher(t, w, storage.GetDB())
	child := exec.Command(os.Args[0], "-test.run=^TestStorageWatcherExternalProcessHelper$", "-test.timeout=30s")
	child.Env = append(os.Environ(), "WATCHER_PROCESS_DB="+storage.Path(), "WATCHER_PROCESS_ID="+inst.ID)
	out, err := child.CombinedOutput()
	require.NoError(t, err, string(out))
	applyRefresh := func(want string) {
		w.checkAndNotify()
		requireWatcherSignal(t, w)
		ticket, err := w.beginLoad()
		require.NoError(t, err)
		instances, groups, raw, err := storage.LoadWithGroupsSnapshot()
		require.NoError(t, err)
		ticket, err = w.endLoad(ticket)
		require.NoError(t, err)
		state := h.preserveState()
		_, _ = h.Update(loadSessionsMsg{instances: instances, groups: groups, persistedSnapshot: raw, watcherTicket: &ticket, restoreState: &state})
		require.Equal(t, want, h.getInstanceByID(inst.ID).GetTitleThreadSafe())
	}
	applyRefresh("raw-process-title")
	perTestModules := filepath.Join(os.Getenv("HOME"), "go", "pkg", "mod")
	_, err = os.Stat(perTestModules)
	require.True(t, os.IsNotExist(err), "control HOME already contains modules")
	binary := filepath.Join(t.TempDir(), "agent-deck")
	build := exec.Command("go", "build", "-p", "1", "-o", binary, "../../cmd/agent-deck")
	build.Env = watcherBuildEnvironment(os.Environ())
	out, err = build.CombinedOutput()
	require.NoError(t, err, string(out))
	cli := exec.Command(binary, "-p", h.profile, "session", "set", inst.ID, "title", "cli-process-title")
	out, err = cli.CombinedOutput()
	require.NoError(t, err, string(out))
	applyRefresh("cli-process-title")
	_, err = os.Stat(perTestModules)
	require.True(t, os.IsNotExist(err), "go build populated per-test HOME with modules; normal cleanup would be unsafe")
}

var watcherBuildCaches map[string]string
var watcherBuildCacheErr error

func resolveWatcherBuildCaches(env []string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "env", "-json", "GOCACHE", "GOMODCACHE")
	cmd.WaitDelay = time.Second
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("resolve test build caches: %w", err)
	}
	var caches map[string]string
	if err = json.Unmarshal(out, &caches); err != nil {
		return nil, err
	}
	for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
		if !filepath.IsAbs(caches[key]) {
			return nil, fmt.Errorf("invalid %s for test build", key)
		}
	}
	return caches, nil
}
func watcherBuildEnvironment(base []string) []string {
	env := make([]string, 0, len(base)+2)
	for _, value := range base {
		key, _, _ := strings.Cut(value, "=")
		if key != "GOCACHE" && key != "GOMODCACHE" {
			env = append(env, value)
		}
	}
	return append(env, "GOCACHE="+watcherBuildCaches["GOCACHE"], "GOMODCACHE="+watcherBuildCaches["GOMODCACHE"])
}
func TestStorageWatcherBuildCacheDefaultsSurviveHomeIsolation(t *testing.T) {
	harness := t.TempDir()
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "HOME", "GOPATH", "GOCACHE", "GOMODCACHE", "XDG_CACHE_HOME":
			continue
		}
		env = append(env, value)
	}
	env = append(env, "HOME="+harness, "GOPATH="+filepath.Join(harness, "go"))
	caches, err := resolveWatcherBuildCaches(env)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(harness, "go", "pkg", "mod"), caches["GOMODCACHE"])
	t.Setenv("HOME", harness)
	t.Setenv("XDG_CACHE_HOME", "")
	cacheRoot, err := os.UserCacheDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cacheRoot, "go-build"), caches["GOCACHE"])
	require.NoError(t, watcherBuildCacheErr)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOCACHE", "")
	buildEnv := watcherBuildEnvironment(os.Environ())
	require.Contains(t, buildEnv, "GOMODCACHE="+watcherBuildCaches["GOMODCACHE"])
	require.Contains(t, buildEnv, "GOCACHE="+watcherBuildCaches["GOCACHE"])
	require.NotEqual(t, filepath.Join(os.Getenv("HOME"), "go", "pkg", "mod"), watcherBuildCaches["GOMODCACHE"])
}
