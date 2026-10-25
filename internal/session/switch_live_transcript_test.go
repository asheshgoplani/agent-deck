// Regression coverage for LGFALSE1019 (2026-10-19): after switch-account the
// destination holds the conversation under every project key, but the
// restarted Claude appends to only one of them. Liveness readers (list --json
// transcript_path, the transcript limit verdict that limit-guard reads, the
// hook rebind gate's mtime and size) took the first key that existed, so a
// session working on the new account was judged from a copy frozen at the
// switch and reported as stuck for hours.
package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSwitchAccount_LivenessReadsTheKeyTheLiveClaudeWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		liveKey func(f *switchKeysFixture) string
	}{
		{"typed key", func(f *switchKeysFixture) string { return f.keys[0] }},
		{"realpath key", func(f *switchKeysFixture) string { return f.keys[len(f.keys)-1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSwitchKeysFixture(t)
			switchedAt := time.Now().Add(-12 * time.Hour)
			oldPath := f.write(t, "personal", f.keys[0], keysSID, transcriptBody(keysSID, switchedAt, 6, "before-switch"))
			require.NoError(t, os.Chtimes(oldPath, switchedAt, switchedAt))

			inst, storage := f.instance(t, keysSID)
			result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
			require.NoError(t, err)
			require.NoError(t, CommitAccountSwitch(storage, inst, result))
			require.Equal(t, "work", inst.Account)
			require.Equal(t, filepath.Join(f.home, ".claude-work"), GetClaudeConfigDirForInstance(inst))

			// Every installed copy dates from the switch; the restarted Claude
			// then appends to one key only, ending on a fresh weekly-limit turn.
			for _, key := range f.keys {
				require.NoError(t, os.Chtimes(f.path("work", key, keysSID), switchedAt, switchedAt))
			}
			livePath := f.path("work", tc.liveKey(f), keysSID)
			now := time.Now().UTC().Format(time.RFC3339Nano)
			fh, err := os.OpenFile(livePath, os.O_APPEND|os.O_WRONLY, 0o600)
			require.NoError(t, err)
			_, err = fh.WriteString(recUserAt(now) + "\n" + recFieldAPIErrorAt(now, 429, "rate_limit", fieldWeeklyText) + "\n")
			require.NoError(t, err)
			require.NoError(t, fh.Close())

			require.Equal(t, livePath, ListedTranscriptPath(inst), "list --json transcript_path must name the copy the live Claude writes")
			require.Equal(t, livePath, ClaudeTranscriptPathForInstance(inst))
			info, err := os.Stat(ListedTranscriptPath(inst))
			require.NoError(t, err)
			require.Less(t, time.Since(info.ModTime()), time.Minute, "a working session must not look idle since the switch")
			// The hook rebind gate (#661/#856) weighs the bound conversation by the
			// same file: a frozen copy would make it look dormant.
			require.True(t, sessionConversationMtime(inst, keysSID).Equal(info.ModTime()), "rebind gate mtime must come from the live copy")
			require.Equal(t, info.Size(), sessionConversationByteSize(inst, keysSID))

			kind, detail := inst.TranscriptLimit()
			require.Equal(t, SubstateUsageLimit, kind, "the limit verdict must come from the live copy")
			require.Contains(t, detail, "weekly limit")
		})
	}
}
