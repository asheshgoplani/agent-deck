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

	"github.com/asheshgoplani/agent-deck/internal/update"
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
	// Nothing on the non-interactive PATH, no agent_deck_path configured:
	// the controller itself runs `agent-deck` through PATH, so this is a
	// failure with the full remedy.
	r := l.runner(t, configured, noSudo)
	r.configuredPath = ""
	err := r.InstallBinary(context.Background(), []byte(fakeAgentDeckPayload("1.16.6")), "1.16.6")
	if err == nil || !strings.Contains(err.Error(), "not on the remote's $PATH") || !strings.Contains(err.Error(), "add "+configured+" to PATH") {
		t.Fatalf("got %v, want the not-on-PATH remedy in full", err)
	}
}

// #2249: an explicit agent_deck_path that is off the remote's
// non-interactive PATH is how the controller reaches that remote anyway.
// Once the configured binary is deployed and verified the update is a
// success; the missing PATH entry is a warning in the report, not a
// failure, and the sweep records the new version.
func TestInstallBinary_ConfiguredPathOffPathIsAWarning(t *testing.T) {
	setupSessionXDGPathEnv(t)
	l := newRemoteLayout(t)
	configured := filepath.Join(l.home, "bin", "agent-deck")
	fakeAgentDeck(t, configured, "1.16.6", 0o755)
	r := l.runner(t, configured, noSudo)

	opts := RemoteUpdateOptions{
		NewRunner:    func(string, RemoteConfig) RemoteBinaryInstaller { return localPlatform{r} },
		FetchRelease: func(target string) (*update.Release, error) { return &update.Release{TagName: "v" + target}, nil },
		Download: func(*update.Release, string, string) ([]byte, error) {
			return []byte(fakeAgentDeckPayload("1.16.7")), nil
		},
	}
	results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"home": {Host: "x@home", AgentDeckPath: configured}}, "1.16.7", opts)
	if len(results) != 1 || results[0].Outcome != RemoteUpdateOutcomeUpdated || results[0].Err != nil {
		t.Fatalf("results = %+v, want updated", results)
	}
	if got, _ := os.ReadFile(configured); string(got) != fakeAgentDeckPayload("1.16.7") {
		t.Fatalf("configured path not deployed: %q", got)
	}
	for _, want := range []string{"warning", "not on the remote's non-interactive PATH", "add " + configured + " to PATH"} {
		if !strings.Contains(results[0].Note, want) {
			t.Errorf("report %q lacks %q", results[0].Note, want)
		}
	}
	if CountRemoteUpdateFailures(results) != 0 {
		t.Fatal("a PATH warning must not be a failure")
	}
	if cached := LoadRemoteVersions()["home"]; cached.Version != "1.16.7" || !cached.Found {
		t.Fatalf("cache = %+v, want the verified version recorded", cached)
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

// Review of #2245: a $PATH binary that is already newer than (or equal to)
// the version being deployed is left alone; the configured path is still
// updated and the report says what was left where. Overwriting it would be
// the downgrade the sweep guards against everywhere else.
func TestInstallBinary_NeverDowngradesANewerPathBinary(t *testing.T) {
	l := newRemoteLayout(t)
	configured := filepath.Join(l.home, "opt", "agent-deck")
	fakeAgentDeck(t, configured, "1.16.5", 0o755)
	onPath := filepath.Join(l.pathDir, "agent-deck")
	fakeAgentDeck(t, onPath, "1.16.7", 0o755)
	r := l.runner(t, configured, noSudo)

	if err := r.InstallBinary(context.Background(), []byte(fakeAgentDeckPayload("1.16.6")), "1.16.6"); err != nil {
		t.Fatalf("InstallBinary: %v", err)
	}
	if got, _ := os.ReadFile(onPath); string(got) != fakeAgentDeckPayload("1.16.7") {
		t.Fatalf("the newer $PATH binary was overwritten: %q", got)
	}
	if got, _ := os.ReadFile(configured); string(got) != fakeAgentDeckPayload("1.16.6") {
		t.Fatalf("configured path not updated: %q", got)
	}
	report := r.LastInstallReport()
	if !strings.Contains(report, onPath) || !strings.Contains(report, "v1.16.7") || !strings.Contains(report, "left") {
		t.Errorf("report must say the newer $PATH binary was left as is: %q", report)
	}
}

// When the second of two deploys fails, the report already names the one
// that succeeded, so the operator knows what changed.
func TestInstallBinary_ReportsPartialDeployOnSecondFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	l := newRemoteLayout(t)
	configured := filepath.Join(l.home, "locked", "agent-deck")
	fakeAgentDeck(t, configured, "1.16.5", 0o755)
	onPath := filepath.Join(l.pathDir, "agent-deck")
	fakeAgentDeck(t, onPath, "1.16.5", 0o755)
	if err := os.Chmod(filepath.Dir(configured), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(configured), 0o755) })
	r := l.runner(t, configured, noSudo)

	err := r.InstallBinary(context.Background(), []byte(fakeAgentDeckPayload("1.16.6")), "1.16.6")
	if err == nil {
		t.Fatal("the configured path is unwritable; expected a failure")
	}
	if got, _ := os.ReadFile(onPath); string(got) != fakeAgentDeckPayload("1.16.6") {
		t.Fatalf("the $PATH binary should have been updated first: %q", got)
	}
	if report := r.LastInstallReport(); !strings.Contains(report, "deployed to "+onPath) {
		t.Errorf("report must record the deploy that did happen: %q", report)
	}
}

// A $PATH agent-deck that reports the right version but is not the file
// that was deployed (a wrapper, a second copy) is a verification failure,
// not a success.
func TestInstallBinary_RejectsSameVersionDifferentInodeOnPath(t *testing.T) {
	r, _ := recordingRunner(func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "resolve '/home/tester/.local/bin/agent-deck'"):
			return "/home/tester/.local/bin/agent-deck\n", nil
		case strings.Contains(cmd, "command -v agent-deck 2>/dev/null); [ -n \"$pb\" ] && resolve"):
			return "", errors.New("exit status 1") // nothing on PATH at resolve time
		case strings.Contains(cmd, "-ef"):
			return "", errors.New("exit status 1") // PATH runs a different inode
		case strings.Contains(cmd, "command -v agent-deck"):
			return "/home/tester/.local/bin/agent-deck\n", nil
		case strings.Contains(cmd, "'agent-deck' version"):
			return "Agent Deck v1.16.6\n", nil
		}
		return "", nil
	})
	err := r.InstallBinary(context.Background(), []byte("BINARY"), "1.16.6")
	if err == nil || !strings.Contains(err.Error(), "is not the file deployed to") {
		t.Fatalf("got %v, want the different-inode rejection", err)
	}
}

// chown failing under root is a deploy failure, not a silently changed
// owner: the original file stays and the staging file is removed.
func TestDeployScript_ChownFailureAbortsBeforeReplacing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	prelude := "id() { echo 0; }; chown() { return 1; }; "
	cmd := exec.Command("sh", "-c", prelude+remoteDeployScript, "sh", dir, target)
	cmd.Stdin = strings.NewReader("new")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 5 {
		t.Fatalf("script must fail with exit 5 when chown fails; got %v: %s", err, out)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("original replaced despite chown failure: %q", got)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "*.new.*")); len(entries) != 0 {
		t.Errorf("staging left behind: %v", entries)
	}
}

// The symlink guard runs again right before the rename: a link that
// appears while the bytes are streaming is not replaced either.
func TestDeployScript_RechecksSymlinkBeforeRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-deck")
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("other"), 0o755); err != nil {
		t.Fatal(err)
	}
	// cat streams the payload and then someone turns the path into a link.
	prelude := "cat() { command cat; ln -s " + shellQuote(elsewhere) + " " + shellQuote(target) + "; }; "
	cmd := exec.Command("sh", "-c", prelude+remoteDeployScript, "sh", dir, target)
	cmd.Stdin = strings.NewReader("new")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != remoteDeploySymlinkExit {
		t.Fatalf("script must refuse the late symlink with exit %d; got %v: %s", remoteDeploySymlinkExit, err, out)
	}
	if info, lerr := os.Lstat(target); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", info, lerr)
	}
	if got, _ := os.ReadFile(elsewhere); string(got) != "other" {
		t.Fatalf("link target touched: %q", got)
	}
}

// Re-review of #2245: a $PATH binary whose version cannot be read is not
// "older", it is unknown. Nothing is overwritten (not even the configured
// path), the probe failure is the error, and the sweep reports it as a
// skip rather than a failure.
func TestInstallBinary_ProbeFailureOverwritesNothing(t *testing.T) {
	for _, tc := range []struct{ name, pathBinary string }{
		{"version command fails", "#!/bin/sh\nexit 1\n"},
		{"version output unparseable", "#!/bin/sh\necho 'development build'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newRemoteLayout(t)
			configured := filepath.Join(l.home, "opt", "agent-deck")
			fakeAgentDeck(t, configured, "1.16.5", 0o755)
			onPath := filepath.Join(l.pathDir, "agent-deck")
			if err := os.WriteFile(onPath, []byte(tc.pathBinary), 0o755); err != nil {
				t.Fatal(err)
			}
			r := l.runner(t, configured, noSudo)

			err := r.InstallBinary(context.Background(), []byte(fakeAgentDeckPayload("1.16.6")), "1.16.6")
			if !errors.Is(err, ErrRemoteProbeFailed) {
				t.Fatalf("got %v, want ErrRemoteProbeFailed", err)
			}
			if !strings.Contains(err.Error(), onPath) {
				t.Errorf("the error must name the binary whose probe failed: %v", err)
			}
			if got, _ := os.ReadFile(onPath); string(got) != tc.pathBinary {
				t.Fatalf("$PATH binary overwritten: %q", got)
			}
			if got, _ := os.ReadFile(configured); string(got) != fakeAgentDeckPayload("1.16.5") {
				t.Fatalf("configured path overwritten: %q", got)
			}
		})
	}
}

// The `command -v` and resolve probes themselves failing (SSH hiccup, odd
// shell) are the same: no deploy command is ever issued.
func TestInstallBinary_PathProbeExecFailureIssuesNoDeploy(t *testing.T) {
	for _, tc := range []struct{ name, failOn string }{
		{"command -v probe", "command -v agent-deck 2>/dev/null); if"},
		{"resolve probe", "resolve '/home/tester/.local/bin/agent-deck'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, calls := recordingRunner(func(cmd string) (string, error) {
				switch {
				case strings.Contains(cmd, tc.failOn):
					return "", errors.New("ssh: connection reset")
				case strings.Contains(cmd, "resolve '/home/tester/.local/bin/agent-deck'"):
					return "/home/tester/.local/bin/agent-deck\n", nil
				case strings.Contains(cmd, "command -v agent-deck"):
					return "/home/tester/.local/bin/agent-deck\n", nil
				}
				return "", nil
			})
			err := r.InstallBinary(context.Background(), []byte("BINARY"), "1.16.6")
			if !errors.Is(err, ErrRemoteProbeFailed) {
				t.Fatalf("got %v, want ErrRemoteProbeFailed", err)
			}
			for _, c := range *calls {
				if strings.Contains(c, "cat >") {
					t.Fatalf("a deploy was issued after a failed probe: %s", c)
				}
			}
		})
	}
}

// An ambiguous `command -v` answer (not an absolute path) is a probe
// failure too.
func TestInstallBinary_AmbiguousPathAnswerIsAProbeFailure(t *testing.T) {
	r, calls := recordingRunner(func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "resolve '/home/tester/.local/bin/agent-deck'"):
			return "/home/tester/.local/bin/agent-deck\n", nil
		case strings.Contains(cmd, "command -v agent-deck 2>/dev/null); if"):
			return "alias agent-deck='agent-deck --profile x'\n", nil
		case strings.Contains(cmd, "command -v agent-deck"):
			return "/home/tester/.local/bin/agent-deck\n", nil
		}
		return "", nil
	})
	if err := r.InstallBinary(context.Background(), []byte("BINARY"), "1.16.6"); !errors.Is(err, ErrRemoteProbeFailed) {
		t.Fatalf("got %v, want ErrRemoteProbeFailed", err)
	}
	for _, c := range *calls {
		if strings.Contains(c, "cat >") {
			t.Fatalf("a deploy was issued after an ambiguous probe: %s", c)
		}
	}
}

// Nothing on $PATH at all is not a probe failure: the configured path is
// deployed and the not-on-PATH remedy follows as before.
func TestInstallBinary_NothingOnPathStillDeploysConfigured(t *testing.T) {
	l := newRemoteLayout(t)
	configured := filepath.Join(l.home, ".local", "bin", "agent-deck")
	fakeAgentDeck(t, configured, "1.16.5", 0o755)
	r := l.runner(t, configured, noSudo)
	err := r.InstallBinary(context.Background(), []byte(fakeAgentDeckPayload("1.16.6")), "1.16.6")
	if err != nil {
		t.Fatalf("an explicit agent_deck_path off PATH is a warning, not a failure (#2249): %v", err)
	}
	if !strings.Contains(r.LastInstallReport(), "not on the remote's non-interactive PATH") {
		t.Fatalf("report must carry the PATH warning: %q", r.LastInstallReport())
	}
	if got, _ := os.ReadFile(configured); string(got) != fakeAgentDeckPayload("1.16.6") {
		t.Fatalf("configured path not deployed: %q", got)
	}
}

// Non-root deploys keep the group of the file they replace: the staged
// file is chgrp'd to it (a group the deploying user is in, or the deploy
// would not own the file), and a chgrp that fails aborts with the original
// in place. stat is shadowed to report a foreign group deterministically.
func TestDeployScript_NonRootKeepsGroup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "chgrp.log")
	prelude := "stat() { case \"$*\" in *%u:%g*) printf '%s:99999\\n' \"$(id -u)\";; *) command stat \"$@\";; esac; }; " +
		"chgrp() { printf '%s\\n' \"$*\" >> " + shellQuote(log) + "; }; "
	cmd := exec.Command("sh", "-c", prelude+remoteDeployScript, "sh", dir, target)
	cmd.Stdin = strings.NewReader("new")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, out)
	}
	logged, _ := os.ReadFile(log)
	if !strings.HasPrefix(string(logged), "99999 "+target+".new.") {
		t.Fatalf("the staged file must be chgrp'd to the previous group before the rename; got %q", logged)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Fatalf("installed %q", got)
	}
}

func TestDeployScript_NonRootGroupFailureAborts(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	prelude := "stat() { case \"$*\" in *%u:%g*) printf '%s:99999\\n' \"$(id -u)\";; *) command stat \"$@\";; esac; }; chgrp() { return 1; }; "
	cmd := exec.Command("sh", "-c", prelude+remoteDeployScript, "sh", dir, target)
	cmd.Stdin = strings.NewReader("new")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 5 || !strings.Contains(string(out), "could not keep group") {
		t.Fatalf("script must abort with exit 5 and say why when the group cannot be kept; got %v: %s", err, out)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("original replaced despite group failure: %q", got)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "*.new.*")); len(entries) != 0 {
		t.Errorf("staging left behind: %v", entries)
	}
}

// With a real second group available, the group is actually preserved.
func TestDeployScript_NonRootKeepsGroup_Real(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil || len(groups) < 2 {
		t.Skip("needs a user with a second group")
	}
	other := -1
	for _, g := range groups {
		if g != os.Getgid() {
			other = g
			break
		}
	}
	if other < 0 {
		t.Skip("no second group")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(target, os.Getuid(), other); err != nil {
		t.Skipf("cannot assign group %d: %v", other, err)
	}
	cmd := exec.Command("sh", "-c", remoteDeployScript, "sh", dir, target)
	cmd.Stdin = strings.NewReader("new")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, out)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Gid) != other {
		t.Fatalf("group = %d after deploy, want %d kept", st.Gid, other)
	}
}

// localPlatform is an SSHRunner whose platform probe is answered locally;
// everything else (version, deploy, report) runs the real code paths.
type localPlatform struct{ *SSHRunner }

func (localPlatform) DetectPlatform(context.Context) (string, string, error) {
	return "linux", "amd64", nil
}
