package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func uploadCLIImage(t *testing.T, home, target, name, body string) session.ImageUpload {
	t.Helper()
	out, stderr, code := runAgentDeckStdin(t, home, body, "session", "image-upload", target, "--name", name, "--json")
	var result session.ImageUpload
	if code != 0 || json.Unmarshal([]byte(out), &result) != nil || result.Path == "" || result.Bytes != int64(len(body)) {
		t.Fatalf("upload: exit=%d stdout=%s stderr=%s result=%+v", code, out, stderr, result)
	}
	return result
}

func TestSessionImageUploadCLI(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "upload-by-title", "claude")
	body := "\x89PNG\r\n\x1a\n\x00binary\xff"
	result := uploadCLIImage(t, home, "upload-by-title", "attachment.png", body)
	want := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "macapp-uploads", id, "attachment.png")
	if result.Path != want || !filepath.IsAbs(result.Path) {
		t.Fatalf("upload path=%q want=%q", result.Path, want)
	}
	if data, err := os.ReadFile(result.Path); err != nil || string(data) != body {
		t.Fatalf("upload bytes=%q err=%v", data, err)
	}
	for path, mode := range map[string]os.FileMode{result.Path: 0600, filepath.Dir(result.Path): 0700, filepath.Dir(filepath.Dir(result.Path)): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s: info=%v err=%v want=%o", path, info, err, mode)
		}
	}
	for _, tc := range []struct{ target, name, body string }{
		{"unknown-upload-session", "valid.png", body},
		{id, "../escape.png", body},
		{id, "nested/file.png", body},
		{id, `nested\file.png`, body},
		{id, "invalid.txt", body},
		{id, "oversize.png", strings.Repeat("x", int(session.MaxImageUploadBytes)+1)},
	} {
		t.Run(tc.target+"/"+tc.name, func(t *testing.T) {
			out, stderr, code := runAgentDeckStdin(t, home, tc.body, "session", "image-upload", tc.target, "--name", tc.name, "--json")
			if code == 0 || strings.TrimSpace(stderr) == "" || strings.TrimSpace(out) != "" {
				t.Fatalf("invalid upload: exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
		})
	}
	entries, err := os.ReadDir(filepath.Dir(result.Path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "attachment.png" {
		t.Fatalf("refused uploads left files: %v %v", entries, err)
	}
	out, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--force", "--json")
	if code != 0 {
		t.Fatalf("remove: %d %s %s", code, out, stderr)
	}
	if _, err := os.Lstat(filepath.Dir(result.Path)); !os.IsNotExist(err) {
		t.Fatalf("removed session upload dir remains: %v", err)
	}
}

func TestSessionImageUploadCLIStartupPrune(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "upload-prune", "claude")
	old := uploadCLIImage(t, home, id, "old.png", "old")
	fresh := uploadCLIImage(t, home, id, "fresh.png", "fresh")
	age := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old.Path, age, age); err != nil {
		t.Fatal(err)
	}
	if out, stderr, code := runAgentDeck(t, home, "list", "--json"); code != 0 {
		t.Fatalf("startup: %d %s %s", code, out, stderr)
	}
	if _, err := os.Stat(old.Path); !os.IsNotExist(err) {
		t.Fatalf("old upload survives startup: %v", err)
	}
	if data, err := os.ReadFile(fresh.Path); err != nil || string(data) != "fresh" {
		t.Fatalf("fresh upload changed: %q %v", data, err)
	}
}

func TestSessionImageUploadCLISendAcceptsReturnedPath(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "upload-send", "claude")
	for _, tc := range []struct{ name, body string }{{"image.png", "\x89PNG\r\n\x1a\n"}, {"document.pdf", "%PDF-1.7\nfixture"}} {
		t.Run(tc.name, func(t *testing.T) {
			result := uploadCLIImage(t, home, id, tc.name, tc.body)
			out, stderr, code := runAgentDeck(t, home, "session", "send", id, "inspect", "--image", result.Path, "--json", "--queue")
			var rec sendRecordJSON
			if code != 1 || json.Unmarshal([]byte(out), &rec) != nil || rec.State != "failed" || rec.Reason != "target not running" || rec.SendID == "" {
				t.Fatalf("send acceptance: %d %s %s", code, out, stderr)
			}
			// The stopped fixture proves attachment acceptance and copying only.
			// It cannot establish delivery to an agent.
			if !strings.HasPrefix(rec.Message, "inspect @") {
				t.Fatalf("attachment absent from message: %q", rec.Message)
			}
			copyPath := strings.TrimPrefix(rec.Message, "inspect @")
			if !strings.Contains(copyPath, string(filepath.Separator)+".agentdeck-images"+string(filepath.Separator)) {
				t.Fatalf("attachment copy path: %q", copyPath)
			}
			if data, err := os.ReadFile(copyPath); err != nil || string(data) != tc.body {
				t.Fatalf("attachment copy: %q %v", data, err)
			}
		})
	}
}

func TestSessionImageUploadRemoteStdin(t *testing.T) {
	args := []string{"session", "image-upload", "remote-upload", "--name", "remote.png", "--json"}
	if got, err := remoteCommandArgs(args); err != nil || !reflect.DeepEqual(got, args) {
		t.Fatalf("remote upload allowlist: %v %v", got, err)
	}
	controller, remote, shim := t.TempDir(), t.TempDir(), t.TempDir()
	configDir := filepath.Join(controller, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nagent_deck_path = '%s'\n", channelsCLIBinary(t))
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	startParitySSH(t, remote, shim)
	env := []string{"PATH=" + shim + ":" + os.Getenv("PATH")}
	if out, stderr, code := runAgentDeckEnv(t, controller, "", env, "remote", "lab", "add", remote, "-t", "remote-upload", "-c", "claude", "--no-parent", "--json"); code != 0 {
		t.Fatalf("remote add: %d %s %s", code, out, stderr)
	}
	body := "\x89PNG\r\n\x1a\n\x00\xffbinary through SSH\n"
	out, stderr, code := runAgentDeckEnv(t, controller, body, env, append([]string{"remote", "lab"}, args...)...)
	var result session.ImageUpload
	if code != 0 || json.Unmarshal([]byte(out), &result) != nil || result.Bytes != int64(len(body)) {
		t.Fatalf("remote upload: %d %s %s", code, out, stderr)
	}
	if !strings.HasPrefix(result.Path, remote+string(filepath.Separator)) || strings.HasPrefix(result.Path, controller+string(filepath.Separator)) {
		t.Fatalf("upload used controller storage: %q", result.Path)
	}
	if data, err := os.ReadFile(result.Path); err != nil || string(data) != body {
		t.Fatalf("SSH stdin mismatch: %q %v", data, err)
	}
}
