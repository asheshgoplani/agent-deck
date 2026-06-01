package session

import "testing"

// helper: build a session item
func sessItem(id string, status Status, path string) Item {
	return Item{
		Type:    ItemTypeSession,
		Session: &Instance{ID: id, Status: status, GroupPath: path},
		Path:    path,
	}
}

func groupItem(path string) Item {
	return Item{Type: ItemTypeGroup, Path: path}
}

// summarize renders a partitioned list into a compact, assertable form.
// Group rows -> "G:<path>", session rows -> "S:<id>", divider -> "---".
func summarize(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch it.Type {
		case ItemTypeGroup:
			out = append(out, "G:"+it.Path)
		case ItemTypeSession:
			out = append(out, "S:"+it.Session.ID)
		case ItemTypeDivider:
			out = append(out, "---")
		}
	}
	return out
}

func eqSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPartitionNormalIsIdentity(t *testing.T) {
	items := []Item{
		groupItem("a"),
		sessItem("1", StatusRunning, "a"),
	}
	got := PartitionByViewMode(items, GroupViewNormal, nil)
	if len(got) != len(items) {
		t.Fatalf("normal mode must be identity, got %d items", len(got))
	}
}

func TestPartitionActiveTopSplitsGroup(t *testing.T) {
	// proj-a has one running + one idle; proj-b has only idle.
	items := []Item{
		groupItem("proj-a"),
		sessItem("1", StatusRunning, "proj-a"),
		sessItem("2", StatusIdle, "proj-a"),
		groupItem("proj-b"),
		sessItem("3", StatusIdle, "proj-b"),
	}
	got := summarize(PartitionByViewMode(items, GroupViewActiveTop, nil))
	want := []string{
		"G:proj-a", "S:1", // top: only the active session, with its group
		"---",
		"G:proj-a", "S:2", // bottom: the idle remainder, group re-shown
		"G:proj-b", "S:3",
	}
	if !eqSlice(got, want) {
		t.Fatalf("active-top mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestPartitionActiveTopWaitingAndStartingCountAsActive(t *testing.T) {
	items := []Item{
		groupItem("a"),
		sessItem("1", StatusWaiting, "a"),
		groupItem("b"),
		sessItem("2", StatusStarting, "b"),
		groupItem("c"),
		sessItem("3", StatusIdle, "c"),
	}
	got := summarize(PartitionByViewMode(items, GroupViewActiveTop, nil))
	want := []string{
		"G:a", "S:1", "G:b", "S:2",
		"---",
		"G:c", "S:3",
	}
	if !eqSlice(got, want) {
		t.Fatalf("waiting/starting should be active:\n got=%v\nwant=%v", got, want)
	}
}

func TestPartitionActiveTopNothingActiveIsIdentity(t *testing.T) {
	items := []Item{
		groupItem("a"),
		sessItem("1", StatusIdle, "a"),
		sessItem("2", StatusStopped, "a"),
	}
	got := PartitionByViewMode(items, GroupViewActiveTop, nil)
	if len(got) != len(items) {
		t.Fatalf("no active sessions => identity (no divider), got %d", len(got))
	}
	for _, it := range got {
		if it.Type == ItemTypeDivider {
			t.Fatal("did not expect a divider when nothing is active")
		}
	}
}

func TestPartitionPopulatedTopSinksEmptyGroups(t *testing.T) {
	// proj-a has a session; proj-b is empty. Sessions are NOT split by status.
	items := []Item{
		groupItem("proj-a"),
		sessItem("1", StatusIdle, "proj-a"),
		groupItem("proj-b"), // empty
	}
	got := summarize(PartitionByViewMode(items, GroupViewPopulatedTop, nil))
	want := []string{
		"G:proj-a", "S:1",
		"---",
		"G:proj-b",
	}
	if !eqSlice(got, want) {
		t.Fatalf("populated-top mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestPartitionPopulatedTopParentOfPopulatedSubgroupStaysTop(t *testing.T) {
	// "proj" has no direct sessions but a populated subgroup; "scratch" is empty.
	items := []Item{
		groupItem("proj"),
		groupItem("proj/api"),
		sessItem("1", StatusIdle, "proj/api"),
		groupItem("scratch"), // empty
	}
	got := summarize(PartitionByViewMode(items, GroupViewPopulatedTop, nil))
	want := []string{
		"G:proj", "G:proj/api", "S:1",
		"---",
		"G:scratch",
	}
	if !eqSlice(got, want) {
		t.Fatalf("nested populated mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestPartitionPopulatedTopEmptySubgroupStaysNestedUnderPopulatedParent(t *testing.T) {
	// "proj" has a direct session AND an empty subgroup "proj/scratch".
	// The populated parent stays on top "with all its contents, unsplit", so the
	// empty subgroup must stay nested under it — NOT get ripped out and dumped in
	// the bottom "empty groups" section, where it would render indented (level 1)
	// with no parent header above it (orphaned, grouping lost). Only the
	// genuinely-empty top-level group "trash" sinks.
	items := []Item{
		groupItem("proj"),
		sessItem("1", StatusIdle, "proj"),
		{Type: ItemTypeGroup, Path: "proj/scratch", Level: 1}, // empty subgroup
		groupItem("trash"),                                    // empty top-level
	}
	got := summarize(PartitionByViewMode(items, GroupViewPopulatedTop, nil))
	want := []string{
		"G:proj", "S:1", "G:proj/scratch",
		"---",
		"G:trash",
	}
	if !eqSlice(got, want) {
		t.Fatalf("empty subgroup must stay nested under populated parent:\n got=%v\nwant=%v", got, want)
	}
}

func TestPartitionActiveTopEmptySubgroupNestsUnderReshownParent(t *testing.T) {
	// "p" has one running + one idle session AND an empty subgroup "p/empty".
	// Active-top splits "p": running session up top, idle remainder + the empty
	// subgroup down in the "idle / done" section, both nested under the re-shown
	// "p" header. The empty subgroup must follow its parent into the bottom — not
	// get hoisted to the top away from any header.
	items := []Item{
		groupItem("p"),
		sessItem("1", StatusRunning, "p"),
		sessItem("2", StatusIdle, "p"),
		{Type: ItemTypeGroup, Path: "p/empty", Level: 1}, // empty subgroup
	}
	got := summarize(PartitionByViewMode(items, GroupViewActiveTop, nil))
	want := []string{
		"G:p", "S:1",
		"---",
		"G:p", "S:2", "G:p/empty",
	}
	if !eqSlice(got, want) {
		t.Fatalf("empty subgroup must nest under re-shown parent in bottom:\n got=%v\nwant=%v", got, want)
	}
}

func TestPartitionPopulatedTopNoEmptyGroupsIsIdentity(t *testing.T) {
	items := []Item{
		groupItem("a"),
		sessItem("1", StatusIdle, "a"),
		groupItem("b"),
		sessItem("2", StatusRunning, "b"),
	}
	got := PartitionByViewMode(items, GroupViewPopulatedTop, nil)
	if len(got) != len(items) {
		t.Fatalf("no empty groups => identity, got %d", len(got))
	}
}

func TestPartitionActiveTopCollapsedRunningGroupStaysTop(t *testing.T) {
	// "proj-a" is COLLAPSED: its header is present but its (running) session row
	// is not flattened. The tree-derived activity map must keep it on top.
	// "proj-b" is expanded with only an idle session.
	items := []Item{
		groupItem("proj-a"), // collapsed: no session rows follow
		groupItem("proj-b"),
		sessItem("2", StatusIdle, "proj-b"),
	}
	activity := map[string]GroupActivity{
		"proj-a": {HasAny: true, HasActive: true}, // collapsed, holds a runner
		"proj-b": {HasAny: true, HasActive: false},
	}
	got := summarize(PartitionByViewMode(items, GroupViewActiveTop, activity))
	want := []string{
		"G:proj-a", // collapsed running group hoisted to top (header only)
		"---",
		"G:proj-b", "S:2",
	}
	if !eqSlice(got, want) {
		t.Fatalf("collapsed running group must stay top:\n got=%v\nwant=%v", got, want)
	}
}

func TestPartitionActiveTopCollapsedIdleGroupSinks(t *testing.T) {
	items := []Item{
		groupItem("proj-a"), // expanded, running
		sessItem("1", StatusRunning, "proj-a"),
		groupItem("proj-b"), // collapsed, all idle
	}
	activity := map[string]GroupActivity{
		"proj-a": {HasAny: true, HasActive: true},
		"proj-b": {HasAny: true, HasActive: false}, // collapsed, nothing active
	}
	got := summarize(PartitionByViewMode(items, GroupViewActiveTop, activity))
	want := []string{
		"G:proj-a", "S:1",
		"---",
		"G:proj-b", // collapsed all-idle group sinks below the divider
	}
	if !eqSlice(got, want) {
		t.Fatalf("collapsed idle group must sink:\n got=%v\nwant=%v", got, want)
	}
}
