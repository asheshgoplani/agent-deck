package telemetry

import (
	"strings"
	"testing"
)

// TestUnemittedEventsArePublishedAsPlanned: Tier 2/3 events have no call
// site in any release yet. The published catalog must say so instead of
// naming a release ("1.16.19+") that has long shipped without them.
func TestUnemittedEventsArePublishedAsPlanned(t *testing.T) {
	doc := SchemaMarkdown()
	for _, e := range Events {
		if e.Ships == shipsNow {
			continue
		}
		if e.Ships != "planned" {
			t.Errorf("%s: Ships = %q, want \"planned\"", e.Name, e.Ships)
		}
		head := "**`" + e.Name + "`** (tier "
		i := strings.Index(doc, head)
		if i < 0 {
			t.Fatalf("%s missing from SchemaMarkdown", e.Name)
		}
		line := doc[i : i+strings.IndexByte(doc[i:], '\n')]
		if !strings.Contains(line, "planned, not emitted yet") || strings.Contains(line, "call sites") {
			t.Errorf("%s doc line = %q, want it marked planned, not emitted yet", e.Name, line)
		}
	}
}
