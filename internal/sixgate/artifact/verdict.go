package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// VerdictSchema is the VERDICT.json schema version.
const VerdictSchema = 1

// Gate statuses. Only StatusPass counts towards a passing verdict.
const (
	// StatusPass means the gate ran and its own artifact says it passed.
	StatusPass = "PASS"
	// StatusFail means the gate ran and its artifact says it failed.
	StatusFail = "FAIL"
	// StatusMissing means the gate's required artifacts are not on disk. This
	// is the default state and it is not a pass: no transcript, not done.
	StatusMissing = "MISSING"
	// StatusUnverified means the artifacts exist but carry no machine-readable
	// verdict. Evidence that does not say whether it passed is not evidence.
	StatusUnverified = "UNVERIFIED"
)

// SourceHash binds one source file the feature owns to its content.
type SourceHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// GateResult is one gate's line in the verdict.
type GateResult struct {
	ID        GateID   `json:"id"`
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Purpose   string   `json:"purpose"`
	Detail    string   `json:"detail,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
}

// Verdict is the roll-up artifact. It is the answer to "is this done?", and it
// expires by itself: `verdict --check` re-hashes every owned source, so a
// transcript recorded three commits ago stops counting as evidence.
type Verdict struct {
	Schema      int          `json:"schema"`
	Slug        string       `json:"slug"`
	Sentence    string       `json:"sentence"`
	Tool        string       `json:"tool"`
	GitSHA      string       `json:"git_sha"`
	GitDirty    bool         `json:"git_dirty"`
	GeneratedAt string       `json:"generated_at"`
	Overall     string       `json:"overall"`
	Gates       []GateResult `json:"gates"`
	Sources     []SourceHash `json:"sources"`
}

// BuildInput carries everything Build needs that it cannot read off disk.
type BuildInput struct {
	Tree     Tree
	Slug     string
	Sentence string
	// Owns are the source patterns from the G0 script.
	Owns []string
	// Tool identifies the sixgate build that wrote the verdict.
	Tool string
	// Now is the generation timestamp; injectable so a caller can make the
	// artifact reproducible.
	Now time.Time
	// G0Status is supplied by the caller because G0 passes by validating, not
	// by leaving a JSON file behind.
	G0Status string
	// G0Detail explains a non-passing G0.
	G0Detail string
}

// Build audits the tree and produces a verdict. It never asks the builder
// whether a gate passed; it reads the artifacts.
func Build(in BuildInput) (*Verdict, error) {
	sha, dirty := gitState(in.Tree.Repo)
	v := &Verdict{
		Schema:      VerdictSchema,
		Slug:        in.Slug,
		Sentence:    in.Sentence,
		Tool:        in.Tool,
		GitSHA:      sha,
		GitDirty:    dirty,
		GeneratedAt: in.Now.UTC().Format(time.RFC3339),
	}

	for _, g := range Gates {
		res := GateResult{ID: g.ID, Name: g.Name, Purpose: g.Purpose}
		pres, err := in.Tree.Inspect(g)
		if err != nil {
			return nil, err
		}
		res.Artifacts = pres.Found
		switch {
		case g.ID == G0:
			res.Status = in.G0Status
			res.Detail = in.G0Detail
		case !pres.Present:
			res.Status = StatusMissing
			res.Detail = "no artifact: " + strings.Join(pres.Missing, ", ")
		default:
			st, detail, err := passSignal(in.Tree, g)
			if err != nil {
				return nil, err
			}
			res.Status, res.Detail = st, detail
		}
		v.Gates = append(v.Gates, res)
	}

	srcs, err := HashSources(in.Tree.Repo, in.Owns)
	if err != nil {
		return nil, err
	}
	v.Sources = srcs

	v.Overall = StatusPass
	for _, g := range v.Gates {
		if g.Status != StatusPass {
			v.Overall = StatusFail
			break
		}
	}
	if len(v.Sources) == 0 {
		v.Overall = StatusFail
	}
	return v, nil
}

// passSignal reads a gate's own verdict file(s).
func passSignal(t Tree, g GateSpec) (status, detail string, err error) {
	if g.PassSignal == "" {
		return StatusUnverified, "gate declares no pass signal", nil
	}
	pattern := filepath.Join(t.GateDir(g), filepath.FromSlash(g.PassSignal))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", "", fmt.Errorf("gate %s: bad pass-signal pattern %q: %w", g.ID, g.PassSignal, err)
	}
	if len(matches) == 0 {
		return StatusUnverified, "no " + g.PassSignal + " recording whether this gate passed", nil
	}
	sort.Strings(matches)
	var failed []string
	for _, m := range matches {
		raw, readErr := os.ReadFile(m) //nolint:gosec // path derived from the gate tree
		if readErr != nil {
			return "", "", readErr
		}
		var probe struct {
			Pass *bool `json:"pass"`
		}
		if jsonErr := json.Unmarshal(raw, &probe); jsonErr != nil {
			return StatusUnverified, fmt.Sprintf("%s is not readable JSON: %v", t.Rel(m), jsonErr), nil
		}
		if probe.Pass == nil {
			return StatusUnverified, t.Rel(m) + " carries no top-level \"pass\" boolean", nil
		}
		if !*probe.Pass {
			failed = append(failed, t.Rel(m))
		}
	}
	if len(failed) > 0 {
		return StatusFail, "failing: " + strings.Join(failed, ", "), nil
	}
	return StatusPass, fmt.Sprintf("%d artifact(s) report pass", len(matches)), nil
}

// HashSources resolves the G0 script's `owns` patterns and hashes every file.
//
// Two pattern forms are supported, both repo-root relative:
//
//	internal/ui/context_pager.go      a path or filepath.Glob pattern
//	internal/ctxinspect/...           a Go-style recursive directory
//
// A pattern that matches nothing is an error: silently hashing zero files is
// how a verdict comes to vouch for nothing at all.
func HashSources(repo string, owns []string) ([]SourceHash, error) {
	seen := map[string]SourceHash{}
	for _, pat := range owns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		files, err := resolveOwn(repo, pat)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("owns pattern %q matched no file", pat)
		}
		for _, f := range files {
			rel, err := filepath.Rel(repo, f)
			if err != nil {
				return nil, err
			}
			rel = filepath.ToSlash(rel)
			if _, dup := seen[rel]; dup {
				continue
			}
			h, n, err := hashFile(f)
			if err != nil {
				return nil, err
			}
			seen[rel] = SourceHash{Path: rel, SHA256: h, Bytes: n}
		}
	}
	out := make([]SourceHash, 0, len(seen))
	for _, s := range seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func resolveOwn(repo, pat string) ([]string, error) {
	if strings.HasSuffix(pat, "/...") {
		root := filepath.Join(repo, filepath.FromSlash(strings.TrimSuffix(pat, "/...")))
		var out []string
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if p != root && strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}
			out = append(out, p)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("owns pattern %q: %w", pat, err)
		}
		sort.Strings(out)
		return out, nil
	}
	matches, err := filepath.Glob(filepath.Join(repo, filepath.FromSlash(pat)))
	if err != nil {
		return nil, fmt.Errorf("owns pattern %q: %w", pat, err)
	}
	var files []string
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("owns pattern %q matched directory %s; use %q for a recursive directory", pat, m, pat+"/...")
		}
		files = append(files, m)
	}
	sort.Strings(files)
	return files, nil
}

func hashFile(path string) (string, int64, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path derived from the repo-relative owns pattern
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), int64(len(raw)), nil
}

func gitState(repo string) (sha string, dirty bool) {
	sha = "unknown"
	cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	if out, err := cmd.Output(); err == nil {
		sha = strings.TrimSpace(string(out))
	}
	st := exec.Command("git", "-C", repo, "status", "--porcelain")
	if out, err := st.Output(); err == nil {
		dirty = strings.TrimSpace(string(out)) != ""
	}
	return sha, dirty
}

// Markdown renders the human-readable verdict. It is a pure function of the
// Verdict struct so that `verdict --check` can re-render and byte-compare: a
// hand-edited VERDICT.md is then a check failure rather than a passing lie.
func (v *Verdict) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# VERDICT — %s\n\n", v.Slug)
	fmt.Fprintf(&b, "> %s\n\n", v.Sentence)
	b.WriteString("*\"Done\" is not a builder's claim, it is an artifact. This file is generated by\n")
	b.WriteString("`sixgate verdict` from the evidence on disk and re-verified by `sixgate verdict --check`.\n")
	b.WriteString("Do not edit it by hand: the check re-renders it and a mismatch fails.*\n\n")

	fmt.Fprintf(&b, "- **Overall:** %s\n", v.Overall)
	fmt.Fprintf(&b, "- **Commit:** `%s`%s\n", v.GitSHA, dirtySuffix(v.GitDirty))
	fmt.Fprintf(&b, "- **Generated:** %s\n", v.GeneratedAt)
	fmt.Fprintf(&b, "- **Tool:** %s\n\n", v.Tool)

	b.WriteString("## Gates\n\n")
	b.WriteString("| gate | name | status | evidence | detail |\n")
	b.WriteString("|------|------|--------|----------|--------|\n")
	for _, g := range v.Gates {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			g.ID, g.Name, g.Status, mdCount(len(g.Artifacts)), mdCell(g.Detail))
	}
	b.WriteString("\nWhat each gate is for:\n\n")
	for _, g := range v.Gates {
		fmt.Fprintf(&b, "- **%s %s** — %s\n", g.ID, g.Name, g.Purpose)
	}

	b.WriteString("\n## Source binding\n\n")
	b.WriteString("These are the files this feature owns, as declared in `G0-script.yaml`. If any\n")
	b.WriteString("hash below no longer matches the working tree, the evidence above describes\n")
	b.WriteString("software that no longer exists and `verdict --check` fails.\n\n")
	b.WriteString("| file | bytes | sha256 |\n")
	b.WriteString("|------|-------|--------|\n")
	for _, s := range v.Sources {
		fmt.Fprintf(&b, "| `%s` | %d | `%s` |\n", s.Path, s.Bytes, s.SHA256)
	}
	if len(v.Sources) == 0 {
		b.WriteString("| _none_ | 0 | _a verdict that vouches for no source vouches for nothing_ |\n")
	}
	return b.String()
}

func dirtySuffix(dirty bool) string {
	if dirty {
		return " (working tree dirty)"
	}
	return ""
}

func mdCount(n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%d file(s)", n)
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if s == "" {
		return "—"
	}
	return s
}

// Write persists both verdict artifacts.
func Write(t Tree, v *Verdict) error {
	if err := t.CreateRoot(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(t.VerdictJSONPath(), raw, 0o644); err != nil { //nolint:gosec // committed artifact
		return err
	}
	return os.WriteFile(t.VerdictMarkdownPath(), []byte(v.Markdown()), 0o644) //nolint:gosec // committed artifact
}

// CheckResult is the outcome of `verdict --check`.
type CheckResult struct {
	Verdict *Verdict
	// Problems are the reasons the verdict is not currently valid.
	Problems []string
}

// OK reports whether the verdict still stands.
func (c *CheckResult) OK() bool { return len(c.Problems) == 0 }

// Coupling is a check that one gate's artifact still agrees with another's.
//
// The gates are deliberately independent programs reading each other's files,
// so nothing inside this package can know that, say, G2 obeyed the contract G4
// wrote. Couplings are how the caller states such a relationship without this
// package growing knowledge of any particular gate: each returns the problems
// it found, or nothing.
type Coupling func(Tree) []string

// Check re-verifies a written verdict against the working tree. It fails on a
// missing artifact, a failing gate, a hand-edited VERDICT.md, a changed source
// file, on a source file that appeared or disappeared since the verdict was
// written, and on any supplied gate-to-gate coupling that no longer holds. This
// is what makes "done" expire on its own.
func Check(t Tree, owns []string, couplings ...Coupling) (*CheckResult, error) {
	res := &CheckResult{}
	raw, err := os.ReadFile(t.VerdictJSONPath()) //nolint:gosec // path derived from the gate tree
	if err != nil {
		if os.IsNotExist(err) {
			res.Problems = append(res.Problems, "no "+VerdictJSONFile+": nothing has been verdicted — no transcript, not done")
			return res, nil
		}
		return nil, err
	}
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		res.Problems = append(res.Problems, VerdictJSONFile+" is not readable JSON: "+err.Error())
		return res, nil
	}
	res.Verdict = &v

	if v.Schema != VerdictSchema {
		res.Problems = append(res.Problems, fmt.Sprintf("%s schema %d, this tool understands %d", VerdictJSONFile, v.Schema, VerdictSchema))
	}
	if v.Slug != t.Slug {
		res.Problems = append(res.Problems, fmt.Sprintf("verdict is for slug %q, checked tree is %q", v.Slug, t.Slug))
	}

	onDisk, err := os.ReadFile(t.VerdictMarkdownPath()) //nolint:gosec // path derived from the gate tree
	switch {
	case os.IsNotExist(err):
		res.Problems = append(res.Problems, "no "+VerdictMarkdownFile+" beside "+VerdictJSONFile)
	case err != nil:
		return nil, err
	case string(onDisk) != v.Markdown():
		res.Problems = append(res.Problems, VerdictMarkdownFile+" does not match "+VerdictJSONFile+": it was edited by hand, or written by a different tool version")
	}

	for _, g := range v.Gates {
		if g.Status != StatusPass {
			detail := g.Detail
			if detail == "" {
				detail = "no detail recorded"
			}
			res.Problems = append(res.Problems, fmt.Sprintf("gate %s %s is %s — %s", g.ID, g.Name, g.Status, detail))
		}
	}

	for _, c := range couplings {
		res.Problems = append(res.Problems, c(t)...)
	}

	current, err := HashSources(t.Repo, owns)
	if err != nil {
		res.Problems = append(res.Problems, "cannot hash owned sources: "+err.Error())
		return res, nil
	}
	res.Problems = append(res.Problems, diffSources(v.Sources, current)...)
	return res, nil
}

func diffSources(recorded, current []SourceHash) []string {
	was := map[string]SourceHash{}
	for _, s := range recorded {
		was[s.Path] = s
	}
	now := map[string]SourceHash{}
	for _, s := range current {
		now[s.Path] = s
	}
	var problems []string
	for _, s := range current {
		old, ok := was[s.Path]
		if !ok {
			problems = append(problems, "source appeared since the verdict was written: "+s.Path+" — the evidence never covered it")
			continue
		}
		if old.SHA256 != s.SHA256 {
			problems = append(problems, "source changed since the verdict was written: "+s.Path+" — the transcript describes older software")
		}
	}
	for _, s := range recorded {
		if _, ok := now[s.Path]; !ok {
			problems = append(problems, "source vouched for by the verdict is gone: "+s.Path)
		}
	}
	sort.Strings(problems)
	return problems
}
