// Package session — regression tests for issue #1222.
//
// The work-profile 401 "/login" loop. agent-deck seeds each managed
// session's scratch `$CLAUDE_CONFIG_DIR/.credentials.json` as a SYMLINK to
// the canonical profile credentials (e.g. `~/.claude-work/.credentials.json`)
// so every session shares one OAuth token. Running `/login` INSIDE a managed
// session makes Claude overwrite that symlink with a real-file COPY of the
// token. Anthropic OAuth rotates the refresh token on each refresh, so every
// stale real-file copy 401s on the next rotation, and the fresh in-session
// token is stranded in scratch instead of reaching canonical.
//
// Root cause, confirmed on the innotrade conductor 2026-05-29: 72 work
// sessions correctly symlinked, 12 had stale diverged real-file copies — and
// those 12 were exactly the sessions that 401'd. `mirrorProfileEntries` was
// idempotent the wrong way (`Lstat(linkPath)==nil → continue`), so once the
// symlink was clobbered into a real file it was never repaired, on any
// start/restart/resume.
//
// These tests pin the heal: for `.credentials.json` specifically the scratch
// seeding must re-assert the symlink to canonical, promoting a fresh
// in-session login to canonical first when the scratch copy is newer.

package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCredFile writes a fake credentials file with the given token and mtime
// (0600 — token perms). Returns nothing; fails the test on error.
func writeCredFile(t *testing.T, path, token string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatalf("write cred file %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// assertIsSymlinkTo asserts linkPath is a symlink resolving to wantTarget.
func assertIsSymlinkTo(t *testing.T, linkPath, wantTarget string) {
	t.Helper()
	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat %s: %v", linkPath, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s must be a symlink after re-assertion; got mode %v", linkPath, fi.Mode())
	}
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink %s: %v", linkPath, err)
	}
	if got != wantTarget {
		t.Fatalf("%s points at %q; want %q", linkPath, got, wantTarget)
	}
}

// (a) A stale REAL-FILE copy of .credentials.json in scratch (the /login
// clobber) must be re-asserted to a symlink to canonical. Canonical is the
// source of truth here (scratch copy is older), so it must be left unchanged.
func TestMirrorProfileEntries_StaleRealFileCredentials_ReassertedToSymlink(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()

	canonicalToken := `{"token":"canonical-fresh"}`
	staleToken := `{"token":"stale-copy"}`
	now := time.Now()
	writeCredFile(t, filepath.Join(source, ".credentials.json"), canonicalToken, now)
	// Scratch real-file copy is OLDER than canonical → stale, relink only.
	writeCredFile(t, filepath.Join(dest, ".credentials.json"), staleToken, now.Add(-time.Hour))

	if err := mirrorProfileEntries(dest, source); err != nil {
		t.Fatalf("mirrorProfileEntries: %v", err)
	}

	assertIsSymlinkTo(t, filepath.Join(dest, ".credentials.json"), filepath.Join(source, ".credentials.json"))

	// Canonical untouched — we did not promote a stale copy over it.
	got, err := os.ReadFile(filepath.Join(source, ".credentials.json"))
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if string(got) != canonicalToken {
		t.Fatalf("canonical credentials must be untouched by a stale relink; got %q want %q", string(got), canonicalToken)
	}
}

// (b) A correct symlink to canonical must be left alone (idempotent). Running
// the seeding repeatedly must not churn the link or rewrite canonical.
func TestMirrorProfileEntries_CorrectCredentialSymlink_LeftAlone(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()

	canonicalToken := `{"token":"canonical"}`
	target := filepath.Join(source, ".credentials.json")
	writeCredFile(t, target, canonicalToken, time.Now())
	if err := os.Symlink(target, filepath.Join(dest, ".credentials.json")); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	canonicalStatBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat canonical: %v", err)
	}

	if err := mirrorProfileEntries(dest, source); err != nil {
		t.Fatalf("mirrorProfileEntries: %v", err)
	}

	assertIsSymlinkTo(t, filepath.Join(dest, ".credentials.json"), target)

	canonicalStatAfter, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat canonical after: %v", err)
	}
	if !canonicalStatBefore.ModTime().Equal(canonicalStatAfter.ModTime()) {
		t.Fatalf("idempotent run rewrote canonical (mtime changed %v → %v)", canonicalStatBefore.ModTime(), canonicalStatAfter.ModTime())
	}
}

// (d) NO-PROMOTE (subscription OAuth fix): a scratch real-file even NEWER than
// canonical (a diverged in-session /login, or an mtime-bumped stale copy) must
// be force-replaced with a clean symlink to canonical, and canonical must NEVER
// be overwritten. Matching Claude Code's own realpath-keyed lock behavior, mtime
// is not a valid-token signal for single-use rotating refresh tokens, so the old
// mtime-promote could clobber a good canonical with a stale scratch token and
// fork a second rotation chain. The single canonical token is the only source
// of truth; in-session /login must be done in the canonical profile instead.
func TestMirrorProfileEntries_NewerScratchCredentials_NeverPromoted(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()

	canonical := `{"token":"canonical-source-of-truth"}`
	divergedNewer := `{"token":"diverged-in-session-copy"}`
	now := time.Now()
	target := filepath.Join(source, ".credentials.json")
	writeCredFile(t, target, canonical, now.Add(-2*time.Hour))
	// Scratch real-file is NEWER than canonical — the old code would have
	// promoted it. The fix must refuse to promote.
	writeCredFile(t, filepath.Join(dest, ".credentials.json"), divergedNewer, now)

	canonicalStatBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat canonical: %v", err)
	}

	if err := mirrorProfileEntries(dest, source); err != nil {
		t.Fatalf("mirrorProfileEntries: %v", err)
	}

	// Canonical must be byte-for-byte unchanged — never overwritten by a
	// newer scratch copy.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if string(got) != canonical {
		t.Fatalf("canonical must NEVER be overwritten by a newer scratch copy; got %q want %q", string(got), canonical)
	}
	canonicalStatAfter, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat canonical after: %v", err)
	}
	if !canonicalStatBefore.ModTime().Equal(canonicalStatAfter.ModTime()) {
		t.Fatalf("canonical mtime changed (%v → %v): it was rewritten", canonicalStatBefore.ModTime(), canonicalStatAfter.ModTime())
	}

	// Scratch real-file is replaced by a clean symlink to canonical, so it
	// now resolves to the single source-of-truth token.
	assertIsSymlinkTo(t, filepath.Join(dest, ".credentials.json"), target)
	viaLink, err := os.ReadFile(filepath.Join(dest, ".credentials.json"))
	if err != nil {
		t.Fatalf("read scratch via link: %v", err)
	}
	if string(viaLink) != canonical {
		t.Fatalf("scratch link must resolve to canonical token; got %q", string(viaLink))
	}
}

// (f) Canonical missing + scratch real-file: with no canonical to point at, the
// reassert must leave the only token copy in place rather than stranding it
// behind a dangling symlink (and it must still never write canonical). Once the
// user logs in to canonical, the next start replaces this with a symlink.
func TestMirrorProfileEntries_CanonicalMissing_RealFileLeftIntact(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()

	onlyCopy := `{"token":"stranded-but-only-copy"}`
	scratchCred := filepath.Join(dest, ".credentials.json")
	writeCredFile(t, scratchCred, onlyCopy, time.Now())
	// No canonical credentials in source.

	if err := mirrorProfileEntries(dest, source); err != nil {
		t.Fatalf("mirrorProfileEntries: %v", err)
	}

	// Canonical must not have been created by a promote.
	if _, err := os.Lstat(filepath.Join(source, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("canonical must not be created when absent; lstat err = %v", err)
	}
	// The only token copy must survive as a real file.
	fi, err := os.Lstat(scratchCred)
	if err != nil {
		t.Fatalf("lstat scratch cred: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("scratch credentials must remain a real file when canonical is absent (no dangling symlink)")
	}
	got, err := os.ReadFile(scratchCred)
	if err != nil {
		t.Fatalf("read scratch cred: %v", err)
	}
	if string(got) != onlyCopy {
		t.Fatalf("only token copy must be left intact; got %q want %q", string(got), onlyCopy)
	}
}

// (e) settings.json must remain excluded from the symlink/credentials path —
// it is OWNED (copied + mutated) by the scratch seeding. The credentials heal
// must not generalize to settings.json or break the telegram-gate behavior.
func TestMirrorProfileEntries_SettingsJsonStillExcluded(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()

	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"enabledPlugins":{}}`), 0o644); err != nil {
		t.Fatalf("write source settings: %v", err)
	}
	writeCredFile(t, filepath.Join(source, ".credentials.json"), `{"token":"canonical"}`, time.Now())

	// dest already owns a real-file settings.json (written by the seeding
	// before mirrorProfileEntries runs).
	ownedSettings := `{"enabledPlugins":{"telegram@claude-plugins-official":false}}`
	destSettings := filepath.Join(dest, "settings.json")
	if err := os.WriteFile(destSettings, []byte(ownedSettings), 0o600); err != nil {
		t.Fatalf("write dest settings: %v", err)
	}

	if err := mirrorProfileEntries(dest, source); err != nil {
		t.Fatalf("mirrorProfileEntries: %v", err)
	}

	// settings.json must stay a REAL FILE with its owned content (never a symlink).
	fi, err := os.Lstat(destSettings)
	if err != nil {
		t.Fatalf("lstat dest settings: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("settings.json must remain a real file, never symlinked; got symlink")
	}
	got, err := os.ReadFile(destSettings)
	if err != nil {
		t.Fatalf("read dest settings: %v", err)
	}
	if string(got) != ownedSettings {
		t.Fatalf("owned settings.json must be untouched; got %q want %q", string(got), ownedSettings)
	}

	// But .credentials.json IS symlinked.
	assertIsSymlinkTo(t, filepath.Join(dest, ".credentials.json"), filepath.Join(source, ".credentials.json"))
}

// (c) Start/restart re-assertion: the heal must run through the real spawn
// entry point EnsureWorkerScratchConfigDir (called on every Start / Restart /
// --resume via prepareWorkerScratchConfigDirForSpawn). First spawn links;
// an in-session /login clobbers the link into a real file; the next start
// re-asserts the symlink.
func TestEnsureWorkerScratchConfigDir_RestartRepairsClobberedCredentialSymlink(t *testing.T) {
	withTelegramConductorPresent(t)

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"enabledPlugins":{}}`), 0o644); err != nil {
		t.Fatalf("write source settings: %v", err)
	}
	canonicalToken := `{"token":"canonical"}`
	writeCredFile(t, filepath.Join(source, ".credentials.json"), canonicalToken, time.Now())

	home := t.TempDir()
	t.Setenv("HOME", home)

	inst := &Instance{
		ID:    "00000000-0000-0000-0000-0000000012222",
		Tool:  "claude",
		Title: "my-worker",
	}

	// First spawn — scratch .credentials.json is a symlink to canonical.
	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("first EnsureWorkerScratchConfigDir: %v", err)
	}
	if scratch == "" {
		t.Fatal("setup: scratch dir should be non-empty for a worker")
	}
	scratchCred := filepath.Join(scratch, ".credentials.json")
	assertIsSymlinkTo(t, scratchCred, filepath.Join(source, ".credentials.json"))

	// Simulate `/login` inside the session: Claude replaces the symlink with
	// a real-file copy (older than canonical → stale on next rotation).
	if err := os.Remove(scratchCred); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}
	writeCredFile(t, scratchCred, `{"token":"stale-in-session-copy"}`, time.Now().Add(-time.Hour))
	if fi, _ := os.Lstat(scratchCred); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("setup: clobbered entry should be a real file, not a symlink")
	}

	// Next start / restart / --resume re-runs the seeding and must repair it.
	if _, err := inst.EnsureWorkerScratchConfigDir(source); err != nil {
		t.Fatalf("restart EnsureWorkerScratchConfigDir: %v", err)
	}
	assertIsSymlinkTo(t, scratchCred, filepath.Join(source, ".credentials.json"))
}
