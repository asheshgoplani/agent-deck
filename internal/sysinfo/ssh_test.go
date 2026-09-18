package sysinfo

import (
	"testing"
	"time"
)

// TestParseWho_GNU pins the Linux (GNU coreutils) `who` shape: ISO date,
// host in parentheses. tmux/screen pseudo-hosts and X displays are local,
// real hosts and mosh are remote; sessions aggregate per user with the
// oldest login as "since" and its host as "from".
func TestParseWho_GNU(t *testing.T) {
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, loc)
	out := `alice    pts/0        2026-09-17 11:41 (tmux(276714).%820)
carol    pts/1        2026-09-18 09:10 (10.0.0.5)
carol    pts/6        2026-09-18 09:40 (10.0.0.5)
bob      pts/2        2026-09-18 11:02 (203.0.113.7)
alice    pts/3        2026-09-18 08:54 (203.0.113.7)
alice    pts/4        2026-09-18 08:55 (203.0.113.7)
alice    pts/5        2026-09-18 10:00 (mosh [4242])
root     tty1         2026-09-01 07:00
xuser    :0           2026-09-01 07:00 (:0)
`
	got, err := ParseWho(out, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	want := []SSHSession{
		{User: "alice", Count: 3, HasSince: true, Since: time.Date(2026, 9, 18, 8, 54, 0, 0, loc), From: "203.0.113.7"},
		{User: "bob", Count: 1, HasSince: true, Since: time.Date(2026, 9, 18, 11, 2, 0, 0, loc), From: "203.0.113.7"},
		{User: "carol", Count: 2, HasSince: true, Since: time.Date(2026, 9, 18, 9, 10, 0, 0, loc), From: "10.0.0.5"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d users %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i].User != want[i].User || got[i].Count != want[i].Count || !got[i].Since.Equal(want[i].Since) || got[i].From != want[i].From || got[i].HasSince != want[i].HasSince {
			t.Errorf("user %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestParseWho_BSD pins the macOS shape: "Mon DD HH:MM", no year, and the
// year-boundary rule (a date after "now" belongs to last year).
func TestParseWho_BSD(t *testing.T) {
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, loc)
	out := `alice            console      Sep  5 18:32
alice            ttys000      Dec 31 14:56 (192.168.1.5)
alice            ttys020      Jan  2 10:42 (192.168.1.5)
`
	got, err := ParseWho(out, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].User != "alice" || got[0].Count != 2 {
		t.Fatalf("got %+v", got)
	}
	if want := time.Date(2025, 12, 31, 14, 56, 0, 0, loc); !got[0].Since.Equal(want) {
		t.Errorf("since = %v, want %v (previous year)", got[0].Since, want)
	}
}

// TestParseWho_Empty: nobody connected is an empty list, not an error; an
// unrecognised shape (busybox header) is an error so the caller reports
// unknown rather than "nobody".
func TestParseWho_Empty(t *testing.T) {
	loc := time.UTC
	if got, err := ParseWho("", time.Now(), loc); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	if got, err := ParseWho("alice    pts/0        2026-09-17 11:41 (tmux(1).%2)\n", time.Now(), loc); err != nil || len(got) != 0 {
		t.Fatalf("tmux only: %v %v", got, err)
	}
	if _, err := ParseWho("USER TTY IDLE TIME HOST\nroot pts/0 00:01 12:00 1.2.3.4\n", time.Now(), loc); err == nil {
		t.Fatal("busybox header must be rejected")
	}
}
