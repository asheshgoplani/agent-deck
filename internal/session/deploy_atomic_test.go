package session

import (
	"context"
	"strings"
	"testing"
)

// `agent-deck remote update` deployed the new binary with `cat > <path>`, which
// truncates the destination in place. But agent-deck always keeps a long-lived
// `agent-deck session attach …` process running from that same <path>, so the
// kernel refused to reopen the live executable for writing and returned ETXTBSY
// — `bash: line 1: /home/.../agent-deck: Text file busy` — on every update.
//
// The fix stages the bytes to a sibling temp file and atomically renames it into
// place. rename(2) only repoints the directory entry, so it succeeds while the
// old binary is still executing (the running process keeps the unlinked inode);
// the next launch picks up the new binary. This test pins that behavior.
//
// The SSH layer is stubbed via remoteExecFn (see recordingRunner) so no real
// remote is required.
func TestDeployBinary_StagesAndRenames_NeverTruncatesLiveBinary(t *testing.T) {
	const target = "/home/daniel/agent-deck"

	r, calls := recordingRunner(func(string) (string, error) { return "", nil })

	if err := r.DeployBinary(context.Background(), []byte("new-binary-bytes"), target); err != nil {
		t.Fatalf("DeployBinary returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("expected exactly one remote command, got %d: %v", len(*calls), *calls)
	}
	cmd := (*calls)[0]

	// Must NOT redirect the new bytes straight onto the live binary (ETXTBSY).
	if strings.Contains(cmd, "cat > "+shellQuote(target)) {
		t.Fatalf("deploy truncates the live binary in place (would hit ETXTBSY):\n%s", cmd)
	}

	// Must stage to a temp path and atomically rename it onto the target.
	if !strings.Contains(cmd, "mv -f ") {
		t.Fatalf("deploy must atomically `mv -f` the staged binary into place; got:\n%s", cmd)
	}
	if !strings.HasSuffix(strings.TrimSpace(cmd), shellQuote(target)) {
		t.Fatalf("the rename destination must be the final target %s; got:\n%s", target, cmd)
	}
}
