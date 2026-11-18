package main

import (
	"strings"
	"testing"
)

// R1 (round 2 review): a look-alike ("pay rent" for "pay rental
// insurance") ticks nothing off and answers close_match with exit 3 and
// the candidate, so the voice desk can ask "Did you mean ...?".
func TestTodoCLICloseMatch(t *testing.T) {
	for _, c := range []struct{ item, query string }{
		{"pay rental insurance", "pay rent"},
		{"book the plane", "plan"},
		{"get ready for demo", "read"},
		{"check the portal", "port"},
	} {
		t.Run(c.query, func(t *testing.T) {
			h := newTodoHarness(t)
			if code, _, _ := h.run("add", c.item); code != 0 {
				t.Fatalf("add exit %d", code)
			}
			before := len(h.published)
			code, out, errOut := h.run("done", "--json", c.query)
			if code != 3 {
				t.Fatalf("done %q: exit %d, want 3\nstdout: %s\nstderr: %s", c.query, code, out, errOut)
			}
			m := decodeJSONObject(t, out)
			if m["status"] != "close_match" || m["query"] != c.query {
				t.Fatalf("done %q: %s; want status close_match", c.query, out)
			}
			cands, _ := m["candidates"].([]any)
			if len(cands) != 1 || cands[0].(map[string]any)["text"] != c.item {
				t.Fatalf("done %q: candidates %v; want [%q]", c.query, m["candidates"], c.item)
			}
			if len(h.published) != before {
				t.Fatalf("done %q published %+v", c.query, h.published[before:])
			}
			code, out, _ = h.run("done", c.query)
			if code != 3 || !strings.Contains(out, "Did you mean") || !strings.Contains(out, c.item) {
				t.Fatalf("done %q text: exit %d\n%s", c.query, code, out)
			}
			if _, out, _ := h.run("list", "--json"); !strings.Contains(out, `"open":1`) {
				t.Fatalf("list after close match: %s", out)
			}
		})
	}
}
