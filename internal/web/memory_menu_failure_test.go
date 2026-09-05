package web

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type failingRevisionMenuLoader struct {
	revision      int64
	mode          string
	probes, loads int
}

func (l *failingRevisionMenuLoader) MenuDataRevision() (int64, error) {
	l.probes++
	if l.mode == "revision" {
		return 0, errors.New("revision unavailable")
	}
	if l.mode == "unstable" {
		l.revision++
	}
	return l.revision, nil
}

func (l *failingRevisionMenuLoader) LoadMenuSnapshot() (*MenuSnapshot, error) {
	l.loads++
	if l.mode == "load" {
		return nil, errors.New("snapshot unavailable")
	}
	return &MenuSnapshot{Profile: fmt.Sprint(l.revision)}, nil
}

func TestMemoryMenuData_FailureBurstIsBoundedAndNeverReturnsStaleSuccess(t *testing.T) {
	for _, mode := range []string{"revision", "load", "unstable"} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cached=%t", mode, cached), func(t *testing.T) {
				loader := &failingRevisionMenuLoader{revision: 1}
				store := NewMemoryMenuData(loader)
				if cached {
					if _, err := store.LoadMenuSnapshot(); err != nil {
						t.Fatal(err)
					}
					store.lastRevisionCheck = time.Now().Add(-2 * memoryMenuRevisionCheckInterval)
				}
				loader.revision++
				loader.mode = mode
				if snapshot, err := store.LoadMenuSnapshot(); err == nil || snapshot != nil {
					t.Fatalf("failure = (%v, %v), want nil snapshot and error", snapshot, err)
				}
				probes, loads := loader.probes, loader.loads
				var wg sync.WaitGroup
				for i := 0; i < 32; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if snapshot, err := store.LoadMenuSnapshot(); err == nil || snapshot != nil {
							t.Errorf("cached failure = (%v, %v), want nil snapshot and error", snapshot, err)
						}
					}()
				}
				wg.Wait()
				if loader.probes != probes || loader.loads != loads {
					t.Fatalf("burst retried storage: probes %d -> %d, loads %d -> %d", probes, loader.probes, loads, loader.loads)
				}
				loader.mode = ""
				store.lastRevisionCheck = time.Now().Add(-2 * memoryMenuRevisionCheckInterval)
				if snapshot, err := store.LoadMenuSnapshot(); err != nil || snapshot.Profile != fmt.Sprint(loader.revision) {
					t.Fatalf("recovery = (%v, %v)", snapshot, err)
				}
			})
		}
	}
}

func TestMemoryMenuData_ExplicitUpdatesClearFailureBackoff(t *testing.T) {
	for _, publish := range []bool{false, true} {
		t.Run(fmt.Sprint(publish), func(t *testing.T) {
			loader := &failingRevisionMenuLoader{mode: "revision"}
			store := NewMemoryMenuData(loader)
			if _, err := store.LoadMenuSnapshot(); err == nil {
				t.Fatal("expected failure")
			}
			loader.mode = ""
			if publish {
				store.SetSnapshot(&MenuSnapshot{Profile: "publisher"})
			} else {
				store.InvalidateCache()
			}
			if _, err := store.LoadMenuSnapshot(); err != nil {
				t.Fatalf("explicit update retained failure: %v", err)
			}
		})
	}
}
