package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/docker"
)

// cursorSandboxHome and cursorSandboxWorkDir match internal/docker/config.go
// containerHome and containerWorkDir.
const (
	cursorSandboxHome         = "/root"
	cursorSandboxWorkDir      = "/workspace"
	cursorTrustCommandTimeout = 30 * time.Second
)

// validateCursorProjectKey rejects project keys that could escape the projects/
// subtree via path traversal. Cursor itself allows dots, spaces, and other
// characters from the workspace path in the slug.
func validateCursorProjectKey(key string) error {
	if key == "" {
		return fmt.Errorf("cursor project key is empty")
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("cursor project key %q contains ..", key)
	}
	if strings.ContainsAny(key, `/\`) {
		return fmt.Errorf("cursor project key %q contains path separator", key)
	}
	return nil
}

// validateCursorTrustPathContained ensures trustPath stays under
// cursorConfigDir/projects/ and names the expected trust file.
func validateCursorTrustPathContained(cursorConfigDir, trustPath string) error {
	cleanConfig, err := filepath.Abs(filepath.Clean(cursorConfigDir))
	if err != nil {
		return fmt.Errorf("abs cursor config dir: %w", err)
	}
	cleanTrust, err := filepath.Abs(filepath.Clean(trustPath))
	if err != nil {
		return fmt.Errorf("abs trust path: %w", err)
	}
	projectsRoot := filepath.Join(cleanConfig, "projects")
	if cleanTrust != projectsRoot && !strings.HasPrefix(cleanTrust, projectsRoot+string(os.PathSeparator)) {
		return fmt.Errorf("trust path %q escapes cursor config dir", trustPath)
	}
	if filepath.Base(cleanTrust) != ".workspace-trusted" {
		return fmt.Errorf("trust path %q is not a .workspace-trusted file", trustPath)
	}
	return nil
}

// cursorWorkspaceProjectKey maps an absolute workspace path to the directory
// name Cursor uses under ~/.cursor/projects/ (e.g.
// /Users/me/proj → Users-me-proj).
func cursorWorkspaceProjectKey(workspacePath string) (string, error) {
	if workspacePath == "" {
		return "", fmt.Errorf("workspacePath is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(workspacePath))
	if err != nil {
		return "", fmt.Errorf("abs %s: %w", workspacePath, err)
	}
	abs = strings.TrimSuffix(abs, string(os.PathSeparator))
	key := strings.TrimPrefix(abs, string(os.PathSeparator))
	key = strings.ReplaceAll(key, string(os.PathSeparator), "-")
	if key == "" {
		return "", fmt.Errorf("workspacePath resolves to empty key: %s", workspacePath)
	}
	if err := validateCursorProjectKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// cursorWorkspaceTrustPath returns the ~/.cursor/projects/<key>/.workspace-trusted
// path for workspacePath.
func cursorWorkspaceTrustPath(cursorConfigDir, workspacePath string) (string, string, error) {
	if cursorConfigDir == "" {
		return "", "", fmt.Errorf("cursorConfigDir is empty")
	}
	key, err := cursorWorkspaceProjectKey(workspacePath)
	if err != nil {
		return "", "", err
	}
	abs, err := filepath.Abs(filepath.Clean(workspacePath))
	if err != nil {
		return "", "", fmt.Errorf("abs %s: %w", workspacePath, err)
	}
	projectDir := filepath.Join(cursorConfigDir, "projects", key)
	trustPath := filepath.Join(projectDir, ".workspace-trusted")
	if err := validateCursorTrustPathContained(cursorConfigDir, trustPath); err != nil {
		return "", "", err
	}
	return trustPath, abs, nil
}

// cursorTrustEntryJSON returns the JSON bytes Cursor writes for workspace trust
// and the absolute workspace path stored in the file.
func cursorTrustEntryJSON(workspacePath string) ([]byte, string, error) {
	abs, err := filepath.Abs(filepath.Clean(workspacePath))
	if err != nil {
		return nil, "", fmt.Errorf("abs %s: %w", workspacePath, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	entry := map[string]string{
		"trustedAt":     now.Format("2006-01-02T15:04:05.000Z"),
		"workspacePath": abs,
	}
	out, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("marshal cursor trust: %w", err)
	}
	out = append(out, '\n')
	return out, abs, nil
}

// writeCursorTrustFileExclusive creates the trust file with O_EXCL so concurrent
// seeders do not overwrite each other. An existing file is left unchanged.
func writeCursorTrustFileExclusive(trustPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(trustPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", trustPath, err)
	}
	// #nosec G304 -- trustPath is confined under cursorConfigDir/projects by
	// validateCursorTrustPathContained before this write is attempted.
	if err := writeFileIfAbsent(trustPath, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", trustPath, err)
	}
	return nil
}

// PreAcceptCursorTrust seeds ~/.cursor/projects/<key>/.workspace-trusted for
// workspacePath so interactive `cursor agent` skips the workspace-trust prompt.
//
// Cursor keys trust by the literal workspace path (stored in the JSON file) and
// a slugified directory under ~/.cursor/projects/. Pre-seeding matches what
// accepting the prompt in the UI would write. This mirrors Cursor's observed
// trust format as of 2026-06; if Cursor changes that schema, seeding may become
// a harmless no-op until this mapping is updated.
//
// If the trust file already exists it is left unchanged. Malformed existing
// trust files are not overwritten.
func PreAcceptCursorTrust(cursorConfigDir, workspacePath string) error {
	trustPath, _, err := cursorWorkspaceTrustPath(cursorConfigDir, workspacePath)
	if err != nil {
		return err
	}
	content, _, err := cursorTrustEntryJSON(workspacePath)
	if err != nil {
		return err
	}
	return writeCursorTrustFileExclusive(trustPath, content)
}

// buildCursorTrustRemoteShellScript returns a POSIX shell script that writes
// trust JSON to path through a same-directory temp file and exclusive hard link
// so concurrent seeders do not overwrite an existing file. pathSetup must set
// path= (and optionally key=) before the write runs.
func buildCursorTrustRemoteShellScript(pathSetup string, content []byte) string {
	b64 := base64.StdEncoding.EncodeToString(content)
	return fmt.Sprintf(`set -e
%s
if [ -e "$path" ] || [ -L "$path" ]; then
  exit 0
fi
dir="$(dirname "$path")"
mkdir -p "$dir"
tmp="$(mktemp "$dir/.workspace-trusted.XXXXXX")"
trap 'rm -f "$tmp"' EXIT HUP INT TERM
echo %s | base64 -d > "$tmp"
if ln "$tmp" "$path" 2>/dev/null; then
  rm -f "$tmp"
elif [ ! -e "$path" ] && [ ! -L "$path" ]; then
  exit 1
fi
`, pathSetup, shellQuote(b64))
}

// runCursorTrustRemoteScript executes a trust-seeding shell script on a remote
// host over SSH. The script is passed on stdin to avoid embedding it in argv.
func runCursorTrustRemoteScript(host, script string) error {
	_ = os.MkdirAll(sshControlDir, 0o700)
	args := append(sessionSSHConnOpts(), host, "sh", "-s")
	ctx, cancel := context.WithTimeout(context.Background(), cursorTrustCommandTimeout)
	defer cancel()
	// #nosec G204 -- host is validated by ValidateSSHHost; script is generated
	// locally from a slugified key and base64 JSON, delivered via stdin.
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("ssh cursor trust seed: %w: %s", ctxErr, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("ssh cursor trust seed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runCursorTrustContainerScript executes a trust-seeding shell script inside a
// managed sandbox container. The script is passed on stdin to docker exec.
func runCursorTrustContainerScript(containerName, script string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cursorTrustCommandTimeout)
	defer cancel()
	// #nosec G204 -- containerName is restricted to agent-deck-managed names;
	// script is generated locally and delivered via stdin.
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", containerName, "sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("docker exec cursor trust seed: %w: %s", ctxErr, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("docker exec cursor trust seed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PreAcceptCursorTrustSSH seeds Cursor workspace trust on a POSIX remote host
// via ssh. Project-key generation assumes matching POSIX path semantics locally.
func PreAcceptCursorTrustSSH(host, workspacePath string) error {
	if err := ValidateSSHHost(host); err != nil {
		return err
	}
	content, absWorkspace, err := cursorTrustEntryJSON(workspacePath)
	if err != nil {
		return err
	}
	key, err := cursorWorkspaceProjectKey(absWorkspace)
	if err != nil {
		return err
	}
	pathSetup := fmt.Sprintf("key=%s\npath=\"$HOME/.cursor/projects/$key/.workspace-trusted\"", shellQuote(key))
	script := buildCursorTrustRemoteShellScript(pathSetup, content)
	return runCursorTrustRemoteScript(host, script)
}

// PreAcceptCursorTrustInContainer seeds Cursor workspace trust inside a sandbox
// container via docker exec.
func PreAcceptCursorTrustInContainer(containerName, workspacePath string) error {
	if strings.TrimSpace(containerName) == "" {
		return fmt.Errorf("containerName is empty")
	}
	if !docker.IsManagedContainer(containerName) {
		return fmt.Errorf("container %q is not an agent-deck managed container", containerName)
	}
	if workspacePath != cursorSandboxWorkDir {
		return fmt.Errorf("sandbox workspace path %q is not %q", workspacePath, cursorSandboxWorkDir)
	}
	content, absWorkspace, err := cursorTrustEntryJSON(workspacePath)
	if err != nil {
		return err
	}
	key, err := cursorWorkspaceProjectKey(absWorkspace)
	if err != nil {
		return err
	}
	trustPath := path.Join(cursorSandboxHome, ".cursor", "projects", key, ".workspace-trusted")
	pathSetup := fmt.Sprintf("path=%s", shellQuote(trustPath))
	script := buildCursorTrustRemoteShellScript(pathSetup, content)
	return runCursorTrustContainerScript(containerName, script)
}

// sessionSSHConnOpts returns SSH connection options shared with SSHRunner paths.
func sessionSSHConnOpts() []string {
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + sshControlDir + "/%r@%h:%p",
		"-o", "ControlPersist=600",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
	}
}
