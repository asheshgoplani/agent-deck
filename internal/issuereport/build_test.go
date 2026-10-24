package issuereport

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type issueForm struct {
	Title  string   `yaml:"title"`
	Labels []string `yaml:"labels"`
	Body   []struct {
		Type       string `yaml:"type"`
		ID         string `yaml:"id"`
		Attributes struct {
			Label   string   `yaml:"label"`
			Options []string `yaml:"options"`
		} `yaml:"attributes"`
	} `yaml:"body"`
}

func loadForm(t *testing.T, name string) issueForm {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "ISSUE_TEMPLATE", name))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	var f issueForm
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse template %s: %v", name, err)
	}
	return f
}

func (f issueForm) fieldLabels() []string {
	var out []string
	for _, b := range f.Body {
		if b.Type != "markdown" {
			out = append(out, b.Attributes.Label)
		}
	}
	return out
}

func (f issueForm) options(id string) []string {
	for _, b := range f.Body {
		if b.ID == id {
			return b.Attributes.Options
		}
	}
	return nil
}

var headingRe = regexp.MustCompile(`(?m)^### (.+)$`)

func headings(body string) []string {
	var out []string
	for _, m := range headingRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func sectionContent(t *testing.T, body, heading string) string {
	t.Helper()
	secs, _, ok := parseBody(body)
	if !ok {
		t.Fatalf("body not parseable:\n%s", body)
	}
	for _, s := range secs {
		if s.heading == heading {
			return s.content
		}
	}
	t.Fatalf("no section %q in body:\n%s", heading, body)
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var testDiag = Diagnostics{
	Version: "1.16.33", OS: "Linux (Ubuntu 24.04 LTS)", Arch: "amd64",
	Terminal: "xterm-256color, tmux 3.4", LogExcerpt: "line one\nline two",
}

// The body sections are exactly the bug form's field labels, in order,
// so the template and the in-app body cannot drift apart.
func TestBuild_BugMatchesTemplate(t *testing.T) {
	form := loadForm(t, "bug.yml")
	is := Build(Report{Kind: KindBug, Title: "Typed letters doubled", WhatHappened: "each key shows twice",
		Expected: "each key once", Steps: "1. open a session\n2. type abc", Tool: "claude", Surface: "tui"}, testDiag)

	if got, want := headings(is.Body), form.fieldLabels(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("body headings drifted from bug.yml\n got: %q\nwant: %q", got, want)
	}
	if want := form.Title + "Typed letters doubled"; is.Title != want {
		t.Fatalf("title = %q, want %q", is.Title, want)
	}
	if strings.Join(is.Labels, ",") != strings.Join(form.Labels, ",") {
		t.Fatalf("labels = %v, want template labels %v", is.Labels, form.Labels)
	}
	if v := sectionContent(t, is.Body, hAIDisclosure); !contains(form.options("ai_disclosure"), v) {
		t.Fatalf("AI disclosure %q is not a bug.yml option %v", v, form.options("ai_disclosure"))
	}
	for heading, want := range map[string]string{
		hVersion:     "1.16.33",
		hEnvironment: "Linux (Ubuntu 24.04 LTS) amd64, xterm-256color, tmux 3.4, tool: claude",
		hRepro:       "1. open a session\n2. type abc",
		hExpected:    "Expected: each key once\nActual: each key shows twice",
		hLogs:        "```text\nline one\nline two\n```",
		hAIModel:     noResponse,
	} {
		if got := sectionContent(t, is.Body, heading); got != want {
			t.Errorf("section %q = %q, want %q", heading, got, want)
		}
	}
	if !strings.HasSuffix(is.Body, "---\nFiled from agent-deck tui\n") {
		t.Fatalf("footer missing:\n%s", is.Body)
	}
}

func TestBuild_IdeaMatchesTemplate(t *testing.T) {
	form := loadForm(t, "feature.yml")
	is := Build(Report{Kind: KindIdea, Title: "Report from the app", WhatHappened: "no quick way to report",
		Expected: "a menu item"}, testDiag)

	if got, want := headings(is.Body), form.fieldLabels(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("body headings drifted from feature.yml\n got: %q\nwant: %q", got, want)
	}
	if want := form.Title + "Report from the app"; is.Title != want {
		t.Fatalf("title = %q, want %q", is.Title, want)
	}
	if strings.Join(is.Labels, ",") != strings.Join(form.Labels, ",") {
		t.Fatalf("labels = %v, want %v", is.Labels, form.Labels)
	}
	if v := sectionContent(t, is.Body, hWilling); !contains(form.options("willing"), v) {
		t.Fatalf("willing %q not a feature.yml option", v)
	}
	if v := sectionContent(t, is.Body, hAIDisclosure); !contains(form.options("ai_disclosure"), v) {
		t.Fatalf("AI disclosure %q not a feature.yml option", v)
	}
	if got := sectionContent(t, is.Body, hAlternatives); got != noResponse {
		t.Fatalf("empty optional field = %q, want %q", got, noResponse)
	}
	if !strings.HasSuffix(is.Body, "---\nFiled from agent-deck cli\n") {
		t.Fatalf("default surface footer missing:\n%s", is.Body)
	}
}

// User text goes through Scrub too, and cannot inject a heading.
func TestBuild_ScrubsUserTextAndEscapesHeadings(t *testing.T) {
	home, _ := os.UserHomeDir()
	tok := fake("gh"+"p_", 36)
	is := Build(Report{Kind: KindBug, Title: "crash for jane@example.com",
		WhatHappened: "opened " + home + "/proj with " + tok + "\n### Logs (redacted)\nfake", Expected: "no crash"}, testDiag)
	for _, leak := range []string{"jane@example.com", tok, home + "/proj"} {
		if strings.Contains(is.Title+is.Body, leak) {
			t.Errorf("leaked %q:\n%s", leak, is.Body)
		}
	}
	if n := strings.Count(is.Body, "\n### Logs (redacted)"); n != 1 {
		t.Fatalf("user text injected a heading (%d log headings):\n%s", n, is.Body)
	}
}

func TestBuild_TitleFromWhatWhenEmpty(t *testing.T) {
	is := Build(Report{Kind: KindBug, WhatHappened: "browser back button does nothing\nmore detail"}, Diagnostics{})
	if is.Title != "[bug] browser back button does nothing" {
		t.Fatalf("title = %q", is.Title)
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		r  Report
		ok bool
	}{
		{Report{Kind: KindBug, WhatHappened: "x"}, true},
		{Report{Kind: KindIdea, Title: "x"}, true},
		{Report{Kind: KindBug}, false},
		{Report{Kind: "other", WhatHappened: "x"}, false},
	} {
		err := tc.r.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("Validate(%+v) = %v, want ok=%v", tc.r, err, tc.ok)
		}
		if err != nil && CodeOf(err) != ErrInvalidInput {
			t.Errorf("code = %q, want invalid_input", CodeOf(err))
		}
	}
}

func decodeURL(t *testing.T, raw string) (title, body, labels string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Host != "github.com" || u.Path != "/asheshgoplani/agent-deck/issues/new" {
		t.Fatalf("unexpected url target %s%s", u.Host, u.Path)
	}
	q := u.Query()
	return q.Get("title"), q.Get("body"), q.Get("labels")
}

func TestNewIssueURL_SmallIsUntrimmed(t *testing.T) {
	is := Build(Report{Kind: KindBug, Title: "t", WhatHappened: "w & = ? # x"}, testDiag)
	u, trimmed := NewIssueURL(is)
	if trimmed {
		t.Fatal("small issue reported trimmed")
	}
	title, body, labels := decodeURL(t, u)
	if title != is.Title || body != is.Body || labels != "bug,triage" {
		t.Fatalf("round trip mismatch: %q %q %q", title, body, labels)
	}
	if !strings.HasPrefix(u, "https://github.com/asheshgoplani/agent-deck/issues/new?title=") ||
		!strings.Contains(u, "&body=") || !strings.HasSuffix(u, "&labels=bug%2Ctriage") {
		t.Fatalf("query is not title, body, labels: %s", u)
	}
}

// A huge log is cut first, newest lines kept, and the user's text stays whole.
func TestNewIssueURL_HugeLogTrimsLogFirst(t *testing.T) {
	var lines []string
	for i := 0; i < 20000; i++ {
		lines = append(lines, "log line number "+strings.Repeat("x", 30)+" #"+strconv.Itoa(i))
	}
	d := testDiag
	d.LogExcerpt = strings.Join(lines, "\n")
	what := strings.Repeat("the pane froze after typing; ", 20)
	is := Build(Report{Kind: KindBug, Title: "freeze", WhatHappened: what, Expected: "no freeze", Steps: "1. type"}, d)

	u, trimmed := NewIssueURL(is)
	if !trimmed {
		t.Fatal("trimmed = false for a 20000-line log")
	}
	if len(u) > MaxURLLen {
		t.Fatalf("url length %d > %d", len(u), MaxURLLen)
	}
	_, body, _ := decodeURL(t, u)
	if !strings.Contains(body, strings.TrimSpace(what)) {
		t.Fatal("user text was cut although trimming the log was enough")
	}
	logs := sectionContent(t, body, hLogs)
	if !strings.Contains(logs, trimmedMark) {
		t.Fatalf("log not marked trimmed: %q", logs[:min(200, len(logs))])
	}
	if !strings.Contains(logs, "#19999") || strings.Contains(logs, "#0\n") {
		t.Fatal("log trim did not keep the newest lines")
	}
	if len(u) < MaxURLLen-500 {
		t.Fatalf("url only %d long; trimmed far more than needed", len(u))
	}
}

// When the user text alone is too long, it is cut too and the URL still fits.
func TestNewIssueURL_HugeTextStillFits(t *testing.T) {
	d := testDiag
	d.LogExcerpt = strings.Repeat("noise\n", 5000)
	is := Build(Report{Kind: KindBug, Title: strings.Repeat("T", 300), WhatHappened: strings.Repeat("é ü long text ", 3000),
		Steps: strings.Repeat("step ", 4000)}, d)
	u, trimmed := NewIssueURL(is)
	if !trimmed || len(u) > MaxURLLen {
		t.Fatalf("trimmed=%v len=%d", trimmed, len(u))
	}
	_, body, _ := decodeURL(t, u)
	if !strings.Contains(body, trimmedMark) {
		t.Fatal("no trimmed marker")
	}
	if !strings.Contains(body, "Filed from agent-deck cli") {
		t.Fatal("footer lost while trimming")
	}
}
