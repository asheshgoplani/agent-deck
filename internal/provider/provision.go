package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Labels agent-deck puts on every sandbox it creates.
const (
	LabelRemote  = "agent-deck/remote"
	LabelProfile = "agent-deck/profile"
	// LabelCreateID is a random id per create request. When a create is cut
	// off (timeout, cancel) before the provider answered, it finds exactly
	// the sandbox that request made, if any.
	LabelCreateID = "agent-deck/create-id"
	// LabelOwner names the person who created the sandbox
	// (UserConfig.SandboxOwner), so a provider list or a cloud console
	// shows whose machine it is.
	LabelOwner = "agent-deck/owner"
)

// rollbackTimeout is the least time the cleanup after a failed
// provisioning gets; it runs on a fresh context so a cancelled caller still
// cleans up. A var so tests can shorten it.
var rollbackTimeout = 2 * time.Minute

// cleanupBudget is the time a cleanup of calls provider calls gets: at
// least rollbackTimeout, and enough for each call to use the provider's
// own timeout_seconds. A cloud destroy deletes several resources one after
// another; a shorter budget would kill it half way and leave resources
// that list never shows.
func cleanupBudget(client *Client, calls int) time.Duration {
	d := time.Duration(calls)*client.Config.GetTimeout() + killWait
	if d < rollbackTimeout {
		return rollbackTimeout
	}
	return d
}

const (
	// waitSSHBudget bounds the default ssh readiness probe.
	waitSSHBudget = 3 * time.Minute
	// waitSSHAttempt bounds one probe attempt.
	waitSSHAttempt = 20 * time.Second
	// waitSSHInterval is the pause between probe attempts.
	waitSSHInterval = 2 * time.Second
)

// ProvisionOptions configures Provision.
type ProvisionOptions struct {
	// Provider names the [providers.<name>] entry to create the sandbox with.
	Provider string
	// RemoteName is the remote to register; empty picks
	// "<provider>-<random suffix>".
	RemoteName string
	// Profile is the effective agent-deck profile, sent as the
	// agent-deck/profile label.
	Profile string
	// Progress receives one line per step; nil discards them.
	Progress func(string)
	// WaitSSH waits until the sandbox answers over ssh; nil probes with real
	// ssh, retrying for a bounded time.
	WaitSSH func(ctx context.Context, remoteName string, rc session.RemoteConfig) error
	// Install makes sure agent-deck runs on the sandbox; nil only checks that
	// the configured binary answers (the CLI passes the remote-add install).
	Install func(ctx context.Context, remoteName string, rc session.RemoteConfig) error
}

// Provisioned is a sandbox registered as a remote.
type Provisioned struct {
	RemoteName string
	Remote     session.RemoteConfig
	Sandbox    Sandbox
}

// Provision creates a sandbox with the provider, validates it, writes its
// ssh files, registers it as a remote (SaveUserConfig), waits for ssh and
// installs agent-deck. Any failure after create destroys the sandbox,
// unregisters the remote and removes the files, and the error reports both
// the failure and any rollback failure.
func Provision(ctx context.Context, opts ProvisionOptions) (*Provisioned, error) {
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	client, err := Lookup(cfg, opts.Provider)
	if err != nil {
		return nil, err
	}
	name := opts.RemoteName
	if name == "" {
		if name, err = defaultRemoteName(opts.Provider); err != nil {
			return nil, err
		}
	}
	if !ValidRemoteName(name) {
		return nil, fmt.Errorf("invalid remote name %q: use letters, digits, '-' and '_' (max 64, starting with a letter or digit)", name)
	}
	// One provisioning per remote name at a time: a second one would
	// overwrite the first's ssh files and, on its rollback, remove them.
	unlock, err := lockRemoteName(name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if cfg, err = session.ReloadUserConfig(); err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if _, exists := cfg.Remotes[name]; exists {
		return nil, fmt.Errorf("remote %q already exists (use 'agent-deck remote remove %s' or pick another --name)", name, name)
	}
	owner, ownerSource := cfg.SandboxOwnerSource()
	if err := validateOwner(owner, ownerSource); err != nil {
		return nil, err
	}

	createID, err := randomHex(8)
	if err != nil {
		return nil, fmt.Errorf("generating create id: %w", err)
	}
	labels := map[string]string{LabelRemote: name, LabelCreateID: createID}
	if opts.Profile != "" {
		labels[LabelProfile] = opts.Profile
	}
	if owner != "" {
		labels[LabelOwner] = owner
	}
	progress(fmt.Sprintf("Creating sandbox %q with provider %q...", name, opts.Provider))
	sb, err := client.Create(ctx, name, labels)
	if err != nil {
		if sb != nil {
			return nil, rollback(client, name, sb.ID, false, err)
		}
		if errors.Is(err, ErrTimeout) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// The provider may have made the sandbox before it was cut off.
			return nil, sweepInterruptedCreate(client, name, createID, err)
		}
		return nil, err
	}
	progress(fmt.Sprintf("Sandbox %s is up at %s:%d", sb.ID, sb.Host, sb.Port))

	sshConfig, err := writeSandboxFiles(name, opts.Provider, sb)
	if err != nil {
		return nil, rollback(client, name, sb.ID, false, fmt.Errorf("writing sandbox ssh files: %w", err))
	}
	rc := session.RemoteConfig{
		Host:          HostAlias(name),
		AgentDeckPath: client.Config.AgentDeckPath,
		Provider:      opts.Provider,
		SandboxID:     sb.ID,
		SSHConfig:     sshConfig,
		Owner:         owner,
	}
	if err := registerRemote(name, rc); err != nil {
		return nil, rollback(client, name, sb.ID, false, err)
	}

	waitSSH := opts.WaitSSH
	if waitSSH == nil {
		waitSSH = defaultWaitSSH
	}
	progress("Waiting for ssh...")
	if err := waitSSH(ctx, name, rc); err != nil {
		return nil, rollback(client, name, sb.ID, true, fmt.Errorf("waiting for ssh on sandbox %s: %w", sb.ID, err))
	}
	install := opts.Install
	if install == nil {
		install = defaultInstall
	}
	if err := install(ctx, name, rc); err != nil {
		return nil, rollback(client, name, sb.ID, true, fmt.Errorf("installing agent-deck on sandbox %s: %w", sb.ID, err))
	}
	return &Provisioned{RemoteName: name, Remote: rc, Sandbox: *sb}, nil
}

// maxOwner caps the owner name, in runes.
const maxOwner = 64

// validateOwner refuses an owner name that cannot be a label value or a
// display name: control or non-printable characters, or more than maxOwner
// runes. Empty is allowed (no owner known; the label is left out). The
// refusal names source (where the value came from) and never echoes it.
func validateOwner(owner, source string) error {
	if !utf8.ValidString(owner) || utf8.RuneCountInString(owner) > maxOwner {
		return fmt.Errorf("invalid sandbox owner from %s: at most %d characters of UTF-8 text%s", source, maxOwner, ownerFixHint(source))
	}
	for _, r := range owner {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("invalid sandbox owner from %s: it contains a control or non-printable character%s", source, ownerFixHint(source))
		}
	}
	return nil
}

// ownerFixHint says how to fix an owner that did not come from the config.
func ownerFixHint(source string) string {
	if source == session.OwnerSourceConfig {
		return ""
	}
	return " (set owner in the [sandbox] section of config.toml to use another name)"
}

// rollback undoes a failed provisioning and joins its failures to cause.
func rollback(client *Client, name, id string, registered bool, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget(client, 1))
	defer cancel()
	var errs []error
	if err := client.Destroy(ctx, id); err != nil {
		errs = append(errs, fmt.Errorf("destroying sandbox %s: %w (destroy it by hand)", id, err))
	}
	if registered {
		if err := unregisterRemote(name); err != nil {
			errs = append(errs, err)
		}
	}
	if err := RemoveSandboxFiles(name); err != nil {
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return fmt.Errorf("%w (sandbox %s destroyed)", cause, id)
	}
	providerLog.Warn("provider_rollback_incomplete", slog.String("provider", client.Name), slog.String("remote", name), slog.Int("failures", len(errs)))
	return fmt.Errorf("%w; rollback failed: %w", cause, errors.Join(errs...))
}

// sweepInterruptedCreate destroys the sandbox a cut-off create request
// made, found by its create-id label, on a fresh context so a cancelled
// caller still cleans up. Its failures are joined to cause, with the label
// to look for by hand.
func sweepInterruptedCreate(client *Client, name, createID string, cause error) error {
	// A list, then normally one destroy.
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget(client, 2))
	defer cancel()
	list, err := client.List(ctx)
	if err != nil {
		providerLog.Warn("provider_create_sweep_failed", slog.String("provider", client.Name), slog.String("remote", name))
		return fmt.Errorf("%w; could not check for a leftover sandbox labelled %s=%s: %w (check the provider by hand)", cause, LabelRemote, name, err)
	}
	var destroyed []string
	var errs []error
	for _, sb := range list {
		if sb.Labels[LabelCreateID] != createID || sb.Labels[LabelRemote] != name {
			continue
		}
		if err := client.Destroy(ctx, sb.ID); err != nil {
			errs = append(errs, fmt.Errorf("destroying leftover sandbox %s: %w (destroy it by hand)", sb.ID, err))
			continue
		}
		destroyed = append(destroyed, sb.ID)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w; %w", cause, errors.Join(errs...))
	}
	if len(destroyed) > 0 {
		return fmt.Errorf("%w (leftover sandbox %s destroyed)", cause, strings.Join(destroyed, ", "))
	}
	return cause
}

// ErrDestroyedCleanupFailed matches a DestroyRemote error where the provider
// destroyed the sandbox but removing the remote or its files failed.
var ErrDestroyedCleanupFailed = errors.New("local cleanup failed")

// DestroyRemote destroys a sandbox remote: provider destroy, then
// unregister, then remove the sandbox files. When the provider fails the
// remote stays registered, so the user can retry. A failure after the
// provider destroy matches ErrDestroyedCleanupFailed. Stopping the remote's
// sessions and asking for confirmation are the caller's job, and so is
// holding LockRemote(remoteName) from before the sessions are listed.
func DestroyRemote(ctx context.Context, remoteName string) error {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	rc, ok := cfg.Remotes[remoteName]
	if !ok {
		return fmt.Errorf("remote %q not found", remoteName)
	}
	if !IsSandbox(rc) {
		return fmt.Errorf("remote %q is a plain SSH remote, not a provider sandbox; use 'agent-deck remote remove %s' to forget it", remoteName, remoteName)
	}
	client, err := Lookup(cfg, rc.Provider)
	if err != nil {
		return fmt.Errorf("destroying sandbox for remote %q: %w", remoteName, err)
	}
	if err := client.Destroy(ctx, rc.SandboxID); err != nil {
		return fmt.Errorf("destroying sandbox %s for remote %q: %w", rc.SandboxID, remoteName, err)
	}
	// The sandbox is gone from here on: a failure says so, so the caller
	// does not tell the user it is still there.
	if err := unregisterRemote(remoteName); err != nil {
		return fmt.Errorf("sandbox %s was destroyed, but removing remote %q failed (%w): %w", rc.SandboxID, remoteName, ErrDestroyedCleanupFailed, err)
	}
	if err := removeFilesFor(remoteName, rc); err != nil {
		return fmt.Errorf("sandbox %s was destroyed and remote %q removed, but removing its files failed (%w): %w", rc.SandboxID, remoteName, ErrDestroyedCleanupFailed, err)
	}
	return nil
}

// removeFilesFor removes the sandbox dir the remote's ssh_config lives in
// (falling back to the computed dir), so a data-dir change since
// provisioning still cleans up the right place.
func removeFilesFor(remoteName string, rc session.RemoteConfig) error {
	if rc.SSHConfig != "" {
		dir := filepath.Dir(rc.SSHConfig)
		if filepath.Base(dir) == remoteName && filepath.Base(filepath.Dir(dir)) == sandboxesDirName {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("removing sandbox files for %q: %w", remoteName, err)
			}
			return nil
		}
	}
	return RemoveSandboxFiles(remoteName)
}

// IsSandbox reports whether rc was registered from a provider sandbox.
func IsSandbox(rc session.RemoteConfig) bool {
	return rc.Provider != "" && rc.SandboxID != ""
}

// DisplayKind labels a sandbox remote for listings ("sandbox (provider
// docker)"); "" for a plain remote.
func DisplayKind(rc session.RemoteConfig) string {
	if !IsSandbox(rc) {
		return ""
	}
	return "sandbox (provider " + rc.Provider + ")"
}

func registerRemote(name string, rc session.RemoteConfig) error {
	cfg, err := session.ReloadUserConfig()
	if err != nil {
		return fmt.Errorf("loading config to register remote %q: %w", name, err)
	}
	if _, exists := cfg.Remotes[name]; exists {
		return fmt.Errorf("remote %q already exists", name)
	}
	remotes := make(map[string]session.RemoteConfig, len(cfg.Remotes)+1)
	for k, v := range cfg.Remotes {
		remotes[k] = v
	}
	remotes[name] = rc
	cfg.Remotes = remotes
	if err := session.SaveUserConfig(cfg); err != nil {
		return fmt.Errorf("saving config to register remote %q: %w", name, err)
	}
	return nil
}

func unregisterRemote(name string) error {
	cfg, err := session.ReloadUserConfig()
	if err != nil {
		return fmt.Errorf("loading config to unregister remote %q: %w", name, err)
	}
	if _, exists := cfg.Remotes[name]; !exists {
		return nil
	}
	remotes := make(map[string]session.RemoteConfig, len(cfg.Remotes))
	for k, v := range cfg.Remotes {
		if k != name {
			remotes[k] = v
		}
	}
	if len(remotes) == 0 {
		remotes = nil
	}
	cfg.Remotes = remotes
	if err := session.SaveUserConfig(cfg); err != nil {
		return fmt.Errorf("saving config to unregister remote %q: %w", name, err)
	}
	return nil
}

// defaultRemoteName is "<provider>-<6 hex>", sanitized to the remote-name
// rule. The name is chosen before create because the create request
// carries it.
func defaultRemoteName(providerName string) (string, error) {
	suffix, err := randomHex(3)
	if err != nil {
		return "", fmt.Errorf("generating remote name: %w", err)
	}
	prefix := sanitizeNamePart(providerName)
	if prefix == "" {
		prefix = "sandbox"
	}
	return prefix + "-" + suffix, nil
}

// randomHex is n random bytes, hex encoded.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sanitizeNamePart(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
		if len(out) == 40 {
			break
		}
	}
	for len(out) > 0 && (out[0] == '-' || out[0] == '_') {
		out = out[1:]
	}
	return string(out)
}

// defaultWaitSSH probes the sandbox with real ssh (through its ssh_config)
// until a trivial command succeeds, for at most waitSSHBudget.
func defaultWaitSSH(ctx context.Context, name string, rc session.RemoteConfig) error {
	ctx, cancel := context.WithTimeout(ctx, waitSSHBudget)
	defer cancel()
	runner := session.NewSSHRunner(name, rc)
	var lastErr error
	for {
		attempt, cancelAttempt := context.WithTimeout(ctx, waitSSHAttempt)
		_, _, err := runner.DetectPlatform(attempt)
		cancelAttempt()
		if err == nil {
			return nil
		}
		lastErr = err
		timer := time.NewTimer(waitSSHInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("ssh did not answer within %s: %w", waitSSHBudget, lastErr)
		case <-timer.C:
		}
	}
}

// defaultInstall checks that the configured agent-deck answers on the
// sandbox; the CLI replaces it with the remote-add install path.
func defaultInstall(ctx context.Context, name string, rc session.RemoteConfig) error {
	runner := session.NewSSHRunner(name, rc)
	if _, found := runner.CheckBinary(ctx); !found {
		return fmt.Errorf("agent-deck not found on the sandbox at %q", rc.GetAgentDeckPath())
	}
	return nil
}
