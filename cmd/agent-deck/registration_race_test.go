package main

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Required behaviour 8: registration, rename and identity decisions must be safe
// under concurrent add/launch.
//
// The hazard is a read-decide-write window, not a data race on a variable:
// `add` LOADS the instance list, asks "is this (title, location) taken?", and
// INSERTs much later. Between the question and the answer another process can
// take the pair, so two racing `add -t dup <path>` runs both see "free" and both
// create — the exact state #1850 makes `add` refuse — and two racing bumps pick
// the same "(2)".
//
// What makes each decision safe: session.AcquireRegistrationLock(profile), held
// across [load → decide → insert], with the instance list re-read INSIDE the
// lock. The in-process sync.Mutex covers goroutines (the TUI, tests) and the
// advisory flock on a per-profile lockfile covers separate agent-deck processes.
//
// These tests run the racing sequence the CLI runs. Run them with -race.

// registerConcurrently performs the same [lock → reload → decide → insert →
// release] sequence handleAdd performs, from n goroutines at once.
func registerConcurrently(t *testing.T, profile string, n int, title string, loc session.Location, userProvidedTitle bool) (created []string, refused int) {
	t.Helper()

	var mu sync.Mutex
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)

	for i := 0; i < n; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()

			// The lock comes first, then the storage handle. Opening N
			// connections at once makes SQLite's WAL-mode setup itself contend
			// (SQLITE_BUSY at open), which is a property of the storage layer
			// and not what this test is about — the window under test is
			// [load -> decide -> insert].
			lock, err := session.AcquireRegistrationLock(profile)
			if err != nil {
				t.Errorf("acquire registration lock: %v", err)
				return
			}
			defer lock.Release()

			storage, err := session.NewStorageWithProfile(profile)
			if err != nil {
				t.Errorf("storage: %v", err)
				return
			}
			defer func() { _ = storage.Close() }()

			instances, groups, err := storage.LoadWithGroups()
			if err != nil {
				t.Errorf("load: %v", err)
				return
			}

			d := decideAddTitle(instances, title, loc, userProvidedTitle)
			if d.Duplicate != nil {
				mu.Lock()
				refused++
				mu.Unlock()
				return
			}

			inst := session.NewInstance(d.Title, loc.Path)
			if !loc.IsLocal() {
				inst.SSHHost = loc.Host
				inst.SSHRemotePath = loc.Path
				inst.ProjectPath = controllerCWD
			}
			instances = append(instances, inst)
			if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups)); err != nil {
				t.Errorf("save: %v", err)
				return
			}

			mu.Lock()
			created = append(created, d.Title)
			mu.Unlock()
		}(i)
	}

	start.Done()
	done.Wait()
	return created, refused
}

func sandboxProfile(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return "race_test_profile"
}

// TestConcurrentAdd_ExplicitTitleRegistersExactlyOnce is the #1850 refusal under
// contention: N concurrent `add -t dup <path>` must produce exactly one session,
// with every other attempt refused. Without the lock, several would see "free"
// and create, which is the state `add` exists to prevent.
func TestConcurrentAdd_ExplicitTitleRegistersExactlyOnce(t *testing.T) {
	profile := sandboxProfile(t)
	loc := session.LocalLocation(t.TempDir())

	const racers = 8
	created, refused := registerConcurrently(t, profile, racers, "dup", loc, true)

	if len(created) != 1 {
		t.Fatalf("concurrent `add -t dup` created %d sessions (%v); exactly one must win and the rest must be refused", len(created), created)
	}
	if refused != racers-1 {
		t.Fatalf("refused %d of %d racers, want %d", refused, racers, racers-1)
	}

	// And the persisted state agrees.
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer func() { _ = storage.Close() }()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("state db holds %d sessions after the race, want 1", len(instances))
	}
}

// TestConcurrentAdd_AutoRenameNeverCollides is the #1850 rename path under
// contention: without -t every racer is created, but each must get a DISTINCT
// title. Two racers both reading "project is taken, project (2) is free" would
// otherwise both register "project (2)".
func TestConcurrentAdd_AutoRenameNeverCollides(t *testing.T) {
	profile := sandboxProfile(t)
	loc := session.LocalLocation(t.TempDir())

	const racers = 8
	created, refused := registerConcurrently(t, profile, racers, "project", loc, false)

	if refused != 0 {
		t.Fatalf("the auto-rename path must never refuse; %d racers were refused", refused)
	}
	if len(created) != racers {
		t.Fatalf("created %d sessions, want %d", len(created), racers)
	}

	seen := map[string]bool{}
	for _, title := range created {
		if seen[title] {
			t.Fatalf("two concurrent registrations both took the title %q at one location", title)
		}
		seen[title] = true
	}
	if !seen["project"] {
		t.Errorf("no racer got the unbumped base title; created=%v", created)
	}
}

// TestConcurrentAdd_DifferentRemoteLocationsDoNotBlockEachOther: the lock must
// serialize the DECISION without turning genuinely independent registrations
// into duplicates. Same title, different hosts — all must be created.
func TestConcurrentAdd_DifferentRemoteLocationsDoNotBlockEachOther(t *testing.T) {
	profile := sandboxProfile(t)

	const racers = 6
	var mu sync.Mutex
	var created, refusedTitles []string
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)

	for i := 0; i < racers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()

			loc := session.RemoteLocation(fmt.Sprintf("user@host-%d", i), "/srv/app")

			lock, err := session.AcquireRegistrationLock(profile)
			if err != nil {
				t.Errorf("lock: %v", err)
				return
			}
			defer lock.Release()

			storage, err := session.NewStorageWithProfile(profile)
			if err != nil {
				t.Errorf("storage: %v", err)
				return
			}
			defer func() { _ = storage.Close() }()

			instances, groups, err := storage.LoadWithGroups()
			if err != nil {
				t.Errorf("load: %v", err)
				return
			}

			d := decideAddTitle(instances, "shared", loc, true)
			mu.Lock()
			if d.Duplicate != nil {
				refusedTitles = append(refusedTitles, loc.String())
				mu.Unlock()
				return
			}
			mu.Unlock()

			inst := session.NewInstance(d.Title, controllerCWD)
			inst.SSHHost = loc.Host
			inst.SSHRemotePath = loc.Path
			instances = append(instances, inst)
			if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups)); err != nil {
				t.Errorf("save: %v", err)
				return
			}
			mu.Lock()
			created = append(created, loc.String())
			mu.Unlock()
		}(i)
	}

	start.Done()
	done.Wait()

	if len(refusedTitles) != 0 {
		t.Fatalf("remote sessions on different hosts were refused as duplicates of each other: %v", refusedTitles)
	}
	if len(created) != racers {
		t.Fatalf("created %d remote sessions, want %d", len(created), racers)
	}
}

// TestConcurrentTitleRename_OnlyOneWins is required behaviour 8 for the rename
// side (#1853): two sessions at one location racing to take the same title.
func TestConcurrentTitleRename_OnlyOneWins(t *testing.T) {
	profile := sandboxProfile(t)
	dir := t.TempDir()

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	seed := []*session.Instance{
		session.NewInstance("alpha", dir),
		session.NewInstance("beta", dir),
		session.NewInstance("gamma", dir),
	}
	if err := storage.SaveWithGroups(seed, session.NewGroupTreeWithGroups(seed, nil)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	renameTargets := []string{seed[1].ID, seed[2].ID}
	_ = storage.Close()

	var mu sync.Mutex
	var applied int
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)

	for _, id := range renameTargets {
		done.Add(1)
		go func(id string) {
			defer done.Done()
			start.Wait()

			lock, err := session.AcquireRegistrationLock(profile)
			if err != nil {
				t.Errorf("lock: %v", err)
				return
			}
			defer lock.Release()

			s, err := session.NewStorageWithProfile(profile)
			if err != nil {
				t.Errorf("storage: %v", err)
				return
			}
			defer func() { _ = s.Close() }()

			instances, groups, err := s.LoadWithGroups()
			if err != nil {
				t.Errorf("load: %v", err)
				return
			}
			var target *session.Instance
			for _, inst := range instances {
				if inst.ID == id {
					target = inst
				}
			}
			if target == nil {
				t.Errorf("seeded session %s vanished", id)
				return
			}
			if msg, _ := checkTitleConflict(instances, target, "alpha"); msg != "" {
				return // correctly refused
			}
			if _, _, err := session.SetField(target, session.FieldTitle, "alpha", nil); err != nil {
				t.Errorf("SetField: %v", err)
				return
			}
			if err := s.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups)); err != nil {
				t.Errorf("save: %v", err)
				return
			}
			mu.Lock()
			applied++
			mu.Unlock()
		}(id)
	}

	start.Done()
	done.Wait()

	if applied != 0 {
		t.Fatalf("%d concurrent renames onto a title already held at that location were applied; every one must be refused", applied)
	}

	s, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer func() { _ = s.Close() }()
	instances, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	count := 0
	for _, inst := range instances {
		if inst.Title == "alpha" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d sessions hold the title \"alpha\" at one location after the race, want 1", count)
	}
}

// TestRegistrationLock_SerializesAndIsPerProfile pins the two properties the
// decisions above rely on.
func TestRegistrationLock_SerializesAndIsPerProfile(t *testing.T) {
	sandboxProfile(t)

	// Mutual exclusion: a counter incremented non-atomically under the lock must
	// come out exact under -race.
	var counter int
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := session.AcquireRegistrationLock("p1")
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			counter++
			lock.Release()
		}()
	}
	wg.Wait()
	if counter != 24 {
		t.Fatalf("counter = %d, want 24 — the registration lock did not serialize", counter)
	}

	// Release must be safe on a nil lock (the error paths return one).
	var nilLock *session.RegistrationLock
	nilLock.Release()
}

// --- Review round 1, finding F2: a failed re-read must abort ------------------
//
// The reload inside the lock used to be `if fresh, g, err := ...; err == nil`,
// so a transient failure (SQLITE_BUSY) silently left the caller running on the
// PRE-LOCK snapshot — the exact stale list the lock exists to invalidate. That
// gives up the atomicity the lock was taken for, and in `add` the subsequent
// whole-list SaveWithGroups rewrites the table from that stale slice, erasing
// any row registered in between.

// TestReloadForRegistration_PropagatesFailure pins the seam itself: a broken
// storage handle must produce an error, never an empty-but-usable list that a
// caller would mistake for "no sessions registered".
func TestReloadForRegistration_PropagatesFailure(t *testing.T) {
	profile := sandboxProfile(t)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	// Closing the handle makes LoadWithGroups fail the way a transient
	// SQLITE_BUSY would, without having to provoke real contention.
	if err := storage.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	instances, groups, err := reloadForRegistration(storage)
	if err == nil {
		t.Fatalf("a failed re-read returned no error (instances=%v groups=%v); the caller would proceed on the stale pre-lock snapshot", instances, groups)
	}
	if instances != nil || groups != nil {
		t.Errorf("a failed re-read must return no list at all, got instances=%v groups=%v", instances, groups)
	}
}

// TestConcurrentAdd_FailedReloadNeverCreatesADuplicate is the racing test.
//
// Half the racers' in-lock re-reads fail. The invariant is not "every racer
// succeeds" — it is that a racer which cannot see current state never
// REGISTERS. So exactly one session may exist at the end, never two, and the
// title must never be taken twice.
func TestConcurrentAdd_FailedReloadNeverCreatesADuplicate(t *testing.T) {
	profile := sandboxProfile(t)
	loc := session.LocalLocation(t.TempDir())

	var mu sync.Mutex
	var reloadCalls int
	orig := reloadForRegistrationFn
	reloadForRegistrationFn = func(s *session.Storage) ([]*session.Instance, []*session.GroupData, error) {
		mu.Lock()
		reloadCalls++
		fail := reloadCalls%2 == 0
		mu.Unlock()
		if fail {
			return nil, nil, errors.New("statedb: wal mode: database is locked (5) (SQLITE_BUSY)")
		}
		return orig(s)
	}
	t.Cleanup(func() { reloadForRegistrationFn = orig })

	const racers = 8
	var created, aborted int
	var start, done sync.WaitGroup
	start.Add(1)

	for i := 0; i < racers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()

			lock, err := session.AcquireRegistrationLock(profile)
			if err != nil {
				t.Errorf("lock: %v", err)
				return
			}
			defer lock.Release()

			storage, err := session.NewStorageWithProfile(profile)
			if err != nil {
				t.Errorf("storage: %v", err)
				return
			}
			defer func() { _ = storage.Close() }()

			// This is the shape every registration path now has: abort on a
			// failed re-read rather than fall back to a pre-lock snapshot.
			instances, groups, err := reloadForRegistration(storage)
			if err != nil {
				mu.Lock()
				aborted++
				mu.Unlock()
				return
			}

			d := decideAddTitle(instances, "dup", loc, true)
			if d.Duplicate != nil {
				return
			}
			instances = append(instances, session.NewInstance(d.Title, loc.Path))
			if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups)); err != nil {
				t.Errorf("save: %v", err)
				return
			}
			mu.Lock()
			created++
			mu.Unlock()
		}()
	}

	start.Done()
	done.Wait()

	if aborted == 0 {
		t.Fatal("no racer hit the injected reload failure; this test is not exercising the path it claims to")
	}
	if created > 1 {
		t.Fatalf("%d sessions were created for one (title, location); a racer registered on a stale snapshot after its re-read failed", created)
	}

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer func() { _ = storage.Close() }()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(instances) > 1 {
		t.Fatalf("state db holds %d sessions, want at most 1", len(instances))
	}
}
