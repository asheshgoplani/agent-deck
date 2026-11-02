package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// GitHub checks a called workflow's job permissions when it loads the
// caller: a nested job may not ask for more than the calling job grants,
// even when an `if:` would skip it. release.yml's `tests` job calls
// go-test.yml, so one job-level permission there that release.yml does not
// grant makes every release run fail at startup ("The nested job ... is
// requesting 'actions: read', but is only allowed 'actions: none'"). No PR
// run loads release.yml, so only this test catches it.

type wfJob struct {
	Permissions any    `yaml:"permissions"`
	Uses        string `yaml:"uses"`
}

type workflow struct {
	Permissions any              `yaml:"permissions"`
	Jobs        map[string]wfJob `yaml:"jobs"`
}

var permLevel = map[string]int{"none": 0, "read": 1, "write": 2}

func readWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var wf workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	return wf
}

// perms turns a permissions block into scope -> level. Only the map form is
// expected in these workflows; anything else fails the test.
func perms(t *testing.T, where string, p any) map[string]int {
	t.Helper()
	out := map[string]int{}
	if p == nil {
		return out
	}
	m, ok := p.(map[string]any)
	if !ok {
		t.Fatalf("%s: permissions is %T, want a map of scope: level", where, p)
	}
	for scope, v := range m {
		s, _ := v.(string)
		lvl, ok := permLevel[s]
		if !ok {
			t.Fatalf("%s: permission %s: %v is not none/read/write", where, scope, v)
		}
		out[scope] = lvl
	}
	return out
}

// checkCalled fails when any job of the called workflow (or of a local
// workflow it calls in turn) asks for more than granted.
func checkCalled(t *testing.T, name string, granted map[string]int, seen map[string]bool) {
	t.Helper()
	if seen[name] {
		return
	}
	seen[name] = true
	wf := readWorkflow(t, name)
	// Jobs without their own block get the workflow-level permissions.
	asked := map[string]any{name + " (workflow level)": wf.Permissions}
	for jobName, job := range wf.Jobs {
		asked[name+" job "+jobName] = job.Permissions
		if called, ok := strings.CutPrefix(job.Uses, "./.github/workflows/"); ok {
			checkCalled(t, called, granted, seen)
		}
	}
	for where, p := range asked {
		for scope, lvl := range perms(t, where, p) {
			if lvl > granted[scope] {
				t.Errorf("%s asks for %s: %s, but release.yml's tests job grants only %d (0 none, 1 read, 2 write)", where, scope, map[int]string{1: "read", 2: "write"}[lvl], granted[scope])
			}
		}
	}
}

func TestReleaseTestsJobGrantsGoTestPermissions(t *testing.T) {
	rel := readWorkflow(t, "release.yml")
	tests, ok := rel.Jobs["tests"]
	if !ok {
		t.Fatal("release.yml has no tests job")
	}
	if tests.Uses != "./.github/workflows/go-test.yml" {
		t.Fatalf("release.yml tests job uses %q, want ./.github/workflows/go-test.yml", tests.Uses)
	}
	granted := perms(t, "release.yml job tests", tests.Permissions)
	checkCalled(t, "go-test.yml", granted, map[string]bool{})
}
