package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func runShellScriptForTest(script string) (string, error) {
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCursorWorkspaceProjectKey(t *testing.T) {
	workspace := filepath.Join("/Users", "me", "dev", "agent-deck")
	key, err := cursorWorkspaceProjectKey(workspace)
	if err != nil {
		t.Fatalf("cursorWorkspaceProjectKey: %v", err)
	}
	want := "Users-me-dev-agent-deck"
	if key != want {
		t.Fatalf("key = %q, want %q", key, want)
	}
}

func TestCursorWorkspaceProjectKey_AllowsDotsAndSpaces(t *testing.T) {
	workspace := filepath.Join("/Users", "me", "my.repo", "My Project")
	key, err := cursorWorkspaceProjectKey(workspace)
	if err != nil {
		t.Fatalf("cursorWorkspaceProjectKey: %v", err)
	}
	if !strings.Contains(key, ".") {
		t.Fatalf("key %q should preserve dots from workspace path", key)
	}
	if !strings.Contains(key, " ") {
		t.Fatalf("key %q should preserve spaces from workspace path", key)
	}
}

func TestPreAcceptCursorTrust_CreatesTrustFile(t *testing.T) {
	tmpHome := t.TempDir()
	cursorDir := filepath.Join(tmpHome, ".cursor")
	workspace := filepath.Join(tmpHome, "worktrees", "feature-x")

	if err := PreAcceptCursorTrust(cursorDir, workspace); err != nil {
		t.Fatalf("PreAcceptCursorTrust: %v", err)
	}

	trustPath, absWorkspace, err := cursorWorkspaceTrustPath(cursorDir, workspace)
	if err != nil {
		t.Fatalf("cursorWorkspaceTrustPath: %v", err)
	}
	data, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read trust file: %v", err)
	}
	var entry map[string]string
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal trust file: %v", err)
	}
	if entry["workspacePath"] != absWorkspace {
		t.Fatalf("workspacePath = %q, want %q", entry["workspacePath"], absWorkspace)
	}
	if entry["trustedAt"] == "" {
		t.Fatal("trustedAt is empty")
	}
	if !strings.HasSuffix(entry["trustedAt"], "Z") {
		t.Fatalf("trustedAt = %q, want UTC Z suffix", entry["trustedAt"])
	}
}

func TestPreAcceptCursorTrust_Idempotent(t *testing.T) {
	tmpHome := t.TempDir()
	cursorDir := filepath.Join(tmpHome, ".cursor")
	workspace := filepath.Join(tmpHome, "repo")

	if err := PreAcceptCursorTrust(cursorDir, workspace); err != nil {
		t.Fatalf("first PreAcceptCursorTrust: %v", err)
	}
	trustPath, _, err := cursorWorkspaceTrustPath(cursorDir, workspace)
	if err != nil {
		t.Fatalf("cursorWorkspaceTrustPath: %v", err)
	}
	before, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read trust file: %v", err)
	}

	if err := PreAcceptCursorTrust(cursorDir, workspace); err != nil {
		t.Fatalf("second PreAcceptCursorTrust: %v", err)
	}
	after, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read trust file after second call: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("trust file changed on second call:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestPreAcceptCursorTrust_EmptyInputs(t *testing.T) {
	if err := PreAcceptCursorTrust("", "/tmp"); err == nil {
		t.Fatal("expected error for empty cursorConfigDir")
	}
	if err := PreAcceptCursorTrust("/tmp/.cursor", ""); err == nil {
		t.Fatal("expected error for empty workspacePath")
	}
}

func TestWriteCursorTrustFileExclusive_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(dir, "projects", "key", ".workspace-trusted")
	content := []byte("original\n")

	if err := writeCursorTrustFileExclusive(trustPath, content); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeCursorTrustFileExclusive(trustPath, []byte("overwrite\n")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content = %q, want %q", got, content)
	}
}

func TestWriteCursorTrustFileExclusive_Concurrent(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(dir, ".workspace-trusted")
	workspace := filepath.Join(dir, "proj")
	content, _, err := cursorTrustEntryJSON(workspace)
	if err != nil {
		t.Fatalf("cursorTrustEntryJSON: %v", err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = writeCursorTrustFileExclusive(trustPath, content)
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read trust file: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("unexpected content: %s", data)
	}
}

func TestInstance_preAcceptCursorWorkspaceTrust_Local(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	workspace := filepath.Join(tmpHome, "proj")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	inst := NewInstanceWithTool("cursor-trust", workspace, "cursor")
	inst.preAcceptCursorWorkspaceTrust()

	trustPath, absWorkspace, err := cursorWorkspaceTrustPath(GetCursorConfigDir(), workspace)
	if err != nil {
		t.Fatalf("cursorWorkspaceTrustPath: %v", err)
	}
	if _, err := os.Stat(trustPath); err != nil {
		t.Fatalf("trust file missing: %v", err)
	}
	if absWorkspace != workspace {
		t.Fatalf("abs workspace = %q, want %q", absWorkspace, workspace)
	}
}

func TestInstance_cursorTrustWorkspacePath(t *testing.T) {
	inst := NewInstanceWithTool("x", "/host/proj", "cursor")
	inst.SSHHost = "user@remote"
	inst.SSHRemotePath = "/remote/proj"
	if got := inst.cursorTrustWorkspacePath(); got != "/remote/proj" {
		t.Fatalf("ssh path = %q, want /remote/proj", got)
	}

	inst.SSHHost = ""
	inst.Sandbox = NewSandboxConfig("")
	if got := inst.cursorTrustWorkspacePath(); got != cursorSandboxWorkDir {
		t.Fatalf("sandbox path = %q, want %s", got, cursorSandboxWorkDir)
	}
}

func TestInstance_cursorTrustWorkspacePath_SandboxTakesPrecedenceOverSSH(t *testing.T) {
	inst := NewInstanceWithTool("x", "/host/proj", "cursor")
	inst.SSHHost = "user@remote"
	inst.SSHRemotePath = "/remote/proj"
	inst.Sandbox = NewSandboxConfig("")
	inst.SandboxContainer = "agent-deck-test-12345678"

	if got := inst.cursorTrustWorkspacePath(); got != cursorSandboxWorkDir {
		t.Fatalf("workspace path = %q, want sandbox %s", got, cursorSandboxWorkDir)
	}
}

func TestInstance_preAcceptCursorWorkspaceTrust_SandboxTakesPrecedenceOverSSH(t *testing.T) {
	fakeBin := t.TempDir()
	capturedDockerScript := filepath.Join(t.TempDir(), "docker-script.sh")
	sshMarker := filepath.Join(t.TempDir(), "ssh-called")

	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte(`#!/bin/sh
set -eu
if [ "$1" != "exec" ] || [ "$2" != "-i" ]; then
  echo "unexpected docker args: $*" >&2
  exit 20
fi
cat > "$FAKE_DOCKER_SCRIPT"
`), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "ssh"), []byte(`#!/bin/sh
echo called > "$FAKE_SSH_MARKER"
exit 99
`), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}

	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_SCRIPT", capturedDockerScript)
	t.Setenv("FAKE_SSH_MARKER", sshMarker)

	inst := NewInstanceWithTool("x", "/host/proj", "cursor")
	inst.SSHHost = "user@remote"
	inst.SSHRemotePath = "/remote/proj"
	inst.Sandbox = NewSandboxConfig("")
	inst.SandboxContainer = "agent-deck-test-12345678"

	inst.preAcceptCursorWorkspaceTrust()

	script, err := os.ReadFile(capturedDockerScript)
	if err != nil {
		t.Fatalf("docker script was not captured: %v", err)
	}
	if !strings.Contains(string(script), cursorSandboxHome+"/.cursor/projects/") {
		t.Fatalf("docker script did not target sandbox cursor home:\n%s", script)
	}
	if _, err := os.Stat(sshMarker); !os.IsNotExist(err) {
		t.Fatalf("ssh path was invoked for sandboxed session; stat marker err=%v", err)
	}
}

func TestBuildCursorTrustRemoteShellScript_ExpandsHome(t *testing.T) {
	pathSetup := "key='foo'\npath=\"$HOME/.cursor/projects/$key/.workspace-trusted\""
	script := buildCursorTrustRemoteShellScript(pathSetup, []byte("data\n"))
	if !strings.Contains(script, `path="$HOME/.cursor/projects/$key/.workspace-trusted"`) {
		t.Fatalf("script missing HOME path expansion: %s", script)
	}
	if !strings.Contains(script, "mktemp") || !strings.Contains(script, `ln "$tmp" "$path"`) {
		t.Fatalf("script missing temp-file exclusive link: %s", script)
	}
	if !strings.Contains(script, "base64 -d") {
		t.Fatalf("script missing base64 decode: %s", script)
	}
}

func TestBuildCursorTrustRemoteShellScript_WritesWithoutClobber(t *testing.T) {
	remoteHome := t.TempDir()
	trustPath := filepath.Join(remoteHome, ".cursor", "projects", "foo", ".workspace-trusted")
	script := buildCursorTrustRemoteShellScript("path="+shellQuote(trustPath), []byte("created\n"))

	if out, err := runShellScriptForTest(script); err != nil {
		t.Fatalf("script failed: %v: %s", err, out)
	}
	data, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read trust file: %v", err)
	}
	if string(data) != "created\n" {
		t.Fatalf("trust content = %q, want created", data)
	}

	if out, err := runShellScriptForTest(buildCursorTrustRemoteShellScript("path="+shellQuote(trustPath), []byte("overwrite\n"))); err != nil {
		t.Fatalf("second script failed: %v: %s", err, out)
	}
	after, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read trust file after second script: %v", err)
	}
	if string(after) != "created\n" {
		t.Fatalf("trust content after second script = %q, want original", after)
	}
}

func TestBuildCursorTrustRemoteShellScript_PropagatesRealWriteErrors(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(parentFile, []byte("file\n"), 0o644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	trustPath := filepath.Join(parentFile, ".workspace-trusted")
	script := buildCursorTrustRemoteShellScript("path="+shellQuote(trustPath), []byte("data\n"))

	if out, err := runShellScriptForTest(script); err == nil {
		t.Fatalf("script succeeded unexpectedly: %s", out)
	}
}

func TestPreAcceptCursorTrustSSH_UsesRemoteHomeAndWorkspace(t *testing.T) {
	fakeBin := t.TempDir()
	remoteHome := t.TempDir()
	workspace := "/remote/My Project"
	fakeSSH := filepath.Join(fakeBin, "ssh")
	if err := os.WriteFile(fakeSSH, []byte(`#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      shift 2
      ;;
    *)
      break
      ;;
  esac
done
host="$1"
shift
if [ "$host" != "user@remote" ]; then
  echo "unexpected host: $host" >&2
  exit 20
fi
if [ "$1" != "sh" ] || [ "$2" != "-s" ]; then
  echo "unexpected remote command: $*" >&2
  exit 21
fi
export HOME="$FAKE_REMOTE_HOME"
exec sh -s
`), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_REMOTE_HOME", remoteHome)

	if err := PreAcceptCursorTrustSSH("user@remote", workspace); err != nil {
		t.Fatalf("PreAcceptCursorTrustSSH: %v", err)
	}

	key, err := cursorWorkspaceProjectKey(workspace)
	if err != nil {
		t.Fatalf("cursorWorkspaceProjectKey: %v", err)
	}
	trustPath := filepath.Join(remoteHome, ".cursor", "projects", key, ".workspace-trusted")
	data, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read remote trust file: %v", err)
	}
	var entry map[string]string
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal remote trust file: %v", err)
	}
	if entry["workspacePath"] != workspace {
		t.Fatalf("workspacePath = %q, want %q", entry["workspacePath"], workspace)
	}
}

func TestPreAcceptCursorTrustSSH_PropagatesRemoteWriteError(t *testing.T) {
	fakeBin := t.TempDir()
	remoteHome := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(remoteHome, []byte("file\n"), 0o644); err != nil {
		t.Fatalf("write fake remote home: %v", err)
	}
	fakeSSH := filepath.Join(fakeBin, "ssh")
	if err := os.WriteFile(fakeSSH, []byte(`#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      shift 2
      ;;
    *)
      break
      ;;
  esac
done
shift
if [ "$1" != "sh" ] || [ "$2" != "-s" ]; then
  echo "unexpected remote command: $*" >&2
  exit 21
fi
export HOME="$FAKE_REMOTE_HOME"
exec sh -s
`), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_REMOTE_HOME", remoteHome)

	if err := PreAcceptCursorTrustSSH("user@remote", "/remote/project"); err == nil {
		t.Fatal("PreAcceptCursorTrustSSH succeeded despite remote write error")
	}
}

func TestValidateCursorProjectKey_RejectsTraversal(t *testing.T) {
	if err := validateCursorProjectKey("Users-me-my.repo"); err != nil {
		t.Fatalf("dots in key should be allowed: %v", err)
	}
	if err := validateCursorProjectKey("Users-me-My Project"); err != nil {
		t.Fatalf("spaces in key should be allowed: %v", err)
	}
	if err := validateCursorProjectKey(".."); err == nil {
		t.Fatal("expected .. to be rejected")
	}
	if err := validateCursorProjectKey("foo/bar"); err == nil {
		t.Fatal("expected path separators to be rejected")
	}
}

func TestValidateCursorTrustPathContained(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), ".cursor")
	trustPath := filepath.Join(configDir, "projects", "Users-me-proj", ".workspace-trusted")
	if err := validateCursorTrustPathContained(configDir, trustPath); err != nil {
		t.Fatalf("expected contained path to pass: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside", ".workspace-trusted")
	if err := validateCursorTrustPathContained(configDir, outside); err == nil {
		t.Fatal("expected path outside cursor config dir to be rejected")
	}
}

func TestPreAcceptCursorTrustInContainer_RejectsUnmanagedName(t *testing.T) {
	err := PreAcceptCursorTrustInContainer("evil-container", cursorSandboxWorkDir)
	if err == nil {
		t.Fatal("expected unmanaged container name to be rejected")
	}
}

func TestPreAcceptCursorTrustInContainer_RejectsWrongWorkspace(t *testing.T) {
	err := PreAcceptCursorTrustInContainer("agent-deck-test-12345678", "/host/path")
	if err == nil {
		t.Fatal("expected non-sandbox workspace path to be rejected")
	}
}

func TestCursorTrustContainerScript_UsesPOSIXPath(t *testing.T) {
	content, absWorkspace, err := cursorTrustEntryJSON(cursorSandboxWorkDir)
	if err != nil {
		t.Fatalf("cursorTrustEntryJSON: %v", err)
	}
	key, err := cursorWorkspaceProjectKey(absWorkspace)
	if err != nil {
		t.Fatalf("cursorWorkspaceProjectKey: %v", err)
	}
	trustPath := cursorSandboxHome + "/.cursor/projects/" + key + "/.workspace-trusted"
	script := buildCursorTrustRemoteShellScript("path="+shellQuote(trustPath), content)
	if !strings.Contains(script, "path='"+trustPath+"'") {
		t.Fatalf("script missing POSIX container trust path %q:\n%s", trustPath, script)
	}
	if strings.Contains(script, `\`) {
		t.Fatalf("container script must not use host path separators:\n%s", script)
	}
}

func TestInstance_Start_CursorSeedsWorkspaceTrust(t *testing.T) {
	skipIfNoTmuxServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	workspace := filepath.Join(tmpHome, "my.repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	inst := NewInstanceWithTool("cursor-start-trust", workspace, "cursor")
	inst.Command = "/bin/true"
	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = inst.Kill() }()

	trustPath, _, err := cursorWorkspaceTrustPath(GetCursorConfigDir(), workspace)
	if err != nil {
		t.Fatalf("cursorWorkspaceTrustPath: %v", err)
	}
	if _, err := os.Stat(trustPath); err != nil {
		t.Fatalf("trust file missing after Start: %v", err)
	}
}
