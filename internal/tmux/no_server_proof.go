package tmux

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A no-server proof records that a tmux client on a socket just found no
// server listening there: tmux printed "no server running on <path>"
// (nothing accepts on the socket file) or "error connecting to <path> (No such
// file or directory)" (no socket file). Every has-session on that socket would
// then exit non-zero, which the liveness probes (existsProbe,
// tmuxSessionExistsOnSocket) read as "gone". With the server gone, 200 stored
// sessions paid 400 has-session forks per `list --json` just to read the same
// answer 400 times. The proof answers those probes without a subprocess.
//
// The proof is only as strong as a has-session would have been at the same
// moment, and stays so:
//
//   - It is recorded only from a completed client's own stderr (never from a
//     timeout, a killed client or a protocol mismatch, which stay
//     indeterminate and keep their "assume alive" verdict).
//   - It is checked against the socket path on every use with one lstat: a
//     server that started since (in this process or any other) has created a
//     new socket file there, so the proof no longer matches and the caller
//     probes as before. No subprocess is needed to see a new server.
//   - It expires after noServerProofTTL, the age a status pass already trusts
//     one listing of a socket for (passSessionListMaxAge in internal/session).
//   - Session.Start, and any tmux command that can start a server
//     (new-session, start-server), drops the socket's proof first.
//   - It is keyed by the socket name plus TMUX_TMPDIR and TMUX, the
//     environment tmux resolves the socket path from.
//
// It never feeds a positive: a socket without a proof probes as before, and a
// cached session listing's negative is still not trusted (#755).
const noServerProofTTL = 250 * time.Millisecond

type noServerProof struct {
	path string
	at   time.Time
	// missing: tmux could not find the socket file. Otherwise the file exists
	// but refused the connection, and file identifies that stale file (device
	// and inode via os.SameFile, plus its modification time, so a new socket
	// that reuses the inode number still reads as different).
	missing bool
	file    fs.FileInfo
}

var (
	noServerProofMu sync.Mutex
	noServerProofs  = map[string]noServerProof{}
	// noServerProofNow is the clock; a variable so tests can age a proof.
	noServerProofNow = time.Now
)

func noServerProofKey(socketName string) string {
	return strings.TrimSpace(socketName) + "\x00" + os.Getenv("TMUX_TMPDIR") + "\x00" + os.Getenv("TMUX")
}

// parseNoServerStderr returns the socket path a tmux client named when it
// found no server, and whether the socket file itself was missing. Only the
// exact one-line diagnostics tmux's client prints for ECONNREFUSED and ENOENT
// qualify; anything else (a mismatch, a permission error, a missing session
// on a live server) is not a proof.
func parseNoServerStderr(stderr string) (path string, missing, ok bool) {
	line := strings.TrimSpace(stderr)
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", false, false
	}
	if rest, found := strings.CutPrefix(line, "no server running on "); found {
		path = rest
	} else if rest, found := strings.CutPrefix(line, "error connecting to "); found {
		p, cut := strings.CutSuffix(rest, " (No such file or directory)")
		if !cut {
			return "", false, false
		}
		path, missing = p, true
	} else {
		return "", false, false
	}
	if !filepath.IsAbs(path) {
		return "", false, false
	}
	return path, missing, true
}

// socketFile returns the socket file at path, or nil when there is none.
func socketFile(path string) fs.FileInfo {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSocket == 0 {
		return nil
	}
	return info
}

func socketFileMissing(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// noteNoServer records a proof for socketName when err is a tmux client that
// ran to a non-zero exit with a no-server diagnostic on its captured stderr.
// Callers pass only errors from a completed, unexpired command.
func noteNoServer(socketName string, err error) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState == nil || !exitErr.Exited() {
		return
	}
	noteNoServerStderr(socketName, string(exitErr.Stderr))
}

// noteNoServerStderr is noteNoServer for a caller that captured stderr itself.
// The socket path is checked right after the client exited: a socket file
// that already looks different (a server came up meanwhile) records nothing.
func noteNoServerStderr(socketName, stderr string) {
	path, missing, ok := parseNoServerStderr(stderr)
	if !ok {
		return
	}
	proof := noServerProof{path: path, missing: missing}
	if missing {
		if !socketFileMissing(path) {
			return
		}
	} else {
		if proof.file = socketFile(path); proof.file == nil {
			return
		}
	}
	key := noServerProofKey(socketName)
	noServerProofMu.Lock()
	proof.at = noServerProofNow()
	noServerProofs[key] = proof
	noServerProofMu.Unlock()
}

// socketProvenServerless reports whether a fresh proof says socketName has no
// tmux server, re-checked against the socket file now. A proof that no longer
// holds is dropped.
func socketProvenServerless(socketName string) bool {
	key := noServerProofKey(socketName)
	noServerProofMu.Lock()
	defer noServerProofMu.Unlock()
	proof, ok := noServerProofs[key]
	if !ok {
		return false
	}
	if noServerProofNow().Sub(proof.at) >= noServerProofTTL || !proof.stillHolds() {
		delete(noServerProofs, key)
		return false
	}
	return true
}

func (p noServerProof) stillHolds() bool {
	if p.missing {
		return socketFileMissing(p.path)
	}
	now := socketFile(p.path)
	return now != nil && os.SameFile(now, p.file) && now.ModTime().Equal(p.file.ModTime())
}

// forgetNoServerProof drops socketName's proof: a server may start there now.
func forgetNoServerProof(socketName string) {
	key := noServerProofKey(socketName)
	noServerProofMu.Lock()
	delete(noServerProofs, key)
	noServerProofMu.Unlock()
}

// serverStartingCommands are the tmux commands (and aliases) that can start a
// server. Package level, so the #1694 new-session lint does not read this
// list as a spawn.
var serverStartingCommands = map[string]struct{}{
	"new-session": {}, "new": {}, "start-server": {}, "start": {},
}

// forgetNoServerProofIfStarting drops the proof before a tmux command that can
// start a server on socketName. A false match only costs the next probe.
func forgetNoServerProofIfStarting(socketName string, args []string) {
	for _, a := range args {
		if _, ok := serverStartingCommands[a]; ok {
			forgetNoServerProof(socketName)
			return
		}
	}
}

// hasSessionProvenGone reports, without a subprocess, that a has-session on
// socketName would exit non-zero because no server is listening, and that
// hasSessionProbeAssumesAlive would read that exit as "gone". A fresh cached
// protocol-mismatch verdict keeps its "assume alive" reading, so the proof
// stands aside for it.
func hasSessionProvenGone(socketName string) bool {
	if !socketProvenServerless(socketName) {
		return false
	}
	if mismatched, fresh := cachedSocketMismatch(strings.TrimSpace(socketName)); fresh && mismatched {
		return false
	}
	return true
}

// resetNoServerProofsForTest clears every proof.
func resetNoServerProofsForTest() {
	noServerProofMu.Lock()
	noServerProofs = map[string]noServerProof{}
	noServerProofMu.Unlock()
}
