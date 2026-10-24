package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/issuereport"
)

// useRealDebugLog points the diagnostics at the real collector reading a
// debug.log under a temp XDG cache, and returns a function that appends a
// line to it.
func useRealDebugLog(t *testing.T, first string) (appendLine func(string)) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p, err := agentpaths.CachePath("debug.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(first+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	feedbackCollectDiagnostics = issuereport.CollectDiagnostics
	return func(l string) {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// MAJOR-2: --issue - files exactly the previewed title/body/labels, even
// when debug.log changed between the preview and the send.
func TestFeedbackReport_IssueFilesExactlyThePreview(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	appendLog := useRealDebugLog(t, "first log line seen in the preview")

	dry, code := runReport(t, issuereport.KindBug, "", append([]string{"--json", "--dry-run"}, bugArgs...)...)
	if code != 0 {
		t.Fatalf("dry run exit %d\n%s", code, dry)
	}
	preview := decodeReportJSON(t, dry)
	if !strings.Contains(preview["body"].(string), "first log line seen in the preview") {
		t.Fatalf("preview lacks the log line:\n%s", preview["body"])
	}

	appendLog("second log line written after the preview")
	feedbackCollectDiagnostics = func(string, int) issuereport.Diagnostics {
		t.Error("--issue rebuilt the issue (read diagnostics again)")
		return issuereport.Diagnostics{}
	}

	out, code := runReport(t, issuereport.KindBug, dry, "--yes", "--json", "--issue", "-")
	if code != 0 {
		t.Fatalf("send exit %d\n%s", code, out)
	}
	if m := decodeReportJSON(t, out); m["ok"] != true || m["method"] != "gh" || m["url"] != "https://github.com/asheshgoplani/agent-deck/issues/4242" {
		t.Fatalf("send output %v", m)
	}
	sent, ok := env.sentBody()
	if !ok {
		t.Fatal("no body reached gh")
	}
	if sent != preview["body"].(string) {
		t.Fatalf("gh got a body that differs from the preview\nsent: %q\nshown: %q", sent, preview["body"])
	}
	calls := env.calls()
	if !strings.Contains(calls, "--title "+preview["title"].(string)+" --body-file - --label bug --label triage") {
		t.Fatalf("gh args do not carry the previewed title/labels: %q", calls)
	}
}

// --issue <file> works too, and a file given to an interactive run is
// shown and needs y.
func TestFeedbackReport_IssueFromFile(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	is := issuereport.Build(issuereport.Report{Kind: issuereport.KindIdea, Title: "keep it", WhatHappened: "exact body"}, issuereport.Diagnostics{})
	data, _ := json.Marshal(map[string]any{"ok": true, "dry_run": true, "title": is.Title, "labels": is.Labels, "body": is.Body, "url_trimmed": false})
	path := filepath.Join(t.TempDir(), "issue.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runReport(t, issuereport.KindIdea, "n\n", "--issue", path)
	if code != 0 || !strings.Contains(out, is.Text()) || !strings.Contains(out, "Not sent.") {
		t.Fatalf("interactive --issue file: exit %d\n%s", code, out)
	}
	if _, sent := env.sentBody(); sent {
		t.Fatal("declined --issue still sent")
	}
	out, code = runReport(t, issuereport.KindIdea, "y\n", "--issue", path)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if sent, _ := env.sentBody(); sent != is.Body {
		t.Fatalf("sent %q, want %q", sent, is.Body)
	}
}

// MAJOR-2: --issue rejects anything that is not a previewed issue of this
// kind, before gh is touched.
func TestFeedbackReport_IssueInvalidInput(t *testing.T) {
	bugBody := issuereport.Build(issuereport.Report{Kind: issuereport.KindBug, WhatHappened: "x"}, issuereport.Diagnostics{}).Body
	ideaBody := issuereport.Build(issuereport.Report{Kind: issuereport.KindIdea, WhatHappened: "x"}, issuereport.Diagnostics{}).Body
	good := map[string]any{"title": "[bug] x", "labels": []string{"bug", "triage"}, "body": bugBody}
	var with func(k string, v any) string
	withBody := func(b string) string { return with("body", b) }
	with = func(k string, v any) string {
		m := map[string]any{}
		for kk, vv := range good {
			m[kk] = vv
		}
		m[k] = v
		b, _ := json.Marshal(m)
		return string(b)
	}
	cases := map[string]struct {
		kind  issuereport.Kind
		stdin string
		args  []string
	}{
		"wrong prefix":       {issuereport.KindBug, with("title", "[feature] x"), nil},
		"no prefix":          {issuereport.KindBug, with("title", "x"), nil},
		"empty title":        {issuereport.KindBug, with("title", ""), nil},
		"bug labels on idea": {issuereport.KindIdea, with("title", "[feature] x"), nil},
		"extra label":        {issuereport.KindBug, with("labels", []string{"bug", "triage", "security"}), nil},
		"no labels":          {issuereport.KindBug, with("labels", []string{}), nil},
		"empty body":         {issuereport.KindBug, with("body", ""), nil},
		"huge body":          {issuereport.KindBug, with("body", strings.Repeat("a", issuereport.MaxBodyLen+1)), nil},
		"not json":           {issuereport.KindBug, "title: x", nil},
		"mixed with --title": {issuereport.KindBug, with("title", "[bug] x"), []string{"--title", "other"}},
		// MINOR-N2 / NIT-N8
		"no template sections": {issuereport.KindBug, withBody("just some text\n"), nil},
		"idea sections on bug": {issuereport.KindBug, withBody(ideaBody), nil},
		"title over 256":       {issuereport.KindBug, with("title", "[bug] "+strings.Repeat("x", 300)), nil},
		"rescrub changes it":   {issuereport.KindBug, withBody(bugBody + "token " + "gh" + "p_" + strings.Repeat("aB3", 12) + "\n"), nil},
		"escape in title":      {issuereport.KindBug, with("title", "[bug] x \x1b]0;evil\x07"), nil},
		"mixed with --surface": {issuereport.KindBug, with("title", "[bug] x"), []string{"--surface", "tui"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := setupReportTest(t, true, "ok", "ok")
			out, code := runReport(t, tc.kind, tc.stdin, append([]string{"--yes", "--json", "--issue", "-"}, tc.args...)...)
			m := decodeReportJSON(t, out)
			if code != 1 || m["ok"] != false || m["error_code"] != "invalid_input" {
				t.Fatalf("exit %d %v", code, m)
			}
			if strings.Contains(env.calls(), "issue create") || len(env.fileOpts) != 0 {
				t.Fatalf("invalid --issue reached gh: %q", env.calls())
			}
		})
	}

	// Without --yes in --json mode it is still confirm_required.
	env := setupReportTest(t, true, "ok", "ok")
	out, code := runReport(t, issuereport.KindBug, with("title", "[bug] x"), "--json", "--issue", "-")
	if m := decodeReportJSON(t, out); code != 1 || m["error_code"] != "confirm_required" || env.calls() != "" {
		t.Fatalf("--issue without --yes: exit %d %v calls %q", code, m, env.calls())
	}
}

// MINOR-5: when gh is not ready and the URL had to be trimmed, the
// interactive preview and the dry run say the browser gets less text; the
// browser send JSON reports url_trimmed.
func TestFeedbackReport_TrimmedBrowserNote(t *testing.T) {
	setupReportTest(t, false, "", "")
	big := strings.Repeat("log line with some words in it\n", 600)
	feedbackCollectDiagnostics = func(v string, _ int) issuereport.Diagnostics {
		return issuereport.Diagnostics{Version: v, OS: "Linux", Arch: "amd64", LogExcerpt: big}
	}
	out, _ := runReport(t, issuereport.KindBug, "n\n", bugArgs...)
	if !strings.Contains(out, trimmedBrowserNote) || strings.Contains(out, "Full text (no more, no less)") {
		t.Fatalf("interactive preview does not say the browser text is trimmed:\n%s", out[len(out)-600:])
	}
	out, _ = runReport(t, issuereport.KindBug, "", append([]string{"--dry-run"}, bugArgs...)...)
	if !strings.Contains(out, trimmedBrowserNote) {
		t.Fatal("dry run lacks the trimmed note")
	}
	out, _ = runReport(t, issuereport.KindBug, "", append([]string{"--json", "--yes"}, bugArgs...)...)
	if m := decodeReportJSON(t, out); m["method"] != "browser" || m["url_trimmed"] != true {
		t.Fatalf("browser send JSON: %v", m)
	}

	// Not trimmed, or gh ready: no note.
	setupReportTest(t, true, "ok", "ok")
	feedbackCollectDiagnostics = func(v string, _ int) issuereport.Diagnostics {
		return issuereport.Diagnostics{Version: v, LogExcerpt: big}
	}
	out, _ = runReport(t, issuereport.KindBug, "n\n", bugArgs...)
	if strings.Contains(out, trimmedBrowserNote) || !strings.Contains(out, "Full text (no more, no less)") {
		t.Fatal("trimmed note shown although gh files the full text")
	}
}

// MINOR-6: Ctrl-C (ctx cancelled) at the y/N prompt or a field prompt
// returns promptly with "Not sent" and files nothing, while stdin is still
// blocked.
func TestFeedbackReport_InterruptAtPromptSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"confirm prompt", bugArgs},
		{"field prompt", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupReportTest(t, true, "ok", "ok")
			pr, pw := io.Pipe() // never written: a terminal nobody types into
			t.Cleanup(func() { pw.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			var out strings.Builder
			done := make(chan int, 1)
			go func() { done <- runFeedbackReport(ctx, issuereport.KindBug, tc.args, "1.16.33", pr, &out) }()
			time.Sleep(100 * time.Millisecond)
			cancel()
			select {
			case code := <-done:
				if code == 0 {
					t.Fatalf("interrupt exit code 0")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still waiting at the prompt 5s after Ctrl-C")
			}
			if !strings.Contains(out.String(), "Not sent.") {
				t.Fatalf("output lacks Not sent:\n%s", out.String())
			}
			if strings.Contains(env.calls(), "issue create") || len(env.fileOpts) != 0 {
				t.Fatal("interrupt filed the issue")
			}
		})
	}
}

// MINOR-N2: an --issue file shown at the interactive prompt is shown and
// filed rescrubbed (control characters and secrets removed), never raw.
func TestFeedbackReport_IssueFileInteractiveShowsRescrubbed(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	tok := "gh" + "p_" + strings.Repeat("aB3", 12)
	body := issuereport.Build(issuereport.Report{Kind: issuereport.KindBug, WhatHappened: "x"}, issuereport.Diagnostics{}).Body
	data, _ := json.Marshal(map[string]any{"title": "[bug] red \x1b[31mtext " + tok, "labels": []string{"bug", "triage"}, "body": body + tok + "\n"})
	path := filepath.Join(t.TempDir(), "issue.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runReport(t, issuereport.KindBug, "y\n", "--issue", path)
	if code != 0 || strings.Contains(out, tok) || strings.Contains(out, "\x1b") || !strings.Contains(out, "[bug] red text <token>") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if sent, _ := env.sentBody(); sent != body+"<token>\n" {
		t.Fatalf("sent %q", sent)
	}
}

// MAJOR-N11: ordinary reports with shell text (ls -la, wc -l, bash -lc,
// tmux send-keys -l) go through the Mac app path: dry-run --json, then
// --yes --json --issue - with that JSON. The send succeeds and gh gets
// exactly the previewed body.
func TestFeedbackReport_ShellTextPreviewSends(t *testing.T) {
	cases := []struct {
		name, log string
		args      []string
		keep      string
	}{
		{"ls -la", "", []string{"--what", "ls -la shows nothing in the worktree", "--steps", "1. ssh into the remote"}, "Actual: ls -la shows nothing in the worktree"},
		{"wc -l", "", []string{"--what", "wc -l notes.txt prints 0", "--steps", "1. ssh dev"}, "Actual: wc -l notes.txt prints 0"},
		{"send-keys -l", `time=x level=INFO msg=send args="send-keys -t agentdeck_x_1 -l hello"`, []string{"--what", "my ssh remote session froze"}, "send-keys -t agentdeck_x_1 -l hello"},
		{"one field", "", []string{"--what", "ssh dev; ls -la; bash -lc make"}, "Actual: ssh <host>; ls -la; bash -lc make"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupReportTest(t, true, "ok", "ok")
			log := tc.log
			feedbackCollectDiagnostics = func(version string, _ int) issuereport.Diagnostics {
				return issuereport.Diagnostics{Version: version, OS: "Linux (Test OS)", Arch: "amd64", Terminal: "xterm-256color", LogExcerpt: log}
			}
			dry, code := runReport(t, issuereport.KindBug, "", append([]string{"--json", "--dry-run"}, tc.args...)...)
			if code != 0 {
				t.Fatalf("dry run exit %d\n%s", code, dry)
			}
			preview := decodeReportJSON(t, dry)
			body := preview["body"].(string)
			if !strings.Contains(body, tc.keep) {
				t.Fatalf("preview lacks %q\n%s", tc.keep, body)
			}
			out, code := runReport(t, issuereport.KindBug, dry, "--yes", "--json", "--issue", "-")
			if code != 0 {
				t.Fatalf("send exit %d\n%s", code, out)
			}
			if m := decodeReportJSON(t, out); m["ok"] != true || m["method"] != "gh" {
				t.Fatalf("send output %v", m)
			}
			if sent, ok := env.sentBody(); !ok || sent != body {
				t.Fatalf("gh body differs from the preview\nsent:  %q\nshown: %q", sent, body)
			}
		})
	}
}

// MINOR-R4-2: a 60000-character report previews (dry-run --json) and the
// preview sends through --yes --json --issue - unchanged.
func TestFeedbackReport_HugePreviewSends(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	feedbackCollectDiagnostics = func(version string, _ int) issuereport.Diagnostics {
		return issuereport.Diagnostics{Version: version, OS: "Linux (Test OS)", Arch: "amd64", Terminal: "xterm-256color"}
	}
	dry, code := runReport(t, issuereport.KindBug, "", "--json", "--dry-run", "--what", strings.Repeat("w", 60000))
	if code != 0 {
		t.Fatalf("dry run exit %d\n%s", code, dry)
	}
	body := decodeReportJSON(t, dry)["body"].(string)
	if len(body) > issuereport.MaxBodyLen || !strings.Contains(body, "(trimmed)") {
		t.Fatalf("preview body %d bytes, trimmed mark %v", len(body), strings.Contains(body, "(trimmed)"))
	}
	out, code := runReport(t, issuereport.KindBug, dry, "--yes", "--json", "--issue", "-")
	if code != 0 {
		t.Fatalf("send exit %d\n%s", code, out)
	}
	if m := decodeReportJSON(t, out); m["ok"] != true || m["method"] != "gh" {
		t.Fatalf("send output %v", m)
	}
	if sent, ok := env.sentBody(); !ok || sent != body {
		t.Fatalf("gh body differs from the preview (sent %d bytes, shown %d)", len(sent), len(body))
	}
}
