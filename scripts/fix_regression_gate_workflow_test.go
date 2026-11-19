package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Fix PR regression gate judges the PR's own code, so the PR must not be
// able to change the judge (review R-1, 2026-11-19: a fix PR that edited
// check-fix-has-test to always pass went green), and the step that runs the
// PR's tests must not hold a token (R-7).

type gateStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
	Env  map[string]string `yaml:"env"`
	Dir  string            `yaml:"working-directory"`
}

func gateSteps(t *testing.T) []gateStep {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "fix-regression-gate.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Env   map[string]string `yaml:"env"`
			Steps []gateStep        `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}
	job, ok := wf.Jobs["gate"]
	if !ok {
		t.Fatal("no gate job")
	}
	for _, env := range []map[string]string{wf.Env, job.Env} {
		for k, v := range env {
			if strings.Contains(k, "TOKEN") || strings.Contains(v, "token") || strings.Contains(v, "secrets.") {
				t.Fatalf("workflow or job env carries a token (%s): every step, including the PR's tests, would see it", k)
			}
		}
	}
	return job.Steps
}

func TestFixRegressionGate_CheckerComesFromTheBase(t *testing.T) {
	steps := gateSteps(t)
	trusted := ""
	for _, s := range steps {
		if strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["ref"] == "${{ github.event.pull_request.base.sha }}" {
			trusted = s.With["path"]
		}
	}
	if trusted == "" {
		t.Fatal("no checkout of github.event.pull_request.base.sha: the checker must be built from the base, not the PR")
	}
	built := false
	for _, s := range steps {
		if strings.Contains(s.Run, "scripts/check-fix-has-test.sh") {
			t.Fatalf("step %q runs the wrapper script, which builds the checker from whatever tree it sits in", s.Name)
		}
		if strings.Contains(s.Run, "go build") && strings.Contains(s.Run, "./scripts/check-fix-has-test") {
			if s.Dir != trusted {
				t.Fatalf("step %q builds the checker in %q, want the base checkout %q", s.Name, s.Dir, trusted)
			}
			built = true
		}
	}
	if !built {
		t.Fatal("no step builds ./scripts/check-fix-has-test from the base checkout")
	}
}

func TestFixRegressionGate_PRTestsRunWithoutAToken(t *testing.T) {
	ran := false
	for _, s := range gateSteps(t) {
		if !strings.Contains(s.Run, "--red-on-base") {
			continue
		}
		ran = true
		for k, v := range s.Env {
			if strings.Contains(k, "TOKEN") || strings.Contains(v, "token") || strings.Contains(v, "secrets.") {
				t.Fatalf("step %q runs the PR's tests and has %s in its env", s.Name, k)
			}
		}
		if strings.Contains(s.Run, "github.token") || strings.Contains(s.Run, "gh ") || strings.Contains(s.Run, "--pr ") {
			t.Fatalf("step %q runs the PR's tests and also talks to GitHub", s.Name)
		}
	}
	if !ran {
		t.Fatal("no step runs --red-on-base")
	}
}
