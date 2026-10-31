package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// scripts/ci/release-test-reuse.sh decides whether release.yml may skip the
// race suite because the tag commit (or the release PR head with the same
// tree and parent) already has a green Go tests run (#2929). A wrong "reuse"
// ships a binary nobody tested, so these cases pin every rule: only a green
// run with green gate and shard jobs on the same commit or the same tree and
// parent counts, and every other answer is "run the shards".

const (
	reuseRepo    = "acme/agent-deck"
	reuseTagSHA  = "1111111111111111111111111111111111111111"
	reuseHeadSHA = "2222222222222222222222222222222222222222"
	reuseParent  = "3333333333333333333333333333333333333333"
	reuseTree    = "4444444444444444444444444444444444444444"
	reuseOther   = "5555555555555555555555555555555555555555"
)

// fakeGH answers `gh api <path> --jq <expr>` from JSON fixtures keyed by the
// API path. A path with fixtures key.1.json, key.2.json, ... answers them in
// order (the last one repeats), so a run can go from in_progress to completed.
const fakeGH = `#!/usr/bin/env bash
[ "$1" = api ] || { echo "fake gh: only api is supported" >&2; exit 2; }
path=$2; shift 2
expr=.
if [ "${1:-}" = --jq ]; then expr=$2; fi
key=$(printf %s "$path" | tr -c 'A-Za-z0-9' _)
dir=$FAKE_GH_DIR
if [ -f "$dir/$key.1.json" ]; then
  n=$(( $(cat "$dir/$key.count" 2>/dev/null || echo 0) + 1 ))
  echo "$n" > "$dir/$key.count"
  while [ "$n" -gt 1 ] && [ ! -f "$dir/$key.$n.json" ]; do n=$((n - 1)); done
  f="$dir/$key.$n.json"
else
  f="$dir/$key.json"
fi
[ -f "$f" ] || { echo "fake gh: HTTP 404 for $path" >&2; exit 1; }
jq -r "$expr" "$f"
`

var fixtureKey = regexp.MustCompile(`[^A-Za-z0-9]`)

type reuseFixture map[string]any // API path (or path + ".N") -> JSON body

func commitJSON(tree string, parents ...string) map[string]any {
	ps := []map[string]string{}
	for _, p := range parents {
		ps = append(ps, map[string]string{"sha": p})
	}
	return map[string]any{"tree": map[string]string{"sha": tree}, "parents": ps}
}

func runJSON(id int, status, conclusion, headSHA, branch string) map[string]any {
	r := map[string]any{
		"id": id, "status": status, "head_sha": headSHA, "head_branch": branch,
		"path":     ".github/workflows/go-test.yml",
		"html_url": "https://github.com/acme/agent-deck/actions/runs/" + strconv.Itoa(id),
	}
	if conclusion != "" {
		r["conclusion"] = conclusion
	} else {
		r["conclusion"] = nil
	}
	return r
}

func runsJSON(runs ...map[string]any) map[string]any {
	if runs == nil {
		runs = []map[string]any{}
	}
	return map[string]any{"workflow_runs": runs}
}

func jobsJSON(pairs ...string) map[string]any { // name, conclusion, ...
	jobs := []map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		jobs = append(jobs, map[string]any{"name": pairs[i], "conclusion": pairs[i+1]})
	}
	return map[string]any{"jobs": jobs}
}

var greenJobs = jobsJSON(
	"Detect Go-relevant changes", "success",
	"Test shard (cmd-a)", "success",
	"Test shard (session)", "success",
	"Test shard (proof)", "success",
	"Comms matrix advisory / Comms (claude)", "failure", // advisory: ignored
	"Full test suite (PR gate)", "success",
)

func pushRunsPath(sha string) string {
	return "repos/" + reuseRepo + "/actions/workflows/go-test.yml/runs?head_sha=" + sha + "&event=push&per_page=30"
}

func prRunsPath(sha string) string {
	return "repos/" + reuseRepo + "/actions/workflows/go-test.yml/runs?head_sha=" + sha + "&event=pull_request&per_page=30"
}

func jobsPath(id string) string {
	return "repos/" + reuseRepo + "/actions/runs/" + id + "/jobs?filter=latest&per_page=100"
}

func commitPath(sha string) string { return "repos/" + reuseRepo + "/git/commits/" + sha }

func pullsPath() string { return "repos/" + reuseRepo + "/commits/" + reuseTagSHA + "/pulls" }

// reuseHeadRef is the merged PR's head branch; a pull_request run counts only
// on that branch.
const reuseHeadRef = "release/v1"

func mergedPR(headSHA, headRepo string) []map[string]any {
	return mergedPRInto(headSHA, headRepo, "main")
}

func mergedPRInto(headSHA, headRepo, baseRef string) []map[string]any {
	return []map[string]any{{
		"merged_at":        "2026-10-31T14:45:00Z",
		"merge_commit_sha": reuseTagSHA,
		"head":             map[string]any{"sha": headSHA, "ref": reuseHeadRef, "repo": map[string]string{"full_name": headRepo}},
		"base":             map[string]any{"ref": baseRef},
	}}
}

// baseFixture: tag commit squash-merged from a same-repo PR head with the
// same tree and parent; no runs anywhere yet.
func baseFixture() reuseFixture {
	return reuseFixture{
		commitPath(reuseTagSHA):   commitJSON(reuseTree, reuseParent),
		commitPath(reuseHeadSHA):  commitJSON(reuseTree, reuseParent),
		pullsPath():               mergedPR(reuseHeadSHA, reuseRepo),
		pushRunsPath(reuseTagSHA): runsJSON(),
		prRunsPath(reuseHeadSHA):  runsJSON(),
	}
}

type reuseResult struct {
	out  map[string]string
	log  string
	exit int
}

func runReuse(t *testing.T, fx reuseFixture, env ...string) reuseResult {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		// The fake gh applies the script's --jq expressions with jq, which
		// every GitHub-hosted Ubuntu runner has. Never skip there.
		if os.Getenv("CI") != "" {
			t.Fatal("jq not installed on a CI runner; the fake gh needs it")
		}
		t.Skip("jq not installed; the fake gh needs it (CI runners have it)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	fixtures := filepath.Join(dir, "fixtures")
	for _, d := range []string{bin, fixtures} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range fx {
		name := path
		suffix := ""
		if i := strings.LastIndex(path, "#"); i >= 0 { // "path#2" -> key.2.json
			name, suffix = path[:i], "."+path[i+1:]
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(fixtures, fixtureKey.ReplaceAllString(name, "_")+suffix+".json")
		if err := os.WriteFile(file, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(dir, "github_output")
	summary := filepath.Join(dir, "summary")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(wd, "ci", "release-test-reuse.sh"))
	cmd.Env = append([]string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + dir,
		"FAKE_GH_DIR=" + fixtures,
		"REPO=" + reuseRepo,
		"TAG_SHA=" + reuseTagSHA,
		"GITHUB_OUTPUT=" + output,
		"GITHUB_STEP_SUMMARY=" + summary,
		"REUSE_POLL_SECONDS=0",
	}, env...)
	logOut, runErr := cmd.CombinedOutput()
	res := reuseResult{out: map[string]string{}, log: string(logOut)}
	if ee, ok := runErr.(*exec.ExitError); ok {
		res.exit = ee.ExitCode()
	} else if runErr != nil {
		t.Fatal(runErr)
	}
	data, _ := os.ReadFile(output)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			res.out[k] = v
		}
	}
	return res
}

func TestReleaseTestReuse(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(reuseFixture)
		env       []string
		wantReuse string
		wantMatch string
		wantLog   string
	}{
		{
			name: "green push run on the tag commit is reused",
			mutate: func(fx reuseFixture) {
				fx[pushRunsPath(reuseTagSHA)] = runsJSON(runJSON(11, "completed", "success", reuseTagSHA, "main"))
				fx[jobsPath("11")] = greenJobs
			},
			wantReuse: "true", wantMatch: "exact-sha",
		},
		{
			name: "green PR run on a head with the same tree and parent is reused",
			mutate: func(fx reuseFixture) {
				fx[prRunsPath(reuseHeadSHA)] = runsJSON(runJSON(12, "completed", "success", reuseHeadSHA, "release/v1"))
				fx[jobsPath("12")] = greenJobs
			},
			wantReuse: "true", wantMatch: "same-tree",
		},
		{
			name: "PR head with the same tree but another parent is not reused",
			mutate: func(fx reuseFixture) {
				fx[commitPath(reuseHeadSHA)] = commitJSON(reuseTree, reuseOther)
				fx[prRunsPath(reuseHeadSHA)] = runsJSON(runJSON(12, "completed", "success", reuseHeadSHA, "release/v1"))
				fx[jobsPath("12")] = greenJobs
			},
			wantReuse: "false", wantLog: "parent",
		},
		{
			name: "PR head with another tree is not reused",
			mutate: func(fx reuseFixture) {
				fx[commitPath(reuseHeadSHA)] = commitJSON(reuseOther, reuseParent)
				fx[prRunsPath(reuseHeadSHA)] = runsJSON(runJSON(12, "completed", "success", reuseHeadSHA, "release/v1"))
				fx[jobsPath("12")] = greenJobs
			},
			wantReuse: "false", wantLog: "tree",
		},
		{
			name: "PR from a fork is not reused",
			mutate: func(fx reuseFixture) {
				fx[pullsPath()] = mergedPR(reuseHeadSHA, "someone/agent-deck")
				fx[prRunsPath(reuseHeadSHA)] = runsJSON(runJSON(12, "completed", "success", reuseHeadSHA, "release/v1"))
				fx[jobsPath("12")] = greenJobs
			},
			wantReuse: "false", wantLog: "someone/agent-deck",
		},
		{
			name: "PR run on another branch than the merged PR head is not reused",
			mutate: func(fx reuseFixture) {
				fx[prRunsPath(reuseHeadSHA)] = runsJSON(runJSON(12, "completed", "success", reuseHeadSHA, "other-branch"))
				fx[jobsPath("12")] = greenJobs
			},
			wantReuse: "false", wantLog: "not the PR head branch",
		},
		{
			name: "PR merged into another base than main is not reused",
			mutate: func(fx reuseFixture) {
				fx[pullsPath()] = mergedPRInto(reuseHeadSHA, reuseRepo, "release-1.x")
				fx[prRunsPath(reuseHeadSHA)] = runsJSON(runJSON(12, "completed", "success", reuseHeadSHA, reuseHeadRef))
				fx[jobsPath("12")] = greenJobs
			},
			wantReuse: "false", wantLog: "base release-1.x",
		},
		{
			name: "merge commit tag (two parents) never uses the same-tree rule",
			mutate: func(fx reuseFixture) {
				fx[commitPath(reuseTagSHA)] = commitJSON(reuseTree, reuseParent, reuseOther)
				fx[prRunsPath(reuseHeadSHA)] = runsJSON(runJSON(12, "completed", "success", reuseHeadSHA, "release/v1"))
				fx[jobsPath("12")] = greenJobs
			},
			wantReuse: "false",
		},
		{
			name: "red run is not reused",
			mutate: func(fx reuseFixture) {
				fx[pushRunsPath(reuseTagSHA)] = runsJSON(runJSON(11, "completed", "failure", reuseTagSHA, "main"))
			},
			wantReuse: "false", wantLog: "concluded failure",
		},
		{
			name: "green run with a failed shard on the latest attempt is not reused",
			mutate: func(fx reuseFixture) {
				fx[pushRunsPath(reuseTagSHA)] = runsJSON(runJSON(11, "completed", "success", reuseTagSHA, "main"))
				fx[jobsPath("11")] = jobsJSON("Test shard (cmd-a)", "success", "Test shard (session)", "cancelled", "Full test suite (PR gate)", "success")
			},
			wantReuse: "false", wantLog: "did not conclude success",
		},
		{
			name: "a run whose shards were skipped (no Go changes) is not reused",
			mutate: func(fx reuseFixture) {
				fx[pushRunsPath(reuseTagSHA)] = runsJSON(runJSON(11, "completed", "success", reuseTagSHA, "main"))
				fx[jobsPath("11")] = jobsJSON("Detect Go-relevant changes", "success", "Test shard (cmd-a)", "skipped", "Full test suite (PR gate)", "success")
			},
			wantReuse: "false",
		},
		{
			name: "a run without the gate job is not reused",
			mutate: func(fx reuseFixture) {
				fx[pushRunsPath(reuseTagSHA)] = runsJSON(runJSON(11, "completed", "success", reuseTagSHA, "main"))
				fx[jobsPath("11")] = jobsJSON("Test shard (cmd-a)", "success")
			},
			wantReuse: "false",
		},
		{
			name: "push run on another branch is not reused",
			mutate: func(fx reuseFixture) {
				fx[pushRunsPath(reuseTagSHA)] = runsJSON(runJSON(11, "completed", "success", reuseTagSHA, "feature"))
				fx[jobsPath("11")] = greenJobs
			},
			wantReuse: "false", wantLog: "not main",
		},
		{
			name: "a run listed for another sha is not reused",
			mutate: func(fx reuseFixture) {
				fx[pushRunsPath(reuseTagSHA)] = runsJSON(runJSON(11, "completed", "success", reuseOther, "main"))
				fx[jobsPath("11")] = greenJobs
			},
			wantReuse: "false",
		},
		{
			name: "an in-progress run is waited for and reused once green",
			mutate: func(fx reuseFixture) {
				delete(fx, pushRunsPath(reuseTagSHA))
				fx[pushRunsPath(reuseTagSHA)+"#1"] = runsJSON(runJSON(11, "in_progress", "", reuseTagSHA, "main"))
				fx[pushRunsPath(reuseTagSHA)+"#2"] = runsJSON(runJSON(11, "completed", "success", reuseTagSHA, "main"))
				fx[jobsPath("11")] = greenJobs
			},
			env:       []string{"REUSE_WAIT_SECONDS=60"},
			wantReuse: "true", wantMatch: "exact-sha", wantLog: "is in_progress",
		},
		{
			name: "an in-progress run past the deadline falls back to the shards",
			mutate: func(fx reuseFixture) {
				fx[pushRunsPath(reuseTagSHA)] = runsJSON(runJSON(11, "in_progress", "", reuseTagSHA, "main"))
			},
			env:       []string{"REUSE_WAIT_SECONDS=0"},
			wantReuse: "false", wantLog: "still running",
		},
		{
			name: "an in-progress run that turns red falls back to the shards",
			mutate: func(fx reuseFixture) {
				delete(fx, pushRunsPath(reuseTagSHA))
				fx[pushRunsPath(reuseTagSHA)+"#1"] = runsJSON(runJSON(11, "queued", "", reuseTagSHA, "main"))
				fx[pushRunsPath(reuseTagSHA)+"#2"] = runsJSON(runJSON(11, "completed", "failure", reuseTagSHA, "main"))
			},
			env:       []string{"REUSE_WAIT_SECONDS=60"},
			wantReuse: "false", wantLog: "concluded failure",
		},
		{
			name: "API errors fall back to the shards without failing",
			mutate: func(fx reuseFixture) {
				delete(fx, commitPath(reuseTagSHA))
			},
			wantReuse: "false", wantLog: "could not read commit",
		},
		{
			name:      "no run at all falls back to the shards",
			mutate:    func(reuseFixture) {},
			wantReuse: "false", wantLog: "no completed green candidate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := baseFixture()
			tc.mutate(fx)
			res := runReuse(t, fx, tc.env...)
			if res.exit != 0 {
				t.Fatalf("script exited %d, want 0 (it must never fail the release)\n%s", res.exit, res.log)
			}
			if got := res.out["reuse"]; got != tc.wantReuse {
				t.Fatalf("reuse=%q, want %q\n%s", got, tc.wantReuse, res.log)
			}
			if tc.wantMatch != "" && res.out["match"] != tc.wantMatch {
				t.Fatalf("match=%q, want %q\n%s", res.out["match"], tc.wantMatch, res.log)
			}
			if tc.wantReuse == "true" && !strings.HasPrefix(res.out["run_url"], "https://github.com/") {
				t.Fatalf("run_url=%q, want the reused run's URL", res.out["run_url"])
			}
			if tc.wantReuse == "false" && res.out["run_url"] != "" {
				t.Fatalf("run_url=%q on a no-reuse answer", res.out["run_url"])
			}
			if tc.wantLog != "" && !strings.Contains(res.log, tc.wantLog) {
				t.Fatalf("log does not mention %q:\n%s", tc.wantLog, res.log)
			}
		})
	}
}

// release.yml must build only behind the gate: the release job needs the
// reuse decision and the shards, and its condition accepts exactly "reused
// (tests skipped)" or "tests succeeded".
func TestReleaseWorkflowGatesOnTests(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"    needs: [ref-check, reuse, tests, eval]\n",
		"needs.reuse.outputs.reuse == 'true' && needs.tests.result == 'skipped'",
		"|| needs.tests.result == 'success'",
		"    uses: ./.github/workflows/go-test.yml\n    with:\n      shards_only: true\n",
		"run: bash scripts/ci/release-test-reuse.sh",
		"Require the checkout to be the tested commit",
		// A failed reuse lookup alone falls back to the shards; once the
		// release job succeeded it is no failure to page anyone about.
		"    if: failure() && needs.release.result != 'success'\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("release.yml lost %q", want)
		}
	}
	if strings.Contains(text, "gotestsum") {
		t.Errorf("release.yml runs gotestsum itself again; the suite lives in go-test.yml")
	}
}
