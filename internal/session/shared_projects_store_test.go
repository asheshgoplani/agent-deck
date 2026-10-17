package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSwitchSharedProjectsStore(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "archive-requested"}[archive], func(t *testing.T) {
			f := newSwitchKeysFixture(t)
			shared := filepath.Join(f.home, "shared-transcripts")
			require.NoError(t, os.MkdirAll(shared, 0o700))
			for _, account := range []string{"personal", "work"} {
				root := filepath.Join(f.home, ".claude-"+account)
				require.NoError(t, os.MkdirAll(root, 0o700))
				require.NoError(t, os.Symlink(shared, filepath.Join(root, "projects")))
			}
			newest := transcriptBody(keysSID, octTwo, 2, "latest")
			stale := transcriptBody(keysSID, octOne, 1, "older-key")
			latestPath := f.write(t, "personal", f.keys[0], keysSID, newest)
			stalePath := f.write(t, "personal", f.keys[1], keysSID, stale)
			beforeLatest, err := os.Stat(latestPath)
			require.NoError(t, err)
			beforeStale, err := os.Stat(stalePath)
			require.NoError(t, err)
			sidecar := filepath.Join(filepath.Dir(latestPath), keysSID, "subagents", "worker.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o700))
			require.NoError(t, os.WriteFile(sidecar, []byte("sidechain\n"), 0o600))

			inst, storage := f.instance(t, keysSID)
			result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{
				Storage: storage, NoRestart: true, ArchiveDestination: archive,
			})
			require.NoError(t, err, "an already-shared store needs no migration")
			require.NoError(t, CommitAccountSwitch(storage, inst, result))
			require.Equal(t, "work", inst.Account)
			require.NotNil(t, result.Transcript)
			require.Empty(t, result.Transcript.Installed)
			require.Empty(t, result.Transcript.BackedUp)
			require.Empty(t, result.DestinationArchived)
			require.Contains(t, result.Conversation, "already present")
			require.NotContains(t, result.Conversation, "conversation migrated")
			require.Equal(t, newest, readFile(t, latestPath))
			require.Equal(t, stale, readFile(t, stalePath), "no alias is overwritten")
			require.Equal(t, "sidechain\n", readFile(t, sidecar))
			afterLatest, err := os.Stat(latestPath)
			require.NoError(t, err)
			afterStale, err := os.Stat(stalePath)
			require.NoError(t, err)
			require.True(t, os.SameFile(beforeLatest, afterLatest))
			require.True(t, os.SameFile(beforeStale, afterStale))
			require.Equal(t, beforeLatest.ModTime(), afterLatest.ModTime())
			require.Equal(t, beforeStale.ModTime(), afterStale.ModTime())
			stageRoot, err := switchStageRoot(inst.ID)
			require.NoError(t, err)
			_, err = os.Stat(stageRoot)
			require.True(t, os.IsNotExist(err), "shared transcripts are never staged")
		})
	}
}

func TestSwitchSharedProjectsStoreRejectsNestedSymlink(t *testing.T) {
	f := newSwitchKeysFixture(t)
	shared := filepath.Join(f.home, "shared")
	outside := filepath.Join(f.home, "outside")
	require.NoError(t, os.MkdirAll(shared, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))
	for _, account := range []string{"personal", "work"} {
		root := filepath.Join(f.home, ".claude-"+account)
		require.NoError(t, os.MkdirAll(root, 0o700))
		require.NoError(t, os.Symlink(shared, filepath.Join(root, "projects")))
	}
	require.NoError(t, os.Symlink(outside, filepath.Join(shared, f.keys[0])))
	p := filepath.Join(outside, keysSID+".jsonl")
	body := transcriptBody(keysSID, octTwo, 2, "outside")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	inst, storage := f.instance(t, keysSID)
	result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.ErrorContains(t, err, "symlink")
	require.Nil(t, result)
	require.Equal(t, "personal", inst.Account)
	require.Equal(t, body, readFile(t, p))
}

func TestMigrateSharedProjectsStoreIsNoOp(t *testing.T) {
	f := newSwitchKeysFixture(t)
	shared := filepath.Join(f.home, "shared")
	require.NoError(t, os.MkdirAll(shared, 0o700))
	for _, account := range []string{"personal", "work"} {
		root := filepath.Join(f.home, ".claude-"+account)
		require.NoError(t, os.MkdirAll(root, 0o700))
		require.NoError(t, os.Symlink(shared, filepath.Join(root, "projects")))
	}
	body := transcriptBody(keysSID, octTwo, 2, "shared")
	p := f.write(t, "personal", f.keys[0], keysSID, body)
	inst, _ := f.instance(t, keysSID)
	destination, size, err := MigrateConversationFromSized(inst,
		filepath.Join(f.home, ".claude-personal"), filepath.Join(f.home, ".claude-work"))
	require.NoError(t, err)
	require.Empty(t, destination)
	require.Zero(t, size)
	require.Equal(t, body, readFile(t, p))
}

func TestMigrateSharedProjectsStoreIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, stored, want string
		ambiguous          bool
	}{
		{name: "stale", stored: "11111111-2222-3333-4444-555555555555", want: keysSID},
		{name: "uncaptured", want: keysSID},
		{name: "ambiguous", stored: "11111111-2222-3333-4444-555555555555", ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSwitchKeysFixture(t)
			shared := filepath.Join(f.home, "shared")
			require.NoError(t, os.MkdirAll(shared, 0o700))
			for _, account := range []string{"personal", "work"} {
				root := filepath.Join(f.home, ".claude-"+account)
				require.NoError(t, os.MkdirAll(root, 0o700))
				require.NoError(t, os.Symlink(shared, filepath.Join(root, "projects")))
			}
			body := transcriptBody(keysSID, octTwo, 2, "shared")
			p := f.write(t, "personal", f.keys[0], keysSID, body)
			if tc.ambiguous {
				f.write(t, "personal", f.keys[0], "66666666-7777-8888-9999-aaaaaaaaaaaa", body)
			}
			inst, _ := f.instance(t, tc.stored)
			destination, size, err := MigrateConversationFromSized(inst,
				filepath.Join(f.home, ".claude-personal"), filepath.Join(f.home, ".claude-work"))
			if tc.ambiguous {
				require.ErrorIs(t, err, ErrAmbiguousConversation)
				require.Equal(t, tc.stored, inst.ClaudeSessionID)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, inst.ClaudeSessionID, "shared-store no-op must still repair the source identity")
			}
			require.Empty(t, destination)
			require.Zero(t, size)
			require.Equal(t, body, readFile(t, p))
		})
	}
}

func TestMigrateSharedProjectsStoreRechecksBeforeSuccess(t *testing.T) {
	for _, mode := range []string{"unchanged", "target-link", "source-link", "both-links", "replace-directory"} {
		t.Run(mode, func(t *testing.T) {
			f := newSwitchKeysFixture(t)
			shared := filepath.Join(f.home, "shared")
			require.NoError(t, os.MkdirAll(shared, 0o700))
			for _, account := range []string{"personal", "work"} {
				root := filepath.Join(f.home, ".claude-"+account)
				require.NoError(t, os.MkdirAll(root, 0o700))
				require.NoError(t, os.Symlink(shared, filepath.Join(root, "projects")))
			}
			body := transcriptBody(keysSID, octTwo, 2, "original")
			f.write(t, "personal", f.keys[0], keysSID, body)
			inst, _ := f.instance(t, "")
			type outcome struct {
				destination string
				size        int64
				err         error
			}
			done := make(chan outcome, 1)
			// Source discovery adopts the sole ID under this existing mutex.
			// Holding it parks migration after root selection, without a test hook.
			inst.claudeSessionIDsFromDiskScanMu.Lock()
			locked := true
			t.Cleanup(func() {
				if locked {
					inst.claudeSessionIDsFromDiskScanMu.Unlock()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("migration did not finish after cleanup unlocked source discovery")
					}
				}
			})
			go func() {
				destination, size, err := MigrateConversationFromSized(inst,
					filepath.Join(f.home, ".claude-personal"), filepath.Join(f.home, ".claude-work"))
				done <- outcome{destination, size, err}
			}()
			require.Eventually(t, func() bool {
				stacks := make([]byte, 1<<20)
				n := runtime.Stack(stacks, true)
				for _, stack := range strings.Split(string(stacks[:n]), "\n\n") {
					if strings.Contains(stack, ".markClaudeSessionIDFromDiskScan(") &&
						strings.Contains(stack, ".MigrateConversationFromSized(") {
						return true
					}
				}
				return false
			}, 5*time.Second, time.Millisecond, "migration must be parked after selecting the original shared store")

			originalRoot := shared
			other := filepath.Join(f.home, "other-store")
			if mode == "replace-directory" {
				originalRoot = shared + "-original"
				require.NoError(t, os.Rename(shared, originalRoot))
				other = shared
			}
			if mode != "unchanged" {
				require.NoError(t, os.MkdirAll(other, 0o700))
				for _, account := range []string{"personal", "work"} {
					if mode != "both-links" && mode != map[string]string{"personal": "source-link", "work": "target-link"}[account] {
						continue
					}
					link := filepath.Join(f.home, ".claude-"+account, "projects")
					require.NoError(t, os.Remove(link))
					require.NoError(t, os.Symlink(other, link))
				}
			}
			inst.claudeSessionIDsFromDiskScanMu.Unlock()
			locked = false
			select {
			case got := <-done:
				if mode == "unchanged" {
					require.NoError(t, got.err)
				} else {
					require.ErrorContains(t, got.err, "shared transcript store changed")
				}
				require.Empty(t, got.destination)
				require.Zero(t, got.size)
			case <-time.After(5 * time.Second):
				t.Fatal("migration did not finish after source discovery resumed")
			}
			require.Equal(t, body, readFile(t, filepath.Join(originalRoot, f.keys[0], keysSID+".jsonl")))
			if mode != "unchanged" {
				entries, err := os.ReadDir(other)
				require.NoError(t, err)
				require.Empty(t, entries, "the replacement store must remain untouched")
			}
		})
	}
}

func TestSwitchSharedProjectsStoreRechecksAfterStop(t *testing.T) {
	for _, mode := range []string{"flush", "rebind", "replace-directory"} {
		t.Run(mode, func(t *testing.T) {
			f := newSwitchKeysFixture(t)
			shared := filepath.Join(f.home, "shared")
			require.NoError(t, os.MkdirAll(shared, 0o700))
			for _, account := range []string{"personal", "work"} {
				root := filepath.Join(f.home, ".claude-"+account)
				require.NoError(t, os.MkdirAll(root, 0o700))
				require.NoError(t, os.Symlink(shared, filepath.Join(root, "projects")))
			}
			original := transcriptBody(keysSID, octOne, 1, "before-stop")
			flushed := transcriptBody(keysSID, octTwo, 2, "after-stop")
			p := f.write(t, "personal", f.keys[0], keysSID, original)
			inst, storage := f.instance(t, keysSID)
			running := true
			nativeSwitchRunning = func(*Instance) bool { return running }
			var stops, starts int
			nativeSwitchStop = func(*Instance) error {
				stops++
				running = false
				if mode == "flush" {
					return os.WriteFile(p, []byte(flushed), 0o600)
				}
				other := filepath.Join(f.home, "other-shared-store")
				if mode == "replace-directory" {
					require.NoError(t, os.Rename(shared, shared+"-original"))
					other = shared
				}
				otherFile := filepath.Join(other, f.keys[0], keysSID+".jsonl")
				require.NoError(t, os.MkdirAll(filepath.Dir(otherFile), 0o700))
				require.NoError(t, os.WriteFile(otherFile, []byte(flushed), 0o600))
				for _, account := range []string{"personal", "work"} {
					if mode == "replace-directory" {
						break
					}
					link := filepath.Join(f.home, ".claude-"+account, "projects")
					require.NoError(t, os.Remove(link))
					require.NoError(t, os.Symlink(other, link))
				}
				return nil
			}
			nativeSwitchStart = func(got *Instance) error {
				starts++
				require.Equal(t, "personal", got.Account, "refusal restores the source account")
				running = true
				return nil
			}
			result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
			require.Equal(t, 1, stops)
			if mode != "flush" {
				require.ErrorContains(t, err, "shared transcript store changed")
				require.Nil(t, result)
				require.Equal(t, "personal", inst.Account)
				require.Equal(t, 1, starts)
				originalRoot := shared
				if mode == "replace-directory" {
					originalRoot += "-original"
					require.Equal(t, flushed, readFile(t, p), "replacement store is left untouched")
				}
				require.Equal(t, original, readFile(t, filepath.Join(originalRoot, f.keys[0], keysSID+".jsonl")))
			} else {
				require.NoError(t, err)
				require.NoError(t, CommitAccountSwitch(storage, inst, result))
				require.Equal(t, "work", inst.Account)
				require.Equal(t, 0, starts)
				require.Equal(t, flushed, readFile(t, p))
				hash, err := sha256File(p)
				require.NoError(t, err)
				require.Equal(t, hash, result.Transcript.Chosen.SHA256, "receipt covers the final flushed bytes")
			}
		})
	}
}
