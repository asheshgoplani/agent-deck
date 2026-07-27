package main

import (
	"flag"
	"testing"
	"time"
)

// `session children --follow --heartbeat 300` was rejected: fs.Duration demands
// a unit suffix, so a bare number is a parse error. The command then exited
// non-zero printing its usage block — which, piped into a background-task
// harness (`... | head`), reports the pipeline's exit and reads as success. The
// watcher never started and nothing said so.
//
// Every other tool in this space (sleep, timeout, curl --max-time) treats a
// bare number as seconds. durationFlag does too, while keeping every Go
// duration string working exactly as before.

func parseDurationFlag(t *testing.T, args ...string) (time.Duration, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(discardWriter{})
	d := durationFlag(fs, "heartbeat", 60*time.Second, "")
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	return *d, nil
}

func TestDurationFlag_BareNumberIsSeconds(t *testing.T) {
	got, err := parseDurationFlag(t, "--heartbeat", "300")
	if err != nil {
		t.Fatalf("a bare number must be accepted as seconds: %v", err)
	}
	if got != 5*time.Minute {
		t.Errorf("got %v, want 5m", got)
	}
}

func TestDurationFlag_UnitSuffixesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"250ms", 250 * time.Millisecond},
	} {
		got, err := parseDurationFlag(t, "--heartbeat", tc.in)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.in, got, tc.want)
		}
	}
}

// 0 disables the heartbeat and must keep meaning that in both spellings.
func TestDurationFlag_Zero(t *testing.T) {
	for _, in := range []string{"0", "0s"} {
		got, err := parseDurationFlag(t, "--heartbeat", in)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", in, err)
			continue
		}
		if got != 0 {
			t.Errorf("%s: got %v, want 0", in, got)
		}
	}
}

// Fractional seconds are the natural reading of a bare decimal.
func TestDurationFlag_BareDecimalIsSeconds(t *testing.T) {
	got, err := parseDurationFlag(t, "--heartbeat", "1.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1500*time.Millisecond {
		t.Errorf("got %v, want 1.5s", got)
	}
}

func TestDurationFlag_DefaultWhenUnset(t *testing.T) {
	got, err := parseDurationFlag(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 60*time.Second {
		t.Errorf("got %v, want the 60s default", got)
	}
}

// Genuine garbage must still be rejected — accepting bare seconds is not a
// licence to guess at anything.
func TestDurationFlag_RejectsNonsense(t *testing.T) {
	for _, in := range []string{"soon", "5 minutes", "10x", ""} {
		if _, err := parseDurationFlag(t, "--heartbeat", in); err == nil {
			t.Errorf("%q must be rejected", in)
		}
	}
}

// The default is what `--help` prints, so it must still read as a duration
// rather than a raw nanosecond count.
func TestDurationFlag_DefaultRendersAsDuration(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	durationFlag(fs, "heartbeat", 60*time.Second, "")
	if got := fs.Lookup("heartbeat").DefValue; got != "1m0s" {
		t.Errorf("DefValue: got %q, want %q", got, "1m0s")
	}
}
