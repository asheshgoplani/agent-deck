// Package artifact owns SIXGATE's on-disk evidence tree and the VERDICT that
// binds it to a commit.
//
// The framework's rule is that "done" is not a builder's claim, it is an
// artifact. Everything in this package exists to make that mechanical: where
// each gate's evidence lives, what counts as evidence being present, and how a
// verdict stops being valid the moment the source it vouched for changes.
package artifact

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultGatesRoot is where gate evidence lives, relative to the repo root.
const DefaultGatesRoot = "docs/gates"

// ScriptFile is the G0 artifact's filename inside a gate directory.
const ScriptFile = "G0-script.yaml"

// Verdict artifact filenames. The Markdown file is what a human reads; the
// JSON file is what `verdict --check` re-verifies. Both are written together
// and must agree, so a hand-edited verdict cannot pass a check.
const (
	VerdictMarkdownFile = "VERDICT.md"
	VerdictJSONFile     = "VERDICT.json"
)

// GateID identifies one of the six gates.
type GateID string

// The six gates, in the order they must be satisfied.
const (
	G0 GateID = "G0"
	G1 GateID = "G1"
	G2 GateID = "G2"
	G3 GateID = "G3"
	G4 GateID = "G4"
	G5 GateID = "G5"
)

// GateSpec describes a gate: its name, its directory, and the artifacts that
// must exist before it can be considered run at all.
type GateSpec struct {
	ID   GateID
	Name string
	// Dir is the subdirectory holding this gate's evidence. Empty for G0,
	// whose artifact is a single file at the gate root.
	Dir string
	// Requires lists paths, relative to the gate directory, that must exist.
	// A glob is allowed; at least one match is then required.
	Requires []string
	// PassSignal names the JSON artifact, relative to the gate directory, whose
	// top-level "pass" boolean decides this gate. A glob is allowed and every
	// match must pass. Empty means the gate is decided by the tool that runs it
	// rather than by a file (only G0, which is decided by validation).
	//
	// Presence and pass are separate on purpose: a gate can be present and
	// failing, and a verdict that conflates the two turns "we ran it" into
	// "it passed" — which is the failure SIXGATE exists to prevent.
	PassSignal string
	// Purpose is one line, reproduced into VERDICT.md so the document explains
	// itself to someone who has never read this package.
	Purpose string
}

// Gates is the canonical gate list. Nothing outside this slice is a gate.
var Gates = []GateSpec{
	{
		ID: G0, Name: "SCRIPT", Dir: "",
		Requires: []string{ScriptFile},
		Purpose:  "the journey written as literal keystrokes BEFORE building; if nobody can write it, the feature is undefined",
	},
	{
		ID: G1, Name: "DRIVE", Dir: "G1-drive",
		Requires: []string{"*/transcript.md", "*/run.json", "*/*.screen.txt"}, PassSignal: "*/run.json",
		Purpose: "something executed G0 end to end against the real built artifact and captured what a human would see",
	},
	{
		ID: G2, Name: "ASSERT-ON-SCREEN", Dir: "G2-assert",
		Requires: []string{"results.json", "results.md"}, PassSignal: "results.json",
		Purpose: "assertions read the rendered output, never internals; plus the Blank Detector on every frame",
	},
	{
		ID: G3, Name: "MATRIX", Dir: "G3-matrix",
		Requires: []string{"matrix.json", "matrix.md"}, PassSignal: "matrix.json",
		Purpose: "every tool type, every session state, empty and enormous inputs, and a cold first run with no config",
	},
	{
		ID: G4, Name: "ORACLE", Dir: "G4-oracle",
		Requires: []string{"parity.json", "parity.md"}, PassSignal: "parity.json",
		Purpose: "every number compared against a source of truth the author did not write; unoracled figures must be labelled estimates on screen",
	},
	{
		ID: G5, Name: "COLD-EYE", Dir: "G5-coldeye",
		Requires: []string{"brief.md", "report.md"}, PassSignal: "outcome.json",
		Purpose: "a reviewer given only the binary and one sentence reports their literal first three minutes",
	},
}

// GateByID returns the spec for an ID.
func GateByID(id GateID) (GateSpec, bool) {
	for _, g := range Gates {
		if g.ID == id {
			return g, true
		}
	}
	return GateSpec{}, false
}

// Tree is one feature's evidence directory: <gatesRoot>/<slug>.
type Tree struct {
	// Repo is the repository root every other path is relative to.
	Repo string
	// GatesRoot is repo-relative, normally DefaultGatesRoot.
	GatesRoot string
	// Slug names the feature.
	Slug string
}

// NewTree builds a Tree. An empty gatesRoot falls back to DefaultGatesRoot.
func NewTree(repo, gatesRoot, slug string) Tree {
	if gatesRoot == "" {
		gatesRoot = DefaultGatesRoot
	}
	return Tree{Repo: repo, GatesRoot: gatesRoot, Slug: slug}
}

// Root is the absolute path of the feature's gate directory.
func (t Tree) Root() string { return filepath.Join(t.Repo, t.GatesRoot, t.Slug) }

// Rel renders a path under the tree relative to the repo root, for reports.
func (t Tree) Rel(p string) string {
	rel, err := filepath.Rel(t.Repo, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

// ScriptPath is the G0 artifact's absolute path.
func (t Tree) ScriptPath() string { return filepath.Join(t.Root(), ScriptFile) }

// GateDir is a gate's absolute directory. G0 has no subdirectory of its own.
func (t Tree) GateDir(g GateSpec) string {
	if g.Dir == "" {
		return t.Root()
	}
	return filepath.Join(t.Root(), g.Dir)
}

// VerdictMarkdownPath and VerdictJSONPath locate the roll-up artifacts.
func (t Tree) VerdictMarkdownPath() string {
	return filepath.Join(t.Root(), VerdictMarkdownFile)
}

// VerdictJSONPath is the machine-checkable half of the verdict.
func (t Tree) VerdictJSONPath() string { return filepath.Join(t.Root(), VerdictJSONFile) }

// CreateRoot makes the gate directory. It creates nothing else: the ordering
// rule is that G1..G5 do not exist until G0 validates.
func (t Tree) CreateRoot() error {
	return os.MkdirAll(t.Root(), 0o755)
}

// Unlocked reports whether the G1..G5 directories already exist.
func (t Tree) Unlocked() bool {
	for _, g := range Gates {
		if g.Dir == "" {
			continue
		}
		if _, err := os.Stat(t.GateDir(g)); err != nil {
			return false
		}
	}
	return true
}

// Unlock creates the G1..G5 directories. Callers MUST have validated G0 first;
// tools/sixgate is the only intended caller and it enforces that ordering.
// Each directory gets a .gitkeep so the empty shape is committable and a
// missing gate is visible in review rather than invisible.
func (t Tree) Unlock() ([]string, error) {
	var made []string
	for _, g := range Gates {
		if g.Dir == "" {
			continue
		}
		dir := t.GateDir(g)
		if _, err := os.Stat(dir); err == nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return made, err
		}
		keep := filepath.Join(dir, ".gitkeep")
		if err := os.WriteFile(keep, nil, 0o644); err != nil {
			return made, err
		}
		made = append(made, t.Rel(dir))
	}
	return made, nil
}

// Presence is the evidence audit for one gate.
type Presence struct {
	Gate    GateSpec
	Present bool
	// Missing lists the required paths (gate-relative) with no match.
	Missing []string
	// Found lists the repo-relative paths that satisfied the requirements.
	Found []string
}

// Inspect audits one gate's directory for its required artifacts. It reports
// what exists; it does not judge whether the gate passed. That distinction is
// deliberate: a gate can be present and failing, and a report that conflates
// the two is how "we ran it" becomes "it passed".
func (t Tree) Inspect(g GateSpec) (Presence, error) {
	p := Presence{Gate: g, Present: true}
	dir := t.GateDir(g)
	for _, req := range g.Requires {
		pattern := filepath.Join(dir, filepath.FromSlash(req))
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return p, fmt.Errorf("gate %s: bad requirement pattern %q: %w", g.ID, req, err)
		}
		if len(matches) == 0 {
			p.Present = false
			p.Missing = append(p.Missing, req)
			continue
		}
		for _, m := range matches {
			p.Found = append(p.Found, t.Rel(m))
		}
	}
	return p, nil
}
