package session

import "sync"

// liveSpawnWatchers counts, per instance, the spawn goroutines (fast-death
// watcher, ownership attribution) still running. It exists so a test in
// another package can join them: the instance that spawned is often not the
// one the test holds (core's session.start loads its own), so the test cannot
// call waitForFastDeathWatchers on it. Bookkeeping only; nothing reads it
// outside RetireSpawnWatchersForTest.
var liveSpawnWatchers = struct {
	mu sync.Mutex
	n  map[*Instance]int
}{n: map[*Instance]int{}}

// goSpawnWatcher runs fn as one of the instance's spawn goroutines: joined by
// waitForFastDeathWatchers and listed for RetireSpawnWatchersForTest.
func (i *Instance) goSpawnWatcher(fn func()) {
	i.spawnWatchers.Add(1)
	liveSpawnWatchers.mu.Lock()
	liveSpawnWatchers.n[i]++
	liveSpawnWatchers.mu.Unlock()
	go func() {
		defer i.spawnWatchers.Done()
		defer func() {
			liveSpawnWatchers.mu.Lock()
			if liveSpawnWatchers.n[i]--; liveSpawnWatchers.n[i] <= 0 {
				delete(liveSpawnWatchers.n, i)
			}
			liveSpawnWatchers.mu.Unlock()
		}()
		fn()
	}()
}

// RetireSpawnWatchersForTest supersedes the spawn goroutines of every
// instance in the process and waits for them to return. Those goroutines
// write under the data dir (ownership receipt and its .lock, spawn-failure
// record, lifecycle log) for up to the spawn window, so a test that spawned a
// real session calls this before t.TempDir removes HOME. Test-only.
func RetireSpawnWatchersForTest() {
	liveSpawnWatchers.mu.Lock()
	live := make([]*Instance, 0, len(liveSpawnWatchers.n))
	for i := range liveSpawnWatchers.n {
		live = append(live, i)
	}
	liveSpawnWatchers.mu.Unlock()
	for _, i := range live {
		i.bumpSpawnGenAndBarrier()
		i.waitForFastDeathWatchers()
	}
}
