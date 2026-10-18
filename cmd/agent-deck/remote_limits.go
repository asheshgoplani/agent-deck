package main

import (
	"bytes"
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

// relabelRemotePluginsOff rewrites a forwarded `limits` refusal for [macapp]
// plugins being off on the remote. Its enable command is the remote's own:
// run as given it would turn plugins on HERE. The text form names the
// remote and its host; the JSON hint gets remote, remote_host and
// remote_argv in place of argv. Any other output is returned unchanged.
func relabelRemotePluginsOff(name, host string, args []string, code int, stdout, stderr []byte) ([]byte, []byte) {
	if code != 2 || !isRemoteLimitsArgs(args) {
		return stdout, stderr
	}
	where := fmt.Sprintf("To enable it on remote %s (%s), run there: ", name, host)
	stderr = bytes.ReplaceAll(stderr, []byte("To enable it, run: "), []byte(where))
	var doc map[string]any
	if json.Unmarshal(stdout, &doc) != nil {
		return stdout, stderr
	}
	hint, ok := doc["hint"].(map[string]any)
	if !ok || hint["key"] != "macapp.plugins" {
		return stdout, stderr
	}
	if msg, ok := doc["error"].(string); ok {
		doc["error"] = strings.Replace(msg, "To enable it, run: ", where, 1)
	}
	hint["remote"], hint["remote_host"] = name, host
	if argv, ok := hint["argv"]; ok {
		hint["remote_argv"] = argv
		delete(hint, "argv")
	}
	if cmd, ok := hint["command"].(string); ok {
		hint["command"] = fmt.Sprintf("on remote %s (%s): %s", name, host, cmd)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return stdout, stderr
	}
	return append(out, '\n'), stderr
}
