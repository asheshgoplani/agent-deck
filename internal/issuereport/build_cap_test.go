package issuereport

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// MINOR-R4-2: every preview is sendable. Build caps each user field so
// the body stays under MaxBodyLen, marks the cut "(trimmed)", and the
// capped preview still survives SanitizeIssue byte-identical.
func TestBuild_HugeFieldsStaySendable(t *testing.T) {
	withConfiguredIdentity(t, "", "")
	big := strings.Repeat("x", MaxBodyLen)
	hashes := strings.Repeat("#\n", MaxBodyLen/2) // userText adds a backslash per line
	wide := strings.Repeat("漢字🙂", MaxBodyLen/10)
	var logLines []string
	for i := 0; i < 300; i++ {
		logLines = append(logLines, fmt.Sprintf("time=t%d level=INFO msg=%s", i, strings.Repeat("y", 380)))
	}
	hugeLog := strings.Join(logLines, "\n")
	cases := map[string]struct {
		r Report
		d Diagnostics
	}{
		"what 60000":      {Report{Kind: KindBug, WhatHappened: big}, Diagnostics{}},
		"all fields":      {Report{Kind: KindBug, Title: big, WhatHappened: big, Expected: big, Steps: big, Tool: big, Surface: big}, Diagnostics{}},
		"idea all fields": {Report{Kind: KindIdea, Title: big, WhatHappened: big, Expected: big, Steps: big}, Diagnostics{}},
		"heading lines":   {Report{Kind: KindBug, WhatHappened: hashes, Expected: hashes, Steps: hashes}, Diagnostics{}},
		"wide runes":      {Report{Kind: KindBug, WhatHappened: wide, Expected: wide, Steps: wide}, Diagnostics{}},
		"huge log":        {Report{Kind: KindBug, WhatHappened: big, Steps: big}, Diagnostics{Version: big, OS: big, Terminal: big, Tool: big, LogExcerpt: hugeLog}},
		"huge log only":   {Report{Kind: KindBug, WhatHappened: "x"}, Diagnostics{LogExcerpt: hugeLog}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			is := Build(tc.r, tc.d)
			if len(is.Body) > MaxBodyLen {
				t.Fatalf("body is %d bytes, over %d", len(is.Body), MaxBodyLen)
			}
			if !utf8.ValidString(is.Body) {
				t.Fatalf("body is not valid UTF-8")
			}
			if !strings.Contains(is.Body, trimmedMark) {
				t.Fatalf("cut text not marked %q", trimmedMark)
			}
			for _, h := range Headings(tc.r.Kind) {
				if !strings.Contains(is.Body, "\n### "+h+"\n") && !strings.HasPrefix(is.Body, "### "+h+"\n") {
					t.Fatalf("body lacks %q", h)
				}
			}
			checkRoundTrip(t, name, tc.r, tc.d)
		})
	}
	// The newest log lines are the ones kept.
	is := Build(Report{Kind: KindBug, WhatHappened: "x"}, Diagnostics{LogExcerpt: hugeLog})
	if !strings.Contains(is.Body, logLines[len(logLines)-1]) || strings.Contains(is.Body, logLines[0]+"\n") {
		t.Fatalf("log trimming did not keep the newest lines")
	}
	// A big but fitting report is not cut.
	fit := strings.Repeat("z", 10000)
	is = Build(Report{Kind: KindBug, WhatHappened: fit, Expected: fit, Steps: fit}, Diagnostics{})
	if strings.Contains(is.Body, trimmedMark) || strings.Count(is.Body, fit) != 3 {
		t.Fatalf("a report that fits was cut")
	}
}

// Cuts land on rune boundaries and keep within the cap, at every cap.
func TestBuild_CapsCutWholeRunes(t *testing.T) {
	s := strings.Repeat("é漢🙂a\n", 40)
	for n := 0; n <= len(s)+1; n++ {
		for name, got := range map[string]string{"capText": capText(s, n), "capTail": capTail(s, n)} {
			if !utf8.ValidString(got) {
				t.Fatalf("%s(n=%d) cut inside a rune: %q", name, n, got)
			}
			if len(s) > n && n >= 64 && len(got) > n {
				t.Fatalf("%s(n=%d) is %d bytes", name, n, len(got))
			}
			if len(s) > n && !strings.Contains(got, trimmedMark) {
				t.Fatalf("%s(n=%d) not marked", name, n)
			}
		}
	}
	is := Build(Report{Kind: KindBug, WhatHappened: strings.Repeat("é漢🙂", MaxBodyLen/9)}, Diagnostics{})
	if !utf8.ValidString(is.Body) || len(is.Body) > MaxBodyLen {
		t.Fatalf("body %d bytes, valid UTF-8 %v", len(is.Body), utf8.ValidString(is.Body))
	}
}
