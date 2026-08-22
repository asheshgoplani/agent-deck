package ctxfixture

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"testing"
)

func TestRedactNormalizesClaudeProjectKey(t *testing.T) {
	root := "/tmp/ctxfix123/padded"
	got := Redact("path /tmp/ctxfix123/padded/project key -tmp-ctxfix123-padded-project", root)
	want := "path <FIXTURE_ROOT>/project key <FIXTURE_ROOT_KEY>-project"
	if got != want {
		t.Fatalf("Redact() = %q, want %q", got, want)
	}
}

// TestMain isolates the package from the developer's real home directory.
//
// Nothing here reads $HOME, but the isolation is unconditional: this repository
// has three times had a test run resolve agent-deck's live profile out of the
// real home and destroy it, and "this package does not do that yet" is not a
// property a later test preserves.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ctxfixture-home-")
	if err != nil {
		panic("ctxfixture: cannot create isolated HOME for tests: " + err.Error())
	}
	for k, v := range map[string]string{
		"HOME":              dir,
		"XDG_CONFIG_HOME":   dir + "/.config",
		"XDG_DATA_HOME":     dir + "/.local/share",
		"XDG_CACHE_HOME":    dir + "/.cache",
		"XDG_STATE_HOME":    dir + "/.local/state",
		"CLAUDE_CONFIG_DIR": dir + "/.claude",
		"AGENTDECK_PROFILE": "_test",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic("ctxfixture: cannot isolate " + k + ": " + err.Error())
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// TestEveryCaseHasAReadableGolden is the guard on the golden corpus itself.
//
// The corpus once asserted nothing for exactly one reason: [Case.Golden] read a
// filename that no case directory contained, so every consumer took the "this
// case has no golden" branch. That failure is invisible from inside the golden
// suite — it looks like a missing file, and the documented repair
// (AGENTDECK_UPDATE_CONTEXT_GOLDEN=1) wrote the name the read side was not
// looking for, so it could never converge.
//
// This test fails on the cause instead of on the symptom: it walks the embedded
// tree, finds the recorded documents by inspecting what is actually there, and
// requires [Case.Golden] to return each one.
func TestEveryCaseHasAReadableGolden(t *testing.T) {
	cases, err := LoadAll()
	if err != nil {
		t.Fatalf("loading the corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("the corpus is empty: every assertion built on it would pass vacuously")
	}

	for _, c := range cases {
		raw, ok := c.Golden()
		if !ok {
			t.Errorf("case %q has no golden readable through Case.Golden; %s",
				c.Name, describeCaseFiles(t, c.Name))
			continue
		}
		if len(raw) == 0 {
			t.Errorf("case %q has an empty golden: a zero-byte document asserts nothing", c.Name)
		}
	}
}

// TestGoldenReadAndWritePathsAgreeOnTheFilename pins the read and write halves
// to one name. They are already one constant; this fails if someone reintroduces
// a literal on either side, which is how they diverged the first time.
func TestGoldenReadAndWritePathsAgreeOnTheFilename(t *testing.T) {
	dir, err := sourceDir()
	if err != nil {
		t.Skipf("not running from a checkout: %v", err)
	}
	cases, err := LoadAll()
	if err != nil {
		t.Fatalf("loading the corpus: %v", err)
	}

	for _, c := range cases {
		// The path WriteGolden targets, derived the same way it derives it.
		written := filepath.Join(dir, casesRoot, c.Name, GoldenFileName)
		if _, err := os.Stat(written); err != nil {
			t.Errorf("case %q: WriteGolden would write %s, which Case.Golden does not read back: %v",
				c.Name, written, err)
			continue
		}
		// The embedded copy Golden reads, addressed by the same constant.
		if _, err := casesFS.ReadFile(path.Join(casesRoot, c.Name, GoldenFileName)); err != nil {
			t.Errorf("case %q: the embedded golden is not at %s: %v", c.Name, GoldenFileName, err)
		}
	}
}

// TestNoStrayGoldenNamesInTheCorpus catches the residue of a rename: a case
// directory holding a document under an old name would keep passing review
// while the live golden went stale beside it.
func TestNoStrayGoldenNamesInTheCorpus(t *testing.T) {
	allowed := map[string]bool{"case.json": true, GoldenFileName: true}

	entries, err := fs.ReadDir(casesFS, casesRoot)
	if err != nil {
		t.Fatalf("listing cases: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := fs.ReadDir(casesFS, path.Join(casesRoot, e.Name()))
		if err != nil {
			t.Fatalf("listing case %q: %v", e.Name(), err)
		}
		for _, f := range files {
			if f.IsDir() {
				// root/ is the materialized tree; its contents are the fixture.
				if f.Name() != "root" {
					t.Errorf("case %q holds an unexpected directory %q", e.Name(), f.Name())
				}
				continue
			}
			if !allowed[f.Name()] {
				t.Errorf("case %q holds the unexpected file %q; a golden under a second name goes stale unnoticed",
					e.Name(), f.Name())
			}
		}
	}
}

// describeCaseFiles lists what a case directory actually contains, so a failure
// names the file that is there instead of only the one that is not.
func describeCaseFiles(t *testing.T, name string) string {
	t.Helper()
	files, err := fs.ReadDir(casesFS, path.Join(casesRoot, name))
	if err != nil {
		return "and its directory could not be listed: " + err.Error()
	}
	out := "it contains:"
	for _, f := range files {
		out += " " + f.Name()
	}
	return out
}
