package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestStorageWatcherAccountOneRefreshComposition(t *testing.T) {
	h, storage, inst := newWatcherEffectsHome(t)
	w, err := NewStorageWatcher(storage.GetDB())
	require.NoError(t, err)
	defer w.Close()
	h.storageWatcher = w
	settleWatcherInitialLoad(t, w, storage.GetDB())
	old := h.sessionLoadCmd(nil, false)()
	writer, err := statedb.Open(storage.Path())
	require.NoError(t, err)
	defer writer.Close()
	_, err = writer.DB().Exec("UPDATE instances SET account=? WHERE id=?", "reviewer", inst.ID)
	require.NoError(t, err)
	w.checkAndNotify()
	requireWatcherSignal(t, w)
	_, batch := h.Update(storageChangedMsg{})
	commands := batch().(tea.BatchMsg)
	_, _ = h.Update(commands[0]())
	loaded := h.getInstanceByID(inst.ID)
	require.Equal(t, "reviewer", loaded.GetAccountThreadSafe())
	require.Equal(t, "reviewer", h.getSessionRenderState(loaded).account)
	var row strings.Builder
	h.renderSessionItem(&row, session.Item{Type: session.ItemTypeSession, Session: loaded, Level: 1, Path: "work", IsLastInGroup: true}, false, h.getSessionRenderSnapshot(), 240)
	require.Contains(t, row.String(), `[account:"reviewer"]`)
	require.Contains(t, h.renderSessionInfoCard(loaded, 240, 40), `"reviewer"`)
	_, _ = h.Update(old)
	require.Equal(t, "reviewer", h.getSessionRenderState(h.getInstanceByID(inst.ID)).account, "late old load reverted account snapshot")
}
