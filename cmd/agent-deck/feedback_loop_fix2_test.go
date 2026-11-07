package main

import (
	"io"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
	"github.com/asheshgoplani/agent-deck/internal/reportloop"
)

// ASK-205 re-review fixes (N1, N2, N5).

// withTerminalStdin makes readerIsTerminal report a terminal on stdin.
func withTerminalStdin(t *testing.T) {
	t.Helper()
	prev := readerIsTerminal
	readerIsTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { readerIsTerminal = prev })
}

// ── N1: in a session with no terminal, a person-path post is an agent's ──

func TestFeedbackInSessionPersonPath_NeedsConsent(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	if err := le.store.Record(reportloop.Report{Number: 4242, Title: "[bug] probe", Kind: "bug", FiledVersion: "1.16.36"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTDECK_INSTANCE_ID", "x")
	previewed := `{"title":"[bug] probe","labels":["bug"],"body":"### What happened\n\nx\n"}`
	for _, tc := range []struct {
		name string
		run  func() (string, int)
	}{
		{"bug --json --yes", func() (string, int) {
			return runReport(t, issuereport.KindBug, "", "--json", "--yes", "--title", "probe", "--what", "x")
		}},
		{"bug --issue - --json --yes", func() (string, int) {
			return runReport(t, issuereport.KindBug, previewed, "--issue", "-", "--json", "--yes")
		}},
		{"fixed --json --yes", func() (string, int) { return runFixed(t, "", "4242", "--fixed", "--json", "--yes") }},
	} {
		out, code := tc.run()
		m := decodeReportJSON(t, out)
		if code == 0 || m["error_code"] != errCodeConsentRequired || !strings.Contains(m["message"].(string), "agent-deck feedback agent-consent") {
			t.Errorf("%s: want consent_required naming the opt-in, exit %d\n%s", tc.name, code, out)
		}
	}
	out, code := runFixed(t, "y\n", "4242", "--fixed")
	if code == 0 || !strings.Contains(out, errCodeConsentRequired) || strings.Contains(out, "[y/N]") {
		t.Errorf("text fixed in a session: want consent_required and no prompt, exit %d\n%s", code, out)
	}
	if strings.Contains(env.calls(), "issue create") || len(env.fileOpts) != 0 || len(le.comments) != 0 || len(le.searches) != 0 {
		t.Fatalf("nothing may be posted: calls=%q files=%d comments=%v searches=%v", env.calls(), len(env.fileOpts), le.comments, le.searches)
	}
	if r := le.state(t).Find(4242); r.Feedback != nil {
		t.Fatalf("no answer may be recorded: %+v", r.Feedback)
	}
	// A dry run sends nothing, so it still works in a session.
	if out, code := runReport(t, issuereport.KindBug, "", "--json", "--dry-run", "--title", "probe", "--what", "x"); code != 0 || decodeReportJSON(t, out)["ok"] != true {
		t.Fatalf("dry run in a session: exit %d\n%s", code, out)
	}
}

func TestFeedbackInSessionPersonPath_WithConsentIsAgentReport(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	grantAgentConsent(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "x")
	out, code := runReport(t, issuereport.KindBug, "", "--json", "--yes", "--title", "probe title", "--what", "x happened")
	if code != 0 {
		t.Fatalf("exit %d\n%s\ncalls:\n%s", code, out, env.calls())
	}
	if len(le.searches) != 1 {
		t.Fatalf("an agent report is de-duplicated first: searches=%v", le.searches)
	}
	body, _ := env.sentBody()
	if strings.Contains(body, "log tail line") || !strings.Contains(body, agentFooter) {
		t.Fatalf("want the agent footer and no debug.log tail:\n%s", body)
	}
	if !strings.Contains(env.calls(), agentLabel) {
		t.Fatalf("want the %s label:\n%s", agentLabel, env.calls())
	}
	st := le.state(t)
	r := st.Find(4242)
	if r == nil || !r.FromAgent {
		t.Fatalf("recorded as an agent report: %+v", st.Reports)
	}
	// feedback fixed: an agent answers only its own reports.
	if err := le.store.Record(reportloop.Report{Number: 9002, Title: "[bug] person", Kind: "bug", FiledVersion: "1.16.36"}); err != nil {
		t.Fatal(err)
	}
	out, code = runFixed(t, "", "9002", "--fixed", "--json", "--yes")
	if code == 0 || decodeReportJSON(t, out)["error_code"] != "invalid_input" || len(le.comments) != 0 {
		t.Fatalf("a person's report is not answered from a session: exit %d\n%s", code, out)
	}
	out, code = runFixed(t, "", "4242", "--fixed", "--json", "--yes")
	if code != 0 || len(le.comments) != 1 || le.comments[0].number != 4242 {
		t.Fatalf("own agent report: exit %d comments=%v\n%s", code, le.comments, out)
	}
}

// The Mac app runs --json --yes from a GUI process without the session env.
func TestFeedbackMacAppPathWithoutSessionEnvStillPosts(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordFixable(t, le)
	out, code := runReport(t, issuereport.KindBug, "", append([]string{"--json", "--yes"}, bugArgs...)...)
	if code != 0 || decodeReportJSON(t, out)["ok"] != true {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if body, _ := env.sentBody(); !strings.Contains(body, "log tail line") || strings.Contains(body, agentFooter) {
		t.Fatalf("a person's report keeps the log tail and has no agent footer:\n%s", body)
	}
	if out, code := runFixed(t, "", "9002", "--fixed", "--json", "--yes"); code != 0 || len(le.comments) != 1 {
		t.Fatalf("fixed: exit %d\n%s", code, out)
	}
}

// ── N2: turning agent reports on needs a terminal ──────────────────────

func TestFeedbackAgentConsent_RefusesWithoutTerminal(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	setupLoopTest(t)
	for _, inSession := range []string{"", "sess-1"} {
		t.Setenv("AGENTDECK_INSTANCE_ID", inSession)
		out, code := runConsent(t, "y\n")
		if code == 0 || !strings.Contains(out, errCodeConfirmRequired) || configAgentReports(t) {
			t.Fatalf("session=%q: piped y must leave agent_reports off with confirm_required: exit %d\n%s", inSession, code, out)
		}
	}
	// --off needs no terminal.
	if out, code := runConsent(t, "", "--off"); code != 0 {
		t.Fatalf("--off: exit %d\n%s", code, out)
	}
}

// ── N5: the dry run runs the answer checks ─────────────────────────────

func TestFeedbackFixed_DryRunReportsRefusal(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	recordFixable(t, le)
	if err := le.store.RecordFeedback(9002, "yes", "1.16.38"); err != nil {
		t.Fatal(err)
	}
	out, code := runFixed(t, "", "9002", "--not-fixed", "--dry-run", "--json")
	m := decodeReportJSON(t, out)
	if code == 0 || m["ok"] != false || m["error_code"] != "invalid_input" || !strings.Contains(m["message"].(string), "--again") {
		t.Fatalf("an answered report: the dry run must report the refusal: exit %d\n%s", code, out)
	}
	if out, code := runFixed(t, "", "9002", "--not-fixed", "--dry-run", "--json", "--again"); code != 0 || decodeReportJSON(t, out)["ok"] != true {
		t.Fatalf("--again dry run: exit %d\n%s", code, out)
	}
	out, code = runFixed(t, "", "9002", "--not-fixed", "--dry-run", "--json", "--from-agent")
	if code == 0 || decodeReportJSON(t, out)["error_code"] != "invalid_input" {
		t.Fatalf("--from-agent on a person's report: the dry run must refuse: exit %d\n%s", code, out)
	}
	out, code = runFixed(t, "", "9002", "--not-fixed", "--dry-run")
	if code == 0 || !strings.Contains(out, "--again") {
		t.Fatalf("text dry run must say it would be refused: exit %d\n%s", code, out)
	}
	if len(le.comments) != 0 {
		t.Fatal("a dry run posted")
	}
}

// ── pseudo-terminal: the session env alone decides (re-review round 3) ──

// An agent can run the CLI under a pseudo-terminal (script -q, or a tool
// whose shell is a pty). Inside a session the env alone makes a post an
// agent's, and consent cannot be granted from there.
func TestFeedbackInSessionWithTerminal_StillAgentPost(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	le := setupLoopTest(t)
	withTerminalStdin(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "x")
	out, code := runReport(t, issuereport.KindBug, "y\n", "--yes", "--no-open", "--title", "probe", "--what", "x", "--expected", "y", "--steps", "z")
	if code == 0 || !strings.Contains(out, errCodeConsentRequired) {
		t.Fatalf("pty in a session without consent: want consent_required, exit %d\n%s", code, out)
	}
	if strings.Contains(env.calls(), "issue create") || len(env.fileOpts) != 0 || len(le.comments) != 0 {
		t.Fatalf("nothing may be posted: calls=%q", env.calls())
	}
}

func TestFeedbackAgentConsent_RefusedInSessionEvenAtTerminal(t *testing.T) {
	setupReportTest(t, true, "ok", "ok")
	setupLoopTest(t)
	withTerminalStdin(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-1")
	out, code := runConsent(t, "y\n")
	if code == 0 || configAgentReports(t) {
		t.Fatalf("consent from inside a session must be refused: exit %d\n%s", code, out)
	}
}
