package ui

// visualcheck review, 2026-09-23: the header read ● 1 • ◐ 2 • ○ 6 • ■ 1 = 10
// while the list drew 11 rows. The claude-error fixture's row carried a
// "starting" status: rowStatusGlyph draws every status outside the five
// buckets as ○, but the header tally and the group preview only counted the
// five named statuses, so that row was drawn and never counted. Every row the
// list draws must land in exactly the bucket its glyph shows.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/x/ansi"
)

var tallyStatuses = []session.Status{
	session.StatusRunning, session.StatusWaiting, session.StatusIdle,
	session.StatusStopped, session.StatusError, session.StatusStarting,
	session.StatusQueued, "",
}

func tallyInstances() []*session.Instance {
	out := make([]*session.Instance, 0, len(tallyStatuses))
	for i, st := range tallyStatuses {
		out = append(out, &session.Instance{
			ID: fmt.Sprintf("s%d", i), Title: fmt.Sprintf("row-%d", i), Tool: "claude",
			GroupPath: session.DefaultGroupPath, Status: st,
		})
	}
	return out
}

// glyphCounts counts the list glyph each instance's row draws.
func glyphCounts(instances []*session.Instance) map[string]int {
	counts := map[string]int{}
	for _, inst := range instances {
		icon, _ := rowStatusGlyph(inst.Status, session.SubstateNone, false)
		counts[icon]++
	}
	return counts
}

func TestStatusTally_HeaderEqualsListGlyphs(t *testing.T) {
	home := NewHome()
	home.width, home.height = 120, 40
	instances := tallyInstances()
	home.instances = instances
	home.refreshSessionRenderSnapshot(nil)
	home.cachedStatusCounts.valid.Store(false)

	running, waiting, idle, stopped, errored := home.countSessionStatuses()
	got := map[string]int{"●": running, "◐": waiting, "○": idle, "■": stopped, "✕": errored}
	want := glyphCounts(instances)
	for icon, n := range want {
		if got[icon] != n {
			t.Errorf("tally %s = %d, list draws %d such rows", icon, got[icon], n)
		}
	}
	if total := running + waiting + idle + stopped + errored; total != len(instances) {
		t.Fatalf("tally total %d != %d rows in the list", total, len(instances))
	}
}

var previewBucket = regexp.MustCompile(`([●◐○■✕]) (\d+) (running|waiting|idle|stopped|error)`)

func TestStatusTally_GroupPreviewEqualsListGlyphs(t *testing.T) {
	home := NewHome()
	home.width, home.height = 120, 40
	instances := tallyInstances()
	group := &session.Group{Name: "My Sessions", Path: session.DefaultGroupPath, Sessions: instances}

	preview := ansi.Strip(home.renderGroupPreview(group, 80, 40))
	if !strings.Contains(preview, fmt.Sprintf("%d sessions", len(instances))) {
		t.Fatalf("preview does not count %d sessions:\n%s", len(instances), preview)
	}
	got := map[string]int{}
	total := 0
	for _, m := range previewBucket.FindAllStringSubmatch(preview, -1) {
		n, _ := strconv.Atoi(m[2])
		got[m[1]] += n
		total += n
	}
	if total != len(instances) {
		t.Errorf("preview buckets sum to %d, header says %d sessions:\n%s", total, len(instances), preview)
	}
	for icon, n := range glyphCounts(instances) {
		if got[icon] != n {
			t.Errorf("preview %s = %d, list draws %d such rows", icon, got[icon], n)
		}
	}
	// The preview's own compact rows draw the same glyph as the list row.
	for _, inst := range instances {
		icon, _ := rowStatusGlyph(inst.Status, session.SubstateNone, false)
		if !strings.Contains(preview, icon+" "+inst.Title) {
			t.Errorf("preview row for %q (status %q) does not draw %s:\n%s", inst.Title, inst.Status, icon, preview)
		}
	}
}

// A session that crashed mid-turn reads error, so it draws ✕ and counts as
// error in the list, the header tally and the group preview alike.
func TestStatusTally_CrashedSessionIsErrorEverywhere(t *testing.T) {
	crashed := &session.Instance{ID: "c", Title: "claude-error", Tool: "claude",
		GroupPath: session.DefaultGroupPath, Status: session.StatusError}
	if icon, _ := rowStatusGlyph(crashed.Status, session.SubstateNone, false); icon != "✕" {
		t.Fatalf("list glyph %q, want ✕", icon)
	}
	home := NewHome()
	home.width, home.height = 120, 40
	home.instances = []*session.Instance{crashed}
	home.refreshSessionRenderSnapshot(nil)
	home.cachedStatusCounts.valid.Store(false)
	if _, _, _, _, errored := home.countSessionStatuses(); errored != 1 {
		t.Fatalf("tally errored = %d, want 1", errored)
	}
	preview := ansi.Strip(home.renderGroupPreview(&session.Group{Name: "g", Path: session.DefaultGroupPath,
		Sessions: []*session.Instance{crashed}}, 80, 40))
	if !strings.Contains(preview, "✕ 1 error") || !strings.Contains(preview, "✕ claude-error") {
		t.Fatalf("group preview does not show the crash:\n%s", preview)
	}
}

// A concrete filter pill shows exactly the rows its glyph bucket holds, for
// every status value.
func TestStatusTally_FilterPillsMatchGlyph(t *testing.T) {
	glyphOf := map[session.Status]string{
		session.StatusRunning: "●", session.StatusWaiting: "◐", session.StatusIdle: "○",
		session.StatusStopped: "■", session.StatusError: "✕",
	}
	home := NewHome()
	for _, st := range tallyStatuses {
		icon, _ := rowStatusGlyph(st, session.SubstateNone, false)
		matched := 0
		for filter, want := range glyphOf {
			if home.matchesStatusFilter(filter, st) {
				matched++
				if want != icon {
					t.Errorf("status %q draws %s but passes the %s pill", st, icon, want)
				}
			}
		}
		if matched != 1 {
			t.Errorf("status %q passes %d pills, want exactly 1", st, matched)
		}
	}
}

// Remote rows draw through rowStatusGlyph too (remoteRowStatusGlyph), so the
// header tally must count every remote row in the bucket it draws.
func TestStatusTally_RemoteRowsCountedLikeTheyDraw(t *testing.T) {
	home := NewHome()
	home.instances = nil
	home.refreshSessionRenderSnapshot(nil)
	var rows []session.RemoteSessionInfo
	for i, st := range tallyStatuses {
		rows = append(rows, session.RemoteSessionInfo{ID: string(rune('a' + i)), Title: "r", Status: string(st)})
	}
	home.remoteSessions = map[string][]session.RemoteSessionInfo{"lab": rows}
	home.cachedStatusCounts.valid.Store(false)
	running, waiting, idle, stopped, errored := home.countSessionStatuses()
	got := map[string]int{"●": running, "◐": waiting, "○": idle, "■": stopped, "✕": errored}
	want := map[string]int{}
	for _, r := range rows {
		icon, _ := remoteRowStatusGlyph(r.Status, "", false)
		want[icon]++
	}
	for icon, n := range want {
		if got[icon] != n {
			t.Errorf("remote rows: tally %s = %d, list draws %d", icon, got[icon], n)
		}
	}
	if total := running + waiting + idle + stopped + errored; total != len(rows) {
		t.Errorf("remote tally total %d != %d drawn rows", total, len(rows))
	}
}
