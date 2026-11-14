package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
	"github.com/asheshgoplani/agent-deck/internal/reportloop"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ASK-205 review fixes: consent, agent-only answers, label fallback,
// dedupe, non-interactive safety and the rest.

func configAgentReports(t *testing.T) bool {
	t.Helper()
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	return cfg.Feedback.AgentReports
}

func runConsent(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	var out strings.Builder
	code := runFeedbackAgentConsent(context.Background(), args, strings.NewReader(stdin), &out)
	return out.String(), code
}

// ── 1. consent ───────────────────────────────────────────────────────

func TestFeedbackFromAgent_NeedsConsent(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordAgentReport(t, le, 9002)
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-1")
	for _, tc := range []struct {
		name string
		run  func() (string, int)
	}{
		{"bug in a session", func() (string, int) {
			return runReport(t, issuereport.KindBug, "", append(agentBugArgs(le.home), "--json")...)
		}},
		{"bug with --yes", func() (string, int) {
			return runReport(t, issuereport.KindBug, "", append(agentBugArgs(le.home), "--json", "--yes")...)
		}},
		{"fixed", func() (string, int) { return runFixed(t, "", "9002", "--fixed", "--from-agent", "--json") }},
		{"fixed with --yes", func() (string, int) { return runFixed(t, "", "9002", "--fixed", "--from-agent", "--json", "--yes") }},
	} {
		out, code := tc.run()
		m := decodeReportJSON(t, out)
		if code == 0 || m["error_code"] != "consent_required" || !strings.Contains(m["message"].(string), "agent-deck feedback agent-consent") {
			t.Errorf("%s: want consent_required naming the opt-in command, exit %d\n%s", tc.name, code, out)
		}
	}
	if strings.Contains(env.calls(), "issue create") || len(le.searches) != 0 || len(le.comments) != 0 {
		t.Fatalf("nothing may reach gh without consent: calls=%q searches=%v comments=%v", env.calls(), le.searches, le.comments)
	}
}

func TestFeedbackAgentConsent_DisclosureThenSetsKey(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	setupLoopTest(t)
	withTerminalStdin(t) // a person at a terminal answers
	defer withFeedbackLogin(t, "octocat")()
	out, code := runConsent(t, "\n")
	if code != 0 || configAgentReports(t) {
		t.Fatalf("Enter must decline and leave agent_reports off: exit %d\n%s", code, out)
	}
	for _, want := range []string{"PUBLIC", "https://github.com/asheshgoplani/agent-deck/issues", "@octocat",
		"found-by-agent", "no log", "[y/N]"} {
		if !strings.Contains(out, want) {
			t.Errorf("disclosure lacks %q:\n%s", want, out)
		}
	}
	out, code = runConsent(t, "y\n")
	if code != 0 || !configAgentReports(t) {
		t.Fatalf("y must set [feedback] agent_reports = true: exit %d\n%s", code, out)
	}
	out, code = runConsent(t, "", "--off")
	if code != 0 || configAgentReports(t) {
		t.Fatalf("--off must turn agent reports off: exit %d\n%s", code, out)
	}
}

func TestFeedbackFromAgent_PostsNoLogTail(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-1")
	if out, code := runReport(t, issuereport.KindBug, "", append(agentBugArgs(le.home), "--json")...); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	body, _ := env.sentBody()
	if strings.Contains(body, "log tail line") || !strings.Contains(body, "Linux (Test OS)") {
		t.Fatalf("an agent report carries version/OS/terminal, never the debug.log tail:\n%s", body)
	}
}

// ── 2. feedback fixed: agents answer only their own reports, once ────

func TestFeedbackFixed_FromAgentOnlyOnOwnUnansweredAgentReports(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-1")
	recordFixable(t, le) // #9002, filed by a person
	recordAgentReport(t, le, 9010)
	recordAgentReport(t, le, 9011)
	if err := le.store.RecordFeedback(9011, "yes", "1.16.38"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"555", "9002", "9011"} {
		out, code := runFixed(t, "", n, "--fixed", "--from-agent", "--json")
		if code == 0 || decodeReportJSON(t, out)["error_code"] != "invalid_input" {
			t.Errorf("#%s: want invalid_input, exit %d\n%s", n, code, out)
		}
	}
	if len(le.comments) != 0 {
		t.Fatalf("posted: %+v", le.comments)
	}
	if out, code := runFixed(t, "", "9010", "--fixed", "--from-agent", "--json"); code != 0 {
		t.Fatalf("own unanswered agent report: exit %d\n%s", code, out)
	}
	if len(le.comments) != 1 || le.comments[0].number != 9010 {
		t.Fatalf("comments: %+v", le.comments)
	}
}

func TestFeedbackFixed_SecondAnswerNeedsAgain(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordFixable(t, le)
	if err := le.store.RecordFeedback(9002, "yes", "1.16.38"); err != nil {
		t.Fatal(err)
	}
	out, code := runFixed(t, "", "9002", "--not-fixed", "--json", "--yes")
	if m := decodeReportJSON(t, out); code == 0 || m["error_code"] != "invalid_input" || !strings.Contains(m["message"].(string), "--again") {
		t.Fatalf("a second answer must be refused naming --again, exit %d\n%s", code, out)
	}
	if len(le.comments) != 0 {
		t.Fatal("second answer posted")
	}
	out, code = runFixed(t, "", "9002", "--not-fixed", "--json", "--yes", "--again")
	if code != 0 || len(le.comments) != 1 {
		t.Fatalf("--again must post: exit %d\n%s", code, out)
	}
	if fb := le.state(t).Find(9002).Feedback; fb == nil || fb.Answer != "no" {
		t.Fatalf("new answer not recorded: %+v", fb)
	}
}

// ── 3. label fallback and footer-based dedupe search ─────────────────

func TestFeedbackFromAgent_RetriesWithoutLabelsWhenLabelMissing(t *testing.T) {
	env := setupReportTest(t, true, "ok", "labelfail")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-1")
	out, code := runReport(t, issuereport.KindBug, "", append(agentBugArgs(le.home), "--json")...)
	if code != 0 {
		t.Fatalf("exit %d\n%s\ncalls:\n%s", code, out, env.calls())
	}
	if m := decodeReportJSON(t, out); m["ok"] != true || m["number"] != float64(4242) {
		t.Fatalf("unexpected JSON: %v", m)
	}
	var creates []string
	for _, l := range strings.Split(env.calls(), "\n") {
		if strings.HasPrefix(l, "issue create") {
			creates = append(creates, l)
		}
	}
	if len(creates) != 2 || !strings.Contains(creates[0], "--label") || strings.Contains(creates[1], "--label") {
		t.Fatalf("want one labelled create, then one retry without labels:\n%s", strings.Join(creates, "\n"))
	}
	if body, _ := env.sentBody(); !strings.Contains(body, agentFooter) {
		t.Fatalf("the retry keeps the footer:\n%s", body)
	}
}

func TestFeedbackFromAgent_DedupeSearchUsesFooterNotLabel(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	_, _ = ghListAgentIssues(context.Background(), agentDedupeSearch("[bug] Fork takes over five minutes"))
	calls := env.calls()
	if !strings.Contains(calls, "issue list") || strings.Contains(calls, "--label") {
		t.Fatalf("the dedupe list must not filter on the label:\n%s", calls)
	}
	if !strings.Contains(calls, `"Found by an agent session" in:body`) || !strings.Contains(calls, "fork takes over five minutes in:title") {
		t.Fatalf("the dedupe search must name the footer and the title:\n%s", calls)
	}
}

// ── 5. unattended update on an empty store keeps the range pending ───

func TestUpdatePrintsWhatsNew_UnattendedEmptyStoreKeepsRangePending(t *testing.T) {
	le := setupWhatsNewTest(t)
	var buf bytes.Buffer
	printWhatsNewAfterUpdate(context.Background(), &buf, "1.16.36", "1.16.38", false)
	if got := le.state(t).LastSeenVersion; got != "1.16.36" {
		t.Fatalf("an empty store must remember the version updated from, got %q", got)
	}
	out, code := runWN(t, "1.16.38")
	if code != 0 || !strings.Contains(out, "What's new in v1.16.38") {
		t.Fatalf("(1.16.36, 1.16.38] must still be shown on the next start: exit %d\n%s", code, out)
	}
}

// ── 7. the "seen again" comment is capped ────────────────────────────

func TestFeedbackFromAgent_SeenAgainCommentIsCapped(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-1")
	le.list = []ghIssueRef{{Number: 77, Title: "[bug] Fork takes over five minutes", Body: agentIssueBody}}
	long := strings.Repeat("the fork hung again ", 1000) // 20000 bytes
	out, code := runReport(t, issuereport.KindBug, "", "--from-agent", "--title", "Fork takes over five minutes", "--what", long, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if len(le.comments) != 1 {
		t.Fatalf("comments: %d", len(le.comments))
	}
	c := le.comments[0].body
	if len([]rune(c)) > 4300 || !strings.Contains(c, "(trimmed)") {
		t.Fatalf("seen-again comment must be capped near 4000 chars and marked (trimmed); got %d chars", len([]rune(c)))
	}
}

// ── 8. inside a session without a terminal, never prompt ─────────────

func TestFeedbackNonInteractiveInSessionNeverPrompts(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordFixable(t, le)
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-1")
	// Nobody can answer a prompt here, so a post is an agent's: without
	// [feedback] agent_reports it is refused with consent_required.
	out, code := runFixed(t, "y\n", "9002", "--fixed")
	if code == 0 || strings.Contains(out, "[y/N]") || !strings.Contains(out, "consent_required") || len(le.comments) != 0 {
		t.Fatalf("feedback fixed must refuse with consent_required, never prompt: exit %d\n%s", code, out)
	}
	out, code = runReport(t, issuereport.KindBug, "y\n", bugArgs...)
	if code == 0 || strings.Contains(out, "[y/N]") || !strings.Contains(out, "consent_required") || len(env.fileOpts) != 0 {
		t.Fatalf("feedback bug must refuse with consent_required, never prompt: exit %d\n%s", code, out)
	}
	out, code = runReport(t, issuereport.KindBug, "", "--json")
	if code == 0 || decodeReportJSON(t, out)["error_code"] == nil {
		t.Fatalf("--json without fields must still fail as data: exit %d\n%s", code, out)
	}
}

// ── 9. gh login runs with the gh env ─────────────────────────────────

func TestFeedbackGhUserLoginUsesGhEnv(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	if got := ghUserLogin(); got != "envlogin-1" {
		t.Fatalf("ghUserLogin must run gh with issuereport.GhEnv (GH_PROMPT_DISABLED=1), got %q", got)
	}
}

// ── 10. dry-run JSON names the account and gh readiness ──────────────

func TestFeedbackFixed_DryRunJSONHasAccountAndGhReady(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordFixable(t, le)
	defer withFeedbackLogin(t, "octocat")()
	out, _ := runFixed(t, "", "9002", "--fixed", "--dry-run", "--json")
	if m := decodeReportJSON(t, out); m["account"] != "octocat" || m["gh_ready"] != true {
		t.Fatalf("want account octocat and gh_ready true: %v", m)
	}

	setupReportTest(t, false, "ok", "ok")
	defer withFeedbackLogin(t, "")()
	out, _ = runFixed(t, "", "9002", "--fixed", "--dry-run", "--json")
	m := decodeReportJSON(t, out)
	if acc, ok := m["account"]; !ok || acc != "" || m["gh_ready"] != false {
		t.Fatalf("without gh: want account \"\" (present) and gh_ready false: %v", m)
	}
}

// ── 11. feedback mine reads fixed_in from the changelog ──────────────

func TestFeedbackMine_ComputesFixedInFromChangelog(t *testing.T) {
	le := setupWhatsNewTest(t)
	if err := le.store.Record(reportloop.Report{Number: 9002, Title: "[bug] fork is slow", FiledVersion: "1.16.36"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if code := runFeedbackMine(context.Background(), nil, "1.16.38", &buf); code != 0 || !strings.Contains(buf.String(), "fixed in v1.16.38") {
		t.Fatalf("mine must read fixed_in from the changelog: exit %d\n%s", code, buf.String())
	}
	buf.Reset()
	if code := runFeedbackMine(context.Background(), []string{"--json"}, "1.16.38", &buf); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if r := decodeReportJSON(t, buf.String())["reports"].([]any)[0].(map[string]any); r["fixed_in"] != "1.16.38" {
		t.Fatalf("mine --json fixed_in: %v", r)
	}
	// Not installed yet: running 1.16.37, the 1.16.38 fix is not shown.
	buf.Reset()
	if code := runFeedbackMine(context.Background(), nil, "1.16.37", &buf); code != 0 || strings.Contains(buf.String(), "fixed in") {
		t.Fatalf("a fix newer than the running version is not shown: exit %d\n%s", code, buf.String())
	}
}

// ── 12. contributing.md tells landers how a reporter learns of a fix ─

func TestFeedbackDocs_ContributingCitesIssueInFixes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "skills", "agent-deck", "references", "contributing.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"### Fixes", "### Fixed", "(#N)", "that is how a reporter learns it is fixed"} {
		if !strings.Contains(s, want) {
			t.Errorf("contributing.md lacks %q", want)
		}
	}
	cr, err := os.ReadFile(filepath.Join("..", "..", "skills", "agent-deck", "references", "config-reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cr), "agent_reports") {
		t.Error("config-reference.md does not document [feedback] agent_reports")
	}
}
