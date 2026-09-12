package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakeAgentDeck writes a stand-in for the remote's agent-deck binary: a shell
// script whose `version` answer is the given version.
func fakeAgentDeck(t *testing.T, path, version string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fakeAgentDeckPayload(version)), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func fakeAgentDeckPayload(version string) string {
	return "#!/bin/sh\necho \"Agent Deck v" + version + "\"\n"
}

// remoteLayout is a throwaway remote filesystem: HOME, a PATH directory and
// the runner that executes deploy commands under a sh whose HOME and PATH
// point at it (#2244).
type remoteLayout struct {
	home, pathDir string
}

func newRemoteLayout(t *testing.T) remoteLayout {
	t.Helper()
	root := t.TempDir()
	l := remoteLayout{home: filepath.Join(root, "home"), pathDir: filepath.Join(root, "pathbin")}
	for _, d := range []string{l.home, l.pathDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// runner executes remote commands locally with HOME and PATH set to the
// layout, a sudo stand-in from prelude, and the given configured path.
func (l remoteLayout) runner(t *testing.T, configuredPath, prelude string) *SSHRunner {
	t.Helper()
	env := "HOME=" + shellQuote(l.home) + "; export HOME; PATH=" + shellQuote(l.pathDir) + ":/usr/bin:/bin; export PATH; "
	r := shellRunner(t, env+prelude)
	r.AgentDeckPath = configuredPath
	r.configuredPath = configuredPath
	return r
}

func mustSameFile(t *testing.T, a, b string) {
	t.Helper()
	ia, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(ia, ib) {
		t.Fatalf("%s and %s are different files", a, b)
	}
}

// The documented remedy for a root-owned install path is a symlink at the
// old path to ~/.local/bin/agent-deck. A deploy onto that path must follow
// the link and replace the file it points at, leaving the symlink alone,
// so $PATH (and the remote's service units) run the new version.
func TestInstallBinary_SymlinkedPathUpdatesTheLinkTarget(t *testing.T) {
	l := newRemoteLayout(t)
	target := filepath.Join(l.home, ".local", "bin", "agent-deck")
	fakeAgentDeck(t, target, "1.16.5", 0o750)
	link := filepath.Join(l.pathDir, "agent-deck")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r := l.runner(t, link, "sudo() { echo sudo-must-not-run >&2; exit 99; }; ")

	if err := r.InstallBinary(context.Background(), []byte(fakeAgentDeckPayload("1.16.6")), "1.16.6"); err != nil {
		t.Fatalf("InstallBinary: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink at the configured path must survive the deploy: %v %v", info, err)
	}
	if got, _ := os.ReadFile(target); string(got) != fakeAgentDeckPayload("1.16.6") {
		t.Fatalf("link target not updated: %q", got)
	}
	if mode := mustMode(t, target); mode != 0o755 {
		t.Errorf("mode = %o, want the file's 0750 kept and made runnable (0755)", mode)
	}
	if ver, found := r.CheckBinary(context.Background()); !found || ver != "1.16.6" {
		t.Errorf("configured path reports %q %v after deploy, want 1.16.6", ver, found)
	}
	if report := r.LastInstallReport(); !strings.Contains(report, target) {
		t.Errorf("report must name the file actually replaced: %q", report)
	}
	mustSameFile(t, link, target) // what $PATH runs is the file that was deployed
}

// A root-owned /usr/local/bin holding a symlink into a user-writable
// directory needs no sudo: the resolved file's directory is what decides.
func TestInstallBinary_RootOwnedDirBehindSymlinkNeedsNoSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	l := newRemoteLayout(t)
	target := filepath.Join(l.home, ".local", "bin", "agent-deck")
	fakeAgentDeck(t, target, "1.16.5", 0o755)
	link := filepath.Join(l.pathDir, "agent-deck")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(l.pathDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(l.pathDir, 0o755) })
	sudoCalls := filepath.Join(t.TempDir(), "sudo.log")
	r := l.runner(t, link, "sudo() { echo called >> "+shellQuote(sudoCalls)+"; return 1; }; ")

	if err := r.InstallBinary(context.Background(), []byte(fakeAgentDeckPayload("1.16.6")), "1.16.6"); err != nil {
		t.Fatalf("InstallBinary: %v", err)
	}
	if _, err := os.Stat(sudoCalls); !os.IsNotExist(err) {
		t.Fatal("sudo must not be consulted when the resolved directory is writable")
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", info, err)
	}
	if got, _ := os.ReadFile(target); string(got) != fakeAgentDeckPayload("1.16.6") {
		t.Fatalf("link target not updated: %q", got)
	}
}

// The configured path and the binary on $PATH are two different files: the
// $PATH one is what the remote runs, so it is updated (and the configured
// one too, so the controller's own commands see the same version), and the
// report says so.
func TestInstallBinary_PathBinaryDiffersFromConfiguredPath(t *testing.T) {
	l := newRemoteLayout(t)
	configured := filepath.Join(l.home, "opt", "agent-deck")
	fakeAgentDeck(t, configured, "1.16.5", 0o755)
	onPath := filepath.Join(l.pathDir, "agent-deck")
	fakeAgentDeck(t, onPath, "1.16.5", 0o755)
	r := l.runner(t, configured, noSudo)

	if err := r.InstallBinary(context.Background(), []byte(fakeAgentDeckPayload("1.16.6")), "1.16.6"); err != nil {
		t.Fatalf("InstallBinary: %v", err)
	}
	for _, p := range []string{onPath, configured} {
		if got, _ := os.ReadFile(p); string(got) != fakeAgentDeckPayload("1.16.6") {
			t.Errorf("%s not updated: %q", p, got)
		}
	}
	report := r.LastInstallReport()
	if !strings.Contains(report, onPath) || !strings.Contains(report, configured) || !strings.Contains(report, "$PATH") {
		t.Errorf("report must name the $PATH binary and the configured path: %q", report)
	}
}

// Post-deploy verification: `command -v agent-deck` must resolve to the same
// inode as the file that was deployed; a $PATH that still runs something
// else fails with the full remedy.
func TestInstallBinary_VerifiesPathResolvesToDeployedInode(t *testing.T) {
	l := newRemoteLayout(t)
	configured := filepath.Join(l.home, ".local", "bin", "agent-deck")
	fakeAgentDeck(t, configured, "1.16.5", 0o755)
	// Nothing on PATH: the deploy lands but the remote cannot run it.
	r := l.runner(t, configured, noSudo)
	err := r.InstallBinary(context.Background(), []byte(fakeAgentDeckPayload("1.16.6")), "1.16.6")
	if err == nil || !strings.Contains(err.Error(), "not on the remote's $PATH") || !strings.Contains(err.Error(), "add "+configured+" to PATH") {
		t.Fatalf("got %v, want the not-on-PATH remedy in full", err)
	}
}

// The deploy script itself refuses to put a regular file over a symlink,
// whatever path it was handed.
func TestDeployScript_NeverReplacesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "agent-deck")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", remoteDeployScript, "sh", dir, link)
	cmd.Stdin = strings.NewReader("new")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != remoteDeploySymlinkExit {
		t.Fatalf("script must refuse a symlink with exit %d; got %v: %s", remoteDeploySymlinkExit, err, out)
	}
	if info, lerr := os.Lstat(link); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", info, lerr)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("target touched: %q", got)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "*.new.*")); len(entries) != 0 {
		t.Errorf("staging left behind: %v", entries)
	}
}

// Owner and mode of the replaced file are kept when the deploy runs as
// root (sudo over a user-owned file): the stand-in records the chown the
// script asks for.
func TestDeployScript_PreservesOwnerUnderSudo(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "chown.log")
	// Pretend to be root and capture chown instead of running it.
	prelude := "id() { echo 0; }; chown() { printf '%s\\n' \"$*\" >> " + shellQuote(log) + "; }; "
	cmd := exec.Command("sh", "-c", prelude+remoteDeployScript, "sh", dir, target)
	cmd.Stdin = strings.NewReader("new")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, out)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	logged, _ := os.ReadFile(log)
	want := ownerOf(t, info)
	if !strings.Contains(string(logged), want+" "+target+".new.") {
		t.Fatalf("root must chown the staged file back to the previous owner %s; got %q", want, logged)
	}
}

// ownerOf renders "uid:gid" the way the deploy script passes it to chown.
func ownerOf(t *testing.T, info os.FileInfo) string {
	t.Helper()
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("owner not available on this platform")
	}
	return fmt.Sprintf("%d:%d", st.Uid, st.Gid)
}
