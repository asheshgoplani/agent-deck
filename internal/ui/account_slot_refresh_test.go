package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

func TestAccountSlotsConfigurationTransition(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured bool
	}{
		{name: "first account added", configured: true},
		{name: "last account removed", configured: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Home{width: 240, height: 40, cursor: 1}
			h.accountSlotsConfigured.Store(!tc.configured)
			inherited := &session.Instance{ID: "inherited", Title: "follows-chain", Tool: "shell", Status: session.StatusIdle}
			unknown := &session.Instance{ID: "unknown", Title: "unknown-slot", Tool: "shell", Status: session.StatusIdle, Account: "unknown-slot"}
			h.instances = []*session.Instance{inherited, unknown}
			for _, inst := range h.instances {
				h.flatItems = append(h.flatItems, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, IsLastInGroup: true})
			}
			before := map[string]sessionRenderState{}
			for _, inst := range h.instances {
				before[inst.ID] = sessionRenderState{
					status: session.StatusError, substate: session.SubstateModelUnavailable,
					tool: "shell", title: "cached title", paneTitle: "cached pane title",
					autoName: true, autoNameDesc: "cached description", account: inst.Account,
					accountDisplay: newAccountPresentation(inst.Account, !tc.configured),
				}
			}
			h.sessionRenderSnapshot.Store(before)

			// This is the settings-save gate update, with an existing snapshot.
			h.setAccountSlotsConfigured(tc.configured)

			require.Equal(t, tc.configured, h.accountSlotsConfigured.Load())
			require.Equal(t, 1, h.cursor)
			require.Same(t, unknown, h.getSelectedSession())
			require.Equal(t, []*session.Instance{inherited, unknown}, h.instances)
			require.Equal(t, "follows-chain", inherited.GetTitleThreadSafe())
			require.Equal(t, "unknown-slot", unknown.GetTitleThreadSafe())
			require.Len(t, h.getSessionRenderSnapshot(), len(before))
			for i, inst := range h.instances {
				want := before[inst.ID]
				want.accountDisplay = newAccountPresentation(inst.Account, tc.configured)
				require.Equal(t, want, h.getSessionRenderState(inst), "only the account presentation may change")
				require.Equal(t, newAccountPresentation(inst.Account, !tc.configured), before[inst.ID].accountDisplay,
					"an already published snapshot must remain immutable")
				require.Equal(t, before[inst.ID].account, inst.GetAccountThreadSafe())
				require.Equal(t, session.StatusIdle, inst.GetStatusThreadSafe())
				require.Equal(t, "shell", inst.GetToolThreadSafe())
				require.Equal(t, h.flatItems[i].Session, inst)
				for _, selected := range []bool{false, true} {
					var row strings.Builder
					h.renderSessionItem(&row, h.flatItems[i], selected, h.getSessionRenderSnapshot(), 240)
					require.Contains(t, row.String(), "cached pane title")
					if inst == inherited && !tc.configured {
						require.NotContains(t, row.String(), "[account:")
					} else {
						require.Contains(t, row.String(), strings.TrimSpace(want.accountDisplay.badge))
					}
				}
				card := h.renderSessionInfoCard(inst, 240, 40)
				if inst == inherited && !tc.configured {
					require.NotContains(t, card, "Account slot:")
				} else {
					require.Contains(t, card, "Account slot:")
					require.Contains(t, card, want.accountDisplay.label)
				}
			}
		})
	}
}

func TestAccountSlotsConfigurationUnchangedKeepsSnapshot(t *testing.T) {
	for _, configured := range []bool{false, true} {
		h := &Home{}
		h.accountSlotsConfigured.Store(configured)
		before := map[string]sessionRenderState{"session": {title: "cached title"}}
		h.sessionRenderSnapshot.Store(before)
		h.setAccountSlotsConfigured(configured)
		require.Equal(t, reflect.ValueOf(before).Pointer(), reflect.ValueOf(h.getSessionRenderSnapshot()).Pointer(),
			"saving settings without a gate transition must not rebuild the snapshot")
	}
}

func TestAccountSlotsConfigurationBeforeSnapshot(t *testing.T) {
	h := &Home{}
	inst := &session.Instance{ID: "new", Tool: "shell", Status: session.StatusIdle}
	for _, configured := range []bool{true, false} {
		h.setAccountSlotsConfigured(configured)
		require.Nil(t, h.getSessionRenderSnapshot(), "do not create session data during a settings change")
		require.Equal(t, newAccountPresentation("", configured), h.getSessionRenderState(inst).accountDisplay)
	}
}

func TestAccountSnapshotPublicationUsesCurrentConfiguration(t *testing.T) {
	for _, configured := range []bool{false, true} {
		h := &Home{}
		h.accountSlotsConfigured.Store(!configured)
		// A background refresh started before the settings save and publishes later.
		pending := map[string]sessionRenderState{
			"session": {title: "fresh title", accountDisplay: newAccountPresentation("", !configured)},
		}
		h.setAccountSlotsConfigured(configured)
		h.publishSessionRenderSnapshot(pending)
		state := h.getSessionRenderSnapshot()["session"]
		require.Equal(t, "fresh title", state.title)
		require.Equal(t, newAccountPresentation("", configured), state.accountDisplay)
	}
}
