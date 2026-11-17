package needsme

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)

func fixed() time.Time { return t0 }

func ok(rows ...Row) func(context.Context) ([]Row, error) {
	return func(context.Context) ([]Row, error) { return rows, nil }
}

func ago(d time.Duration) string { return t0.Add(-d).Format(time.RFC3339) }

func TestReason(t *testing.T) {
	cases := []struct{ sub, detail, want string }{
		{"interactive-menu", "", "asks a question"},
		{"idle-at-empty-prompt", "", "finished, waiting for your next message"},
		{"", "", "waiting for you"},
		{"auth-401", "", "needs you to log in"},
		{"usage-limit", "resets 9pm", "hit a usage limit (resets 9pm)"},
		{"account-blocked", "", "account blocked"},
		{"something-new", "", "waiting for you"},
		{"model-limit", "bad\x1b[31m\ntext", "hit a model limit (bad[31m text)"},
	}
	for _, c := range cases {
		if got := Reason(c.sub, c.detail); got != c.want {
			t.Errorf("Reason(%q,%q) = %q, want %q", c.sub, c.detail, got, c.want)
		}
	}
}

func TestCollectFiltersSortsAndAnnotates(t *testing.T) {
	local := Source{Profile: "default", Fetch: ok(
		Row{ID: "a", Title: "alpha", Group: "team/alex", Profile: "default", Status: "waiting", Substate: "interactive-menu", LastActivityAt: ago(2 * time.Minute)},
		Row{ID: "b", Title: "busy", Status: "running"},
		Row{ID: "c", Title: "old", Status: "waiting", Archived: true},
		Row{ID: "d", Title: "unknown-since", Status: "waiting"},
	)}
	remote := Source{Host: "box1", Fetch: ok(
		Row{ID: "r", Title: "remote-one", Status: "waiting", LastActivityAt: ago(10 * time.Minute), Substate: "idle-at-empty-prompt"},
	)}
	res := Collect(context.Background(), Options{Sources: []Source{local, remote}, Now: fixed})
	if res.Count != 3 || len(res.Sessions) != 3 {
		t.Fatalf("count = %d, sessions %+v", res.Count, res.Sessions)
	}
	if res.Sessions[0].ID != "r" || res.Sessions[1].ID != "a" || res.Sessions[2].ID != "d" {
		t.Fatalf("order = %v %v %v", res.Sessions[0].ID, res.Sessions[1].ID, res.Sessions[2].ID)
	}
	r := res.Sessions[0]
	if r.Host != "box1" || r.WaitingSeconds != 600 || r.Since != ago(10*time.Minute) || r.Status != "waiting" ||
		r.Reason != "finished, waiting for your next message" {
		t.Errorf("remote row = %+v", r)
	}
	a := res.Sessions[1]
	if a.Host != "" || a.Profile != "default" || a.Group != "team/alex" || a.Reason != "asks a question" || a.WaitingSeconds != 120 {
		t.Errorf("local row = %+v", a)
	}
	if d := res.Sessions[2]; d.Since != "" || d.WaitingSeconds != 0 {
		t.Errorf("unknown since = %+v", d)
	}
	if res.Errors == nil || len(res.Errors) != 0 {
		t.Errorf("errors must be an empty list, got %#v", res.Errors)
	}
}

func TestCollectErrorsAreEntriesNotFailures(t *testing.T) {
	good := Source{Fetch: ok(Row{ID: "a", Title: "a", Status: "waiting", LastActivityAt: ago(time.Minute)})}
	bad := Source{Host: "deckbox", Fetch: func(context.Context) ([]Row, error) { return nil, errors.New("ssh: connection refused") }}
	res := Collect(context.Background(), Options{Sources: []Source{good, bad}, Now: fixed})
	if res.Count != 1 || len(res.Errors) != 1 || res.Errors[0].Host != "deckbox" || !strings.Contains(res.Errors[0].Error, "refused") {
		t.Fatalf("res = %+v", res)
	}
}

func TestCollectPerHostTimeout(t *testing.T) {
	slow := Source{Host: "slow", Fetch: func(ctx context.Context) ([]Row, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	fast := Source{Fetch: ok(Row{ID: "a", Title: "a", Status: "waiting"})}
	start := time.Now()
	res := Collect(context.Background(), Options{Sources: []Source{slow, fast}, Timeout: 80 * time.Millisecond, Now: fixed})
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if res.Count != 1 || len(res.Errors) != 1 || res.Errors[0].Host != "slow" || res.Errors[0].Error != "timeout" {
		t.Fatalf("res = %+v", res)
	}
}

func TestCollectRunsInParallel(t *testing.T) {
	mk := func(h string) Source {
		return Source{Host: h, Fetch: func(ctx context.Context) ([]Row, error) {
			time.Sleep(150 * time.Millisecond)
			return nil, nil
		}}
	}
	start := time.Now()
	Collect(context.Background(), Options{Sources: []Source{mk("a"), mk("b"), mk("c"), mk("d")}, Now: fixed})
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("serial? took %v", d)
	}
}

func TestCollectNoSourcesAndDedup(t *testing.T) {
	res := Collect(context.Background(), Options{Now: fixed})
	if res.Count != 0 || res.Sessions == nil || res.Errors == nil {
		t.Fatalf("empty = %#v", res)
	}
	row := Row{ID: "x", Title: "x", Status: "waiting", Profile: "p"}
	res = Collect(context.Background(), Options{Sources: []Source{{Profile: "p", Fetch: ok(row, row)}}, Now: fixed})
	if res.Count != 1 {
		t.Errorf("duplicate row counted twice: %d", res.Count)
	}
}

func TestLine(t *testing.T) {
	s := Session{Title: "alpha", Host: "box1", Reason: "asks a question", WaitingSeconds: 125}
	if got, want := Line(s), "alpha [box1]: asks a question, waiting 2m"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	s = Session{Title: "b", Reason: "waiting for you"}
	if got, want := Line(s), "b: waiting for you"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	s.WaitingSeconds = 45
	if got, want := Line(s), "b: waiting for you, waiting 45s"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	s.WaitingSeconds = 7300
	if got, want := Line(s), "b: waiting for you, waiting 2h"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
