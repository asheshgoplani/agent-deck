package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
	"github.com/asheshgoplani/agent-deck/internal/reportloop"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ASK-205: the report -> fix -> what's new -> "is it fixed for you?" loop.

const loopChangelog = `# Changelog

## [Unreleased]

## [1.16.38] - 2026-11-12

### Highlights

- Forks finish in seconds again.

### Fixes

- Forking a large session took over five minutes (#9002, #9010).

## [1.16.37] - 2026-11-09

### Highlights

- Queued messages are delivered after a restart.

### Fixes

- A queued message stayed queued forever after the target restarted (#9001, #9005).

## [1.16.36] - 2026-11-02

### Fixes

- Old fix (#9003).
`

// fakeSecret is a GitHub token shape the scrubber must remove.
const fakeSecret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

type ghCommentCall struct {
	number int
	body   string
}

// loopEnv pins the report store and the gh list/comment
// seams; nothing here can reach the real gh or GitHub.
type loopEnv struct {
	store    *reportloop.Store
	home     string
	list     []ghIssueRef
	listErr  error
	searches []string
	comments []ghCommentCall
}

func setupLoopTest(t *testing.T) *loopEnv {
	t.Helper()
	e := &loopEnv{}
	e.home, _ = os.UserHomeDir()
	e.store = &reportloop.Store{Path: filepath.Join(t.TempDir(), "reports.json")}
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	prevStore, prevList, prevComment := feedbackReportStore, feedbackGhListIssues, feedbackGhComment
	feedbackReportStore = func() (*reportloop.Store, error) { return e.store, nil }
	feedbackGhListIssues = func(_ context.Context, search string) ([]ghIssueRef, error) {
		e.searches = append(e.searches, search)
		return e.list, e.listErr
	}
	feedbackGhComment = func(_ context.Context, n int, body string) (string, error) {
		e.comments = append(e.comments, ghCommentCall{n, body})
		return "https://github.com/asheshgoplani/agent-deck/issues/" + strconv.Itoa(n) + "#issuecomment-1", nil
	}
	// No test reads the real changelog: `feedback mine` falls back to the
	// stored state unless a test pins one.
	prevCL := whatsNewFetchChangelog
	whatsNewFetchChangelog = func(context.Context) (string, error) { return "", errors.New("no changelog in this test") }
	t.Cleanup(func() {
		feedbackReportStore, feedbackGhListIssues, feedbackGhComment = prevStore, prevList, prevComment
		whatsNewFetchChangelog = prevCL
	})
	return e
}

// grantAgentConsent sets [feedback] agent_reports = true in the isolated
// config, as `agent-deck feedback agent-consent` does after a person's y.
func grantAgentConsent(t *testing.T) {
	t.Helper()
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	cfg.Feedback.AgentReports = true
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()
}

// recordAgentReport stores #n as filed by an agent from this machine.
func recordAgentReport(t *testing.T, le *loopEnv, n int) {
	t.Helper()
	if err := le.store.Record(reportloop.Report{Number: n, Title: "[bug] fork is slow", Kind: "bug", FiledVersion: "1.16.36", FromAgent: true}); err != nil {
		t.Fatal(err)
	}
}

// agentIssueBody is a body carrying the footer an agent issue has.
const agentIssueBody = "### agent-deck version\n\n1.16.33\n\nFound by an agent session (agent-deck --from-agent).\n"

func (e *loopEnv) state(t *testing.T) *reportloop.State {
	t.Helper()
	st, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func agentBugArgs(home string) []string {
	return []string{"--from-agent", "--title", "Fork takes over five minutes",
		"--what", "forking " + filepath.Join(home, "work", "big-repo") + " hung; token " + fakeSecret,
		"--expected", "fork in seconds", "--steps", "1. fork a large session"}
}

// ── feedback bug --from-agent ─────────────────────────────────────────

func TestFeedbackFromAgent_RefusesOutsideAnAgentSession(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	out, code := runReport(t, issuereport.KindBug, "y\n", append(agentBugArgs(le.home), "--json")...)
	if code == 0 {
		t.Fatalf("want refusal without AGENTDECK_INSTANCE_ID or --yes\n%s", out)
	}
	if m := decodeReportJSON(t, out); m["error_code"] != "invalid_input" {
		t.Fatalf("want invalid_input, got %v", m)
	}
	if env.calls() != "" || len(le.searches) != 0 || len(le.comments) != 0 {
		t.Fatalf("a refused report must not call gh: calls=%q searches=%v", env.calls(), le.searches)
	}
}

func TestFeedbackFromAgent_FilesScrubbedIssueWithLabelAndFooter(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-123")
	out, code := runReport(t, issuereport.KindBug, "", append(agentBugArgs(le.home), "--json")...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := decodeReportJSON(t, out)
	if m["ok"] != true || m["number"] != float64(4242) || m["deduped"] != false || m["method"] != "gh" ||
		m["url"] != "https://github.com/asheshgoplani/agent-deck/issues/4242" {
		t.Fatalf("unexpected JSON: %v", m)
	}
	calls := env.calls()
	if !strings.Contains(calls, "issue create") || !strings.Contains(calls, "--label found-by-agent") {
		t.Fatalf("want gh issue create with the found-by-agent label, calls:\n%s", calls)
	}
	if len(le.searches) != 1 || !strings.Contains(le.searches[0], "in:title") || !strings.Contains(strings.ToLower(le.searches[0]), "fork takes over five minutes") ||
		!strings.Contains(le.searches[0], `"Found by an agent session" in:body`) {
		t.Fatalf("want one dedupe search on the footer and the title, got %v", le.searches)
	}
	body, ok := env.sentBody()
	if !ok {
		t.Fatal("no body was sent")
	}
	if !strings.Contains(body, "Found by an agent session (agent-deck --from-agent).") {
		t.Errorf("body lacks the agent footer:\n%s", body)
	}
	if strings.Contains(body, fakeSecret) || strings.Contains(body, le.home) {
		t.Errorf("posted body carries private text:\n%s", body)
	}
	if !strings.Contains(body, "~/work/big-repo") {
		t.Errorf("home path should be posted as ~:\n%s", body)
	}
	for _, o := range env.fileOpts {
		if o.OpenBrowser {
			t.Error("an agent report must never open a browser")
		}
	}
	st := le.state(t)
	r := st.Find(4242)
	if r == nil || !r.FromAgent || r.Deduped || r.FiledVersion != "1.16.33" || !strings.Contains(r.Title, "Fork takes over five minutes") {
		t.Fatalf("report not recorded as an agent report: %+v", st.Reports)
	}
}

func TestFeedbackFromAgent_YesWorksOutsideASession(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	out, code := runReport(t, issuereport.KindBug, "", append(agentBugArgs(le.home), "--yes")...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "https://github.com/asheshgoplani/agent-deck/issues/4242") {
		t.Fatalf("text output lacks the issue URL:\n%s", out)
	}
	if strings.Contains(out, "[y/N]") {
		t.Fatalf("--from-agent must never prompt:\n%s", out)
	}
}

func TestFeedbackFromAgent_DedupesNearTitleMatch(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	le.list = []ghIssueRef{
		{Number: 12, Title: "[bug] Something else entirely", URL: "https://github.com/asheshgoplani/agent-deck/issues/12", Body: agentIssueBody},
		{Number: 76, Title: "[bug] Fork takes over five minutes", URL: "https://github.com/asheshgoplani/agent-deck/issues/76", Body: "a person's issue, no agent footer"},
		{Number: 77, Title: "[bug] Fork takes over five minutes!", URL: "https://github.com/asheshgoplani/agent-deck/issues/77", Body: agentIssueBody},
	}
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-123")
	out, code := runReport(t, issuereport.KindBug, "", append(agentBugArgs(le.home), "--json")...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := decodeReportJSON(t, out)
	if m["ok"] != true || m["number"] != float64(77) || m["deduped"] != true || m["method"] != "gh" {
		t.Fatalf("unexpected JSON: %v", m)
	}
	if strings.Contains(env.calls(), "issue create") {
		t.Fatal("a duplicate must be a comment, not a new issue")
	}
	if len(le.comments) != 1 || le.comments[0].number != 77 {
		t.Fatalf("want one comment on #77, got %+v", le.comments)
	}
	c := le.comments[0].body
	want := "Seen again on v1.16.33 (" + runtime.GOOS + "/" + runtime.GOARCH + ") by an agent session."
	if !strings.HasPrefix(c, want) {
		t.Errorf("comment must start with %q:\n%s", want, c)
	}
	if !strings.Contains(c, "~/work/big-repo") || strings.Contains(c, fakeSecret) || strings.Contains(c, le.home) {
		t.Errorf("comment must carry the scrubbed what-happened only:\n%s", c)
	}
	r := le.state(t).Find(77)
	if r == nil || !r.Deduped || !r.FromAgent {
		t.Fatalf("dedupe not recorded: %+v", le.state(t).Reports)
	}
}

func TestFeedbackFromAgent_NoGhIsGhFailedNeverBrowser(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present bool
		auth    string
	}{{"no gh", false, "ok"}, {"gh logged out", true, "fail"}} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupReportTest(t, tc.present, tc.auth, "ok")
			le := setupLoopTest(t)
			grantAgentConsent(t)
			t.Setenv("AGENTDECK_INSTANCE_ID", "sess-123")
			out, code := runReport(t, issuereport.KindBug, "", append(agentBugArgs(le.home), "--json")...)
			if code == 0 {
				t.Fatalf("want failure\n%s", out)
			}
			if m := decodeReportJSON(t, out); m["error_code"] != "gh_failed" || m["url"] != nil {
				t.Fatalf("want gh_failed without a browser URL, got %v", m)
			}
			if len(env.fileOpts) != 0 || len(le.comments) != 0 {
				t.Fatal("nothing may be filed without gh")
			}
		})
	}
}

func TestFeedbackFromAgentTitleMatch(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"[bug] Fork takes over five minutes", "fork takes over five minutes", true},
		{"[BUG]  Fork: takes over five-minutes!!", "Fork takes over five minutes", true},
		{"[bug] [bug] Fork takes over five minutes", "[BUG] fork takes over five minutes", true},
		{"Fork takes over five minutes", "Fork is slow", false},
		{"[bug] Cookie auth fails", "[bug] Cookie auth works", false},
		{"", "", false},
		// Near matches are different problems: only exact normalized titles match.
		{"Queued message stuck after restart of target session", "queued message stuck after restart of the target session", false},
		{"[bug] Fork is slow", "[bug] Fork is not slow", false},
		{"[bug] Session starts on boot", "[bug] Session does not start on boot", false},
		{"[bug] Status shows running", "[bug] Status shows not running", false},
		{"[bug] Copy works with mouse", "[bug] Copy fails with mouse", false},
		// Same title, different kind.
		{"[bug] Dark mode", "[feature] Dark mode", false},
	} {
		if got := sameReportTitle(tc.a, tc.b); got != tc.want {
			t.Errorf("sameReportTitle(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// ── feedback mine ─────────────────────────────────────────────────────

func TestFeedbackMine_HumanGhFilingIsRecorded(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	out, code := runReport(t, issuereport.KindBug, "", append([]string{"--json", "--yes"}, bugArgs...)...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r := le.state(t).Find(4242)
	if r == nil || r.FromAgent || r.URL != "https://github.com/asheshgoplani/agent-deck/issues/4242" || r.Kind != "bug" {
		t.Fatalf("human gh filing not recorded: %+v", le.state(t).Reports)
	}
	var buf bytes.Buffer
	if code := runFeedbackMine(context.Background(), []string{"--json"}, "1.16.38", &buf); code != 0 {
		t.Fatalf("mine exit %d\n%s", code, buf.String())
	}
	m := decodeReportJSON(t, buf.String())
	reps, _ := m["reports"].([]any)
	if m["ok"] != true || len(reps) != 1 || reps[0].(map[string]any)["number"] != float64(4242) {
		t.Fatalf("mine --json: %v", m)
	}
}

func TestFeedbackMine_ListsReportsAndState(t *testing.T) {
	le := setupLoopTest(t)
	at := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	for _, r := range []reportloop.Report{
		{Number: 9001, Title: "[bug] queued message stuck", Kind: "bug", FiledVersion: "1.16.36", FromAgent: true, FiledAt: at},
		{Number: 9002, Title: "[bug] fork is slow", Kind: "bug", FiledVersion: "1.16.36", FiledAt: at},
	} {
		if err := le.store.Record(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := le.store.WhatsNew(loopChangelog, "1.16.38", "1.16.36"); err != nil {
		t.Fatal(err)
	}
	if err := le.store.RecordFeedback(9001, "yes", "1.16.37"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if code := runFeedbackMine(context.Background(), nil, "1.16.38", &buf); code != 0 {
		t.Fatalf("exit %d\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"#9001", "[bug] queued message stuck", "filed in v1.16.36", "fixed in v1.16.37", "your answer: yes",
		"#9002", "[bug] fork is slow", "fixed in v1.16.38"} {
		if !strings.Contains(out, want) {
			t.Errorf("mine output lacks %q:\n%s", want, out)
		}
	}
	buf.Reset()
	if code := runFeedbackMine(context.Background(), []string{"--json"}, "1.16.38", &buf); code != 0 {
		t.Fatalf("exit %d", code)
	}
	m := decodeReportJSON(t, buf.String())
	reps := m["reports"].([]any)
	first := reps[0].(map[string]any)
	if first["fixed_in"] != "1.16.37" || first["filed_version"] != "1.16.36" || first["feedback"].(map[string]any)["answer"] != "yes" {
		t.Fatalf("mine --json row: %v", first)
	}
}

func TestFeedbackMine_Empty(t *testing.T) {
	setupLoopTest(t)
	var buf bytes.Buffer
	if code := runFeedbackMine(context.Background(), nil, "1.16.38", &buf); code != 0 || !strings.Contains(buf.String(), "No reports filed from this machine yet.") {
		t.Fatalf("exit %d\n%s", code, buf.String())
	}
	buf.Reset()
	if code := runFeedbackMine(context.Background(), []string{"--json"}, "1.16.38", &buf); code != 0 || !strings.Contains(buf.String(), `"reports": []`) {
		t.Fatalf("empty --json must carry an empty list: %s", buf.String())
	}
}

// ── feedback fixed ────────────────────────────────────────────────────

func runFixed(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	var out strings.Builder
	code := runFeedbackFixed(context.Background(), args, "1.16.38", strings.NewReader(stdin), &out)
	return out.String(), code
}

func fixedBody(answer, comment string) string {
	head := "Fixed for me in v1.16.38."
	if answer == "no" {
		head = "Still broken for me in v1.16.38."
	}
	b := head
	if comment != "" {
		b += "\n\n" + comment
	}
	return b + "\n\n(" + runtime.GOOS + "/" + runtime.GOARCH + ", via agent-deck feedback fixed)"
}

func recordFixable(t *testing.T, le *loopEnv) {
	t.Helper()
	if err := le.store.Record(reportloop.Report{Number: 9002, Title: "[bug] fork is slow", Kind: "bug", FiledVersion: "1.16.36"}); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackFixed_JSONNeedsYes(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordFixable(t, le)
	out, code := runFixed(t, "y\n", "9002", "--fixed", "--json")
	if code == 0 || decodeReportJSON(t, out)["error_code"] != "confirm_required" {
		t.Fatalf("want confirm_required, exit %d\n%s", code, out)
	}
	if len(le.comments) != 0 {
		t.Fatal("posted without --yes")
	}
}

func TestFeedbackFixed_DryRunJSONShowsScrubbedBody(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordFixable(t, le)
	comment := "works now in " + filepath.Join(le.home, "proj") + " " + fakeSecret
	out, code := runFixed(t, "", "9002", "--not-fixed", "--comment", comment, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := decodeReportJSON(t, out)
	scrubbed := strings.TrimSpace(issuereport.Scrub(comment, le.home))
	if m["ok"] != true || m["dry_run"] != true || m["number"] != float64(9002) || m["body"] != fixedBody("no", scrubbed) {
		t.Fatalf("unexpected dry run: %v\nwant body %q", m, fixedBody("no", scrubbed))
	}
	if strings.Contains(out, fakeSecret) || strings.Contains(out, le.home) {
		t.Fatalf("dry run carries private text:\n%s", out)
	}
	if len(le.comments) != 0 || le.state(t).Find(9002).Feedback != nil {
		t.Fatal("a dry run must post and record nothing")
	}
}

func TestFeedbackFixed_InteractiveShowsDisclosureThenPosts(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordFixable(t, le)
	defer withFeedbackLogin(t, "octocat")()
	out, code := runFixed(t, "y\n", "9002", "--fixed", "--comment", "fork took 3 seconds")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	body := fixedBody("yes", "fork took 3 seconds")
	for _, want := range []string{"PUBLICLY", "https://github.com/asheshgoplani/agent-deck/issues/9002", "@octocat",
		"Exact content that will be posted", "    Fixed for me in v1.16.38.", "[y/N]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(le.comments) != 1 || le.comments[0].number != 9002 || le.comments[0].body != body {
		t.Fatalf("want one comment on #9002 with the shown body, got %+v", le.comments)
	}
	fb := le.state(t).Find(9002).Feedback
	if fb == nil || fb.Answer != "yes" || fb.Version != "1.16.38" {
		t.Fatalf("answer not recorded: %+v", fb)
	}
}

func TestFeedbackFixed_InteractiveDeclinePostsNothing(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordFixable(t, le)
	defer withFeedbackLogin(t, "octocat")()
	out, code := runFixed(t, "\n", "9002", "--not-fixed")
	if code != 0 || !strings.Contains(out, "Not posted.") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if len(le.comments) != 0 || le.state(t).Find(9002).Feedback != nil {
		t.Fatal("declined answer was posted or recorded")
	}
}

func TestFeedbackFixed_FromAgent(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	recordAgentReport(t, le, 9002)
	out, code := runFixed(t, "y\n", "9002", "--not-fixed", "--from-agent", "--json")
	if code == 0 || decodeReportJSON(t, out)["error_code"] != "invalid_input" {
		t.Fatalf("--from-agent outside a session without --yes must refuse, exit %d\n%s", code, out)
	}
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-1")
	out, code = runFixed(t, "", "9002", "--not-fixed", "--from-agent", "--comment", "still slow", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := decodeReportJSON(t, out)
	if m["ok"] != true || m["number"] != float64(9002) || m["answer"] != "no" || m["url"] == nil {
		t.Fatalf("unexpected JSON: %v", m)
	}
	if len(le.comments) != 1 || le.comments[0].body != fixedBody("no", "still slow") {
		t.Fatalf("comments: %+v", le.comments)
	}
	if fb := le.state(t).Find(9002).Feedback; fb == nil || fb.Answer != "no" {
		t.Fatalf("answer not recorded: %+v", fb)
	}
}

func TestFeedbackFixed_UnknownNumberPostsWithNote(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	defer withFeedbackLogin(t, "")()
	out, code := runFixed(t, "y\n", "555", "--fixed")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if len(le.comments) != 1 || le.comments[0].number != 555 {
		t.Fatalf("confirmed answer on an unknown number must still post: %+v", le.comments)
	}
	if !strings.Contains(out, "#555 is not one of this machine's reports") {
		t.Fatalf("missing note:\n%s", out)
	}
}

func TestFeedbackFixed_InvalidInput(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	for _, args := range [][]string{
		{"9002", "--json", "--yes"},
		{"9002", "--fixed", "--not-fixed", "--json", "--yes"},
		{"--fixed", "--json", "--yes"},
		{"abc", "--fixed", "--json", "--yes"},
		{"-3", "--fixed", "--json", "--yes"},
	} {
		out, code := runFixed(t, "", args...)
		if code == 0 || decodeReportJSON(t, out)["error_code"] != "invalid_input" {
			t.Errorf("%v: want invalid_input, exit %d\n%s", args, code, out)
		}
	}
	if len(le.comments) != 0 {
		t.Fatal("invalid input posted")
	}
}

func TestFeedbackFixed_NoGhIsGhFailed(t *testing.T) {
	setupReportTest(t, false, "ok", "ok")
	le := setupLoopTest(t)
	out, code := runFixed(t, "", "9002", "--fixed", "--json", "--yes")
	if code == 0 || decodeReportJSON(t, out)["error_code"] != "gh_failed" || len(le.comments) != 0 {
		t.Fatalf("want gh_failed, exit %d\n%s", code, out)
	}
}
