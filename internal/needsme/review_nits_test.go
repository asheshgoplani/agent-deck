package needsme

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoteFetchErrorUsesTheParseSentinel(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{name: "wrapped sentinel is bad-reply", err: fmt.Errorf("fetch: %w", session.ErrRemoteSessionsParse), want: ErrBadReply},
		{name: "same words without the sentinel is not", err: errors.New("failed to parse remote sessions: x"), want: nil},
		{name: "ssh failure passes through", err: errors.New("exit status 255"), want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := remoteFetchError(tc.err)
			if tc.want != nil {
				if !errors.Is(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
				return
			}
			if errors.Is(got, ErrBadReply) {
				t.Fatalf("got bad-reply for %v", tc.err)
			}
		})
	}
}

// F1: a remote row keeps waiting_since, so remote sessions get since,
// waiting_seconds and their place in the longest-first order.
func TestRemoteRowsCarryWaitingSince(t *testing.T) {
	rows := remoteRows([]session.RemoteSessionInfo{{ID: "r1", Title: "t", Status: "waiting", WaitingSince: "2026-11-17T21:30:00Z"}}, "work")
	if len(rows) != 1 || rows[0].WaitingSince != "2026-11-17T21:30:00Z" || rows[0].Profile != "work" {
		t.Fatalf("rows = %+v", rows)
	}
}

// F1 / N6: longest waiting first from waiting_since; a time in the future
// (a skewed or hostile remote clock) counts as unknown, never first.
func TestCollectLongestFirstAndFutureIgnored(t *testing.T) {
	now := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
	rows := []Row{
		{ID: "a", Title: "a", Status: "waiting", WaitingSince: "2026-11-17T21:59:00Z"},
		{ID: "b", Title: "b", Status: "waiting", WaitingSince: "2026-11-17T21:30:00Z"},
		{ID: "c", Title: "c", Status: "waiting", WaitingSince: "2026-11-18T09:00:00Z"},
		{ID: "d", Title: "d", Status: "waiting"},
	}
	res := Collect(context.Background(), Options{Now: func() time.Time { return now }, Sources: []Source{
		{Host: "box1", Fetch: func(context.Context) ([]Row, error) { return rows, nil }},
	}})
	var order []string
	for _, s := range res.Sessions {
		order = append(order, s.ID)
	}
	if got := strings.Join(order, ","); got != "b,a,c,d" {
		t.Fatalf("order %s, want b,a,c,d", got)
	}
	if res.Sessions[0].WaitingSeconds != 1800 || res.Sessions[0].Since != "2026-11-17T21:30:00Z" {
		t.Fatalf("first = %+v", res.Sessions[0])
	}
	if c := res.Sessions[2]; c.Since != "" || c.WaitingSeconds != 0 {
		t.Fatalf("future waiting_since kept: %+v", c)
	}
}
