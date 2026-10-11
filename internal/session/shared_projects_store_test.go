package session

import (
	"os"
	"path/filepath"
	"testing"

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

func TestSwitchSharedProjectsStoreRechecksAfterStop(t *testing.T) {
	for _, rebind := range []bool{false, true} {
		t.Run(map[bool]string{false: "flush", true: "rebind"}[rebind], func(t *testing.T) {
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
				if !rebind {
					return os.WriteFile(p, []byte(flushed), 0o600)
				}
				other := filepath.Join(f.home, "other-shared-store")
				otherFile := filepath.Join(other, f.keys[0], keysSID+".jsonl")
				require.NoError(t, os.MkdirAll(filepath.Dir(otherFile), 0o700))
				require.NoError(t, os.WriteFile(otherFile, []byte(flushed), 0o600))
				for _, account := range []string{"personal", "work"} {
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
			if rebind {
				require.ErrorContains(t, err, "shared transcript store changed")
				require.Nil(t, result)
				require.Equal(t, "personal", inst.Account)
				require.Equal(t, 1, starts)
				require.Equal(t, original, readFile(t, filepath.Join(shared, f.keys[0], keysSID+".jsonl")))
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
