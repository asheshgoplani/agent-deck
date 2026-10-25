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
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Labels agent-deck puts on every sandbox it creates.
const (
	LabelRemote  = "agent-deck/remote"
	LabelProfile = "agent-deck/profile"
)

const (
	// rollbackTimeout bounds the cleanup after a failed provisioning; it
	// runs on a fresh context so a cancelled caller still cleans up.
	rollbackTimeout = 2 * time.Minute
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
	if _, exists := cfg.Remotes[name]; exists {
		return nil, fmt.Errorf("remote %q already exists (use 'agent-deck remote remove %s' or pick another --name)", name, name)
	}

	labels := map[string]string{LabelRemote: name}
	if opts.Profile != "" {
		labels[LabelProfile] = opts.Profile
	}
	progress(fmt.Sprintf("Creating sandbox %q with provider %q...", name, opts.Provider))
	sb, err := client.Create(ctx, name, labels)
	if err != nil {
		if sb != nil {
			return nil, rollback(client, name, sb.ID, false, err)
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

// rollback undoes a failed provisioning and joins its failures to cause.
func rollback(client *Client, name, id string, registered bool, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
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

// DestroyRemote destroys a sandbox remote: provider destroy, then
// unregister, then remove the sandbox files. When the provider fails the
// remote stays registered, so the user can retry. Stopping the remote's
// sessions and asking for confirmation are the caller's job.
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
	if err := unregisterRemote(remoteName); err != nil {
		return err
	}
	return removeFilesFor(remoteName, rc)
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
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating remote name: %w", err)
	}
	prefix := sanitizeNamePart(providerName)
	if prefix == "" {
		prefix = "sandbox"
	}
	return prefix + "-" + hex.EncodeToString(b[:]), nil
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
