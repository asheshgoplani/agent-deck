package git

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

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
	// an interactive terminal or the TUI dialog, or the repo is pre-approved
	// via `agent-deck worktree trust-hooks`.
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

// ErrWorktreeScriptNotApproved marks a lifecycle script that was skipped
// (not run) by the consent gate: unapproved, changed since approval,
// declined, or blocked by run_repo_scripts = "never". Callers use
// errors.Is to report "skipped" instead of "failed".
var ErrWorktreeScriptNotApproved = errors.New("worktree script not approved")

// notApprovedError carries a user-facing skip message and matches
// ErrWorktreeScriptNotApproved without prefixing its text.
type notApprovedError struct{ msg string }

func (e *notApprovedError) Error() string        { return e.msg }
func (e *notApprovedError) Is(target error) bool { return target == ErrWorktreeScriptNotApproved }

func notApproved(format string, args ...any) error {
	return &notApprovedError{msg: fmt.Sprintf(format, args...)}
}

// ScriptConsentConfig is the resolved consent policy the git package
// consults before running any worktree lifecycle script.
//
// internal/git cannot import internal/session — session already imports
// git (see DefaultWorktreeDestructionTimeout above) — so the [worktree]
// run_repo_scripts value and the --run-hooks flag/env var are resolved once
// at process startup by cmd/agent-deck and pushed down here via
// SetScriptConsentConfig. This single injection point is also what lets
// the destruction gate live inside RemoveWorktree (git.go) without
// threading a new parameter through its ten call sites and the
// vcs.Backend/JJBackend interfaces — RemoveWorktree already funnels every
// caller through RunWorktreeDestructionBeforeRemove, so gating there covers
// all of them, including the remote-triggered path in web_mutator.go.
type ScriptConsentConfig struct {
	Policy ScriptConsentPolicy
	// AllowOverride runs an unapproved script instead of skipping it
	// (--run-hooks / --allow-repo-scripts / AGENT_DECK_ALLOW_REPO_SCRIPTS).
	// The script's identity (sha256, interpreter) is printed before it
	// runs. Intended for CI and other non-interactive automation.
	AllowOverride bool
	// PersistOverride (--trust, only meaningful with AllowOverride) also
	// records the identity that ran as trusted, so later runs need no flag.
	PersistOverride bool
	// AllowInteractivePrompt gates whether checkScriptConsent may attempt a
	// synchronous stdin/stdout prompt at all, independent of whether
	// isInteractiveConsoleStdio reports a TTY. It must be false whenever a
	// blocking read on os.Stdin would be unsafe or meaningless even though
	// stdio happens to be a terminal:
	//   - the bubbletea TUI owns the terminal in raw mode (stdin/stdout are
	//     still term.IsTerminal==true in raw mode, so the TTY check alone
	//     cannot tell the two apart) — a blocking bufio read here races the
	//     TUI's own input reader and can steal keystrokes or never return
	//     (Enter yields '\r' in raw mode, not the '\n' ReadString waits for).
	//     The TUI asks through SetScriptConsentPrompter instead.
	//   - the call was triggered remotely (agent-deck web / the mutation
	//     HTTP handler) — even if the hosting process's own stdio is a
	//     plain, non-raw terminal, no operator is watching it for a prompt
	//     in response to someone else's HTTP request.
	// When false, checkScriptConsent resolves through the prompter (if one
	// is installed and not suppressed) or the non-interactive, fail-closed
	// skip. Direct CLI subcommands that return before the TUI/web server
	// ever starts are unaffected and keep prompting on the terminal.
	AllowInteractivePrompt bool
}

var (
	scriptConsentMu sync.RWMutex
	// Fail closed before SetScriptConsentConfig ever runs: AllowInteractivePrompt
	// is false by zero-value already, but spelled out here for clarity.
	scriptConsentCfg = ScriptConsentConfig{Policy: ScriptConsentPrompt, AllowInteractivePrompt: false}
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

// ScriptConsentDecision is the answer to a first-use / changed-script
// question, from the terminal prompt or the TUI dialog.
type ScriptConsentDecision int

const (
	// ScriptConsentSkip does not run the script and records nothing.
	ScriptConsentSkip ScriptConsentDecision = iota
	// ScriptConsentRunOnce runs this invocation only; nothing is recorded.
	ScriptConsentRunOnce
	// ScriptConsentTrust runs it and records this exact identity as trusted.
	ScriptConsentTrust
)

// ScriptConsentPrompter asks a human about an unapproved script and blocks
// until they answer. The TUI installs one that shows a dialog. It is always
// called from a background goroutine (worktree work runs in tea.Cmds),
// never the bubbletea update loop.
type ScriptConsentPrompter func(id WorktreeScriptIdentity) ScriptConsentDecision

var (
	scriptConsentPrompter   atomic.Pointer[ScriptConsentPrompter]
	scriptConsentSuppressed atomic.Int32
)

// SetScriptConsentPrompter installs (or, with nil, removes) the process-wide
// prompter used when no terminal prompt is allowed.
func SetScriptConsentPrompter(p ScriptConsentPrompter) {
	if p == nil {
		scriptConsentPrompter.Store(nil)
		return
	}
	scriptConsentPrompter.Store(&p)
}

// SuppressScriptConsentPrompter disables the prompter until the returned
// func is called. The web mutation handlers run inside the TUI process; a
// request from a browser must fail closed like any other non-interactive
// caller rather than pop a dialog nobody asked for. Process-wide, so a TUI
// operation racing a web request also fails closed (skips) — the safe side.
func SuppressScriptConsentPrompter() (restore func()) {
	scriptConsentSuppressed.Add(1)
	var once sync.Once
	return func() { once.Do(func() { scriptConsentSuppressed.Add(-1) }) }
}

func activeScriptConsentPrompter() ScriptConsentPrompter {
	if scriptConsentSuppressed.Load() > 0 {
		return nil
	}
	if p := scriptConsentPrompter.Load(); p != nil {
		return *p
	}
	return nil
}

// maxScriptHashBytes bounds how much of a worktree lifecycle script
// readScriptFile will read. Generous for a shell/setup script; refuses to
// hash (and therefore refuses consent for, and therefore refuses to run
// under the "prompt" policy) anything larger, rather than either hanging
// on an unbounded read or silently hashing a truncated prefix.
const maxScriptHashBytes = 10 << 20 // 10 MiB

// ScriptPreviewLines is how many leading lines of a script the terminal
// prompt, the TUI dialog and `worktree trust-hooks` show before asking.
const ScriptPreviewLines = 20

// Interpreter decisions recorded in the trust store. The dispatch in
// buildSetupCmd depends only on the executable bit, so the bit is part of
// the approved identity: the same bytes run under `sh -e` and under their
// own #! line are different programs.
const (
	ScriptInterpreterExec  = "exec"  // executable bit set: kernel honors the #! line
	ScriptInterpreterShell = "sh -e" // not executable: run via `sh -e <path>`
)

// ScriptInterpreterForMode reports how buildSetupCmd will dispatch a script
// with this mode.
func ScriptInterpreterForMode(mode os.FileMode) string {
	if mode&0o111 != 0 {
		return ScriptInterpreterExec
	}
	return ScriptInterpreterShell
}

// WorktreeScriptIdentity is everything a trust decision is bound to: which
// repo, which hook, the exact bytes (sha256), where those bytes really live
// (symlinks resolved) and how they will be interpreted. Any change to one of
// these re-asks.
type WorktreeScriptIdentity struct {
	Kind         string      // "setup" or "destruction"
	RepoRoot     string      // canonical repo root (absolute, symlinks resolved)
	ScriptPath   string      // .agent-deck/worktree-<kind>.sh as discovered
	ResolvedPath string      // ScriptPath with symlinks resolved
	Mode         os.FileMode // mode of the hashed inode; drives dispatch
	Interpreter  string      // ScriptInterpreterExec or ScriptInterpreterShell
	SHA256       string      // hex sha256 of the script bytes
	// Preview holds up to ScriptPreviewLines leading lines, with control and
	// bidi characters escaped so the script cannot restyle or hide text in
	// the terminal that displays it.
	Preview    []string
	TotalLines int
	// scriptBytes are the same bytes used for SHA256 and the preview. The
	// execution gate runs a private copy so a path replacement after consent
	// cannot change what was approved.
	scriptBytes []byte
}

// CommandLine renders the effective command for display.
func (id WorktreeScriptIdentity) CommandLine() string {
	if id.Interpreter == ScriptInterpreterShell {
		return "sh -e " + id.ScriptPath
	}
	if len(id.Preview) > 0 && strings.HasPrefix(id.Preview[0], "#!") {
		return id.ScriptPath + "  (runs via its " + id.Preview[0] + " line)"
	}
	return id.ScriptPath + "  (executed directly)"
}

// ShortHash is the first 12 hex digits of SHA256, for one-line notices.
func (id WorktreeScriptIdentity) ShortHash() string {
	if len(id.SHA256) > 12 {
		return id.SHA256[:12]
	}
	return id.SHA256
}

func worktreeScriptPath(repoDir, kind string) string {
	return filepath.Join(repoDir, ".agent-deck", "worktree-"+kind+".sh")
}

// canonicalRepoRoot makes the trust key independent of how the repo was
// reached (relative path, /tmp vs /private/tmp, a symlinked checkout).
func canonicalRepoRoot(repoDir string) (string, error) {
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// InspectWorktreeScript computes the identity of repoDir's worktree
// lifecycle script of the given kind. It returns (nil, nil) when the repo
// has no such script.
func InspectWorktreeScript(repoDir, kind string) (*WorktreeScriptIdentity, error) {
	scriptPath := worktreeScriptPath(repoDir, kind)
	info, err := os.Stat(scriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return inspectWorktreeScript(kind, repoDir, scriptPath, info.Mode())
}

// inspectWorktreeScript hashes the script once and derives the rest of its
// identity. The mode used for the interpreter decision comes from fstat on
// the very descriptor that was hashed, and the gate dispatches with that
// mode and a private copy of those bytes.
func inspectWorktreeScript(kind, repoDir, scriptPath string, discoveredMode os.FileMode) (*WorktreeScriptIdentity, error) {
	if !discoveredMode.IsRegular() {
		return nil, fmt.Errorf("worktree %s script consent: %s is not a regular file (refusing to hash a symlink/FIFO/device target, which could hang indefinitely); point .agent-deck/worktree-%s.sh at a real file", kind, scriptPath, kind)
	}
	repoRoot, err := canonicalRepoRoot(repoDir)
	if err != nil {
		return nil, fmt.Errorf("worktree %s script consent: resolve repo root: %w", kind, err)
	}
	resolved, err := filepath.EvalSymlinks(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("worktree %s script: resolve %s: %w", kind, scriptPath, err)
	}
	data, mode, err := readScriptFile(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("worktree %s script: reading %s for consent check: %w", kind, scriptPath, err)
	}
	sum := sha256.Sum256(data)
	preview, total := scriptPreview(data, ScriptPreviewLines)
	return &WorktreeScriptIdentity{
		Kind:         kind,
		RepoRoot:     repoRoot,
		ScriptPath:   scriptPath,
		ResolvedPath: resolved,
		Mode:         mode,
		Interpreter:  ScriptInterpreterForMode(mode),
		SHA256:       hex.EncodeToString(sum[:]),
		Preview:      preview,
		TotalLines:   total,
		scriptBytes:  data,
	}, nil
}

// readScriptFile reads a lifecycle script's bytes and the mode of the inode
// it read. The file is opened via openScriptFileForHashing, not a plain
// os.Open: a committed symlink pointing at a FIFO (or, on some platforms, a
// device that blocks at open()) would otherwise hang the open() call itself
// before a single byte is read. openScriptFileForHashing opens non-blocking
// and checks the *resulting descriptor's* mode via fstat, which makes every
// caller (including TrustScript, which has no other guard in front of it)
// safe against a non-regular script file.
func readScriptFile(path string) ([]byte, os.FileMode, error) {
	f, err := openScriptFileForHashing(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxScriptHashBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > maxScriptHashBytes {
		return nil, 0, fmt.Errorf("worktree script %s exceeds the %d byte consent-hash limit", path, maxScriptHashBytes)
	}
	return data, info.Mode(), nil
}

// hashScriptFile returns the hex-encoded SHA-256 of a script's content.
func hashScriptFile(path string) (string, error) {
	data, _, err := readScriptFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

const maxPreviewLineRunes = 160

// scriptPreview returns up to n leading lines of data, made safe to print,
// and the script's total line count.
func scriptPreview(data []byte, n int) ([]string, int) {
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil, 0
	}
	lines := strings.Split(text, "\n")
	preview := make([]string, 0, min(n, len(lines)))
	for _, l := range lines[:min(n, len(lines))] {
		preview = append(preview, SanitizeScriptLine(l))
	}
	return preview, len(lines)
}

// SanitizeScriptLine escapes everything in a script line that could change
// how a terminal renders the surrounding text: C0/C1 controls (ANSI escape
// sequences, carriage returns that overwrite a line), DEL, invalid UTF-8 and
// Unicode bidi overrides ("trojan source"). Tabs become spaces. Long lines
// are truncated.
func SanitizeScriptLine(s string) string {
	var b strings.Builder
	runes := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.Is(unicode.Bidi_Control, r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
		i += size
		runes++
		if runes >= maxPreviewLineRunes && i < len(s) {
			b.WriteString("…")
			break
		}
	}
	return b.String()
}

// scriptConsentPolicyShortCircuit resolves the "always"/"never" policy
// tiers without ever touching the script's content — no os.Open, no read,
// nothing that could block. handled=true means the policy alone decided
// the outcome (err is nil for "always", non-nil for "never"); handled=false
// means the caller is under "prompt" and must go on to compute the script
// identity and consult the trust store. This exists specifically so that
// run_repo_scripts = "never" rejects instantly — including for a script
// file that would otherwise hang a content read (symlink to /dev/zero, a
// FIFO with no writer) — instead of hanging during hashing before the
// policy ever gets consulted.
func scriptConsentPolicyShortCircuit(kind, scriptPath string) (handled bool, err error) {
	switch getScriptConsentConfig().Policy {
	case ScriptConsentAlways:
		return true, nil
	case ScriptConsentNever:
		return true, notApproved("worktree %s hook not run: blocked by [worktree] run_repo_scripts = \"never\": %s", kind, scriptPath)
	default:
		return false, nil
	}
}

// scriptConsentEntry is one persisted trust decision. Entries written by
// agent-deck <= 1.16.21 carry only SHA256; they no longer match (the
// interpreter and resolved path were never approved), so those scripts are
// asked about once more after upgrading.
type scriptConsentEntry struct {
	RepoRoot     string    `json:"repo_root"`
	Kind         string    `json:"kind"`
	SHA256       string    `json:"sha256"`
	ResolvedPath string    `json:"resolved_path,omitempty"`
	Interpreter  string    `json:"interpreter,omitempty"`
	TrustedAt    time.Time `json:"trusted_at"`
}

func (e scriptConsentEntry) matches(id *WorktreeScriptIdentity) bool {
	return e.SHA256 == id.SHA256 && e.ResolvedPath == id.ResolvedPath && e.Interpreter == id.Interpreter
}

const scriptConsentStoreVersion = 2

type scriptConsentStore struct {
	Version int                           `json:"version"`
	Entries map[string]scriptConsentEntry `json:"entries"`
}

// scriptConsentStorePath is the on-disk trust database, keyed by canonical
// repo root + script kind. It intentionally lives in agent-deck's data dir
// (not the repo, which is untrusted, and not a per-profile dir — trust in a
// specific script's exact bytes is a machine-level security decision, not a
// per-profile preference).
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
// identity — never a bypass.
var scriptConsentFileMu sync.Mutex

func loadScriptConsentStore() (*scriptConsentStore, error) {
	path, err := scriptConsentStorePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &scriptConsentStore{Version: scriptConsentStoreVersion, Entries: map[string]scriptConsentEntry{}}, nil
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
	store.Version = scriptConsentStoreVersion
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

func scriptConsentKey(repoRoot, kind string) string {
	return kind + "\x00" + repoRoot
}

// ScriptTrustStatus is the trust store's view of a script identity.
type ScriptTrustStatus int

const (
	ScriptUntrusted ScriptTrustStatus = iota // no record for this repo + hook
	ScriptChanged                            // a record exists for a different identity
	ScriptTrusted                            // this exact identity is trusted
)

// WorktreeScriptTrustStatus reports whether id is trusted. A record for the
// same repo + hook with a different hash, resolved path or interpreter
// reports ScriptChanged, forcing re-consent rather than silently running
// the new identity under the old approval.
func WorktreeScriptTrustStatus(id *WorktreeScriptIdentity) (ScriptTrustStatus, error) {
	scriptConsentFileMu.Lock()
	defer scriptConsentFileMu.Unlock()
	store, err := loadScriptConsentStore()
	if err != nil {
		return ScriptUntrusted, err
	}
	entry, ok := store.Entries[scriptConsentKey(id.RepoRoot, id.Kind)]
	switch {
	case !ok:
		return ScriptUntrusted, nil
	case entry.matches(id):
		return ScriptTrusted, nil
	default:
		return ScriptChanged, nil
	}
}

// TrustWorktreeScript records id as trusted, replacing any earlier record
// for the same repo + hook.
func TrustWorktreeScript(id *WorktreeScriptIdentity) error {
	scriptConsentFileMu.Lock()
	defer scriptConsentFileMu.Unlock()
	store, err := loadScriptConsentStore()
	if err != nil {
		return err
	}
	store.Entries[scriptConsentKey(id.RepoRoot, id.Kind)] = scriptConsentEntry{
		RepoRoot:     id.RepoRoot,
		Kind:         id.Kind,
		SHA256:       id.SHA256,
		ResolvedPath: id.ResolvedPath,
		Interpreter:  id.Interpreter,
		TrustedAt:    time.Now().UTC(),
	}
	return saveScriptConsentStore(store)
}

// RevokeScriptConsent removes any stored trust decision for repoDir+kind.
// Used by `agent-deck worktree trust-hooks --revoke`. Deliberately takes
// no dependency on the script still existing on disk at repoDir — a stale
// consent-store entry for a script that was since deleted must still be
// removable, so the caller (the CLI) should invoke this unconditionally
// under --revoke rather than gating it on FindWorktree*Script finding a
// file. Both the canonical key and the pre-canonicalization (plain
// absolute path) key are removed. existed reports whether an entry was
// actually present to remove.
func RevokeScriptConsent(repoDir, kind string) (existed bool, err error) {
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		return false, fmt.Errorf("resolve repo root: %w", err)
	}
	canonical, err := canonicalRepoRoot(repoDir)
	if err != nil {
		return false, fmt.Errorf("resolve repo root: %w", err)
	}
	scriptConsentFileMu.Lock()
	defer scriptConsentFileMu.Unlock()
	store, loadErr := loadScriptConsentStore()
	if loadErr != nil {
		return false, loadErr
	}
	for _, root := range []string{canonical, abs} {
		key := scriptConsentKey(root, kind)
		if _, ok := store.Entries[key]; ok {
			existed = true
			delete(store.Entries, key)
		}
	}
	if !existed {
		return false, nil
	}
	return true, saveScriptConsentStore(store)
}

// TrustScript pre-approves the script at scriptPath for repoDir+kind,
// recording its current identity. Returns the sha256.
func TrustScript(repoDir, kind, scriptPath string) (sha256Hex string, err error) {
	info, err := os.Stat(scriptPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", scriptPath, err)
	}
	id, err := inspectWorktreeScript(kind, repoDir, scriptPath, info.Mode())
	if err != nil {
		return "", err
	}
	if err := TrustWorktreeScript(id); err != nil {
		return "", err
	}
	return id.SHA256, nil
}

// isInteractiveConsoleStdio reports whether this process can hold a
// question-and-answer conversation on the controlling terminal right now.
// A variable so tests can simulate a terminal.
var isInteractiveConsoleStdio = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// consentStdin is where the terminal prompt reads its answer. A variable so
// tests can feed answers.
var consentStdin io.Reader = os.Stdin

// DescribeWorktreeScript renders the identity block shown before asking:
// repo, hook, effective command, working directory, hash and the first
// lines of the script.
func DescribeWorktreeScript(id WorktreeScriptIdentity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  repo:        %s\n", id.RepoRoot)
	fmt.Fprintf(&b, "  hook:        %s\n", id.ScriptPath)
	if id.ResolvedPath != id.ScriptPath {
		fmt.Fprintf(&b, "  symlink to:  %s\n", id.ResolvedPath)
	}
	fmt.Fprintf(&b, "  command:     %s\n", id.CommandLine())
	fmt.Fprintf(&b, "  runs in:     the worktree directory, with your full environment\n")
	fmt.Fprintf(&b, "  sha256:      %s\n", id.SHA256)
	fmt.Fprintf(&b, "  --- first %d of %d lines ---\n", len(id.Preview), id.TotalLines)
	for _, l := range id.Preview {
		fmt.Fprintf(&b, "  | %s\n", l)
	}
	if id.TotalLines > len(id.Preview) {
		fmt.Fprintf(&b, "  | … %d more lines in %s\n", id.TotalLines-len(id.Preview), id.ResolvedPath)
	}
	return b.String()
}

// promptScriptConsent asks the user on the terminal about an unapproved
// script. interactive=false means no terminal could be asked (or allowPrompt
// is false — see ScriptConsentConfig.AllowInteractivePrompt); the caller
// must then fail closed rather than hang or prompt on the wrong terminal.
func promptScriptConsent(id WorktreeScriptIdentity, status ScriptTrustStatus, out io.Writer, allowPrompt bool) (decision ScriptConsentDecision, interactive bool) {
	if !allowPrompt || !isInteractiveConsoleStdio() {
		return ScriptConsentSkip, false
	}
	what := "is not approved yet"
	if status == ScriptChanged {
		what = "changed since you approved it"
	}
	fmt.Fprintf(out, "\nagent-deck: this repository's worktree %s hook %s:\n%s", id.Kind, what, DescribeWorktreeScript(id))
	fmt.Fprint(out, "Run it? [o]nce, [a]lways trust this version, [N]o/skip: ")

	line, _ := bufio.NewReader(consentStdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "o", "once":
		return ScriptConsentRunOnce, true
	case "a", "always", "y", "yes":
		return ScriptConsentTrust, true
	default:
		return ScriptConsentSkip, true
	}
}

// TrustHooksCommand is the approval command named in skip notices.
func TrustHooksCommand(repoRoot, kind string) string {
	return fmt.Sprintf("agent-deck worktree trust-hooks %s --hook %s", repoRoot, kind)
}

// checkScriptConsent is the single gate every worktree lifecycle script
// dispatch point must pass through before the script runs. A nil return
// means run it; an error wrapping ErrWorktreeScriptNotApproved means skip.
func checkScriptConsent(id *WorktreeScriptIdentity, out io.Writer) error {
	cfg := getScriptConsentConfig()

	switch cfg.Policy {
	case ScriptConsentAlways:
		return nil
	case ScriptConsentNever:
		return notApproved("worktree %s hook not run: blocked by [worktree] run_repo_scripts = \"never\": %s", id.Kind, id.ScriptPath)
	}

	status, lookupErr := WorktreeScriptTrustStatus(id)
	if lookupErr != nil {
		fmt.Fprintf(out, "agent-deck: warning: could not read worktree script consent store: %v\n", lookupErr)
	}
	if status == ScriptTrusted {
		return nil
	}

	if cfg.AllowOverride {
		fmt.Fprintf(out, "agent-deck: running unapproved worktree %s hook under --run-hooks: %s (sha256:%s, %s)\n", id.Kind, id.ScriptPath, id.SHA256, id.Interpreter)
		if cfg.PersistOverride {
			if err := TrustWorktreeScript(id); err != nil {
				fmt.Fprintf(out, "agent-deck: warning: could not persist worktree script consent: %v\n", err)
			} else {
				fmt.Fprintf(out, "agent-deck: trusted this version (--trust)\n")
			}
		}
		return nil
	}

	var decision ScriptConsentDecision
	var asked bool
	if p := activeScriptConsentPrompter(); p != nil {
		decision, asked = p(*id), true
	} else {
		decision, asked = promptScriptConsent(*id, status, out, cfg.AllowInteractivePrompt)
	}
	switch decision {
	case ScriptConsentTrust:
		if err := TrustWorktreeScript(id); err != nil {
			fmt.Fprintf(out, "agent-deck: warning: could not persist worktree script consent: %v\n", err)
		}
		return nil
	case ScriptConsentRunOnce:
		return nil
	}
	if asked {
		return notApproved("worktree %s hook skipped (not approved): %s", id.Kind, id.ScriptPath)
	}

	why := "is not approved yet"
	if status == ScriptChanged {
		why = "changed since it was approved"
	}
	return notApproved("worktree %s hook not run: %s %s (sha256:%s, %s); review and approve with `%s`, or run it once with --run-hooks",
		id.Kind, id.ScriptPath, why, id.ShortHash(), id.Interpreter, TrustHooksCommand(id.RepoRoot, id.Kind))
}
