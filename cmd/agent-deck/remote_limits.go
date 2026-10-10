package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// validateRemoteLimitsArgs admits the read-only `limits [--json]` form (and a
// help request); any other option or a positional argument is refused before
// SSH.
func validateRemoteLimitsArgs(args []string) error {
	for _, arg := range args {
		switch arg {
		case "--json", "-json", "--help", "-help", "-h":
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("unsupported option %q for remote limits", arg)
		}
		return fmt.Errorf("remote limits takes no arguments, got %q", arg)
	}
	return nil
}

func isRemoteLimitsArgs(args []string) bool {
	return len(args) > 0 && args[0] == "limits"
}

// remoteLimitsUnsupported reports a remote whose agent-deck predates the
// `limits` command: its no-TTY guard (or its interactive "unknown command"
// path) names the word it did not recognize.
func remoteLimitsUnsupported(args []string, code int, stdout, stderr string) bool {
	if code == 0 || !isRemoteLimitsArgs(args) {
		return false
	}
	combined := stdout + "\n" + stderr
	return strings.Contains(combined, `"limits" is not a recognized command`) || strings.Contains(combined, `unknown command "limits"`)
}

// remoteLimitsUnsupportedMessage is the standard refusal for a forwarded read
// the remote's agent-deck does not have.
func remoteLimitsUnsupportedMessage(remote string) string {
	return fmt.Sprintf("unsupported remote command %q on remote %q; update its agent-deck", "limits", remote)
}

// remoteLimitsUnsupportedJSON is the --json shape: {error, remote, remote_version}.
func remoteLimitsUnsupportedJSON(remote, remoteVersion string) []byte {
	if remoteVersion == "" {
		remoteVersion = remoteVersionUnknown
	}
	out, _ := json.Marshal(struct {
		Error         string `json:"error"`
		Remote        string `json:"remote"`
		RemoteVersion string `json:"remote_version"`
	}{remoteLimitsUnsupportedMessage(remote), remote, remoteVersion})
	return append(out, '\n')
}
