package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// OpenCodeSSETarget returns the event stream that feeds this instance's
// status, or false when status stays on tmux content sniffing.
//
//   - 1.x launched with --port: the TUI's own server, as before (issue #1614).
//   - 2.x with a bound session id: the shared background service, filtered to
//     that session (issue #2511).
//
// The 2.x check is the memoised version probe the launch already used; the
// service itself is looked up later, inside the stream goroutine.
func (i *Instance) OpenCodeSSETarget() (SSETarget, bool) {
	if i.Tool != "opencode" {
		return SSETarget{}, false
	}
	if port := i.GetOpenCodePort(); port > 0 {
		return SSETarget{InstanceID: i.ID, Port: port}, true
	}
	i.mu.RLock()
	sessionID := i.OpenCodeSessionID
	i.mu.RUnlock()
	if sessionID == "" {
		return SSETarget{}, false
	}
	if config, err := LoadUserConfig(); err == nil && config != nil && config.OpenCode.DisableSSEStatus {
		return SSETarget{}, false
	}
	if !i.openCodeRejectsV1LaunchFlags() {
		return SSETarget{}, false
	}
	return SSETarget{InstanceID: i.ID, sessionID: sessionID, service: i.resolveOpenCodeService}, true
}

// resolveOpenCodeService asks the session's OpenCode binary where its
// background service listens (`opencode service status` prints the URL) and
// reads the service password from the registration file the service writes
// for its own clients. The password is used only when that file describes the
// same URL, and only for a loopback URL.
func (i *Instance) resolveOpenCodeService() (openCodeService, error) {
	binary, ok := i.openCodeLaunchBinary()
	if !ok {
		return openCodeService{}, errors.New("opencode binary not found")
	}
	out, err := runModelProbeCommand(binary, "service", "status")
	if err != nil {
		return openCodeService{}, err
	}
	serviceURL, err := parseOpenCodeServiceURL(string(out))
	if err != nil {
		return openCodeService{}, err
	}
	return openCodeService{URL: serviceURL, Password: openCodeServicePassword(serviceURL)}, nil
}

// parseOpenCodeServiceURL takes the first line of `opencode service status`
// and accepts it only as an http(s) URL on this machine.
func parseOpenCodeServiceURL(out string) (string, error) {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	line = strings.TrimRight(strings.TrimSpace(line), "/")
	u, err := url.Parse(line)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("opencode service status: not a service URL: %q", line)
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("opencode service status: refusing non-loopback service %q", host)
	}
	return line, nil
}

// openCodeServicePassword reads $XDG_STATE_HOME/opencode/service.json
// (default ~/.local/state), the 0600 registration file OpenCode 2.x writes
// for the running service. Empty when absent or for a different URL.
func openCodeServicePassword(serviceURL string) string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	data, err := os.ReadFile(filepath.Join(stateHome, "opencode", "service.json"))
	if err != nil {
		return ""
	}
	var reg struct {
		URL      string `json:"url"`
		Password string `json:"password"`
	}
	if json.Unmarshal(data, &reg) != nil || strings.TrimRight(reg.URL, "/") != serviceURL {
		return ""
	}
	return reg.Password
}
