package statedb

import (
	"sync"
	"testing"
)

// CodeRabbit (#3024, write_restart_outcome.go): touchStamp's read of
// last_modified and its write are one step. Two writers (two processes, each
// with its own handle) that both read the same stamp before either writes would
// both report it as Before, so each would take itself for the sole writer since
// then (SoleWriterSince) and a TUI would skip the reload the other's change
// needs. Every stamp a writer reports must therefore follow a distinct
// predecessor: the stamps form one chain.
func TestTouchStamp_ConcurrentWritersFormOneChain(t *testing.T) {
	db, path := newTestDBAtPath(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })

	const perWriter = 300
	var mu sync.Mutex
	var all []WriteStamps
	var wg sync.WaitGroup
	for _, h := range []*StateDB{db, other, db, other} {
		wg.Add(1)
		go func(h *StateDB) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				st, err := h.touchStamp()
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				all = append(all, st)
				mu.Unlock()
			}
		}(h)
	}
	wg.Wait()

	before := map[int64]int{}
	after := map[int64]bool{}
	for _, st := range all {
		before[st.Before]++
		after[st.After] = true
	}
	dup := 0
	for b, n := range before {
		if n > 1 {
			dup++
			if dup <= 3 {
				t.Errorf("%d writers report stamp %d as their predecessor: each takes itself for the sole writer since", n, b)
			}
		}
	}
	if dup > 3 {
		t.Errorf("... %d shared predecessors in all", dup)
	}
	for _, st := range all {
		if st.Before != 0 && !after[st.Before] {
			t.Errorf("stamp %d follows %d, which no writer wrote", st.After, st.Before)
			break
		}
	}
}
