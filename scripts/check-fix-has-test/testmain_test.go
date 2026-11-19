package main

// Regression for review finding N-2 (TestMain counted as a test).
// Origin: reviewer round-2 repros. Copy into scripts/check-fix-has-test/ and run
//   deck-test -count=1 -v -run TestReview2 ./scripts/check-fix-has-test/
// Each builds a real git repo with a real Go module and runs redOnBaseMode with
// the real git and go, as the workflow's red step does.

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func tmgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=r", "-c", "user.email=r@x", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// tmsetup: base has pkg a with F() returning 1 (the bug); head fixes it to 2
// and adds the given files. Returns base sha, head dir, base worktree dir.
func tmsetup(t *testing.T, headFiles map[string]string) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	tmgit(t, repo, "init", "-q")
	writeFile(t, repo, "go.mod", "module example.com/m\n\ngo 1.21\n")
	writeFile(t, repo, "a/a.go", "package a\n\nfunc F() int { return 1 }\n")
	tmgit(t, repo, "add", "-A")
	tmgit(t, repo, "commit", "-qm", "base")
	base := tmgit(t, repo, "rev-parse", "HEAD")
	writeFile(t, repo, "a/a.go", "package a\n\nfunc F() int { return 2 }\n")
	for p, s := range headFiles {
		writeFile(t, repo, p, s)
	}
	tmgit(t, repo, "add", "-A")
	tmgit(t, repo, "commit", "-qm", "fix")
	baseDir := filepath.Join(t.TempDir(), "base")
	tmgit(t, repo, "worktree", "add", "-q", "--detach", baseDir, base)
	return base, repo, baseDir
}

func tmrun(t *testing.T, headFiles map[string]string) (bool, string) {
	base, head, baseDir := tmsetup(t, headFiles)
	t.Chdir(head)
	t.Setenv("GOFLAGS", "-mod=mod")
	var out bytes.Buffer
	ok, err := redOnBaseMode(execRunner, base, baseDir, &out)
	if err != nil {
		t.Fatalf("redOnBaseMode: %v\n%s", err, out.String())
	}
	t.Logf("ok=%v\n%s", ok, out.String())
	return ok, out.String()
}

const tmRealTest = "package a\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tif F() != 2 {\n\t\tt.Fatal(\"F() != 2\")\n\t}\n}\n"

// Control: a real red-on-base test passes the gate.
func TestTestMain_Control(t *testing.T) {
	if ok, out := tmrun(t, map[string]string{"a/a_test.go": tmRealTest}); !ok {
		t.Fatalf("control should pass:\n%s", out)
	}
}

// A TestMain in the package is not a test: go test never reports it, so it
// must not count as a changed test, and a good fix must still pass.
func TestTestMain_TestMainIsNotAChangedTest(t *testing.T) {
	ok, out := tmrun(t, map[string]string{
		"a/a_test.go":        tmRealTest,
		"a/testmain_test.go": "package a\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) { os.Exit(m.Run()) }\n",
	})
	if !ok {
		t.Fatalf("a real red test plus a TestMain must pass:\n%s", out)
	}
	if strings.Contains(out, "TestMain:") {
		t.Fatalf("TestMain listed as a changed test:\n%s", out)
	}
}

func TestTestFuncsSkipsTestMain(t *testing.T) {
	got, err := testFuncs([]byte("package a\n\nimport \"testing\"\n\nfunc TestMain(m *testing.M) { m.Run() }\n\nfunc TestX(t *testing.T) {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := got["TestMain"]; bad || len(got) != 1 {
		t.Fatalf("got %v, want only TestX", got)
	}
}
