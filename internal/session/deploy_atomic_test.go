package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/update"
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

	// One round trip resolves symlinks at the path (#2244), one deploys.
	if len(*calls) != 2 || !strings.Contains((*calls)[0], "resolve "+shellQuote(target)) {
		t.Fatalf("expected a resolve then one deploy command, got %d: %v", len(*calls), *calls)
	}
	cmd := (*calls)[1]

	// Must NOT redirect the new bytes straight onto the live binary (ETXTBSY).
	if strings.Contains(cmd, "cat > "+shellQuote(target)) || strings.Contains(cmd, `cat > "$p"`) {
		t.Fatalf("deploy truncates the live binary in place (would hit ETXTBSY):\n%s", cmd)
	}

	// Must stage to a temp path unique to this deploy and atomically rename
	// it onto the target, which arrives as the script's second argument.
	for _, want := range []string{`t="$p.new.$$"`, `cat > "$t"`, `mv -f "$t" "$p"`, `chmod "$mode" "$t"`, `chmod a+rx "$t"`, `if [ -L "$p" ]`} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("deploy script lacks %q; got:\n%s", want, cmd)
		}
	}
	if !strings.Contains(cmd, " sh "+shellQuote("/home/daniel")+" "+shellQuote(target)) {
		t.Fatalf("the script must receive the directory and the final target %s as arguments; got:\n%s", target, cmd)
	}
	// #2164: the deploy is one round trip that falls back to passwordless
	// sudo when the directory is not writable, and otherwise names the
	// problem instead of leaving a bare "permission denied". The sudo probe
	// runs the same binary as the real call.
	// A file owned by someone else in a writable directory takes the sudo
	// route too, so the owner is kept instead of silently becoming us.
	for _, want := range []string{"[ -w '/home/daniel' ]", "-O " + shellQuote(target), "sudo -n sh -c true", "sudo -n sh -c", "is not writable by", "$(id -un)", `mkdir "$lock"`} {
		if !strings.Contains(cmd, want) {
			t.Errorf("deploy command lacks %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "sudo -n true") {
		t.Errorf("the sudo probe must use sh, not true, so a rule that allows only true is not mistaken for deploy rights:\n%s", cmd)
	}
}

// #2164: a remote that refuses the write (root-owned /usr/local/bin, no
// passwordless sudo) reports the install path, the user and the remedy as a
// typed error the CLI, the TUI and the unattended sweep all print verbatim.
func TestDeployBinary_NotWritableReportsRemedy(t *testing.T) {
	const target = "/usr/local/bin/agent-deck"
	r, _ := recordingRunner(func(cmd string) (string, error) {
		if !strings.Contains(cmd, "cat >") {
			return "", nil // probes answer; only the deploy fails
		}
		return "", fmt.Errorf("remote command failed: exit status 3: agent-deck: install path %s is not writable by daniel\n", target)
	})

	err := r.DeployBinary(context.Background(), []byte("bytes"), target)
	var notWritable *update.InstallPathNotWritableError
	if !errors.As(err, &notWritable) {
		t.Fatalf("got %v, want InstallPathNotWritableError", err)
	}
	if notWritable.Path != target || notWritable.User != "daniel" {
		t.Errorf("error carries path %q user %q", notWritable.Path, notWritable.User)
	}
	for _, want := range []string{"install path /usr/local/bin/agent-deck is not writable by daniel", "~/.local/bin", "symlink", "sudo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err.Error(), want)
		}
	}
}

// Any other deploy failure keeps its original shape.
func TestDeployBinary_OtherFailuresAreNotRelabelled(t *testing.T) {
	r, _ := recordingRunner(func(cmd string) (string, error) {
		if !strings.Contains(cmd, "cat >") {
			return "", nil
		}
		return "", errors.New("remote command failed: exit status 255: connection refused")
	})
	err := r.DeployBinary(context.Background(), []byte("bytes"), "/home/daniel/agent-deck")
	if err == nil || !strings.Contains(err.Error(), "failed to deploy binary to /home/daniel/agent-deck") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("got %v", err)
	}
	var notWritable *update.InstallPathNotWritableError
	if errors.As(err, &notWritable) {
		t.Fatal("a connection failure must not be reported as an unwritable path")
	}
}
