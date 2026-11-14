package reportloop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testChangelog = `# Changelog

## [Unreleased]

## [1.16.38] - 2026-11-12

### Highlights

- Forks finish in seconds again.

### Fixes

- Forking a large session took over five minutes (#9002, #9010).

## [1.16.37] - 2026-11-09

### Highlights

- Queued messages are delivered after a restart.

### Fixes

- A queued message stayed queued forever after the target restarted (#9001, #9005; thanks @someone).
- Unrelated fix that mentions #90011 and (#123).

## [1.16.36] - 2026-11-02

### Fixes

- Old fix (#9003).
`

func tempStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "reports.json")}
}

func TestReportLoopStoreRecordAndReload(t *testing.T) {
	s := tempStore(t)
	at := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	if err := s.Record(Report{Number: 9001, URL: "https://github.com/o/r/issues/9001", Title: "[bug] queued message stuck", Kind: "bug", FiledVersion: "1.16.36", FromAgent: true, FiledAt: at}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Recording the same number again (a de-duplicated report) must not add a second row.
	if err := s.Record(Report{Number: 9001, Title: "again", FiledVersion: "1.16.36"}); err != nil {
		t.Fatalf("Record dup: %v", err)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(st.Reports) != 1 || st.Reports[0].Number != 9001 || st.Reports[0].Seen != 2 {
		t.Fatalf("want one report #9001 seen twice, got %+v", st.Reports)
	}
	if st.Reports[0].Title != "[bug] queued message stuck" {
		t.Fatalf("first title must be kept, got %q", st.Reports[0].Title)
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("reports.json must be 0600, got %v", fi.Mode().Perm())
	}
}

func TestReportLoopLoadMissingAndMalformed(t *testing.T) {
	s := tempStore(t)
	st, err := s.Load()
	if err != nil || st == nil || len(st.Reports) != 0 {
		t.Fatalf("missing file must load empty, got %+v %v", st, err)
	}
}

func TestReportLoopFixedInFromChangelog(t *testing.T) {
	got := FixedIn(testChangelog, []int{9001, 9002, 9003, 9004, 123})
	want := map[int]string{9001: "1.16.37", 9002: "1.16.38", 9003: "1.16.36", 123: "1.16.37"}
	if len(got) != len(want) {
		t.Fatalf("FixedIn = %v, want %v", got, want)
	}
	for n, v := range want {
		if got[n] != v {
			t.Fatalf("FixedIn[%d] = %q, want %q (all: %v)", n, got[n], v, got)
		}
	}
	// #90011 must not count as #9001 being cited in a later version.
	if got[9001] != "1.16.37" {
		t.Fatalf("prefix match leaked: %v", got)
	}
}

func TestReportLoopFixedInIgnoresNonFixesSections(t *testing.T) {
	cl := "## [2.0.0] - 2026-12-01\n\n### Highlights\n\n- Mentions #77 in passing.\n\n### Added\n\n- Feature for #77.\n"
	if got := FixedIn(cl, []int{77}); len(got) != 0 {
		t.Fatalf("only ### Fixes counts as a fix, got %v", got)
	}
}

func TestReportLoopWhatsNewYourReportsOnTop(t *testing.T) {
	s := tempStore(t)
	_ = s.Record(Report{Number: 9001, Title: "[bug] queued message stuck", FiledVersion: "1.16.36"})
	_ = s.Record(Report{Number: 9002, Title: "[bug] fork takes five minutes", FiledVersion: "1.16.36"})
	_ = s.Record(Report{Number: 9003, Title: "[bug] old", FiledVersion: "1.16.35"})
	if err := s.MarkSeen("1.16.36"); err != nil {
		t.Fatal(err)
	}
	wn, err := s.WhatsNew(testChangelog, "1.16.37", "")
	if err != nil {
		t.Fatal(err)
	}
	if wn.From != "1.16.36" || wn.To != "1.16.37" {
		t.Fatalf("range = %s..%s", wn.From, wn.To)
	}
	if len(wn.Versions) != 1 || wn.Versions[0].Version != "1.16.37" {
		t.Fatalf("versions = %+v", wn.Versions)
	}
	if len(wn.Highlights()) != 1 || !strings.Contains(wn.Highlights()[0], "Queued messages") {
		t.Fatalf("highlights = %v", wn.Highlights())
	}
	if len(wn.Fixed) != 1 || wn.Fixed[0].Number != 9001 || wn.Fixed[0].FixedIn != "1.16.37" {
		t.Fatalf("fixed = %+v (only #9001 is fixed in the range; #9002 ships later, #9003 was before)", wn.Fixed)
	}
	text := wn.Text()
	first := strings.SplitN(strings.TrimSpace(text), "\n", 3)
	if !strings.Contains(first[0], "What's new") || !strings.Contains(first[1], "Your report #9001") || !strings.Contains(first[1], "fixed in v1.16.37") {
		t.Fatalf("your report must be on top:\n%s", text)
	}
	if !strings.Contains(text, "Is it fixed for you?") {
		t.Fatalf("feedback ask missing:\n%s", text)
	}
	// The fixed-in version is stored so the next run does not ask again after feedback.
	st, _ := s.Load()
	if st.Reports[0].FixedIn != "1.16.37" {
		t.Fatalf("FixedIn not persisted: %+v", st.Reports[0])
	}
}

func TestReportLoopWhatsNewFreshInstallShowsNothing(t *testing.T) {
	s := tempStore(t)
	wn, err := s.WhatsNew(testChangelog, "1.16.37", "")
	if err != nil {
		t.Fatal(err)
	}
	if !wn.Empty() {
		t.Fatalf("a first run has no previous version: nothing to show, got %+v", wn)
	}
}

func TestReportLoopWhatsNewSinceOverride(t *testing.T) {
	s := tempStore(t)
	wn, err := s.WhatsNew(testChangelog, "1.16.38", "1.16.36")
	if err != nil {
		t.Fatal(err)
	}
	if len(wn.Versions) != 2 || wn.Versions[0].Version != "1.16.38" {
		t.Fatalf("want 1.16.38 and 1.16.37 newest first, got %+v", wn.Versions)
	}
}

func TestReportLoopFeedbackAskedOnce(t *testing.T) {
	s := tempStore(t)
	_ = s.Record(Report{Number: 9001, Title: "[bug] queued", FiledVersion: "1.16.36"})
	_ = s.MarkSeen("1.16.36")
	if err := s.RecordFeedback(9001, "yes", "1.16.37"); err != nil {
		t.Fatal(err)
	}
	wn, _ := s.WhatsNew(testChangelog, "1.16.37", "")
	if len(wn.Fixed) != 1 || wn.Fixed[0].Feedback == nil || wn.Fixed[0].Feedback.Answer != "yes" {
		t.Fatalf("feedback not attached: %+v", wn.Fixed)
	}
	if strings.Contains(wn.Text(), "Is it fixed for you?") {
		t.Fatalf("must not ask again after an answer:\n%s", wn.Text())
	}
	if err := s.RecordFeedback(4242, "yes", "1.16.37"); err == nil {
		t.Fatalf("feedback on a report this machine did not file must still be recorded or rejected explicitly")
	}
}

func TestReportLoopFetchChangelogFileOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(p, []byte(testChangelog), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ChangelogEnv, p)
	got, err := FetchChangelog(context.Background())
	if err != nil || got != testChangelog {
		t.Fatalf("FetchChangelog = %q, %v", got[:min(len(got), 40)], err)
	}
}

// Two processes (two Store values on one path) recording at the same time
// must not lose a row: the load-modify-save is under a file lock.
func TestReportLoopConcurrentStoresKeepEveryRow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "reports.json")
	stores := []*Store{{Path: p}, {Path: p}}
	const n = 60
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- stores[i%2].Record(Report{Number: i + 1, Title: fmt.Sprintf("r%d", i)})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	st, err := stores[0].Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Reports) != n {
		t.Fatalf("want %d rows, got %d (a concurrent writer lost rows)", n, len(st.Reports))
	}
}

// WhatsNew writes reports.json only when a FixedIn changed.
func TestReportLoopWhatsNewWritesOnlyOnChange(t *testing.T) {
	empty := tempStore(t)
	if _, err := empty.WhatsNew(testChangelog, "1.16.38", "1.16.36"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(empty.Path); !os.IsNotExist(err) {
		t.Fatalf("WhatsNew on an empty store must not create reports.json (stat err %v)", err)
	}

	s := tempStore(t)
	_ = s.Record(Report{Number: 9001, Title: "[bug] queued", FiledVersion: "1.16.36"})
	_ = s.MarkSeen("1.16.36")
	if _, err := s.WhatsNew(testChangelog, "1.16.37", ""); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(s.Path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WhatsNew(testChangelog, "1.16.37", ""); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(old) {
		t.Fatalf("nothing changed, yet reports.json was rewritten (mtime %v)", fi.ModTime())
	}
}

// A report is fixed only by a version newer than the one it was filed on
// (an old changelog line citing the same number does not count), and
// never by a version newer than the running one.
func TestReportLoopFixedInBoundedByFiledAndCurrent(t *testing.T) {
	cl := "## [1.16.38] - 2026-11-12\n\n### Fixes\n\n- The real fix (#50).\n\n" +
		"## [1.16.37] - 2026-11-09\n\n### Fixes\n\n- Other (#51).\n\n" +
		"## [1.16.35] - 2026-11-01\n\n### Fixes\n\n- An older issue with the same number in a fork's numbering (#50).\n"
	got := FixedInBounded(cl, map[int]string{50: "1.16.36"}, "")
	if got[50] != "1.16.38" {
		t.Fatalf("FixedInBounded = %v, want #50 fixed in 1.16.38 (after its filed version)", got)
	}
	if got := FixedInBounded(cl, map[int]string{50: "1.16.36"}, "1.16.37"); len(got) != 0 {
		t.Fatalf("a version newer than the running one must not count, got %v", got)
	}

	s := tempStore(t)
	_ = s.Record(Report{Number: 50, Title: "[bug] x", FiledVersion: "1.16.36"})
	wn, err := s.WhatsNew(cl, "1.16.37", "1.16.36")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load()
	if len(wn.Fixed) != 0 || st.Find(50).FixedIn != "" {
		t.Fatalf("not fixed in <= 1.16.37: fixed=%+v stored=%q", wn.Fixed, st.Find(50).FixedIn)
	}
	wn, err = s.WhatsNew(cl, "1.16.38", "1.16.36")
	if err != nil {
		t.Fatal(err)
	}
	st, _ = s.Load()
	if len(wn.Fixed) != 1 || wn.Fixed[0].FixedIn != "1.16.38" || st.Find(50).FixedIn != "1.16.38" {
		t.Fatalf("want fixed in 1.16.38: fixed=%+v stored=%q", wn.Fixed, st.Find(50).FixedIn)
	}
}

// The real CHANGELOG.md uses both "### Fixes" and "### Fixed".
func TestReportLoopFixedInAcceptsFixedHeading(t *testing.T) {
	cl := "## [2.0.1] - 2026-12-02\n\n### Fixed\n\n- Crash on start (#61).\n\n## [2.0.0] - 2026-12-01\n\n### Fixes\n\n- Hang (#60).\n"
	got := FixedIn(cl, []int{60, 61})
	if got[60] != "2.0.0" || got[61] != "2.0.1" {
		t.Fatalf("FixedIn = %v, want 60->2.0.0 and 61->2.0.1", got)
	}
}

// A malformed reports.json is moved aside and the store starts empty.
func TestReportLoopMalformedStoreMovedAside(t *testing.T) {
	s := tempStore(t)
	if err := os.WriteFile(s.Path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := s.Load()
	if err != nil || st == nil || len(st.Reports) != 0 {
		t.Fatalf("malformed file must load as an empty store, got %+v %v", st, err)
	}
	bad, _ := filepath.Glob(s.Path + ".bad-*")
	if len(bad) != 1 {
		t.Fatalf("want the malformed file moved to %s.bad-<ts>, got %v", s.Path, bad)
	}
	if b, _ := os.ReadFile(bad[0]); string(b) != "{nope" {
		t.Fatalf("moved-aside file must keep the original bytes, got %q", b)
	}
	if err := s.Record(Report{Number: 1, Title: "x"}); err != nil {
		t.Fatalf("Record after recovery: %v", err)
	}
	if st, err := s.Load(); err != nil || len(st.Reports) != 1 {
		t.Fatalf("after recovery: %+v %v", st, err)
	}
}

// The "Is it fixed for you?" hint shows the person form and the agent form.
func TestReportLoopFeedbackCommandMentionsAgentForm(t *testing.T) {
	c := FeedbackCommand(9001)
	if !strings.Contains(c, "agent-deck feedback fixed 9001 --fixed|--not-fixed") || !strings.Contains(c, "--from-agent --json") {
		t.Fatalf("FeedbackCommand = %q", c)
	}
}
