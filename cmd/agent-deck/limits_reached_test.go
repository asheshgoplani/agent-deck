package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// An unknown default login must not mask the configured slots' verdict: when
// every slot is at its limit, the harness is limited even though the
// default login has no feed.
func TestLimitReachedIgnoresUnknownDefaultAccount(t *testing.T) {
	full := []limitWindowJSON{{Window: "5h", UsedPct: 100}}
	got := limitReachedFrom([][]limitAccountJSON{{
		{Harness: "claude", Name: "work", Windows: full},
		{Harness: "claude", Name: "default", Default: true, Windows: []limitWindowJSON{}, Error: "no feed: run agent-deck hooks install"},
	}})
	require.True(t, got["claude"])
	got = limitReachedFrom([][]limitAccountJSON{{
		{Harness: "claude", Name: "work", Windows: full},
		{Harness: "claude", Name: "default", Default: true, Windows: []limitWindowJSON{{Window: "5h", UsedPct: 10}}},
	}})
	require.False(t, got["claude"], "a known default account with headroom keeps the harness usable")
}
