//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAgentDeck writes an executable shell script at dir/name that prints
// what `agent-deck version` prints for version (exit 1 when version is
// empty, like a binary that cannot run).
func fakeAgentDeck(t *testing.T, dir, name, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nexit 1\n"
	if version != "" {
		body = "#!/bin/sh\necho 'Agent Deck v" + version + "'\n"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// shadowFixture is the incident layout: the updated binary in newDir (a
// stand-in for ~/.local/bin) and an older copy in oldDir (a stand-in for
// /usr/local/bin).
type shadowFixture struct {
	newDir, oldDir string
	self, old      string
}

func newShadowFixture(t *testing.T) shadowFixture {
	t.Helper()
	root := t.TempDir()
	f := shadowFixture{newDir: filepath.Join(root, "local", "bin"), oldDir: filepath.Join(root, "usr", "local", "bin")}
	f.self = fakeAgentDeck(t, f.newDir, "agent-deck", "1.16.28")
	f.old = fakeAgentDeck(t, f.oldDir, "agent-deck", "1.16.3")
	return f
}

func pathOf(dirs ...string) string { return strings.Join(dirs, string(os.PathListSeparator)) }

func writableAll(string) bool { return true }

func assertStillThere(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("the scan must never remove %s: %v", p, err)
		}
	}
}

// The incident: an older copy earlier on PATH is reported with its path,
// both versions and both fixes; in the other order nothing is reported.
func TestFindShadowingCopies_BothOrders(t *testing.T) {
	f := newShadowFixture(t)

	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, f.newDir), Writable: writableAll}.Find()
	if len(got) != 1 {
		t.Fatalf("old copy first on PATH: got %d copies %+v, want 1", len(got), got)
	}
	c := got[0]
	if c.Path != f.old || c.Version != "1.16.3" || c.Installed != "1.16.28" || c.InstalledPath != f.self || !c.InstalledOnPath {
		t.Fatalf("copy = %+v", c)
	}
	if c.Target != "" || c.NeedsSudo {
		t.Fatalf("plain user-writable file reported as symlink or sudo: %+v", c)
	}
	if c.Remove != "rm "+f.old {
		t.Fatalf("remove fix = %q", c.Remove)
	}
	if c.Reorder != `export PATH=`+f.newDir+`:"$PATH"` {
		t.Fatalf("reorder fix = %q", c.Reorder)
	}
	w := c.Warning()
	for _, want := range []string{f.old, "v1.16.3", f.self, "v1.16.28", "comes before", "Fix: rm " + f.old, "put " + f.newDir + " first on PATH", "never removes it"} {
		if !strings.Contains(w, want) {
			t.Fatalf("warning lacks %q:\n%s", want, w)
		}
	}

	if got := (ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.newDir, f.oldDir), Writable: writableAll}.Find()); len(got) != 0 {
		t.Fatalf("updated binary first on PATH: got %+v, want none", got)
	}
	assertStillThere(t, f.self, f.old)
}

// A root-owned /usr/local/bin/agent-deck symlink to an old dev build: the
// copy names its target, says removing drops only the symlink, and the
// remove command carries sudo because the directory is not writable.
func TestFindShadowingCopies_RootOwnedSymlink(t *testing.T) {
	f := newShadowFixture(t)
	if err := os.Remove(f.old); err != nil {
		t.Fatal(err)
	}
	devBuild := fakeAgentDeck(t, filepath.Join(filepath.Dir(f.oldDir), "dev-build"), "agent-deck-dev", "1.16.3")
	if err := os.Symlink(devBuild, f.old); err != nil {
		t.Fatal(err)
	}
	notWritable := func(dir string) bool { return dir != f.oldDir }

	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, f.newDir), Writable: notWritable}.Find()
	if len(got) != 1 {
		t.Fatalf("got %+v, want the symlink", got)
	}
	c := got[0]
	wantTarget, _ := filepath.EvalSymlinks(devBuild)
	if c.Path != f.old || c.Target != wantTarget || c.Version != "1.16.3" || !c.NeedsSudo {
		t.Fatalf("copy = %+v", c)
	}
	if c.Remove != "sudo rm "+f.old {
		t.Fatalf("remove fix = %q, want sudo for a directory this user cannot write", c.Remove)
	}
	w := c.Warning()
	for _, want := range []string{"symlink to " + wantTarget, "not removable without sudo", "Fix: sudo rm " + f.old, "removes the symlink only"} {
		if !strings.Contains(w, want) {
			t.Fatalf("warning lacks %q:\n%s", want, w)
		}
	}

	if got := (ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.newDir, f.oldDir), Writable: notWritable}.Find()); len(got) != 0 {
		t.Fatalf("updated binary first on PATH: got %+v, want none", got)
	}
	assertStillThere(t, f.old, devBuild)
}

// The real permission check (access(2) W_OK) on a read-only directory.
// Root may write anywhere, so this half only runs as a normal user.
func TestFindShadowingCopies_ReadOnlyDirNeedsSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; covered by the Writable seam in TestFindShadowingCopies_RootOwnedSymlink")
	}
	f := newShadowFixture(t)
	if err := os.Chmod(f.oldDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.oldDir, 0o755) })
	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, f.newDir)}.Find()
	if len(got) != 1 || !got[0].NeedsSudo || !strings.HasPrefix(got[0].Remove, "sudo rm ") {
		t.Fatalf("read-only dir: got %+v", got)
	}
}

// A symlink to the updated binary itself earlier on PATH is the updated
// binary, not a shadow: nothing is reported and the fix names that entry.
func TestFindShadowingCopies_SymlinkToSelfIsNotAShadow(t *testing.T) {
	f := newShadowFixture(t)
	linkDir := filepath.Join(t.TempDir(), "links")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.self, filepath.Join(linkDir, "agent-deck")); err != nil {
		t.Fatal(err)
	}
	if got := (ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(linkDir, f.oldDir, f.newDir), Writable: writableAll}.Find()); len(got) != 0 {
		t.Fatalf("symlink to self first: got %+v, want none", got)
	}
	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, linkDir, f.newDir), Writable: writableAll}.Find()
	if len(got) != 1 || got[0].InstalledPath != filepath.Join(linkDir, "agent-deck") || !strings.Contains(got[0].Reorder, linkDir) {
		t.Fatalf("old copy before a symlink to self: got %+v", got)
	}
}

// Same or newer copies earlier on PATH are left alone (a deliberate dev
// build); a copy whose version cannot be read is reported as unknown.
func TestFindShadowingCopies_VersionRules(t *testing.T) {
	f := newShadowFixture(t)
	for _, v := range []string{"1.16.28", "1.17.0"} {
		fakeAgentDeck(t, f.oldDir, "agent-deck", v)
		if got := (ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, f.newDir), Writable: writableAll}.Find()); len(got) != 0 {
			t.Fatalf("v%s earlier on PATH: got %+v, want none", v, got)
		}
	}
	fakeAgentDeck(t, f.oldDir, "agent-deck", "")
	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, f.newDir), Writable: writableAll}.Find()
	if len(got) != 1 || got[0].Version != "" || !strings.Contains(got[0].Warning(), "unknown version") {
		t.Fatalf("unreadable copy: got %+v", got)
	}
}

// A "+local" build of the same version earlier on PATH is older than the
// published release that replaced it (#2710), so it is reported; the
// published release earlier on PATH than a "+local" build of the same
// version is not, and neither is another "+local" build of that version.
func TestFindShadowingCopies_LocalBuildOfSameVersion(t *testing.T) {
	f := newShadowFixture(t)
	fakeAgentDeck(t, f.oldDir, "agent-deck", "1.16.28+local.20261013.abc1234")
	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, f.newDir), Writable: writableAll}.Find()
	if len(got) != 1 || got[0].Version != "1.16.28+local.20261013.abc1234" {
		t.Fatalf("local build of the installed release earlier on PATH: got %+v, want it reported", got)
	}

	fakeAgentDeck(t, f.oldDir, "agent-deck", "1.16.28")
	for _, self := range []string{"1.16.28+local.20261018.def5678", "1.16.28+local"} {
		if got := (ShadowScan{Self: f.self, SelfVersion: self, PathEnv: pathOf(f.oldDir, f.newDir), Writable: writableAll}.Find()); len(got) != 0 {
			t.Fatalf("release earlier on PATH than local build %s: got %+v, want none", self, got)
		}
	}
	fakeAgentDeck(t, f.oldDir, "agent-deck", "1.16.28+local.20261013.abc1234")
	if got := (ShadowScan{Self: f.self, SelfVersion: "1.16.28+local.20261018.def5678", PathEnv: pathOf(f.oldDir, f.newDir), Writable: writableAll}.Find()); len(got) != 0 {
		t.Fatalf("another local build of the same version: got %+v, want none", got)
	}
}

// When the updated binary is not on PATH at all, the older copy shells
// find is still reported, worded for that case.
func TestFindShadowingCopies_SelfNotOnPath(t *testing.T) {
	f := newShadowFixture(t)
	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir), Writable: writableAll}.Find()
	if len(got) != 1 || got[0].InstalledOnPath || !strings.Contains(got[0].Warning(), "is not on PATH") {
		t.Fatalf("self off PATH: got %+v", got)
	}
}

// Empty and relative PATH entries, missing dirs, non-executable files and
// an unknown own version never produce a warning.
func TestFindShadowingCopies_Ignores(t *testing.T) {
	f := newShadowFixture(t)
	if err := os.Chmod(f.old, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := (ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf("", ".", "relative/bin", filepath.Join(t.TempDir(), "missing"), f.oldDir, f.newDir), Writable: writableAll}.Find()); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
	if got := (ShadowScan{Self: f.self, SelfVersion: "", PathEnv: pathOf(f.oldDir, f.newDir)}.Find()); got != nil {
		t.Fatalf("unknown own version: got %+v", got)
	}
}

// writeScript writes an executable shell script at dir/agent-deck.
func writeScript(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A copy whose `version` leaves a child holding stdout open (a wrapper
// that runs `sleep`) must not hold the scan past the probe timeout plus
// BinaryProbeWaitDelay; it is reported with an unknown version.
func TestFindShadowingCopies_HungChildIsBounded(t *testing.T) {
	f := newShadowFixture(t)
	writeScript(t, f.oldDir, "sleep 20")
	start := time.Now()
	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, f.newDir), Writable: writableAll, ProbeTimeout: 300 * time.Millisecond}.Find()
	elapsed := time.Since(start)
	if elapsed > 300*time.Millisecond+BinaryProbeWaitDelay+2*time.Second {
		t.Fatalf("scan of a copy whose child keeps stdout open took %v, want about probe timeout + %v", elapsed, BinaryProbeWaitDelay)
	}
	if len(got) != 1 || got[0].Version != "" {
		t.Fatalf("hung copy: got %+v, want it reported with an unknown version", got)
	}
}

// Probes share one budget: once it is spent the remaining copies are
// reported as unknown version without being run, so N hung copies do not
// cost N probe timeouts.
func TestFindShadowingCopies_ScanBudget(t *testing.T) {
	f := newShadowFixture(t)
	root := t.TempDir()
	var dirs []string
	for _, name := range []string{"a", "b", "c"} {
		dir := filepath.Join(root, name)
		writeScript(t, dir, "exec sleep 20")
		dirs = append(dirs, dir)
	}
	start := time.Now()
	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(append(dirs, f.newDir)...), Writable: writableAll, Budget: time.Second}.Find()
	elapsed := time.Since(start)
	if elapsed > 4*time.Second {
		t.Fatalf("three hung copies with a 1s budget took %v", elapsed)
	}
	if len(got) != 3 {
		t.Fatalf("got %d copies %+v, want all 3 reported", len(got), got)
	}
	for _, c := range got {
		if c.Version != "" {
			t.Fatalf("copy %s: version %q, want unknown", c.Path, c.Version)
		}
	}
}

// The same file reached through two PATH entries (merged /bin and
// /usr/bin, a directory and a symlink to it) is one copy: listed once.
func TestFindShadowingCopies_SameFileTwiceIsOneCopy(t *testing.T) {
	f := newShadowFixture(t)
	alias := filepath.Join(t.TempDir(), "bin-alias")
	if err := os.Symlink(f.oldDir, alias); err != nil {
		t.Fatal(err)
	}
	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, alias, f.newDir), Writable: writableAll}.Find()
	if len(got) != 1 || got[0].Path != f.old {
		t.Fatalf("one file on two PATH entries: got %+v, want it once as %s", got, f.old)
	}
}

// A package-managed copy is not given an rm fix: the warning says to
// remove or upgrade it with its package manager.
func TestFindShadowingCopies_PackageManagedCopy(t *testing.T) {
	f := newShadowFixture(t)
	if err := os.Remove(f.old); err != nil {
		t.Fatal(err)
	}
	cellar := fakeAgentDeck(t, filepath.Join(t.TempDir(), "usr", "local", "Cellar", "agent-deck", "1.16.3", "bin"), "agent-deck", "1.16.3")
	if err := os.Symlink(cellar, f.old); err != nil {
		t.Fatal(err)
	}
	got := ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(f.oldDir, f.newDir), Writable: writableAll}.Find()
	if len(got) != 1 {
		t.Fatalf("got %+v, want the Homebrew copy", got)
	}
	c := got[0]
	if c.ManagedBy != "Homebrew" || c.Remove != "" {
		t.Fatalf("copy = %+v, want managed_by Homebrew and no rm command", c)
	}
	w := c.Warning()
	if !strings.Contains(w, "Fix: remove or upgrade it with its package manager (Homebrew)") || strings.Contains(w, "rm ") || strings.Contains(w, "removes the symlink only") {
		t.Fatalf("warning:\n%s", w)
	}
}

func TestPackageManagerFor(t *testing.T) {
	for _, tc := range []struct{ path, target, want string }{
		{"/usr/local/bin/agent-deck", "/usr/local/Cellar/agent-deck/1.16.3/bin/agent-deck", "Homebrew"},
		{"/opt/homebrew/bin/agent-deck", "", "Homebrew"},
		{"/home/linuxbrew/.linuxbrew/bin/agent-deck", "", "Homebrew"},
		{"/home/u/.nix-profile/bin/agent-deck", "/nix/store/abc-agent-deck/bin/agent-deck", "Nix"},
		{"/run/current-system/sw/bin/agent-deck", "", "Nix"},
		{"/home/u/.asdf/shims/agent-deck", "", "asdf"},
		{"/home/u/.local/share/mise/shims/agent-deck", "", "mise"},
		{"/snap/bin/agent-deck", "", "snap"},
		{"/usr/bin/agent-deck", "", "your distribution's package manager"},
		{"/usr/local/bin/agent-deck", "/opt/dev/agent-deck", ""},
		{"/home/u/.local/bin/agent-deck", "", ""},
	} {
		if got := packageManagerFor(tc.path, tc.target); got != tc.want {
			t.Errorf("packageManagerFor(%q, %q) = %q, want %q", tc.path, tc.target, got, tc.want)
		}
	}
}

// A relative PATH entry depends on the caller's working directory: an
// agent-deck in the cwd with "." (or a relative dir) on PATH is skipped.
func TestFindShadowingCopies_RelativePathEntrySkipped(t *testing.T) {
	f := newShadowFixture(t)
	t.Chdir(f.oldDir)
	for _, entry := range []string{".", "./", filepath.Join("..", filepath.Base(f.oldDir))} {
		if got := (ShadowScan{Self: f.self, SelfVersion: "1.16.28", PathEnv: pathOf(entry, f.newDir), Writable: writableAll}.Find()); len(got) != 0 {
			t.Fatalf("relative PATH entry %q with an older agent-deck in it: got %+v, want none", entry, got)
		}
	}
}
