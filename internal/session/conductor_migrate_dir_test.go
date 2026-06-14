package session

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeConductorHome creates a conductor home dir under base populated with the
// given files (relative names → contents).
func writeConductorHome(t *testing.T, base, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	for fname, content := range files {
		p := filepath.Join(dir, fname)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", p, err)
		}
	}
	return dir
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("file %q = %q, want it to contain %q", path, string(data), want)
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to not exist, stat err = %v", path, err)
	}
}

func TestMigrateConductorDir_MovesHomesAndPreservesUserState(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	meta := `{"name":"alpha","agent":"claude","profile":"default","heartbeat_enabled":true,` +
		`"description":"keep me","created_at":"2020-01-01T00:00:00Z","env":{"K":"V"},` +
		`"env_file":"my.env","heartbeat_idle_minutes":9}`
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json":    meta,
		"CLAUDE.md":    "edited claude",
		"LEARNINGS.md": "my learnings",
		"state.json":   `{"x":1}`,
		"heartbeat.sh": "OLD_ROOT=/old/path/conductor",
	})
	// A base-level user-state file must move too.
	if err := os.WriteFile(filepath.Join(defaultBase, "LEARNINGS.md"), []byte("shared learnings"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "vault-conductors")

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	if !res.ConfigWritten {
		t.Fatal("expected ConfigWritten=true")
	}

	// Source home gone; target home present with user-state preserved.
	assertNotExist(t, filepath.Join(defaultBase, "alpha"))
	td := filepath.Join(target, "alpha")
	assertFileContains(t, filepath.Join(td, "LEARNINGS.md"), "my learnings")
	assertFileContains(t, filepath.Join(td, "state.json"), `"x":1`)
	assertFileContains(t, filepath.Join(target, "LEARNINGS.md"), "shared learnings")

	// meta.json preserved verbatim (no field clobbered by the move).
	m, err := LoadConductorMeta("alpha")
	if err != nil {
		t.Fatalf("LoadConductorMeta: %v", err)
	}
	if m.Description != "keep me" {
		t.Fatalf("Description = %q, want %q", m.Description, "keep me")
	}
	if m.CreatedAt != "2020-01-01T00:00:00Z" {
		t.Fatalf("CreatedAt = %q, want preserved", m.CreatedAt)
	}
	if m.Env["K"] != "V" || m.EnvFile != "my.env" || m.HeartbeatIdleMinutes != 9 {
		t.Fatalf("meta user-state lost: %+v", m)
	}

	// heartbeat.sh re-rendered with the NEW conductor root.
	assertFileContains(t, filepath.Join(td, "heartbeat.sh"), target)

	// Resolver now points at target.
	cd, err := ConductorDir()
	if err != nil {
		t.Fatalf("ConductorDir: %v", err)
	}
	if cd != target {
		t.Fatalf("ConductorDir() = %q, want %q", cd, target)
	}

	// The reconcile set is reported for daemon reload.
	if len(res.Conductors) != 1 || res.Conductors[0] != "alpha" {
		t.Fatalf("res.Conductors = %v, want [alpha]", res.Conductors)
	}
}

func TestMigrateConductorDir_DryRunChangesNothing(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md": "x",
	})
	target := filepath.Join(t.TempDir(), "vault-conductors")

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: false})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	if !res.DryRun || res.ConfigWritten {
		t.Fatalf("dry-run wrote state: DryRun=%v ConfigWritten=%v", res.DryRun, res.ConfigWritten)
	}
	if len(res.Actions) == 0 {
		t.Fatal("dry-run should report a plan")
	}
	// Nothing moved, nothing created.
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "meta.json"), "alpha")
	assertNotExist(t, target)
	// Resolver still the default (no override written).
	cd, _ := ConductorDir()
	if cd != defaultBase {
		t.Fatalf("ConductorDir() = %q, want unchanged default %q", cd, defaultBase)
	}
}

func TestMigrateConductorDir_SkipsExistingWithoutForce(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md": "source version",
	})
	target := filepath.Join(t.TempDir(), "vault-conductors")
	writeConductorHome(t, target, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md": "dest version",
	})

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	// Destination preserved; source NOT removed (no --force).
	assertFileContains(t, filepath.Join(target, "alpha", "CLAUDE.md"), "dest version")
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "CLAUDE.md"), "source version")
	var found bool
	for _, a := range res.Actions {
		if a.Name == "alpha" {
			found = true
			if a.Action != "skip-exists" {
				t.Fatalf("alpha action = %q, want skip-exists", a.Action)
			}
		}
	}
	if !found {
		t.Fatal("no action recorded for alpha")
	}
}

func TestMigrateConductorDir_ForceMergesPreservingDest(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json":  `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md":  "source version",
		"state.json": "source state",
	})
	target := filepath.Join(t.TempDir(), "vault-conductors")
	writeConductorHome(t, target, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md": "dest version",
	})

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true, Force: true})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	td := filepath.Join(target, "alpha")
	// Existing dest file preserved; source-only file merged in.
	assertFileContains(t, filepath.Join(td, "CLAUDE.md"), "dest version")
	assertFileContains(t, filepath.Join(td, "state.json"), "source state")
	// Source removed after merge.
	assertNotExist(t, filepath.Join(defaultBase, "alpha"))

	var merged bool
	for _, a := range res.Actions {
		if a.Name == "alpha" && a.Action == "merge" {
			merged = true
			if !a.Conflict {
				t.Fatal("expected merge to report a conflict (CLAUDE.md existed)")
			}
		}
	}
	if !merged {
		t.Fatal("expected a merge action for alpha")
	}
}

// Finding #1: a conductor whose meta.json differs in source AND target must NOT
// be merge-then-deleted under --force — the source's only durable record would
// be destroyed. It is rejected, the whole migration refused, and both copies
// survive.
func TestMigrateConductorDir_ForceRejectsDifferingMetaPreservesSource(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	srcMeta := `{"name":"alpha","profile":"default","description":"SOURCE record"}`
	dstMeta := `{"name":"alpha","profile":"default","description":"DEST record"}`
	writeConductorHome(t, defaultBase, "alpha", map[string]string{"meta.json": srcMeta})
	target := filepath.Join(t.TempDir(), "vault-conductors")
	writeConductorHome(t, target, "alpha", map[string]string{"meta.json": dstMeta})

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true, Force: true})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	if !res.Refused {
		t.Fatal("expected the migration to be refused (differing meta.json under --force)")
	}
	if res.ConfigWritten {
		t.Fatal("config must NOT be repointed when a conductor is rejected")
	}
	if len(res.Blockers) == 0 {
		t.Fatal("expected a blocker explaining the refusal")
	}
	// The source's durable record survives byte-for-byte; nothing deleted.
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "meta.json"), "SOURCE record")
	assertFileContains(t, filepath.Join(target, "alpha", "meta.json"), "DEST record")
	// Resolver still points at the (unchanged) default base.
	if cd, _ := ConductorDir(); cd != defaultBase {
		t.Fatalf("ConductorDir() = %q, want unchanged default %q", cd, defaultBase)
	}
	// The action records the rejection with a reason.
	var rejected bool
	for _, a := range res.Actions {
		if a.Name == "alpha" {
			if a.Action != "reject-conflict" {
				t.Fatalf("alpha action = %q, want reject-conflict", a.Action)
			}
			if !strings.Contains(a.Reason, "differs") {
				t.Fatalf("reject reason = %q, want it to mention the meta.json differs", a.Reason)
			}
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("no reject-conflict action recorded for alpha")
	}
}

// Finding #2: if ANY home is skipped/left behind, [conductor].dir must NOT be
// repointed — the flip is all-or-nothing. Here a clean home (beta) and a
// dest-exists home (alpha, no --force) share the source; the whole migration is
// refused and the resolver is left unchanged so no home is stranded invisibly.
func TestMigrateConductorDir_SkippedHomeAbortsConfigFlip(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{"meta.json": `{"name":"alpha","profile":"default"}`})
	writeConductorHome(t, defaultBase, "beta", map[string]string{"meta.json": `{"name":"beta","profile":"default"}`})
	target := filepath.Join(t.TempDir(), "vault-conductors")
	// alpha already exists at target → skip-exists without --force.
	writeConductorHome(t, target, "alpha", map[string]string{"meta.json": `{"name":"alpha","profile":"default"}`})

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	if !res.Refused || res.ConfigWritten {
		t.Fatalf("expected refusal with no config flip: Refused=%v ConfigWritten=%v", res.Refused, res.ConfigWritten)
	}
	// Both sources untouched (clean home NOT moved either — all-or-nothing).
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "meta.json"), "alpha")
	assertFileContains(t, filepath.Join(defaultBase, "beta", "meta.json"), "beta")
	assertNotExist(t, filepath.Join(target, "beta"))
	// Resolver unchanged → the stranded-at-old-base failure mode cannot happen.
	if cd, _ := ConductorDir(); cd != defaultBase {
		t.Fatalf("ConductorDir() = %q, want unchanged default %q", cd, defaultBase)
	}
}

// Finding #3: a failure BEFORE the config commit (here the copy phase fails
// because the target's parent is a regular file) leaves every source intact and
// the config unchanged — fully recoverable, never a half-applied durable record.
func TestMigrateConductorDir_CopyFailureLeavesSourcesIntact(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default","description":"durable"}`,
		"CLAUDE.md": "edited",
	})
	// A regular file standing where the target's parent dir must be → MkdirAll
	// (and thus the copy) fails deterministically, before any config commit.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocker, "conductors")

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true})
	if err == nil {
		t.Fatal("expected the copy phase to fail")
	}
	if res != nil && res.ConfigWritten {
		t.Fatal("config must NOT be committed when the copy phase fails")
	}
	// Source survives intact — durable record AND user state recoverable.
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "meta.json"), "durable")
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "CLAUDE.md"), "edited")
	if cd, _ := ConductorDir(); cd != defaultBase {
		t.Fatalf("ConductorDir() = %q, want unchanged default %q", cd, defaultBase)
	}
}

// Finding #4: source/target overlap is rejected up front (containment in either
// direction), while the exact no-op is allowed.
func TestMigrateConductorDir_RejectsOverlap(t *testing.T) {
	setupSessionXDGPathEnv(t)
	root := t.TempDir()
	parent := filepath.Join(root, "a")
	child := filepath.Join(parent, "sub")

	// target inside source.
	if _, err := MigrateConductorDir(ConductorDirMigrateOptions{From: parent, Target: child, Apply: false}); err == nil {
		t.Fatal("expected target-inside-source to be rejected")
	} else if !strings.Contains(err.Error(), "inside") {
		t.Fatalf("error = %v, want it to mention containment", err)
	}
	// source inside target.
	if _, err := MigrateConductorDir(ConductorDirMigrateOptions{From: child, Target: parent, Apply: false}); err == nil {
		t.Fatal("expected source-inside-target to be rejected")
	} else if !strings.Contains(err.Error(), "inside") {
		t.Fatalf("error = %v, want it to mention containment", err)
	}
	// Exact no-op (target == source) is allowed.
	if _, err := MigrateConductorDir(ConductorDirMigrateOptions{From: parent, Target: parent, Apply: false}); err != nil {
		t.Fatalf("exact no-op should be allowed, got %v", err)
	}
}

// Finding #7: a dry-run with a conflicting destination enumerates and reports the
// would-be rejection and mutates nothing.
func TestMigrateConductorDir_DryRunReportsConflictsNoMutation(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{"meta.json": `{"name":"alpha","profile":"default","description":"src"}`})
	target := filepath.Join(t.TempDir(), "vault-conductors")
	writeConductorHome(t, target, "alpha", map[string]string{"meta.json": `{"name":"alpha","profile":"default","description":"dst"}`})

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: false, Force: true})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	if !res.DryRun {
		t.Fatal("expected a dry-run result")
	}
	if !res.Refused || len(res.Blockers) == 0 {
		t.Fatalf("dry-run should flag the would-be refusal: Refused=%v Blockers=%v", res.Refused, res.Blockers)
	}
	var sawReject bool
	for _, a := range res.Actions {
		if a.Name == "alpha" && a.Action == "reject-conflict" {
			sawReject = true
		}
	}
	if !sawReject {
		t.Fatal("dry-run did not report the reject-conflict for alpha")
	}
	// Nothing mutated: both metas as authored, config untouched.
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "meta.json"), "src")
	assertFileContains(t, filepath.Join(target, "alpha", "meta.json"), "dst")
	if cd, _ := ConductorDir(); cd != defaultBase {
		t.Fatalf("ConductorDir() = %q, want unchanged default %q", cd, defaultBase)
	}
}

// Finding #6: a symlink inside a source home that points OUTSIDE the base is
// relocated verbatim as a symlink — never followed/copied-through — so the
// external target's contents are not pulled into the new base and nothing is
// written outside it.
func TestMigrateConductorDir_DoesNotFollowSymlinksOutOfBase(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")

	external := t.TempDir()
	secret := filepath.Join(external, "secret.txt")
	if err := os.WriteFile(secret, []byte("OUTSIDE DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := writeConductorHome(t, defaultBase, "alpha", map[string]string{"meta.json": `{"name":"alpha","profile":"default"}`})
	link := filepath.Join(home, "escape")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "vault-conductors")
	if _, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true}); err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}

	// The link is recreated AS a symlink, not dereferenced into a real dir/file.
	dstLink := filepath.Join(target, "alpha", "escape")
	info, err := os.Lstat(dstLink)
	if err != nil {
		t.Fatalf("Lstat(%q): %v", dstLink, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %q to remain a symlink, got mode %v", dstLink, info.Mode())
	}
	// The external content was NOT copied through into the new base as a real file.
	if _, err := os.Lstat(filepath.Join(target, "alpha", "escape", "secret.txt")); err == nil {
		// Reachable only THROUGH the symlink; assert it is not a real copied file.
		realCopy := filepath.Join(target, "alpha", "secret.txt")
		if _, err := os.Lstat(realCopy); err == nil {
			t.Fatal("symlink target contents were copied through into the base")
		}
	}
	// The external data is untouched.
	assertFileContains(t, secret, "OUTSIDE DATA")
}

// Finding #5: a concurrent meta write racing the migration cannot strand or lose
// the conductor's durable record. Regardless of which side wins the conductor
// base lock first, the racing conductor's meta.json ends up readable at the
// committed target. Run under -race to confirm no data race on the shared lock.
func TestMigrateConductorDir_ConcurrentMetaWriteNotStranded(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{"meta.json": `{"name":"alpha","agent":"claude","profile":"default"}`})
	target := filepath.Join(t.TempDir(), "vault-conductors")

	var wg sync.WaitGroup
	wg.Add(2)
	var migErr error
	go func() {
		defer wg.Done()
		_, migErr = MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true})
	}()
	go func() {
		defer wg.Done()
		// A concurrent `conductor setup`-style durable write for a NEW conductor.
		_ = SaveConductorMeta(&ConductorMeta{Name: "beta", Agent: "claude", Profile: "default"})
	}()
	wg.Wait()

	if migErr != nil {
		t.Fatalf("MigrateConductorDir: %v", migErr)
	}
	// The migration committed the config → the resolver is at target. beta's
	// durable record must live there, whichever side acquired the lock first.
	if cd, _ := ConductorDir(); cd != target {
		t.Fatalf("ConductorDir() = %q, want %q", cd, target)
	}
	if _, err := os.Stat(filepath.Join(target, "beta", "meta.json")); err != nil {
		t.Fatalf("beta's durable record was stranded/lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "alpha", "meta.json")); err != nil {
		t.Fatalf("alpha's durable record missing at target: %v", err)
	}
}

// Blocker 1: a source and target that are lexically distinct but resolve to the
// SAME physical tree (here via a symlinked base) must be rejected under --force,
// not silently merged-then-deleted — which would delete the one shared meta.json.
func TestMigrateConductorDir_RejectsSymlinkAliasedSameTree(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default","description":"SHARED"}`,
	})
	// A second, lexically-distinct path that resolves to the SAME physical base.
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(defaultBase, alias); err != nil {
		t.Fatal(err)
	}

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{
		From: defaultBase, Target: alias, Apply: true, Force: true,
	})
	if err == nil {
		t.Fatal("expected the symlink-aliased same-tree migration to be rejected")
	}
	if !strings.Contains(err.Error(), "same directory") {
		t.Fatalf("error = %v, want it to flag the alias", err)
	}
	if res != nil && res.ConfigWritten {
		t.Fatal("config must NOT be written when the alias is rejected")
	}
	// The single shared meta.json (source and target are one physical tree)
	// survives — nothing was merged or deleted.
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "meta.json"), "SHARED")
	assertFileContains(t, filepath.Join(alias, "alpha", "meta.json"), "SHARED")
}

// Blocker 2: a conductor home whose meta.json is a RELATIVE symlink resolves to a
// different file once the home is relocated (CopyTree preserves the link). A
// content-blind verify would pass against the unrelated record while the real
// identity is stranded at the source; the bytes-equal verify must reject and
// leave the source's record intact.
func TestMigrateConductorDir_VerifyRejectsRelocatedMetaSymlink(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")

	alphaDir := filepath.Join(defaultBase, "alpha")
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// "../../external-id" from <base>/alpha resolves to <xdgDataHome>/agent-deck/external-id.
	srcID := filepath.Join(xdgDataHome, "agent-deck", "external-id")
	if err := os.WriteFile(srcID, []byte(`{"name":"alpha","profile":"default","id":"REAL-SOURCE"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../external-id", filepath.Join(alphaDir, "meta.json")); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "vault-conductors")
	// After the move, alpha/meta.json -> ../../external-id resolves to
	// <dir(target)>/external-id, where a DIFFERENT record waits. A content-blind
	// verify would accept it; the bytes-equal verify must reject.
	decoy := filepath.Join(filepath.Dir(target), "external-id")
	if err := os.WriteFile(decoy, []byte(`{"name":"alpha","profile":"default","id":"DECOY"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true})
	if err == nil {
		t.Fatal("expected verify to reject the relocated meta.json symlink mismatch")
	}
	if res != nil && res.ConfigWritten {
		t.Fatal("config must NOT be committed when verify fails")
	}
	// The source's real identity record is untouched (not deleted/stranded), and
	// the source home still resolves to it.
	assertFileContains(t, srcID, "REAL-SOURCE")
	assertFileContains(t, filepath.Join(alphaDir, "meta.json"), "REAL-SOURCE")
	if cd, _ := ConductorDir(); cd != defaultBase {
		t.Fatalf("ConductorDir() = %q, want unchanged default %q", cd, defaultBase)
	}
}

func TestDetectConductorDirSplitBrain(t *testing.T) {
	_, xdgConfigHome, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
	})

	// No override yet → resolved == default (populated) → no split-brain.
	if _, ok := DetectConductorDirSplitBrain(); ok {
		t.Fatal("no override should not report split-brain")
	}

	// Override set to an empty dir while default stays populated → split-brain.
	override := filepath.Join(t.TempDir(), "empty-override")
	writeConductorDirConfig(t, xdgConfigHome, override)
	msg, ok := DetectConductorDirSplitBrain()
	if !ok {
		t.Fatal("expected split-brain when override empty and default populated")
	}
	if !strings.Contains(msg, "migrate-dir") {
		t.Fatalf("warning should point at migrate-dir, got %q", msg)
	}

	// Once the override is itself populated → no split-brain.
	writeConductorHome(t, override, "beta", map[string]string{
		"meta.json": `{"name":"beta","profile":"default"}`,
	})
	if _, ok := DetectConductorDirSplitBrain(); ok {
		t.Fatal("populated override should not report split-brain")
	}
}
