// Issue #1170 (by @devtechwebsource on v1.9.30): remote sessions created
// after the TUI launches don't appear until quit+relaunch. The remote list
// is polled on a tick, but the cadence was a hardcoded 30s and a single
// slow remote could starve the others. This config knob lets users tune the
// poll interval; the default (15s) tightens the visibility latency the
// reporter hit. Mirrors the [ui] remote_latency_refresh_secs path (#1103).
package session

import "testing"

// TestIssue1170_UISettings_GetRemoteSessionRefreshSecs covers the config
// path the fix introduces: `[ui] remote_session_refresh_secs`. Unset falls
// back to the default; values clamp to the safe [5, 300] range so a user
// can't accidentally hammer SSH every second or freeze the list for an hour.
func TestIssue1170_UISettings_GetRemoteSessionRefreshSecs(t *testing.T) {
	cases := []struct {
		name string
		ui   UISettings
		want int
	}{
		{"unset uses default", UISettings{}, DefaultRemoteSessionRefreshSecs},
		{"explicit value", UISettings{RemoteSessionRefreshSecs: 20}, 20},
		{"clamp below min", UISettings{RemoteSessionRefreshSecs: 1}, MinRemoteSessionRefreshSecs},
		{"clamp above max", UISettings{RemoteSessionRefreshSecs: 9999}, MaxRemoteSessionRefreshSecs},
		{"negative uses default", UISettings{RemoteSessionRefreshSecs: -5}, DefaultRemoteSessionRefreshSecs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ui.GetRemoteSessionRefreshSecs(); got != tc.want {
				t.Fatalf("GetRemoteSessionRefreshSecs() on %+v = %d, want %d", tc.ui, got, tc.want)
			}
		})
	}
}
