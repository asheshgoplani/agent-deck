package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The macOS app feature-detects image upload by probing
// `session image-upload --help` on the owning host: exit 0 or 1, never an
// "unknown" command, and the Go flag usage naming -name. An older core must
// keep answering "unknown session command" so the app leaves the button off.
func TestSessionImageUploadHelpProbeContract(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "session", "image-upload", "--help")
	combined := stdout + stderr
	if code != 0 {
		t.Fatalf("help probe exit=%d, want 0\n%s", code, combined)
	}
	if strings.Contains(strings.ToLower(combined), "unknown") {
		t.Fatalf("help probe reads as unknown command:\n%s", combined)
	}
	for _, want := range []string{"Usage of session image-upload:", "-name", "-json"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("help probe lacks %q:\n%s", want, combined)
		}
	}
	if strings.Contains(combined, "Error:") {
		t.Fatalf("help probe printed an error:\n%s", combined)
	}
}

// The remote route forwards image-upload with its stdin; it is refused by the
// controller allowlist on cores without it.
func TestSessionImageUploadRemoteAllowlistContract(t *testing.T) {
	args := []string{"session", "image-upload", "task", "--name", "0b7d.png", "--json"}
	got, err := remoteCommandArgs(args)
	if err != nil || !reflect.DeepEqual(got, args) {
		t.Fatalf("remoteCommandArgs(%v) = %v, %v", args, got, err)
	}
	forwarded, input, closeInput, err := remoteMessageInput(got)
	defer closeInput()
	if err != nil || !reflect.DeepEqual(forwarded, args) || input != os.Stdin {
		t.Fatalf("image bytes must travel on stdin unchanged: %v %v %v", forwarded, input, err)
	}
}

// JSON receipt is exactly {path, bytes}; the path is absolute under the
// profile's own data dir; a retry with the same name never clobbers.
func TestSessionImageUploadJSONContract(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "upload-contract", "claude")
	body := "\x89PNG\r\n\x1a\n\x00contract\xff"
	name := "5f0c2a8e-7c1b-4d0e-9a51-3b9d2f6e8a10.png"
	stdout, stderr, code := runAgentDeckStdin(t, home, body, "session", "image-upload", id, "--name", name, "--json")
	if code != 0 {
		t.Fatalf("upload exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("receipt is not JSON: %v %q", err, stdout)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"bytes", "path"}) {
		t.Fatalf("receipt keys = %v, want [bytes path]", keys)
	}
	path, _ := raw["path"].(string)
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\n\x00") {
		t.Fatalf("receipt path not a clean absolute path: %q", path)
	}
	if want := filepath.Join("macapp-uploads", id, name); !strings.HasSuffix(path, string(filepath.Separator)+want) {
		t.Fatalf("receipt path %q does not end in %s", path, want)
	}
	if !strings.HasPrefix(path, home+string(filepath.Separator)) {
		t.Fatalf("receipt path %q outside the core's data dir", path)
	}
	if n, _ := raw["bytes"].(float64); int(n) != len(body) {
		t.Fatalf("receipt bytes = %v, want %d", raw["bytes"], len(body))
	}
	stdout, stderr, code = runAgentDeckStdin(t, home, "replacement", "session", "image-upload", id, "--name", name, "--json")
	if code == 0 || strings.TrimSpace(stdout) != "" || !strings.Contains(stderr, "Error:") {
		t.Fatalf("retry with the same name must be refused: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != body {
		t.Fatalf("retry clobbered the upload: %q %v", data, err)
	}
}
