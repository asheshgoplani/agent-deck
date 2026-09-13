package ui

import (
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

// Exercise the actual mutation entry point concurrently with the actual render
// snapshot refresher. Empty is always a valid stored slot and needs no config.
func TestStoredAccountConcurrentMutationSnapshot(t *testing.T) {
	h := NewHome()
	inst := &session.Instance{ID: "concurrent-slot", Title: "title", Tool: "shell", Status: session.StatusIdle, Account: "initial"}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	errs := make(chan error, 1)
	go func() {
		defer wg.Done()
		<-start
		for n := 0; n < 2000; n++ {
			if _, _, err := session.SetField(inst, session.FieldAccount, "", nil); err != nil {
				errs <- err
				return
			}
		}
	}()
	close(start)
	for n := 0; n < 2000; n++ {
		h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	}
	wg.Wait()
	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	require.Equal(t, "", h.getSessionRenderState(inst).account)
}
