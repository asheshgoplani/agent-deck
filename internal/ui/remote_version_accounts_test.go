package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestRenderAccountUsageEntry covers the four states the "accounts" field
// documents: fresh known usage, stale known usage, a slot with no usage file
// at all, and a slot missing UpdatedAt entirely.
func TestRenderAccountUsageEntry(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		u    session.AccountUsage
		want string
	}{
		{
			name: "fresh both windows",
			u: session.AccountUsage{
				Name: "personal", Known: true, HasUpdatedAt: true,
				UpdatedAt: now.Add(-3 * time.Minute),
				FiveHour:  session.AccountUsageWindow{Known: true, Percent: 8},
				SevenDay:  session.AccountUsageWindow{Known: true, Percent: 24},
			},
			want: "personal 5h 8% · 7d 24% (3 min ago)",
		},
		{
			name: "stale",
			u: session.AccountUsage{
				Name: "personal", Known: true, HasUpdatedAt: true,
				UpdatedAt: now.Add(-2 * time.Hour),
				FiveHour:  session.AccountUsageWindow{Known: true, Percent: 8},
			},
			want: "personal 5h 8% (stale, 2 h ago)",
		},
		{
			name: "unknown slot",
			u:    session.AccountUsage{Name: "work", Known: false},
			want: "work usage unknown",
		},
		{
			name: "known but no windows reported",
			u:    session.AccountUsage{Name: "work", Known: true, HasUpdatedAt: true, UpdatedAt: now},
			want: "work usage unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderAccountUsageEntry(tc.u, now)
			if got != tc.want {
				t.Fatalf("renderAccountUsageEntry() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderAccountsPreviewLine_NoSlots pins the "accounts  none" line for a
// host with zero configured Claude account slots.
func TestRenderAccountsPreviewLine_NoSlots(t *testing.T) {
	got := renderAccountsPreviewLine(nil, time.Now())
	want := "accounts  none"
	if got != want {
		t.Fatalf("renderAccountsPreviewLine() = %q, want %q", got, want)
	}
}

// TestRenderAccountsPreviewLine_MultipleSlots pins the maintainer's example
// shape: multiple slots joined with " · ", each with its own age.
func TestRenderAccountsPreviewLine_MultipleSlots(t *testing.T) {
	now := time.Now()
	usage := []session.AccountUsage{
		{Name: "personal", Known: true, HasUpdatedAt: true, UpdatedAt: now.Add(-3 * time.Minute),
			FiveHour: session.AccountUsageWindow{Known: true, Percent: 8}, SevenDay: session.AccountUsageWindow{Known: true, Percent: 24}},
		{Name: "work", Known: true, HasUpdatedAt: true, UpdatedAt: now.Add(-1 * time.Minute),
			FiveHour: session.AccountUsageWindow{Known: true, Percent: 61}, SevenDay: session.AccountUsageWindow{Known: true, Percent: 40}},
	}
	got := renderAccountsPreviewLine(usage, now)
	want := "accounts  personal 5h 8% · 7d 24% (3 min ago) · work 5h 61% · 7d 40% (1 min ago)"
	if got != want {
		t.Fatalf("renderAccountsPreviewLine() = %q, want %q", got, want)
	}
}

// TestRemotePreviewFieldLines_AccountsBackwardCompat pins the three
// remote-compatibility states for the "accounts" field: an older remote that
// cannot be reached at all, a remote whose stats are OK but predate this
// field, and a remote that reports the field.
func TestRemotePreviewFieldLines_AccountsBackwardCompat(t *testing.T) {
	now := time.Now()
	fields := []string{session.PreviewFieldAccounts}

	t.Run("remote unreachable", func(t *testing.T) {
		lines := remotePreviewFieldLines(session.RemoteVersionState{}, "1.16.10", nil, remoteHostStatsResult{}, false, fields, now, previewLayout{})
		// Same-release remote with no stats: never blamed on age (E1).
		if len(lines) != 1 || lines[0] != "stats unknown (remote does not report stats)" {
			t.Fatalf("lines = %v", lines)
		}
	})

	t.Run("stats ok but no accounts key (older agent-deck)", func(t *testing.T) {
		result := remoteHostStatsResult{Stats: session.RemoteHostStats{Ok: true}}
		lines := remotePreviewFieldLines(session.RemoteVersionState{}, "1.16.10", nil, result, true, fields, now, previewLayout{})
		if len(lines) != 1 || lines[0] != "accounts unknown (remote does not report accounts)" {
			t.Fatalf("lines = %v", lines)
		}
	})

	t.Run("remote reports accounts", func(t *testing.T) {
		result := remoteHostStatsResult{Stats: session.RemoteHostStats{
			Ok: true, AccountsAvailable: true,
			Accounts: []session.AccountUsage{{Name: "personal", Known: false}},
		}}
		lines := remotePreviewFieldLines(session.RemoteVersionState{}, "1.16.10", nil, result, true, fields, now, previewLayout{})
		// A remote that reports the slot but predates UnknownReason still
		// reads "usage unknown" — never a guessed reason.
		if len(lines) != 2 || lines[0] != "accounts  1 slot · 1 unknown" || lines[1] != "  personal  —  —  usage unknown" {
			t.Fatalf("lines = %q", lines)
		}
	})
}

// TestHeaderFields_AccountsOptIn pins that "accounts" is opt-in for the
// local header: unset config never shows an accounts segment even with a
// configured slot, and listing it renders "accounts none" for a profile with
// no claude account slots configured (the common case in a throwaway test
// home).
func TestHeaderFields_AccountsOptIn(t *testing.T) {
	withTempAgentDeckHome(t, `
[ui.header]
fields = ["version", "accounts"]
`)

	home := NewHome()
	home.width = 200
	home.height = 30
	home.initialLoading = false

	out := home.View()
	if !strings.Contains(out, "accounts") {
		t.Errorf("fields=[version,accounts] must show the accounts segment: %s", out)
	}
	if !strings.Contains(out, "accounts  none") {
		t.Errorf("no configured claude account slots must render 'accounts none': %s", out)
	}
}

// TestHeaderFields_DefaultOmitsAccounts pins that leaving [ui.header] unset
// never renders an accounts segment — opt-in only, matching harnesses/
// last_poll's precedent.
func TestHeaderFields_DefaultOmitsAccounts(t *testing.T) {
	home := NewHome()
	home.width = 200
	home.height = 30
	home.initialLoading = false

	out := home.View()
	if strings.Contains(out, "accounts") {
		t.Errorf("default header must not show an accounts segment: %s", out)
	}
}
