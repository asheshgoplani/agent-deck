package ask

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// vaultPathDefault is the only vault: deckbox's root-owned deck-secret.
// There is no config key or env var for it (security review 1, HIGH): an
// agent can edit both. Tests swap vaultPath and vaultTrusted from _test.go
// (in this package) or through SetVaultForTest, which works only in a test
// binary.
const vaultPathDefault = "/usr/local/bin/deck-secret"

var (
	vaultPath    = vaultPathDefault
	vaultTrusted = checkVaultTrusted
)

const (
	vaultTimeout   = 30 * time.Second
	vaultOutputCap = 4 << 10
	vaultChildPATH = "/usr/local/bin:/usr/bin:/bin"
)

// checkVaultTrusted refuses a vault that is missing, a symlink, not a
// regular file, or that it or any parent dir is not owned by root or is
// writable by group or others: anyone who can replace it would receive the
// value.
func checkVaultTrusted(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNoVault
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoVault, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !rootOnly(info) {
		return ErrVaultUntrusted
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !rootOnly(info) {
			return ErrVaultUntrusted
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

func rootOnly(info os.FileInfo) bool {
	return ownerUID(info) == 0 && info.Mode().Perm()&0o022 == 0
}

// vaultEnv is the vault child's whole environment: a fixed PATH (deck-secret
// runs sudo through it), HOME, and AGENTDECK_SESSION_ID, the name
// deck-secret's audit log reads.
func vaultEnv(sessionID string) []string {
	home, _ := os.UserHomeDir()
	return []string{"PATH=" + vaultChildPATH, "HOME=" + home, "AGENTDECK_SESSION_ID=" + sessionID}
}

// capBuffer keeps at most max bytes and drops the rest.
type capBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

// runVault runs the vault with args and stdin. Its output goes to capped
// buffers that callers only parse for fixed tokens; it is never printed or
// logged, since a failing vault may echo what it read.
func runVault(sessionID string, stdin []byte, args ...string) (code int, stdout, stderr []byte, err error) {
	if err := vaultTrusted(vaultPath); err != nil {
		return 0, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), vaultTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, vaultPath, args...) //nolint:gosec // G204: the fixed, root-owned vault path checked above; args are a subcommand and a validated name
	cmd.Env = vaultEnv(sessionID)
	cmd.Dir = "/"
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out := &capBuffer{max: vaultOutputCap}
	errOut := &capBuffer{max: vaultOutputCap}
	cmd.Stdout, cmd.Stderr = out, errOut
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		code = exitErr.ExitCode()
	default:
		return 0, nil, nil, fmt.Errorf("%w: could not run the vault", ErrVaultRefused)
	}
	return code, out.buf.Bytes(), errOut.buf.Bytes(), nil
}

// vaultExists asks `deck-secret exists name` (yes: exit 0, no: exit 3).
func vaultExists(name, sessionID string) (bool, error) {
	code, out, _, err := runVault(sessionID, nil, "exists", name)
	if err != nil {
		return false, err
	}
	switch {
	case code == 0 && bytes.Equal(bytes.TrimSpace(out), []byte("yes")):
		return true, nil
	case code == 3:
		return false, nil
	}
	return false, fmt.Errorf("%w (exists: exit %d)", ErrVaultRefused, code)
}

var replacedRe = regexp.MustCompile(`\(replaced=(yes|no)\)`)

// vaultSet runs `deck-secret set name` with value on stdin and returns
// deck-vault's replaced=yes|no (fallback when the line is missing).
func vaultSet(name, sessionID string, value []byte, fallback string) (string, error) {
	code, _, errOut, err := runVault(sessionID, value, "set", name)
	defer clear(errOut) // may echo the value; parsed for a fixed token only
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("%w (exit %d)", ErrVaultRefused, code)
	}
	if m := replacedRe.FindSubmatch(errOut); m != nil {
		return string(m[1]), nil
	}
	return fallback, nil
}

// CheckVault reports whether this host has a trusted vault: nil, ErrNoVault
// (exit 5 in the asker) or ErrVaultUntrusted.
func CheckVault() error { return vaultTrusted(vaultPath) }
