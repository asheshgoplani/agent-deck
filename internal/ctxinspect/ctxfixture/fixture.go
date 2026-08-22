// Package ctxfixture loads recorded harness sessions used to check the context
// inspector against known inputs.
//
// A fixture is a sanitized *head* of a real transcript or rollout — the records
// up to and including the first model turn, which is all the inspector reads —
// paired with the filesystem tree the adapter reconstructs from, and a manifest
// stating what the case is supposed to prove. Two independent test suites
// consume them:
//
//   - a corpus reconciliation test, which asserts the arithmetic closes for
//     every case (Σattributed + unaccounted == anchor, exactly) and that the
//     estimator has not drifted outside its declared band;
//   - a golden test over `agent-deck session context --json`, which asserts the
//     whole document byte-for-byte so that a harness format change fails CI
//     instead of quietly emptying a category.
//
// The cases are embedded rather than found by path so a test can materialize
// one from any working directory, and every case is copied into a caller-owned
// temporary directory before use: nothing here ever reads or writes a real home
// directory, and no fixture contains a real path, a real credential or real
// instruction text.
package ctxfixture

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

//go:embed all:cases
var casesFS embed.FS

// casesRoot is the embedded directory holding one subdirectory per case.
const casesRoot = "cases"

// GoldenFileName is the file, inside a case directory, holding that case's
// recorded `session context --json` report document.
//
// It is one exported constant used by both [Case.Golden] and [Case.WriteGolden]
// on purpose. When the two paths each spelled the name for themselves they
// drifted apart, and the failure was silent in the worst possible way: the read
// side could never find a golden, so the whole byte-for-byte suite failed on
// every case, and regenerating it wrote the *other* name — so the corpus
// asserted nothing and could not be repaired by the documented procedure.
// TestEveryCaseHasAReadableGolden nails the constant to what is on disk.
const GoldenFileName = "expected-report.json"

// FixedNow is the timestamp every fixture-driven report is stamped with.
//
// A report carries the moment it was produced, so a golden document can only be
// byte-stable if that moment is pinned. It is a plain constant rather than a
// zero time so the field is exercised as it is in production.
var FixedNow = time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)

// RootPlaceholder is the token that stands in for the materialized fixture
// directory in a golden document. Absolute paths are part of what the inspector
// reports — they are the lever the user acts on — so they must appear in the
// golden, and they can only appear reproducibly if the temporary root is
// redacted.
const RootPlaceholder = "<FIXTURE_ROOT>"

// RootKeyPlaceholder stands in for Claude Code's path-derived project key.
// Claude replaces path separators with dashes when naming the auto-memory
// directory, so replacing only the ordinary absolute path leaves the random
// fixture parent embedded in golden documents.
const RootKeyPlaceholder = "<FIXTURE_ROOT_KEY>"

// Expect states what a case is supposed to prove, independently of what the
// code currently produces.
//
// It is the half of the fixture that is *not* a snapshot: a golden document
// records whatever the inspector did, while these assertions were derived by
// hand from the recorded numbers, so a change that alters both the code and the
// golden together still has to satisfy them.
type Expect struct {
	// Anchored states whether the case's first turn measures a cold prefix, so
	// a provider-measured anchor may be claimed.
	Anchored bool `json:"anchored"`
	// AnchorTokens is the anchor the case must produce, computed by hand from
	// the fixture's own usage figures. Meaningful only when Anchored.
	AnchorTokens int `json:"anchor_tokens,omitempty"`
	// Reconciles states that attribution plus the residual must equal the
	// anchor with a non-negative remainder.
	Reconciles bool `json:"reconciles"`
	// MinCategories is the smallest number of categories the report must carry.
	// It guards the failure this whole fixture suite exists to catch: a format
	// change that empties a category without erroring.
	MinCategories int `json:"min_categories"`
	// AttributedTokens is the attributed sum a known-good run produced for this
	// case, and EstimateTolerancePct is how far the current run may sit from it.
	//
	// Unlike the fields above it is *recorded*, not derived by hand: there is no
	// offline tokenizer to derive it from. It is the estimator-drift alarm.
	// Because the anchor is fixed by the fixture, a change to the divisors moves
	// this number and moves the share of the measured total that attribution
	// explains, and CI fails rather than the panel quietly re-slicing itself.
	AttributedTokens     int     `json:"attributed_tokens,omitempty"`
	EstimateTolerancePct float64 `json:"estimate_tolerance_pct,omitempty"`
	// Caveats are caveat codes the report must carry. This is how a negative
	// case asserts that the report *said* why it degraded.
	Caveats []string `json:"caveats,omitempty"`
	// Notes explains the case in one sentence.
	Notes string `json:"notes,omitempty"`
}

// Case is one fixture: a manifest plus the tree it materializes.
type Case struct {
	// Name is the case directory name and its identity in test output.
	Name string `json:"name"`
	// Description explains what the case records.
	Description string `json:"description"`
	// Tool is the agent-deck tool name to inspect as.
	Tool string `json:"tool"`
	// Adapter is the adapter expected to claim the case.
	Adapter string `json:"adapter"`
	// Title and Profile identify the session in rendered output.
	Title   string `json:"title"`
	Profile string `json:"profile"`
	// SessionID is the harness session identifier.
	SessionID string `json:"session_id"`
	// Model is the model identifier agent-deck knows for the session.
	Model string `json:"model,omitempty"`
	// ProjectPath, ConfigDir and Transcript are slash-separated paths relative
	// to the case's root/ directory.
	ProjectPath string `json:"project_path"`
	ConfigDir   string `json:"config_dir"`
	Transcript  string `json:"transcript"`
	// Env are environment variables the case needs set while it is inspected.
	Env map[string]string `json:"env,omitempty"`
	// Expect states what the case must prove.
	Expect Expect `json:"expect"`
}

// Load reads one case by name.
func Load(name string) (*Case, error) {
	raw, err := casesFS.ReadFile(path.Join(casesRoot, name, "case.json"))
	if err != nil {
		return nil, fmt.Errorf("ctxfixture: reading case %q: %w", name, err)
	}
	var c Case
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("ctxfixture: decoding case %q: %w", name, err)
	}
	if c.Name == "" {
		c.Name = name
	}
	if c.Name != name {
		return nil, fmt.Errorf("ctxfixture: case %q declares the name %q", name, c.Name)
	}
	return &c, nil
}

// Names returns every embedded case name, sorted.
func Names() ([]string, error) {
	entries, err := fs.ReadDir(casesFS, casesRoot)
	if err != nil {
		return nil, fmt.Errorf("ctxfixture: listing cases: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// LoadAll reads every embedded case, in name order.
func LoadAll() ([]*Case, error) {
	names, err := Names()
	if err != nil {
		return nil, err
	}
	out := make([]*Case, 0, len(names))
	for _, n := range names {
		c, err := Load(n)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Golden returns the case's recorded `session context --json` document.
//
// It is a snapshot of what the inspector produced, kept so that a change to any
// figure, provenance, caveat or path is visible in a diff. A case without one
// returns ok=false rather than an error: not every case has to have a golden.
func (c *Case) Golden() (data []byte, ok bool) {
	raw, err := casesFS.ReadFile(path.Join(casesRoot, c.Name, GoldenFileName))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// WriteGolden records a case's `session context --json` report document.
//
// It writes to the source tree, not to the embedded copy, so it only works from
// a checkout — which is the only place regenerating a golden makes sense. It is
// the -update half of the golden test and is never called by an asserting run.
func (c *Case) WriteGolden(doc []byte) error {
	dir, err := sourceDir()
	if err != nil {
		return err
	}
	dest := filepath.Join(dir, casesRoot, c.Name, GoldenFileName)
	if err := os.WriteFile(dest, doc, 0o644); err != nil {
		return fmt.Errorf("ctxfixture: recording the golden for %q: %w", c.Name, err)
	}
	return nil
}

// sourceDir returns this package's directory in the checkout.
//
// It is derived from the compiled-in source path, which is valid exactly when a
// test runs from the source tree. An explicit error is returned otherwise
// rather than a path that happens to exist somewhere else.
func sourceDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("ctxfixture: cannot locate the fixture source directory")
	}
	dir := filepath.Dir(file)
	if _, err := os.Stat(filepath.Join(dir, casesRoot)); err != nil {
		return "", fmt.Errorf("ctxfixture: the fixture source directory is not present at %s: %w", dir, err)
	}
	return dir, nil
}

// Materialize copies the case's tree into dest and returns the request that
// inspects it.
//
// dest must be a caller-owned temporary directory: the tree contains a fake
// harness config directory and a fake project, and writing either anywhere near
// a real home is the failure mode this repository has been bitten by three
// times. Nothing here resolves a path from the environment.
func (c *Case) Materialize(dest string) (ctxinspect.Request, error) {
	root := path.Join(casesRoot, c.Name, "root")
	if _, err := fs.Stat(casesFS, root); err != nil {
		return ctxinspect.Request{}, fmt.Errorf("ctxfixture: case %q has no root/ tree: %w", c.Name, err)
	}
	if err := copyEmbedded(root, dest); err != nil {
		return ctxinspect.Request{}, err
	}
	return ctxinspect.Request{
		Tool:           c.Tool,
		SessionID:      c.SessionID,
		SessionRef:     c.Title,
		ProjectPath:    c.abs(dest, c.ProjectPath),
		ConfigDir:      c.abs(dest, c.ConfigDir),
		TranscriptPath: c.abs(dest, c.Transcript),
		Model:          c.Model,
		Now:            FixedNow,
	}, nil
}

// FixedRootLength is the exact length of the directory [Case.MaterializeFixed]
// materializes into.
//
// It exists because a report's numbers depend on the length of the paths in it.
// Claude Code wraps every memory file in a header naming its absolute path, so
// the character count of an item — and therefore its estimated token cost —
// changes with the depth of the temporary directory the fixture was unpacked
// into. A byte-for-byte golden and a recorded attributed total are only
// meaningful if that length is the same on every machine.
//
// The value is comfortably longer than a temporary directory on Linux or macOS
// and short enough to stay well inside every platform's path limit.
const FixedRootLength = 120

// ErrParentTooLong is returned when a parent directory leaves no room to pad a
// path to [FixedRootLength]. Callers that need reproducible numbers must skip
// rather than proceed with a shorter root.
var ErrParentTooLong = fmt.Errorf("ctxfixture: the parent directory is too long to pad to %d characters", FixedRootLength)

// MaterializeFixed materializes the case into a directory under parent whose
// absolute path is exactly [FixedRootLength] characters long.
//
// parent must still be caller-owned and temporary; the padding only fixes the
// length, it does not make the location shared. The returned root is what
// [Redact] must be given.
func (c *Case) MaterializeFixed(parent string) (ctxinspect.Request, string, error) {
	abs, err := filepath.Abs(parent)
	if err != nil {
		return ctxinspect.Request{}, "", fmt.Errorf("ctxfixture: resolving %q: %w", parent, err)
	}
	abs = strings.TrimRight(abs, string(filepath.Separator))
	pad := FixedRootLength - len(abs) - 1
	if pad < 1 {
		return ctxinspect.Request{}, "", fmt.Errorf("%w: %q is %d characters", ErrParentTooLong, abs, len(abs))
	}
	root := filepath.Join(abs, strings.Repeat("r", pad))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return ctxinspect.Request{}, "", fmt.Errorf("ctxfixture: creating the fixed-length root: %w", err)
	}
	req, err := c.Materialize(root)
	if err != nil {
		return ctxinspect.Request{}, "", err
	}
	return req, root, nil
}

// abs resolves a case-relative path against the materialized root, returning
// the empty string for an unset field so an absent transcript stays absent
// rather than becoming the root directory itself.
func (c *Case) abs(dest, rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return ""
	}
	return filepath.Join(dest, filepath.FromSlash(rel))
}

// Redact replaces the materialized root with [RootPlaceholder] so a document
// containing absolute paths can be compared byte-for-byte.
//
// The paths are redacted rather than omitted because they are load-bearing: an
// item's absolute path is the lever the user acts on, and a golden that dropped
// it would not notice if the inspector started reporting the wrong file.
func Redact(text, root string) string {
	root = strings.TrimRight(filepath.ToSlash(root), "/")
	if root == "" {
		return text
	}
	// Replace the derived form first: replacing the ordinary root would not
	// touch it, but doing this first makes both forms deterministic.
	rootKey := strings.ReplaceAll(root, "/", "-")
	out := strings.ReplaceAll(text, rootKey, RootKeyPlaceholder)
	out = strings.ReplaceAll(out, root, RootPlaceholder)
	// A JSON document escapes nothing in a POSIX path, but a Windows-style
	// separator would be escaped; normalize the slash form too so the same
	// golden serves both.
	if alt := filepath.FromSlash(root); alt != root {
		out = strings.ReplaceAll(out, strings.ReplaceAll(alt, `\`, `\\`), RootPlaceholder)
	}
	return out
}

// copyEmbedded writes an embedded subtree to disk.
//
// Permissions are fixed rather than carried over: an embedded file has no mode
// worth preserving, and a fixture must not be able to create a
// broader-than-intended file on a developer's machine.
func copyEmbedded(root, dest string) error {
	return fs.WalkDir(casesFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// An embed.FS path is always slash-separated, so the relative part is
		// taken by prefix rather than by filepath.Rel, whose separator rules
		// are the host's and not the archive's.
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := casesFS.ReadFile(p)
		if readErr != nil {
			return fmt.Errorf("ctxfixture: reading %q: %w", p, readErr)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}
