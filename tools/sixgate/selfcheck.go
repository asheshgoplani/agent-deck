//sixgate:lint-exempt tmux-identity — this file defines the banned patterns, so
//it necessarily contains them. It is the only exemption in the tree.

package main

import (
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/artifact"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/coldeye"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/lint"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/matrix"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/oracle"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/script"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/testimony"
)

// cmdSelfcheck proves the framework's own rules still hold. A gate framework
// nobody gates is theatre; this is the gate on the gates.
func cmdSelfcheck(args []string) (int, error) {
	fs := flag.NewFlagSet("selfcheck", flag.ContinueOnError)
	repo, _ := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	root := *repo
	if root == "" {
		found, err := findRepoRoot()
		if err != nil {
			return exitUsage, err
		}
		root = found
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return exitUsage, err
	}

	checks := []struct {
		name string
		run  func(string) error
	}{
		{"blank-detector self-test", func(string) error { return lint.SelfTest() }},
		{"G0 schema validator self-test", func(string) error { return script.SelfTest(lint.KnownRuleIDs()) }},
		{"G3 matrix validator self-test", func(string) error { return matrix.SelfTest() }},
		{"G4 oracle comparator self-test", func(string) error { return oracle.SelfTest() }},
		{"G4b testimony comparator self-test", func(string) error { return testimony.SelfTest() }},
		{"G5 cold-eye protocol self-test", func(string) error { return coldeye.SelfTest() }},
		{"gate catalogue is coherent", func(string) error { return checkGateCatalogue() }},
		{"assert runner cannot import product code", checkImportHygiene},
		{"assert runner imports only stdlib and the framework's text packages", checkAssertAllowlist},
		{"tmux is identified only by socket path", checkTmuxIdentity},
		{"all tmux traffic goes through one chokepoint", checkTmuxChokepoint},
		{"the oracle comparator cannot ask the product for its own answer", checkOracleAllowlist},
		{"the testimony comparator reads only text, never the product", checkTestimonyAllowlist},
		{"live-session collection is unreachable from the matrix runner", checkCollectUnreachable},
	}

	failed := 0
	for _, c := range checks {
		if err := c.run(root); err != nil {
			failed++
			fmt.Printf("FAIL  %s\n", c.name)
			for _, line := range strings.Split(strings.TrimRight(err.Error(), "\n"), "\n") {
				fmt.Printf("      %s\n", line)
			}
			continue
		}
		fmt.Printf("ok    %s\n", c.name)
	}
	if failed > 0 {
		fmt.Printf("\n%d self-check(s) failed\n", failed)
		return exitGate, nil
	}
	fmt.Printf("\nall %d self-checks passed\n", len(checks))
	return exitOK, nil
}

// checkGateCatalogue guards the artifact contract itself: six gates, unique
// IDs, unique directories, and every gate demanding at least one artifact. A
// gate that requires no evidence is a gate that always passes.
func checkGateCatalogue() error {
	var errs []string
	if len(artifact.Gates) != 6 {
		errs = append(errs, fmt.Sprintf("expected 6 gates, catalogue has %d", len(artifact.Gates)))
	}
	seenID := map[artifact.GateID]bool{}
	seenDir := map[string]bool{}
	for _, g := range artifact.Gates {
		if seenID[g.ID] {
			errs = append(errs, "duplicate gate id "+string(g.ID))
		}
		seenID[g.ID] = true
		if g.Dir != "" {
			if seenDir[g.Dir] {
				errs = append(errs, "duplicate gate dir "+g.Dir)
			}
			seenDir[g.Dir] = true
		}
		if len(g.Requires) == 0 {
			errs = append(errs, string(g.ID)+" requires no artifact: it would always pass")
		}
		if g.Purpose == "" {
			errs = append(errs, string(g.ID)+" has no stated purpose")
		}
		if g.ID != artifact.G0 && g.PassSignal == "" {
			errs = append(errs, string(g.ID)+" has no pass signal: presence would be mistaken for passing")
		}
	}
	for _, id := range []artifact.GateID{artifact.G0, artifact.G1, artifact.G2, artifact.G3, artifact.G4, artifact.G5} {
		if _, ok := artifact.GateByID(id); !ok {
			errs = append(errs, "gate "+string(id)+" is missing from the catalogue")
		}
	}
	return joinErrs(errs)
}

// importDenials is G2's structural rule, expressed as a build-time lint: the
// runner that asserts on rendered frames must not be able to reach the code it
// is judging. A green unit test beside a blank on-screen percentage is exactly
// the failure this framework exists to prevent, and importing the product is
// how an assert runner drifts back into testing internals.
var importDenials = []struct {
	pkgDir string
	deny   []string
	why    string
}{
	{
		pkgDir: "internal/sixgate/assert",
		deny: []string{
			"github.com/asheshgoplani/agent-deck/internal/ui",
			"github.com/asheshgoplani/agent-deck/internal/session",
			"github.com/asheshgoplani/agent-deck/internal/ctxinspect",
			"github.com/asheshgoplani/agent-deck/internal/tmux",
		},
		why: "G2 must read the rendered frame and nothing else",
	},
	{
		pkgDir: "internal/sixgate/lint",
		deny: []string{
			"github.com/asheshgoplani/agent-deck/internal/ui",
			"github.com/asheshgoplani/agent-deck/internal/session",
			"github.com/asheshgoplani/agent-deck/internal/ctxinspect",
		},
		why: "the Blank Detector must stay a text rule set, usable by any driver on any project",
	},
	{
		pkgDir: "internal/sixgate/script",
		deny: []string{
			"github.com/asheshgoplani/agent-deck/internal/ui",
			"github.com/asheshgoplani/agent-deck/internal/session",
			"github.com/asheshgoplani/agent-deck/internal/ctxinspect",
		},
		why: "the G0 schema is the universal half of SIXGATE and must stay driver-agnostic",
	},
	{
		pkgDir: "internal/sixgate/matrix",
		deny: []string{
			"github.com/asheshgoplani/agent-deck/internal/ui",
			"github.com/asheshgoplani/agent-deck/internal/session",
			"github.com/asheshgoplani/agent-deck/internal/ctxinspect",
			"github.com/asheshgoplani/agent-deck/internal/sixgate/driver",
		},
		why: "G3's declaration and report are the universal half too: which worlds exist is a statement about the product, not about the driver that visits them",
	},
}

func checkImportHygiene(root string) error {
	var errs []string
	for _, rule := range importDenials {
		dir := filepath.Join(root, filepath.FromSlash(rule.pkgDir))
		if _, err := os.Stat(dir); err != nil {
			continue // package not built yet; a later task adds it
		}
		files, err := goFiles(dir)
		if err != nil {
			return err
		}
		for _, f := range files {
			imports, err := fileImports(f)
			if err != nil {
				return err
			}
			for _, imp := range imports {
				for _, bad := range rule.deny {
					if imp == bad || strings.HasPrefix(imp, bad+"/") {
						errs = append(errs, fmt.Sprintf("%s imports %s — %s",
							relTo(root, f), imp, rule.why))
					}
				}
			}
		}
	}
	return joinErrs(errs)
}

// assertAllowedImports is G2's structural rule stated positively.
//
// A denylist only forbids the packages somebody thought of. The assert runner's
// whole claim is that it reads rendered text and cannot reach the software it
// judges, and that claim is only checkable against an allowlist: standard
// library, the G0 schema, and the Blank Detector — both of which are themselves
// denied the product by the rules above. Anything else, including a helper
// added in good faith, fails here and has to be argued for explicitly.
var assertAllowedImports = map[string]bool{
	"github.com/asheshgoplani/agent-deck/internal/sixgate/script": true,
	"github.com/asheshgoplani/agent-deck/internal/sixgate/lint":   true,
}

// assertPkgDir is the package the allowlist governs.
const assertPkgDir = "internal/sixgate/assert"

func checkAssertAllowlist(root string) error {
	dir := filepath.Join(root, filepath.FromSlash(assertPkgDir))
	if _, err := os.Stat(dir); err != nil {
		return nil // package not built yet
	}
	files, err := goFiles(dir)
	if err != nil {
		return err
	}
	var errs []string
	for _, f := range files {
		imports, err := fileImports(f)
		if err != nil {
			return err
		}
		for _, imp := range imports {
			if assertAllowedImports[imp] || isStdlibImport(imp) {
				continue
			}
			errs = append(errs, fmt.Sprintf(
				"%s imports %s — G2 may import only the standard library, the G0 schema and the Blank Detector. "+
					"An assert runner that can reach the product drifts back into testing internals, which is the "+
					"failure this framework exists to prevent.", relTo(root, f), imp))
		}
	}
	return joinErrs(errs)
}

// oracleAllowedImports is G4's structural rule, stated positively for the same
// reason G2's is.
//
// An oracle is a number the author did not produce. A comparator that could
// import the product could ask it for its own answer — and would, eventually,
// because reading a struct is easier than parsing a screen. So the comparator
// is allowed the standard library and a YAML decoder, and nothing else: its
// only inputs are a frame recorded off a terminal and a file of somebody else's
// numbers.
var oracleAllowedImports = map[string]bool{
	"gopkg.in/yaml.v3": true,
}

// oraclePkgDir is the package the allowlist governs.
const oraclePkgDir = "internal/sixgate/oracle"

// testimonyPkgDir and its empty allowlist are G4b's structural rule: the
// testimony comparator may import the STANDARD LIBRARY AND NOTHING ELSE. Its
// inputs are text — a probe's recorded replies and the inspector's JSON read
// as data — and a comparator that could import the product could grade the
// inspector against its author's explanation, which is no grading at all. It
// gets no YAML decoder either: there is no declaration file, because the four
// claims a probe grades are fixed in code where a diff reviewer can see them.
const testimonyPkgDir = "internal/sixgate/testimony"

func checkTestimonyAllowlist(root string) error {
	dir := filepath.Join(root, filepath.FromSlash(testimonyPkgDir))
	if _, err := os.Stat(dir); err != nil {
		return nil // package not built yet
	}
	files, err := goFiles(dir)
	if err != nil {
		return err
	}
	var errs []string
	for _, f := range files {
		imports, err := fileImports(f)
		if err != nil {
			return err
		}
		for _, imp := range imports {
			if isStdlibImport(imp) {
				continue
			}
			errs = append(errs, fmt.Sprintf(
				"%s imports %s — G4b may import only the standard library. A comparator that can reach the product can grade the inspector against its author's explanation, and a claim checked against its author is not checked.",
				relTo(root, f), imp))
		}
	}
	return joinErrs(errs)
}

func checkOracleAllowlist(root string) error {
	dir := filepath.Join(root, filepath.FromSlash(oraclePkgDir))
	if _, err := os.Stat(dir); err != nil {
		return nil // package not built yet
	}
	files, err := goFiles(dir)
	if err != nil {
		return err
	}
	var errs []string
	for _, f := range files {
		imports, err := fileImports(f)
		if err != nil {
			return err
		}
		for _, imp := range imports {
			if oracleAllowedImports[imp] || isStdlibImport(imp) {
				continue
			}
			errs = append(errs, fmt.Sprintf(
				"%s imports %s — G4 may import only the standard library and a YAML decoder. A comparator that can reach the product can ask it for its own answer, and a number checked against itself is not checked.",
				relTo(root, f), imp))
		}
	}
	return joinErrs(errs)
}

// collectSymbols are the identifiers that belong to the two verbs able to
// touch a live session: G4's collector (types into an EXISTING session under
// --yes) and G4b's probe driver (LAUNCHES a billed session under --yes). They
// exist in exactly one place — a `main` package the matrix runner cannot
// import — and this check proves G3's tree never names them.
var collectSymbols = []string{"cmdOracleCollect", "runOracleCommand", "CollectCommand", "cmdProbe", "probeLifecycle"}

// matrixTrees are the packages G3's runner is built from.
var matrixTrees = []string{"internal/sixgate/matrix", "internal/sixgate/fixture"}

// checkCollectUnreachable proves the one verb that types into a live session
// cannot be reached from the one runner that visits every world.
//
// G3's job is to run the journey in eighteen worlds, unattended. G4's collector
// sends keystrokes to somebody's terminal and may be billed. Those two must
// never meet, and "they don't at the moment" is not a property that survives a
// refactor. Three separate things hold the line: the collector lives in a
// `main` package nothing can import, the matrix packages are forbidden from
// importing the oracle package at all, and this check fails if G3's tree so
// much as names one of the collector's identifiers.
func checkCollectUnreachable(root string) error {
	var errs []string
	for _, sub := range matrixTrees {
		dir := filepath.Join(root, filepath.FromSlash(sub))
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		files, err := goFiles(dir)
		if err != nil {
			return err
		}
		for _, f := range files {
			imports, err := fileImports(f)
			if err != nil {
				return err
			}
			for _, imp := range imports {
				if imp == "github.com/asheshgoplani/agent-deck/internal/sixgate/oracle" {
					errs = append(errs, relTo(root, f)+
						" imports the oracle package. G3 visits every world unattended; G4's collector types into a live, billed session. Those two must not be able to meet.")
				}
			}
			raw, err := os.ReadFile(f) //nolint:gosec // path from the repo tree
			if err != nil {
				return err
			}
			for _, sym := range collectSymbols {
				if strings.Contains(string(raw), sym) {
					errs = append(errs, fmt.Sprintf("%s names %q — the live-session collector must not be reachable from the matrix runner", relTo(root, f), sym))
				}
			}
		}
	}
	return joinErrs(errs)
}

// isStdlibImport reports whether a path names a standard-library package. The
// standard library is the set of import paths whose first segment carries no
// dot: every module path outside it is a domain.
func isStdlibImport(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// The tmux rules, encoded. This machine lost its entire session fleet twice:
// once to leaked test servers exhausting the PTY pool, and once to a reaper
// that matched "tmux -C" by argv and killed the main server. On macOS a process
// keeps its original argv, so argv is not identity. Socket paths are.
var (
	processMatchRe = regexp.MustCompile(`\b(pgrep|pkill|killall)\b`)
	killServerRe   = regexp.MustCompile(`kill-server`)
	socketFlagRe   = regexp.MustCompile(`-[LS]\b|"-L"|"-S"|socket`)
	bareTmuxRe     = regexp.MustCompile(`"tmux"|\btmux -C\b|` + "`tmux`")
	exemptRe       = regexp.MustCompile(`^//sixgate:lint-exempt tmux-identity`)
	// chokepointRe marks THE one file allowed to name the tmux program. Like
	// exemptRe it is anchored at the start of the FILE, so the marker has to be
	// line one — a permission that can be buried on line 400 is a permission
	// nobody reviews.
	chokepointRe = regexp.MustCompile(`^//sixgate:lint-allow-tmux-invocation`)
)

// tmuxChokepointPath is the only file in the framework permitted to build a
// tmux command. Naming it here, rather than accepting the marker wherever it
// appears, is what stops "the chokepoint" from quietly becoming two.
const tmuxChokepointPath = "internal/sixgate/driver/panedrive/tmuxctl.go"

// sixgateTrees are the directories these rules govern.
var sixgateTrees = []string{"internal/sixgate", "tools/sixgate"}

func checkTmuxIdentity(root string) error {
	var errs []string
	for _, f := range sixgateGoFiles(root) {
		raw, err := os.ReadFile(f) //nolint:gosec // path from the repo tree
		if err != nil {
			return err
		}
		body := string(raw)
		if exemptRe.MatchString(body) {
			continue
		}
		// The chokepoint is allowed to name tmux and to terminate its own
		// server — that is its job. It is NOT allowed to match a process by
		// name or argv, so that rule stays armed on it. A blanket exemption
		// would have disarmed the rule on the one file that spawns servers.
		isChokepoint := chokepointRe.MatchString(body)
		for i, line := range strings.Split(body, "\n") {
			where := fmt.Sprintf("%s:%d", relTo(root, f), i+1)
			if processMatchRe.MatchString(line) {
				errs = append(errs, where+" matches processes by name or argv; on macOS argv is not identity — this is what killed the fleet on 2026-07-26. Identify tmux by socket path only.")
			}
			if isChokepoint {
				continue
			}
			if killServerRe.MatchString(line) && !socketFlagRe.MatchString(line) {
				errs = append(errs, where+" terminates a tmux server without naming a socket; an unaddressed one destroys every live session on the host.")
			}
			if bareTmuxRe.MatchString(line) {
				errs = append(errs, where+" invokes tmux without a dedicated socket; every invocation must go through the chokepoint at "+tmuxChokepointPath+", pinned to -L ctxgate-<runid>.")
			}
		}
	}
	return joinErrs(errs)
}

// checkTmuxChokepoint proves the chokepoint is one, structurally.
//
// The rules above are a denylist: they stop other files naming tmux. That alone
// does not stop the permitted file from growing a second, unaddressed
// invocation — and "somewhere in this file there is a tmux command nobody
// noticed" is exactly the shape of the 2026-07-18 leak, where spawn and
// teardown addressed different sockets. So the permitted file is checked
// positively: one marker in the whole tree, on the file we expect, containing
// exactly one exec call and exactly one program-name literal, prepending a
// socket flag, delegating the sweep to the hardened helper, and verifying
// afterwards. With one exec point that always prepends -L, "every invocation is
// addressed" stops being a convention and becomes arithmetic.
func checkTmuxChokepoint(root string) error {
	var errs []string
	var marked []string
	for _, f := range sixgateGoFiles(root) {
		raw, err := os.ReadFile(f) //nolint:gosec // path from the repo tree
		if err != nil {
			return err
		}
		if chokepointRe.MatchString(string(raw)) {
			marked = append(marked, relTo(root, f))
		}
	}
	sort.Strings(marked)

	path := filepath.Join(root, filepath.FromSlash(tmuxChokepointPath))
	if _, err := os.Stat(path); err != nil {
		// No driver that touches tmux exists yet. The denylist above still
		// forbids everything else from naming tmux, so there is nothing to
		// prove here.
		if len(marked) > 0 {
			errs = append(errs, "these files claim the tmux chokepoint marker but "+tmuxChokepointPath+" does not exist: "+strings.Join(marked, ", "))
		}
		return joinErrs(errs)
	}

	switch {
	case len(marked) == 0:
		errs = append(errs, tmuxChokepointPath+" exists but carries no //sixgate:lint-allow-tmux-invocation marker on line one")
	case len(marked) > 1:
		errs = append(errs, fmt.Sprintf(
			"%d files claim the tmux chokepoint marker (%s); there must be exactly one, and it must be %s",
			len(marked), strings.Join(marked, ", "), tmuxChokepointPath))
	case marked[0] != tmuxChokepointPath:
		errs = append(errs, "the tmux chokepoint marker is on "+marked[0]+", but the sanctioned chokepoint is "+tmuxChokepointPath)
	}

	raw, err := os.ReadFile(path) //nolint:gosec // path from the repo tree
	if err != nil {
		return err
	}
	body := string(raw)

	for _, want := range []struct {
		needle string
		count  int
		why    string
	}{
		{`exec.Command(`, 1, "the chokepoint must contain exactly one exec call, so there is no second place an unaddressed tmux command could be built"},
		{`exec.CommandContext(`, 0, "a second exec entry point defeats the chokepoint; add a timeout to the existing one instead"},
		{`"tmux"`, 1, "the program name must appear once, as the single constant every invocation is built from"},
	} {
		if got := strings.Count(body, want.needle); got != want.count {
			errs = append(errs, fmt.Sprintf("%s contains %q %d time(s), want %d — %s",
				tmuxChokepointPath, want.needle, got, want.count, want.why))
		}
	}

	for _, want := range []struct {
		needle string
		why    string
	}{
		{`const tmuxBin = "tmux"`, "the program name must be a named constant, not an inline literal"},
		{`func (c *ctl) ownAddr() []string { return []string{"-L", c.socketName} }`, "this run's own address must be the dedicated socket, by name"},
		{`panic(fmt.Sprintf("panedrive: refusing to build an unaddressed tmux command`, "the exec point must refuse at runtime to build a command that names no socket; an unaddressed one reaches the default socket, which on this host is the live fleet"},
		{`testutil.IsolateTmuxSocket()`, "isolation must come from the hardened helper, which unsets TMUX before setting TMUX_TMPDIR (the 2026-04-17 fix)"},
		{`testutil.KillTmuxServersUnder(`, "the sweep must delegate to the helper that addresses sockets absolutely with a TMUX*-scrubbed environment"},
		{`rep.KillError = err.Error()`, "teardown's error must be recorded; the 2026-07-18 teardown discarded exactly this value"},
		{`lsMustFail`, "teardown must verify by asserting the postcondition FAILS, rather than trusting the kill"},
	} {
		if !strings.Contains(body, want.needle) {
			errs = append(errs, fmt.Sprintf("%s no longer contains %q — %s", tmuxChokepointPath, want.needle, want.why))
		}
	}
	return joinErrs(errs)
}

// sixgateGoFiles lists every Go file the framework's own rules govern.
func sixgateGoFiles(root string) []string {
	var out []string
	for _, sub := range sixgateTrees {
		dir := filepath.Join(root, filepath.FromSlash(sub))
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		files, err := goFiles(dir)
		if err != nil {
			continue
		}
		out = append(out, files...)
	}
	return out
}

func goFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != dir {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func fileImports(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func relTo(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

func joinErrs(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("%s", strings.Join(errs, "\n"))
}
