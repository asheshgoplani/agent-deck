package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// HostAliasPrefix starts the ssh Host alias of every sandbox remote.
const HostAliasPrefix = "agentdeck-sb-"

// sandboxesDirName holds one directory per sandbox remote under the
// agent-deck data dir.
const sandboxesDirName = "sandboxes"

const (
	sshConfigFile   = "ssh_config"
	knownHostsFile  = "known_hosts"
	sandboxInfoFile = "sandbox.json"
)

// remoteNamePattern is stricter than the CLI's remote-name rule: the name
// becomes part of an ssh Host alias and a directory name.
var remoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidRemoteName reports whether name can name a sandbox remote.
func ValidRemoteName(name string) bool { return remoteNamePattern.MatchString(name) }

// HostAlias is the ssh Host alias for the sandbox remote name.
func HostAlias(remoteName string) string { return HostAliasPrefix + remoteName }

// SandboxDir is the directory holding remoteName's ssh_config, known_hosts
// and sandbox.json: <agent-deck data dir>/sandboxes/<remote name>.
func SandboxDir(remoteName string) (string, error) {
	if !ValidRemoteName(remoteName) {
		return "", fmt.Errorf("invalid sandbox remote name %q", remoteName)
	}
	base, err := session.GetAgentDeckDir()
	if err != nil {
		return "", fmt.Errorf("resolving agent-deck data dir: %w", err)
	}
	return filepath.Join(base, sandboxesDirName, remoteName), nil
}

// sandboxInfo is sandbox.json: what `remote list` shows about the sandbox.
type sandboxInfo struct {
	Provider  string            `json:"provider,omitempty"`
	ID        string            `json:"id"`
	Labels    map[string]string `json:"labels,omitempty"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// WriteSandboxFiles writes remoteName's ssh_config and known_hosts (and
// sandbox.json) for a validated sandbox into a private (0700) directory, each
// file 0600, and returns the ssh_config path. With host keys the keys are
// pinned (StrictHostKeyChecking yes); without them the first key seen is
// trusted into this sandbox's own known_hosts (accept-new), never the user's.
func WriteSandboxFiles(remoteName string, sb *Sandbox) (string, error) {
	return writeSandboxFiles(remoteName, "", sb)
}

func writeSandboxFiles(remoteName, providerName string, sb *Sandbox) (string, error) {
	dir, err := SandboxDir(remoteName)
	if err != nil {
		return "", err
	}
	if !safeConfigPath(dir) {
		return "", fmt.Errorf("agent-deck data dir %q contains a quote, %%, or control character; it cannot go in an ssh_config", dir)
	}
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	knownHosts := filepath.Join(dir, knownHostsFile)
	var kh strings.Builder
	for _, key := range sb.HostKeys {
		kh.WriteString(knownHostsName(sb.Host, sb.Port))
		kh.WriteString(" ")
		kh.WriteString(key)
		kh.WriteString("\n")
	}
	if err := writePrivateFile(knownHosts, []byte(kh.String())); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("# Written by agent-deck for a provider sandbox; regenerated on provisioning.\n")
	b.WriteString("Host " + HostAlias(remoteName) + "\n")
	b.WriteString("  HostName " + sb.Host + "\n")
	b.WriteString("  Port " + strconv.Itoa(sb.Port) + "\n")
	b.WriteString("  User " + sb.User + "\n")
	if sb.Auth.Type == "key" {
		b.WriteString("  IdentityFile \"" + sb.Auth.KeyPath + "\"\n")
		b.WriteString("  IdentitiesOnly yes\n")
	}
	b.WriteString("  UserKnownHostsFile \"" + knownHosts + "\"\n")
	if len(sb.HostKeys) > 0 {
		b.WriteString("  StrictHostKeyChecking yes\n")
	} else {
		b.WriteString("  StrictHostKeyChecking accept-new\n")
	}
	sshConfig := filepath.Join(dir, sshConfigFile)
	if err := writePrivateFile(sshConfig, []byte(b.String())); err != nil {
		return "", err
	}

	info, err := json.MarshalIndent(sandboxInfo{Provider: providerName, ID: sb.ID, Labels: sb.Labels, ExpiresAt: sb.ExpiresAt, CreatedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding sandbox.json: %w", err)
	}
	if err := writePrivateFile(filepath.Join(dir, sandboxInfoFile), append(info, '\n')); err != nil {
		return "", err
	}
	return sshConfig, nil
}

// knownHostsName is the host as ssh looks it up in known_hosts: bare for
// port 22, "[host]:port" otherwise.
func knownHostsName(host string, port int) string {
	if port == 22 {
		return host
	}
	return "[" + host + "]:" + strconv.Itoa(port)
}

// ensurePrivateDir creates dir (and its parents) and makes sure dir itself
// is a real directory, not a symlink, owned by the current user, mode 0700.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fmt.Errorf("creating sandboxes dir: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("creating sandbox dir: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("checking sandbox dir: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("sandbox dir %q is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("sandbox dir %q is owned by uid %d, not the current user", dir, st.Uid)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("securing sandbox dir: %w", err)
	}
	return nil
}

// writePrivateFile writes data to path (0600) through a temp file and a
// rename, so a symlink planted at path is replaced, never followed.
func writePrivateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	ok = true
	return nil
}

// RemoveSandboxFiles removes remoteName's sandbox directory. A missing
// directory is not an error.
func RemoveSandboxFiles(remoteName string) error {
	dir, err := SandboxDir(remoteName)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing sandbox files for %q: %w", remoteName, err)
	}
	return nil
}

// SandboxLabels returns the labels and expiry recorded in remoteName's
// sandbox.json, for display. Missing or unreadable files yield nothing.
func SandboxLabels(remoteName string) (map[string]string, *time.Time) {
	dir, err := SandboxDir(remoteName)
	if err != nil {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, sandboxInfoFile))
	if err != nil {
		return nil, nil
	}
	var info sandboxInfo
	if json.Unmarshal(data, &info) != nil {
		return nil, nil
	}
	return info.Labels, info.ExpiresAt
}
