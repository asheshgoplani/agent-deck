package git

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// ScriptConsentPolicy is the resolved value of [worktree] run_repo_scripts.
// A repo's .agent-deck/worktree-setup.sh and worktree-destruction.sh run
// with the caller's full environment (SSH_AUTH_SOCK, GITHUB_TOKEN,
// ANTHROPIC_API_KEY, ...), so cloning a repo and creating/removing a
// worktree in it must never silently execute arbitrary shell content —
// that includes the remote worktree-mutation path (web_mutator.go), which
// has no terminal to prompt on at all.
type ScriptConsentPolicy string

const (
	// ScriptConsentPrompt is the secure default: an unrecognized (new or
	// changed) script blocks execution until a human approves it once, from
	// an interactive terminal, or the repo root is pre-approved via
	// `agent-deck worktree trust-scripts`.
	ScriptConsentPrompt ScriptConsentPolicy = "prompt"
	// ScriptConsentAlways restores the pre-gate behavior: every worktree
	// script runs unconditionally. Opt-in only.
	ScriptConsentAlways ScriptConsentPolicy = "always"
	// ScriptConsentNever refuses to run any worktree lifecycle script,
	// trusted or not.
	ScriptConsentNever ScriptConsentPolicy = "never"
)

// ParseScriptConsentPolicy normalizes a config string. Unset, empty, and
// unrecognized values all resolve to ScriptConsentPrompt — the fail-closed
// default — so a config typo can never silently downgrade to "always".
func ParseScriptConsentPolicy(s string) ScriptConsentPolicy {
	switch ScriptConsentPolicy(strings.ToLower(strings.TrimSpace(s))) {
	case ScriptConsentAlways:
		return ScriptConsentAlways
	case ScriptConsentNever:
		return ScriptConsentNever
	default:
		return ScriptConsentPrompt
	}
}

// ScriptConsentConfig is the resolved consent policy the git package
// consults before running any worktree lifecycle script.
//
// internal/git cannot import internal/session — session already imports
// git (see DefaultWorktreeDestructionTimeout above) — so the [worktree]
// run_repo_scripts value and the --allow-repo-scripts flag/env var are
// resolved once at process startup by cmd/agent-deck and pushed down here
// via SetScriptConsentConfig. This single injection point is also what lets
// the destruction gate live inside RemoveWorktree (git.go) without
// threading a new parameter through its ten call sites and the
// vcs.Backend/JJBackend interfaces — RemoveWorktree already funnels every
// caller through RunWorktreeDestructionBeforeRemove, so gating there covers
// all of them, including the remote-triggered path in web_mutator.go.
type ScriptConsentConfig struct {
	Policy ScriptConsentPolicy
	// AllowOverride skips an unresolved consent check instead of blocking
	// (--allow-repo-scripts / AGENT_DECK_ALLOW_REPO_SCRIPTS). It never
	// persists trust: every run under the override re-warns. Intended for
	// CI and other non-interactive automation that cannot answer a prompt.
	AllowOverride bool
}

var (
	scriptConsentMu  sync.RWMutex
	scriptConsentCfg = ScriptConsentConfig{Policy: ScriptConsentPrompt} // fail closed before SetScriptConsentConfig ever runs
)

// SetScriptConsentConfig installs the process-wide consent policy. Call
// once at startup after config + flags are resolved. Safe to call again in
// tests to reset state.
func SetScriptConsentConfig(cfg ScriptConsentConfig) {
	if cfg.Policy == "" {
		cfg.Policy = ScriptConsentPrompt
	}
	scriptConsentMu.Lock()
	scriptConsentCfg = cfg
	scriptConsentMu.Unlock()
}

func getScriptConsentConfig() ScriptConsentConfig {
	scriptConsentMu.RLock()
	defer scriptConsentMu.RUnlock()
	return scriptConsentCfg
}

// hashScriptFile returns the hex-encoded SHA-256 of a script's content at
// the moment of discovery — read once, alongside the os.Stat that captures
// scriptMode (#861), so the consent decision and the dispatch decision are
// both bound to a single atomic snapshot of the file rather than being
// vulnerable to a TOCTOU swap between the check and the run.
func hashScriptFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// scriptConsentEntry is one persisted trust decision.
type scriptConsentEntry struct {
	RepoRoot  string    `json:"repo_root"`
	Kind      string    `json:"kind"`
	SHA256    string    `json:"sha256"`
	TrustedAt time.Time `json:"trusted_at"`
}

type scriptConsentStore struct {
	Version int                           `json:"version"`
	Entries map[string]scriptConsentEntry `json:"entries"`
}

// scriptConsentStorePath is the on-disk trust database, keyed by repo root
// + script kind + content hash. It intentionally lives in agent-deck's data
// dir (not the repo, which is untrusted, and not a per-profile dir — trust
// in a specific script's exact bytes is a machine-level security decision,
// not a per-profile preference).
func scriptConsentStorePath() (string, error) {
	dir, err := agentpaths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "worktree-script-consent.json"), nil
}

// scriptConsentFileMu serializes read-modify-write of the consent store
// within this process. Cross-process races (two agent-deck processes
// approving the same script simultaneously) can still interleave, but the
// worst case is a redundant prompt or a duplicate write of the same
// content hash — never a bypass.
var scriptConsentFileMu sync.Mutex

func loadScriptConsentStore() (*scriptConsentStore, error) {
	path, err := scriptConsentStorePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &scriptConsentStore{Version: 1, Entries: map[string]scriptConsentEntry{}}, nil
		}
		return nil, err
	}
	var store scriptConsentStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse worktree script consent store %s: %w", path, err)
	}
	if store.Entries == nil {
		store.Entries = map[string]scriptConsentEntry{}
	}
	return &store, nil
}

func saveScriptConsentStore(store *scriptConsentStore) error {
	path, err := scriptConsentStorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func scriptConsentKey(repoRootAbs, kind string) string {
	return kind + "\x00" + repoRootAbs
}

// lookupScriptConsent reports whether repoRootAbs+kind is trusted for the
// exact given content hash. A hash mismatch against a stored entry (script
// content changed since it was trusted) reports untrusted, forcing
// re-consent rather than silently running the new content under the old
// approval.
func lookupScriptConsent(repoRootAbs, kind, hash string) (bool, error) {
	scriptConsentFileMu.Lock()
	defer scriptConsentFileMu.Unlock()
	store, err := loadScriptConsentStore()
	if err != nil {
		return false, err
	}
	entry, ok := store.Entries[scriptConsentKey(repoRootAbs, kind)]
	if !ok {
		return false, nil
	}
	return entry.SHA256 == hash, nil
}

func recordScriptConsent(repoRootAbs, kind, hash string) error {
	scriptConsentFileMu.Lock()
	defer scriptConsentFileMu.Unlock()
	store, err := loadScriptConsentStore()
	if err != nil {
		return err
	}
	store.Entries[scriptConsentKey(repoRootAbs, kind)] = scriptConsentEntry{
		RepoRoot:  repoRootAbs,
		Kind:      kind,
		SHA256:    hash,
		TrustedAt: time.Now().UTC(),
	}
	return saveScriptConsentStore(store)
}

// RevokeScriptConsent removes any stored trust decision for repoDir+kind.
// Used by `agent-deck worktree trust-scripts --revoke`.
func RevokeScriptConsent(repoDir, kind string) error {
	repoRootAbs, err := filepath.Abs(repoDir)
	if err != nil {
		return fmt.Errorf("resolve repo root: %w", err)
	}
	scriptConsentFileMu.Lock()
	defer scriptConsentFileMu.Unlock()
	store, err := loadScriptConsentStore()
	if err != nil {
		return err
	}
	delete(store.Entries, scriptConsentKey(repoRootAbs, kind))
	return saveScriptConsentStore(store)
}

// TrustScript pre-approves the script at scriptPath for repoDir+kind,
// recording its current content hash. Used by the CLI trust command so
// users (and CI, ahead of time, over a real approval channel) can grant
// consent without needing an interactive prompt at worktree-creation time.
func TrustScript(repoDir, kind, scriptPath string) (sha256Hex string, err error) {
	repoRootAbs, err := filepath.Abs(repoDir)
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}
	hash, err := hashScriptFile(scriptPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", scriptPath, err)
	}
	if err := recordScriptConsent(repoRootAbs, kind, hash); err != nil {
		return "", err
	}
	return hash, nil
}

// isInteractiveConsoleStdio reports whether this process can hold a
// question-and-answer conversation on the controlling terminal right now.
func isInteractiveConsoleStdio() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// promptScriptConsent asks the user, on stderr/stdin, whether to trust a
// worktree lifecycle script. Returns approved=false, interactive=false when
// no terminal is available to ask on at all (the caller must then fail
// closed with a remediation message rather than hang).
func promptScriptConsent(kind, repoRootAbs, scriptPath string, out io.Writer) (approved bool, interactive bool) {
	if !isInteractiveConsoleStdio() {
		return false, false
	}
	verb := "creating"
	if kind == "destruction" {
		verb = "removing"
	}
	fmt.Fprintf(out, "\nagent-deck: repo %q defines a worktree %s script:\n  %s\n", repoRootAbs, kind, scriptPath)
	fmt.Fprintf(out, "It will run with your full environment (SSH agent, GITHUB_TOKEN, ANTHROPIC_API_KEY, and\n")
	fmt.Fprintf(out, "anything else in your shell) every time a worktree is %s in this repo.\n", verb)
	fmt.Fprint(out, "Run it? [y/N] ")

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", true
}

// checkScriptConsent is the single gate every worktree lifecycle script
// dispatch point must pass through before RunWorktreeSetupScript /
// RunWorktreeDestructionScript is called. kind is "setup" or "destruction".
func checkScriptConsent(kind, repoDir, scriptPath, hash string, out io.Writer) error {
	cfg := getScriptConsentConfig()

	switch cfg.Policy {
	case ScriptConsentAlways:
		return nil
	case ScriptConsentNever:
		return fmt.Errorf("worktree %s script blocked by [worktree] run_repo_scripts = \"never\": %s", kind, scriptPath)
	}

	repoRootAbs, err := filepath.Abs(repoDir)
	if err != nil {
		return fmt.Errorf("worktree %s script consent: resolve repo root: %w", kind, err)
	}

	trusted, lookupErr := lookupScriptConsent(repoRootAbs, kind, hash)
	if lookupErr != nil {
		fmt.Fprintf(out, "agent-deck: warning: could not read worktree script consent store: %v\n", lookupErr)
	}
	if trusted {
		return nil
	}

	if cfg.AllowOverride {
		fmt.Fprintf(out, "agent-deck: warning: running unapproved worktree %s script under --allow-repo-scripts / AGENT_DECK_ALLOW_REPO_SCRIPTS (%s); not saved as trusted\n", kind, scriptPath)
		return nil
	}

	approved, interactive := promptScriptConsent(kind, repoRootAbs, scriptPath, out)
	if approved {
		if err := recordScriptConsent(repoRootAbs, kind, hash); err != nil {
			fmt.Fprintf(out, "agent-deck: warning: could not persist worktree script consent: %v\n", err)
		}
		return nil
	}
	if interactive {
		return fmt.Errorf("worktree %s script declined: %s", kind, scriptPath)
	}

	return fmt.Errorf(
		"worktree %s script needs consent before it can run (%s in %s): "+
			"no interactive terminal to ask on. Approve it once with "+
			"`agent-deck worktree trust-scripts %s`, set [worktree] run_repo_scripts = \"always\" "+
			"in config.toml, or pass --allow-repo-scripts / AGENT_DECK_ALLOW_REPO_SCRIPTS=1 for a one-shot bypass",
		kind, scriptPath, repoRootAbs, repoRootAbs)
}
