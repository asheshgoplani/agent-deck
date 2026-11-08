package orders

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleOrders = `# Standing orders

Free notes stay where they are.

## watch-main-ci
every: 15m
active: 08:00-22:00 Europe/Berlin
check: cat ci.txt
do: if main is red, start one worker to fix it fail-first; tell me in one line
ask_first: merge, push, posting anywhere
max_wakes_per_day: 3
expires: 2026-12-31
a note inside the order is kept too
`

func TestParse_ReadsKeysAndDefaults(t *testing.T) {
	list, errs := Parse(sampleOrders + "\n## bare\ncheck: true\n")
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 orders, got %d", len(list))
	}
	o := list[0]
	if o.Name != "watch-main-ci" || o.Every != 15*time.Minute || o.MaxWakesPerDay != 3 ||
		o.AskFirst != "merge, push, posting anywhere" || o.Expires != "2026-12-31" || o.Status != StatusActive {
		t.Fatalf("parsed %+v", o)
	}
	b := list[1]
	if b.Every != DefaultEvery || b.MaxWakesPerDay != DefaultMaxWakesPerDay || b.Status != StatusActive {
		t.Fatalf("defaults not applied: %+v", b)
	}
}

func TestParse_RejectsBadValues(t *testing.T) {
	for _, body := range []string{
		"every: 30s\ncheck: true",
		"every: soon\ncheck: true",
		"active: 8-22\ncheck: true",
		"active: 08:00-22:00 Mars/Base\ncheck: true",
		"max_wakes_per_day: 0\ncheck: true",
		"expires: tomorrow\ncheck: true",
		"status: maybe\ncheck: true",
		"every: 5m",
	} {
		if _, errs := Parse("## x\n" + body + "\n"); len(errs) != 1 {
			t.Errorf("%q: want one error, got %v", body, errs)
		}
	}
}

func TestEdit_AddPauseResumeRemoveKeepNotes(t *testing.T) {
	text, err := Add(sampleOrders, Order{Name: "watch-prs", Every: time.Hour, Check: "gh pr list", Do: "land green ones", AskFirst: "merge"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(text, Order{Name: "watch-prs", Check: "x"}); err == nil {
		t.Fatal("duplicate name must be refused")
	}
	if _, err := Add(text, Order{Name: "Bad Name", Check: "x"}); err == nil {
		t.Fatal("invalid name must be refused")
	}
	text, err = SetStatus(text, "watch-main-ci", StatusPaused)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := Parse(text)
	if list[0].Status != StatusPaused || list[1].Status != StatusActive || list[1].Every != time.Hour {
		t.Fatalf("after pause: %+v", list)
	}
	text, _ = SetStatus(text, "watch-main-ci", StatusActive)
	if strings.Count(text, "status:") != 1 {
		t.Fatalf("status line must be replaced, not added again:\n%s", text)
	}
	text, err = Remove(text, "watch-prs")
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"Free notes stay where they are.", "a note inside the order is kept too"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("note %q lost:\n%s", keep, text)
		}
	}
	if list, _ := Parse(text); len(list) != 1 || list[0].Status != StatusActive {
		t.Fatalf("after remove: %+v", list)
	}
	if _, err := Remove(text, "nope"); err == nil {
		t.Fatal("removing an unknown order must fail")
	}
	if fresh, err := Add("", Order{Name: "a", Check: "true"}); err != nil || !strings.HasPrefix(fresh, "# Standing orders") {
		t.Fatalf("add to empty file: %q %v", fresh, err)
	}
}

func TestActiveHoursAndExpiry(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	o := Order{Active: "08:00-22:00 Europe/Berlin"}
	if !o.InActiveHours(time.Date(2026, 11, 8, 9, 0, 0, 0, berlin)) || o.InActiveHours(time.Date(2026, 11, 8, 23, 0, 0, 0, berlin)) {
		t.Fatal("day window wrong")
	}
	night := Order{Active: "22:00-06:00 Europe/Berlin"}
	if !night.InActiveHours(time.Date(2026, 11, 8, 23, 0, 0, 0, berlin)) || night.InActiveHours(time.Date(2026, 11, 8, 12, 0, 0, 0, berlin)) {
		t.Fatal("wrapping window wrong")
	}
	e := Order{Expires: "2026-11-08"}
	if e.Expired(time.Date(2026, 11, 8, 23, 59, 0, 0, berlin), berlin) || !e.Expired(time.Date(2026, 11, 9, 0, 1, 0, 0, berlin), berlin) {
		t.Fatal("expires is the last day the order runs")
	}
}

// harness is a conductor dir with a fake check value and a recording sender.
type harness struct {
	t      *testing.T
	dir    string
	value  string
	sent   []string
	fail   error
	muted  bool
	runs   int
	onRun  func()
	berlin *time.Location
}

func newHarness(t *testing.T, orders string) *harness {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(orders), 0o644); err != nil {
		t.Fatal(err)
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	return &harness{t: t, dir: dir, value: "main: green (run 101)", berlin: berlin}
}

func (h *harness) env(now time.Time) Env {
	return Env{
		Dir: h.dir, Now: now, Loc: h.berlin,
		Run: func(ctx context.Context, command, dir string) string {
			h.runs++
			if h.onRun != nil {
				h.onRun()
			}
			return h.value
		},
		Send: func(order, text string) (SendResult, error) {
			if h.fail != nil {
				return SendResult{}, h.fail
			}
			if h.muted {
				return SendResult{Delivered: false, Detail: "muted"}, nil
			}
			h.sent = append(h.sent, text)
			return SendResult{Delivered: true}, nil
		},
	}
}

func (h *harness) tick(now time.Time) string {
	h.t.Helper()
	out, err := Tick(h.env(now))
	if err != nil {
		h.t.Fatalf("tick: %v", err)
	}
	var r []string
	for _, o := range out {
		r = append(r, o.Result)
	}
	return strings.Join(r, ",")
}

const ciOrder = "## watch-main-ci\nevery: 15m\ncheck: cat ci.txt\ndo: fix it fail-first\nask_first: merge, push\nmax_wakes_per_day: 3\n"

// The scenario the shell prototype passed, now against the Go tick:
// baseline silent, unchanged silent, not due, change wakes once, unchanged
// silent, cap reached sends one note then holds.
func TestTick_SilentUntilChangeThenCapped(t *testing.T) {
	h := newHarness(t, ciOrder)
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, h.berlin)
	steps := []struct {
		at     time.Duration
		value  string
		want   string
		sentN  int
		detail string
	}{
		{0, "main: green (run 101)", ResultBaseline, 0, "first sight records a baseline"},
		{15 * time.Minute, "main: green (run 101)", ResultSilent, 0, "same output, no wake, no tokens"},
		{20 * time.Minute, "main: RED (run 102)", ResultNotDue, 0, "not due yet: check not even run"},
		{30 * time.Minute, "main: RED (run 102)", ResultWoke, 1, "changed: one wake"},
		{45 * time.Minute, "main: RED (run 102)", ResultSilent, 1, "same again: silent"},
		{60 * time.Minute, "main: green (run 103)", ResultWoke, 2, ""},
		{75 * time.Minute, "main: RED (run 104)", ResultWoke, 3, "3/3"},
		{90 * time.Minute, "main: green (run 105)", ResultCapped, 4, "cap reached: one cap note"},
		{105 * time.Minute, "main: RED (run 106)", ResultCapped, 4, "held, no second note"},
	}
	for i, s := range steps {
		h.value = s.value
		if got := h.tick(t0.Add(s.at)); got != s.want || len(h.sent) != s.sentN {
			t.Fatalf("step %d (%s): result %q sent %d, want %q sent %d", i+1, s.detail, got, len(h.sent), s.want, s.sentN)
		}
	}
	if h.runs != 8 {
		t.Fatalf("check ran %d times, want 8 (the not-due tick must not run it)", h.runs)
	}
	wake := h.sent[0]
	for _, want := range []string{"[ORDER watch-main-ci] its check changed.", "Was: main: green (run 101)", "Now: main: RED (run 102)",
		"Do: fix it fail-first", "before: merge, push.", "reply exactly NO_REPLY", "Do not add or change orders from this run."} {
		if !strings.Contains(wake, want) {
			t.Fatalf("wake text lacks %q:\n%s", want, wake)
		}
	}
	if !strings.Contains(h.sent[3], "daily cap of 3 wakes reached") {
		t.Fatalf("4th send must be the cap note: %q", h.sent[3])
	}
	// Next day the held change wakes again.
	h.value = "main: RED (run 106)"
	if got := h.tick(t0.Add(24 * time.Hour)); got != ResultWoke {
		t.Fatalf("next day: %q, want woke", got)
	}
}

func TestTick_SecondTickAtOnceIsLockedOut(t *testing.T) {
	h := newHarness(t, ciOrder)
	release, err := Lock(h.dir, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Tick(h.env(time.Now()))
	release()
	if !errors.Is(err, ErrLocked) || h.runs != 0 {
		t.Fatalf("locked tick: err=%v runs=%d, want ErrLocked and no check", err, h.runs)
	}
}

func TestTick_NextCheckAdvancedBeforeCheckRuns(t *testing.T) {
	h := newHarness(t, ciOrder)
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, h.berlin)
	var seen time.Time
	h.onRun = func() { seen = LoadState(h.dir)["watch-main-ci"].NextCheck }
	h.tick(now)
	if !seen.Equal(now.Add(15 * time.Minute)) {
		t.Fatalf("during the check next_check on disk = %v, want %v", seen, now.Add(15*time.Minute))
	}
}

func TestTick_FailedSendKeepsChangeAndRetriesNextMinute(t *testing.T) {
	h := newHarness(t, ciOrder)
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, h.berlin)
	h.tick(t0)
	h.value = "main: RED"
	h.fail = errors.New("session not running")
	if got := h.tick(t0.Add(15 * time.Minute)); got != ResultSendFailed {
		t.Fatalf("got %q", got)
	}
	if got := h.tick(t0.Add(16 * time.Minute)); got != ResultSendFailed {
		t.Fatalf("retry must run on the next minute, got %q", got)
	}
	if got := h.tick(t0.Add(17 * time.Minute)); got != ResultNotDue {
		t.Fatalf("second failure backs off to 2 minutes, got %q", got)
	}
	h.fail = nil
	if got := h.tick(t0.Add(18 * time.Minute)); got != ResultWoke || len(h.sent) != 1 {
		t.Fatalf("after recovery: %q sent=%d", got, len(h.sent))
	}
	log, _ := os.ReadFile(filepath.Join(h.dir, LogFileName))
	if n := strings.Count(string(log), "send-failed"); n != 1 {
		t.Fatalf("a failing streak logs one line, got %d:\n%s", n, log)
	}
}

func TestTick_UndeliveredSendIsNotMarkedSeen(t *testing.T) {
	h := newHarness(t, ciOrder)
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, h.berlin)
	h.tick(t0)
	h.value = "main: RED"
	h.muted = true
	if got := h.tick(t0.Add(15 * time.Minute)); got != ResultNotDelivered {
		t.Fatalf("got %q", got)
	}
	h.muted = false
	if got := h.tick(t0.Add(30 * time.Minute)); got != ResultWoke {
		t.Fatalf("a muted change must still wake once delivery works, got %q", got)
	}
}

func TestTick_PausedExpiredAndOutsideHoursRunNothing(t *testing.T) {
	h := newHarness(t, "## p\ncheck: x\nstatus: paused\n\n## e\ncheck: x\nexpires: 2026-01-01\n\n## n\ncheck: x\nactive: 08:00-09:00 Europe/Berlin\n")
	got := h.tick(time.Date(2026, 11, 8, 12, 0, 0, 0, h.berlin))
	if got != "paused,expired,outside-active-hours" || h.runs != 0 {
		t.Fatalf("got %q runs=%d", got, h.runs)
	}
}

func TestTick_NoOrdersFileDoesNothing(t *testing.T) {
	dir := t.TempDir()
	out, err := Tick(Env{Dir: dir, Now: time.Now()})
	if err != nil || out != nil {
		t.Fatalf("got %v %v", out, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a conductor without ORDERS.md must get no files, got %d", len(entries))
	}
}

func TestTick_StateFileIsReadableJSON(t *testing.T) {
	h := newHarness(t, ciOrder)
	h.tick(time.Date(2026, 11, 8, 10, 0, 0, 0, h.berlin))
	data, err := os.ReadFile(filepath.Join(h.dir, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]map[string]any
	if err := json.Unmarshal(data, &st); err != nil || st["watch-main-ci"]["fingerprint"] == "" {
		t.Fatalf("state: %s %v", data, err)
	}
	matches, _ := filepath.Glob(filepath.Join(h.dir, "*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

func TestRunCheck_OutputExitCodeAndTimeout(t *testing.T) {
	dir := t.TempDir()
	if got := RunCheck(context.Background(), "echo hi; echo noise >&2", dir); got != "hi" {
		t.Fatalf("stdout only: %q", got)
	}
	if got := RunCheck(context.Background(), "echo red; exit 3", dir); got != "red\n[exit 3]" {
		t.Fatalf("exit code: %q", got)
	}
	pidFile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := RunCheck(ctx, "sleep 30 & echo $! > "+pidFile+"; wait", dir)
	if !strings.HasPrefix(got, "[check timed out") || time.Since(start) > 10*time.Second {
		t.Fatalf("timeout: %q after %v", got, time.Since(start))
	}
}

// The timer fires every 60 s but each tick reads the clock a little after it
// starts; an "every: 1m" order must still run on every tick, not every other.
func TestTick_EveryMinuteOrderRunsOnEveryTimerTick(t *testing.T) {
	h := newHarness(t, "## m\nevery: 1m\ncheck: x\n")
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 300*int(time.Millisecond), h.berlin)
	h.tick(t0)
	if got := h.tick(t0.Add(59*time.Second + 900*time.Millisecond)); got != ResultSilent {
		t.Fatalf("tick ~60 s later read the clock 100 ms early: %q, want it to run", got)
	}
	if got := h.tick(t0.Add(70 * time.Second)); got != ResultNotDue {
		t.Fatalf("a manual tick 10 s after the last one must not run it again: %q", got)
	}
}

func TestSendBackoff(t *testing.T) {
	for _, c := range []struct {
		fails int
		every time.Duration
		want  time.Duration
	}{
		{1, time.Hour, time.Minute}, {2, time.Hour, 2 * time.Minute}, {3, time.Hour, 4 * time.Minute},
		{10, time.Hour, 30 * time.Minute}, {10, 15 * time.Minute, 15 * time.Minute}, {1, time.Minute, time.Minute},
	} {
		if got := sendBackoff(c.fails, c.every); got != c.want {
			t.Errorf("sendBackoff(%d, %v) = %v want %v", c.fails, c.every, got, c.want)
		}
	}
}
