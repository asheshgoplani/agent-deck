package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The remote agent builds `group list --json` on every change probe, and the
// probe runs whenever the state DB moves (on a busy host every couple of
// seconds). The group status counts used to refresh a session once per
// ancestor group, so a session three levels deep paid three full status
// refreshes per probe. Each session is refreshed once per listing.
func TestBuildGroupListJSON_RefreshesEachSessionOnce(t *testing.T) {
	instances := []*session.Instance{
		session.NewInstanceWithGroup("top", "/tmp/top", "deck"),
		session.NewInstanceWithGroup("mid", "/tmp/mid", "deck/ashesh"),
		session.NewInstanceWithGroup("deep-1", "/tmp/deep1", "deck/ashesh/archive"),
		session.NewInstanceWithGroup("deep-2", "/tmp/deep2", "deck/ashesh/archive"),
		session.NewInstanceWithGroup("other", "/tmp/other", "deck/yasir/archive"),
	}
	tree := session.NewGroupTreeWithGroups(instances, nil)

	calls := map[*session.Instance]int{}
	orig := groupListRefreshStatus
	t.Cleanup(func() { groupListRefreshStatus = orig })
	groupListRefreshStatus = func(inst *session.Instance) {
		calls[inst]++
		inst.Status = session.StatusRunning
	}

	out, err := buildGroupListJSON(tree)
	if err != nil {
		t.Fatalf("buildGroupListJSON: %v", err)
	}
	for _, inst := range instances {
		if got := calls[inst]; got != 1 {
			t.Errorf("session %q (group %q) refreshed %d times, want 1", inst.Title, inst.GroupPath, got)
		}
	}

	// The counts are still recursive: every group counts its subtree.
	var doc struct {
		Groups []groupCountJSON `json:"groups"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	want := map[string]int{
		"deck": 5, "deck/ashesh": 3, "deck/ashesh/archive": 2, "deck/yasir": 1, "deck/yasir/archive": 1,
	}
	got := map[string]int{}
	var walk func([]groupCountJSON)
	walk = func(gs []groupCountJSON) {
		for _, g := range gs {
			if g.Status != nil {
				got[g.Path] = g.Status.Running
			}
			walk(g.Children)
		}
	}
	walk(doc.Groups)
	for path, n := range want {
		if got[path] != n {
			t.Errorf("group %q running = %d, want %d (all: %v)", path, got[path], n, got)
		}
	}
}

type groupCountJSON struct {
	Path   string `json:"path"`
	Status *struct {
		Running int `json:"running"`
	} `json:"status"`
	Children []groupCountJSON `json:"children"`
}

// remoteAgentContentHashRegexp is the previous scrub, kept as the oracle the
// scanner must match byte for byte.
func remoteAgentContentHashRegexp(listJSON, groupJSON string) string {
	scrub := regexp.MustCompile(`"last_activity_at":\s*"[^"]*",?`).ReplaceAllString(listJSON, "")
	scrub = regexp.MustCompile(`"activity":\s*"[^"]*",?`).ReplaceAllString(scrub, "")
	sum := sha256.Sum256([]byte(scrub + "\x00" + groupJSON))
	return hex.EncodeToString(sum[:8])
}

func TestRemoteAgentContentHash_MatchesRegexpScrub(t *testing.T) {
	big := remoteAgentBenchListing(146)
	cases := []string{
		"",
		"[]",
		big,
		compactJSON(t, big),
		`[{"id":"a","last_activity_at":"2026-11-06T10:00:00Z","status":"running"}]`,
		`[{"id":"a","last_activity_at": "x"}]`,
		"[{\"id\":\"a\",\"last_activity_at\":\t\n \"x\",\n\"b\":1}]",
		`[{"id":"a","last_activity_at":null,"activity":"t"}]`,
		`[{"last_activity_at":"x"`,
		`[{"last_activity_at":`,
		`[{"last_activity_at":   `,
		`"last_activity_at""last_activity_at":"y",`,
		`[{"title":"say \"last_activity_at\":\"z\", ok","last_activity_at":"q"}]`,
		`[{"id":"a","last_activity_at":"a\"b","x":1}]`,
		`[{"viewers":[{"name":"n","activity":"2026","x":1},{"activity":"","y":2}]}]`,
		`"activity":"activity":"activity":"1"`,
		`"last_activity_at":"activity":"x"`,
		"\"activity\":\v\"x\"",
	}
	for i, c := range cases {
		for _, g := range []string{"", `{"groups":[]}`} {
			if got, want := remoteAgentContentHash(c, g), remoteAgentContentHashRegexp(c, g); got != want {
				t.Errorf("case %d (%q): hash %s, regexp scrub gives %s", i, c, got, want)
			}
		}
	}
	// Random token soups around the two keys: same scrub as the regexp.
	tokens := []string{`"last_activity_at":`, `"activity":`, `"`, `,`, ` `, "\t", "\n", "\v", `x`, `:`, `"a"`, `\"`, `{`, `}`}
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		for n := rng.IntN(14); n > 0; n-- {
			sb.WriteString(tokens[rng.IntN(len(tokens))])
		}
		s := sb.String()
		if got, want := remoteAgentContentHash(s, ""), remoteAgentContentHashRegexp(s, ""); got != want {
			t.Fatalf("random case %q: hash %s, regexp scrub gives %s", s, got, want)
		}
	}
	// Activity times do not count as a change; anything else does.
	a := `[{"id":"a","last_activity_at":"2026-11-06T10:00:00Z","viewers":[{"activity":"1"}],"status":"running"}]`
	b := `[{"id":"a","last_activity_at":"2026-11-06T11:00:00Z","viewers":[{"activity":"2"}],"status":"running"}]`
	c := `[{"id":"a","last_activity_at":"2026-11-06T11:00:00Z","viewers":[{"activity":"2"}],"status":"waiting"}]`
	if remoteAgentContentHash(a, "{}") != remoteAgentContentHash(b, "{}") {
		t.Error("activity times alone changed the hash")
	}
	if remoteAgentContentHash(b, "{}") == remoteAgentContentHash(c, "{}") {
		t.Error("a status change did not change the hash")
	}
}

func compactJSON(t testing.TB, s string) string {
	t.Helper()
	cl, _, _ := remoteAgentCompactListings(s, "{}", 1<<30)
	return cl
}

// remoteAgentBenchListing is a `list --json`-shaped listing of n sessions,
// indented the way the CLI prints it.
func remoteAgentBenchListing(n int) string {
	type viewer struct {
		Name     string `json:"name"`
		Activity string `json:"activity"`
	}
	type row struct {
		ID             string    `json:"id"`
		Title          string    `json:"title"`
		Path           string    `json:"path"`
		Group          string    `json:"group"`
		Tool           string    `json:"tool"`
		Status         string    `json:"status"`
		TmuxSession    string    `json:"tmux_session"`
		Profile        string    `json:"profile"`
		CreatedAt      time.Time `json:"created_at"`
		Archived       bool      `json:"archived"`
		TranscriptPath string    `json:"transcript_path"`
		LastActivityAt string    `json:"last_activity_at"`
		Viewers        []viewer  `json:"viewers"`
	}
	rows := make([]row, n)
	at := time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC)
	for i := range rows {
		rows[i] = row{
			ID:             fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i),
			Title:          fmt.Sprintf("session-%d", i),
			Path:           fmt.Sprintf("/home/user/work/project-%d", i),
			Group:          "deck/ashesh/archive",
			Tool:           "claude",
			Status:         "stopped",
			TmuxSession:    fmt.Sprintf("agentdeck_session-%d_%08x", i, i),
			Profile:        "ashesh",
			CreatedAt:      at,
			TranscriptPath: fmt.Sprintf("/home/user/.claude/projects/-home-user-work-project-%d/%08x.jsonl", i, i),
			LastActivityAt: at.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			Viewers:        []viewer{{Name: "ashesh", Activity: at.Format(time.RFC3339)}},
		}
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	return strings.TrimSpace(string(b)) + "\n"
}

// BenchmarkRemoteAgentContentHash is the per-probe change check on a listing
// the size of a 146-session profile.
func BenchmarkRemoteAgentContentHash(b *testing.B) {
	l := remoteAgentBenchListing(146)
	g := `{"groups":[],"total_groups":0,"total_sessions":146}`
	b.SetBytes(int64(len(l) + len(g)))
	b.ReportAllocs()
	for b.Loop() {
		_ = remoteAgentContentHash(l, g)
	}
}

// BenchmarkBuildGroupListJSON counts status refreshes per listing for a
// 146-session tree three levels deep (the shape of the profile that showed
// the cost); refreshes/op is what each probe pays in UpdateStatus calls.
func BenchmarkBuildGroupListJSON(b *testing.B) {
	groups := []string{"deck/ashesh", "deck/ashesh/archive", "deck/ashesh/workers", "deck/yasir/archive", "deck/sharjeel/archive"}
	instances := make([]*session.Instance, 146)
	for i := range instances {
		instances[i] = session.NewInstanceWithGroup(fmt.Sprintf("s%d", i), "/tmp/s", groups[i%len(groups)])
	}
	tree := session.NewGroupTreeWithGroups(instances, nil)
	orig := groupListRefreshStatus
	b.Cleanup(func() { groupListRefreshStatus = orig })
	refreshes := 0
	groupListRefreshStatus = func(inst *session.Instance) { refreshes++; inst.Status = session.StatusStopped }
	b.ReportAllocs()
	for b.Loop() {
		if _, err := buildGroupListJSON(tree); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(refreshes)/float64(b.N), "refreshes/op")
}

// On a busy host the state DB moves every second or so for writes the
// listings never read (each Claude status line refresh, cost events), and
// every move used to cost a full probe: load every session, refresh every
// live status through tmux, build both listings, hash them. A move that
// leaves the listings' rows (the fingerprint) unchanged waits for the slower
// activity interval; one that changes them is probed after the quiet
// interval as before.
func TestRemoteAgent_ProbeWaitsForActivityWhenRowsUnchanged(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	if err := writeFileAtomic(db, "0"); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	var rows atomic.Value
	rows.Store("rows-v0")
	listing := func() string { return `["` + rows.Load().(string) + `"]` }
	const (
		quiet    = 40 * time.Millisecond
		activity = 600 * time.Millisecond
	)
	h := startAgent(t, remoteAgentConfig{
		Run: func(context.Context, []string) (string, string, int) { return "", "", 0 },
		Probe: func() (string, string, error) {
			probes.Add(1)
			return listing(), "{}", nil
		},
		Fingerprint:   func() (string, error) { return rows.Load().(string), nil },
		ActivityEvery: activity,
		WatchPath:     db,
		WatchEvery:    5 * time.Millisecond,
		ProbeQuiet:    quiet,
	})
	time.Sleep(quiet + 30*time.Millisecond)
	if got := probes.Load(); got != 1 {
		t.Fatalf("expected only the seed probe, got %d", got)
	}

	// Status-line style writes every 20ms for a third of the activity
	// interval: the rows never change, so no probe runs.
	start := time.Now()
	for i := 1; time.Since(start) < activity/3; i++ {
		if err := writeFileAtomic(db, strings.Repeat("s", i)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("writes outside the listed rows cost %d probes within the activity interval, want 0", got-1)
	}

	// A write that changes the rows is probed after the quiet interval.
	rows.Store("rows-v1")
	if err := writeFileAtomic(db, "v1"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if r := h.next("changed for rows-v1"); r.Event != "changed" || r.Sessions != `["rows-v1"]` {
		t.Fatalf("expected the rows-v1 listing, got %+v", r)
	}
	if d := time.Since(t0); d > activity/2 {
		t.Fatalf("a change to the listed rows took %s to push, want about the quiet interval", d)
	}
	n := probes.Load()

	// Unrelated writes keep coming: still probed, at the activity interval,
	// so live state that only tmux knows is not left stale for good.
	start = time.Now()
	for i := 1; time.Since(start) < activity+300*time.Millisecond; i++ {
		if err := writeFileAtomic(db, strings.Repeat("t", i)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := probes.Load() - n; got < 1 || got > 2 {
		t.Fatalf("unrelated writes over %s cost %d probes, want 1-2 (one per activity interval)", activity+300*time.Millisecond, got)
	}
	h.closeAndWait()
}
