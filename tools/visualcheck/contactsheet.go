package main

import (
	"fmt"
	"html"
	"os"
	"strings"
)

// writeContactSheet renders every captured frame, one per step and width,
// monospace, each tagged PASS/DIFF/MISSING/ADVISORY — the two-minute human
// look the deck smoke test gate calls for.
func writeContactSheet(path string, reports []frameReport) error {
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\">")
	b.WriteString("<title>agent-deck visual check</title><style>")
	b.WriteString(`
body { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; background: #1a1b26; color: #c0caf5; margin: 0; padding: 16px; }
h1 { font-size: 18px; }
.frame { border: 1px solid #414868; border-radius: 6px; margin: 12px 0; overflow: hidden; }
.frame > .head { display: flex; justify-content: space-between; padding: 6px 10px; background: #24283b; }
.frame > .head .name { font-weight: bold; }
.pass { color: #9ece6a; }
.diff { color: #f7768e; font-weight: bold; }
.fail { color: #f7768e; font-weight: bold; }
.missing { color: #e0af68; font-weight: bold; }
.advisory { color: #7aa2f7; font-style: italic; }
pre { margin: 0; padding: 10px; white-space: pre; overflow-x: auto; font-size: 12px; line-height: 1.25; }
.summary { margin-bottom: 16px; }
`)
	b.WriteString("</style></head><body>")
	b.WriteString("<h1>agent-deck visual check contact sheet</h1>")

	counts := map[string]int{}
	for _, r := range reports {
		counts[r.status]++
	}
	b.WriteString("<div class=\"summary\">")
	fmt.Fprintf(&b, "%d frames — PASS %d, DIFF %d, MISSING %d, ADVISORY %d",
		len(reports), counts["PASS"], counts["DIFF"], counts["MISSING"], counts["ADVISORY"])
	b.WriteString("</div>")

	for _, r := range reports {
		cls := strings.ToLower(r.status)
		b.WriteString("<div class=\"frame\">")
		fmt.Fprintf(&b, "<div class=\"head\"><span class=\"name\">%s — %s</span><span class=\"%s\">%s</span></div>",
			html.EscapeString(r.step), html.EscapeString(r.width), cls, r.status)
		body := r.frame
		if r.status == "ADVISORY" {
			body = "(not captured)\n\n" + r.reason
		} else if r.status == "FAIL" {
			body += "\n\n" + r.reason
		} else if r.status == "MISSING" {
			body += "\n\n(no committed golden yet; run `make visual-check-golden` and get a reviewer's PASS)"
		}
		fmt.Fprintf(&b, "<pre>%s</pre>", html.EscapeString(body))
		b.WriteString("</div>")
	}
	b.WriteString("</body></html>")
	return os.WriteFile(path, []byte(b.String()), 0644)
}
