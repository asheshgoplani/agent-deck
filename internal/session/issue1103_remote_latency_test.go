// Issue #1103 — Remote session latency markers.
//
// Reporter: @ddorman-dn — wants `remotes/<name> — <Xms>` next to each
// remote in the TUI header, refreshed on the CPU/RAM cadence.
//
// This file owns the structural invariants of the measurement primitive:
//   - SSHRunner.MeasureLatency calls a known cheap noop RPC (`--version`).
//   - It returns a non-negative duration that reflects the round-trip.
//   - It surfaces remote/network failure as an error so the renderer can
//     show ` — offline` instead of misleading "0ms".
//   - The RemoteLatency type carries Offline + MS + MeasuredAt so the UI
//     can distinguish "never measured" from "measured offline".

package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIssue1103_MeasureLatency_InvokesVersionAndTimesRoundTrip asserts that
// MeasureLatency hits the cheapest possible noop on the remote (`--version`)
// and returns a duration that reflects the stubbed delay. If a future change
// switches to a heavier command (e.g. `list --json`), this test catches the
// regression because the header would start ticking on a heavyweight RPC.
func TestIssue1103_MeasureLatency_InvokesVersionAndTimesRoundTrip(t *testing.T) {
	const stubDelay = 30 * time.Millisecond

	var calledWith []string
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			calledWith = append([]string(nil), args...)
			time.Sleep(stubDelay)
			return []byte("agent-deck v1.9.23\n"), nil
		},
	}

	d, err := runner.MeasureLatency(context.Background())
	if err != nil {
		t.Fatalf("MeasureLatency returned err=%v", err)
	}

	if len(calledWith) != 1 || calledWith[0] != "--version" {
		t.Fatalf("MeasureLatency must call `--version` (cheapest noop), got args=%v", calledWith)
	}

	if d < stubDelay {
		t.Fatalf("returned duration %v < stubbed delay %v — timer did not span the RPC", d, stubDelay)
	}
}

// TestIssue1103_MeasureLatency_PropagatesError asserts that a failed RPC
// surfaces as an error so the UI can map it to `— offline` rather than
// reporting "0ms" (which would falsely indicate a healthy remote).
func TestIssue1103_MeasureLatency_PropagatesError(t *testing.T) {
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			return nil, errors.New("ssh: connection refused")
		},
	}

	_, err := runner.MeasureLatency(context.Background())
	if err == nil {
		t.Fatal("expected error on failed measurement; got nil — UI would mis-report offline remote as 0ms")
	}
}

// TestIssue1103_RemoteLatency_NeverMeasuredZeroValue asserts the zero value
// of RemoteLatency means "never measured": MeasuredAt.IsZero() is true and
// Offline is false. The renderer keys off MeasuredAt.IsZero() to suppress
// the marker on first paint, so this invariant is load-bearing.
func TestIssue1103_RemoteLatency_NeverMeasuredZeroValue(t *testing.T) {
	var lat RemoteLatency
	if !lat.MeasuredAt.IsZero() {
		t.Fatalf("zero RemoteLatency must have MeasuredAt.IsZero()=true; got %v", lat.MeasuredAt)
	}
	if lat.Offline {
		t.Fatalf("zero RemoteLatency must have Offline=false (= unknown, not offline)")
	}
	if lat.MS != 0 {
		t.Fatalf("zero RemoteLatency must have MS=0; got %d", lat.MS)
	}
}

// TestIssue1103_UISettings_GetRemoteLatencyRefreshSecs covers the config
// path the issue specifies: `[ui] remote_latency_refresh_secs`. When unset,
// it falls back to system_stats.refresh_seconds so the latency marker ticks
// alongside CPU/RAM by default.
func TestIssue1103_UISettings_GetRemoteLatencyRefreshSecs(t *testing.T) {
	cases := []struct {
		name     string
		ui       UISettings
		fallback int
		want     int
	}{
		{"explicit value", UISettings{RemoteLatencyRefreshSecs: 7}, 5, 7},
		{"unset uses fallback", UISettings{}, 10, 10},
		{"unset zero fallback uses default", UISettings{}, 0, 5},
		{"clamp below min", UISettings{RemoteLatencyRefreshSecs: 1}, 5, 5},
		{"clamp above max", UISettings{RemoteLatencyRefreshSecs: 9999}, 5, 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ui.GetRemoteLatencyRefreshSecs(tc.fallback)
			if got != tc.want {
				t.Fatalf("GetRemoteLatencyRefreshSecs(%d) on %+v = %d, want %d", tc.fallback, tc.ui, got, tc.want)
			}
		})
	}
}
