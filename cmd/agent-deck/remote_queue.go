package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// remoteQueueArgs admits `session queue list|release|cancel` (and its help)
// for forwarding. Anything else under `session queue` is refused before SSH.
func remoteQueueArgs(args []string) bool {
	if len(args) < 3 || args[0] != "session" || args[1] != "queue" {
		return false
	}
	switch args[2] {
	case "--help", "-h":
		return len(args) == 3
	case "list", "release", "cancel":
		return true
	}
	return false
}

func isSessionQueueArgs(args []string) bool {
	return len(args) > 1 && args[0] == "session" && args[1] == "queue"
}

// remoteQueueUnsupported reports whether an older remote answered
// `session queue` with its "unknown session command" line. Such a remote ran
// nothing: it refuses before resolving any session or queue entry.
func remoteQueueUnsupported(args []string, code int, stderr string) bool {
	return code != 0 && isSessionQueueArgs(args) && strings.Contains(stderr, "unknown session command: queue")
}

// remoteQueueUnsupportedMessage is the refusal a client feature-detects:
// "session queue is unsupported on this remote".
func remoteQueueUnsupportedMessage(remote, remoteVersion string) string {
	runs := "v" + remoteVersion
	if remoteVersion == "" {
		runs = "an unknown agent-deck version"
	}
	return fmt.Sprintf("session queue is unsupported on this remote %q (it runs %s); update its agent-deck with 'agent-deck remote update %s'", remote, runs, remote)
}

// remoteQueueUnsupportedJSON is the --json shape of the same refusal.
func remoteQueueUnsupportedJSON(remote, remoteVersion string) []byte {
	msg := remoteQueueUnsupportedMessage(remote, remoteVersion)
	if remoteVersion == "" {
		remoteVersion = remoteVersionUnknown
	}
	out, _ := json.Marshal(struct {
		Error         string `json:"error"`
		Remote        string `json:"remote"`
		RemoteVersion string `json:"remote_version"`
	}{msg, remote, remoteVersion})
	return append(out, '\n')
}
