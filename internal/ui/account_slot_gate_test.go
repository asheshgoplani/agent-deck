package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

// #2122 shipped the stored-slot badge ungated, so a machine with no
// [profiles.<name>.claude].config_dir block got "[account:inherited]" on every
// session row while the New/Edit Session dialogs correctly hid their account
// rows (#2152 gates those on ConfiguredAccountNames). The badge must use the
// same gate: no configured slots means the inherited badge carries no
// information and costs title width on every row.
func TestInheritedBadgeHiddenWithoutConfiguredSlots(t *testing.T) {
	inst := &session.Instance{ID: "gate-inherited", Title: "no-slot-session", Tool: "shell", Status: session.StatusIdle}

	for _, refreshed := range []bool{false, true} {
		name := "fallback"
		if refreshed {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			h := NewHome()
			h.width, h.height = 240, 40
			h.accountSlotsConfigured.Store(false)
			if refreshed {
				h.refreshSessionRenderSnapshot([]*session.Instance{inst})
			}
			for _, selected := range []bool{false, true} {
				var b strings.Builder
				h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, selected, h.getSessionRenderSnapshot(), 240)
				row := b.String()
				require.NotContains(t, row, "[account:", "no configured slots means no account badge")
				require.NotContains(t, row, "inherited")
				require.Contains(t, row, "no-slot-session", "suppressing the badge must not disturb the title")
				require.Equal(t, 1, strings.Count(row, "\n"))
			}

			card := h.renderSessionInfoCard(inst, 240, 40)
			require.NotContains(t, card, "Account slot:", "the card drops the line rather than printing an empty value")
			require.NotContains(t, card, "inherited")
		})
	}
}

// The gate is about the inherited label only. A session carrying an explicit
// slot still reports it even when config.toml no longer declares that profile,
// because the session really does run against that slot's config dir.
func TestExplicitSlotStillRendersWithoutConfiguredSlots(t *testing.T) {
	inst := &session.Instance{ID: "gate-explicit", Title: "slotted", Tool: "shell", Status: session.StatusIdle, Account: "work"}

	h := NewHome()
	h.width, h.height = 240, 40
	h.accountSlotsConfigured.Store(false)
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})

	var b strings.Builder
	h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, false, h.getSessionRenderSnapshot(), 240)
	require.Contains(t, b.String(), `[account:"work"]`)
	require.Contains(t, h.renderSessionInfoCard(inst, 240, 40), "Account slot:")
}

// With at least one slot configured the badge is meaningful again: "inherited"
// then distinguishes a session following the conductor/group/env chain from one
// pinned to a named slot, so it must come back for both.
func TestBadgeReturnsWhenSlotsAreConfigured(t *testing.T) {
	inherited := &session.Instance{ID: "gate-on-inherited", Title: "follows-chain", Tool: "shell", Status: session.StatusIdle}
	pinned := &session.Instance{ID: "gate-on-pinned", Title: "pinned", Tool: "shell", Status: session.StatusIdle, Account: "personal"}

	h := NewHome()
	h.width, h.height = 240, 40
	h.accountSlotsConfigured.Store(true)
	h.refreshSessionRenderSnapshot([]*session.Instance{inherited, pinned})

	for inst, want := range map[*session.Instance]string{
		inherited: "[account:inherited]",
		pinned:    `[account:"personal"]`,
	} {
		var b strings.Builder
		h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, false, h.getSessionRenderSnapshot(), 240)
		require.Contains(t, b.String(), want)
	}
}

// The gate is a pure function of the stored slot and the configured-slot flag;
// pin it directly so a future refactor of the render path cannot quietly
// reintroduce a zero-width badge that still reserves prefix width.
func TestAccountPresentationGate(t *testing.T) {
	hidden := newAccountPresentation("", false)
	require.Equal(t, accountPresentation{}, hidden, "suppressed badge must be the zero value, not an empty-labelled one")
	badge, width := hidden.fit(0)
	require.Empty(t, badge)
	require.Zero(t, width)

	shown := newAccountPresentation("", true)
	require.Equal(t, "inherited", shown.label)
	require.Equal(t, " [account:inherited]", shown.badge)
}
