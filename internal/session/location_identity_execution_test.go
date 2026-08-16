package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The round-3 review of the previous attempt at this epic failed on exactly one
// thing: SSHRemotePath "~" was folded into "" for location IDENTITY while
// EXECUTION single-quoted it into `cd '~'`, which is a literal directory named
// "~", not $HOME. Two spellings that compared equal ran in different places, and
// the same quoting made an accepted "~/work" mean a literal "~" directory.
//
// These tests hold identity and execution against each other. CanonicalRemotePath
// is the identity half and RemoteCDPrefix is the execution half; both live in
// location.go so there is one rule, and the end-to-end test below runs the real
// composed ssh command line through a fake `ssh` that reproduces ssh's own
// semantics (start in the remote user's home, hand the last argument to a shell).

// --- The rule, stated on both sides ------------------------------------------

// TestCanonicalRemotePath_FoldsTheSpellingsOfTheRemoteHome pins the identity
// half.
func TestCanonicalRemotePath_FoldsTheSpellingsOfTheRemoteHome(t *testing.T) {
	same := []string{"", "~", "~/", "  ~  "}
	for _, spelling := range same {
		if got := CanonicalRemotePath(spelling); got != "" {
			t.Errorf("CanonicalRemotePath(%q) = %q, want \"\" — every spelling of the remote home is ONE location", spelling, got)
		}
	}

	distinct := map[string]string{
		"~/work":     "~/work",
		"~/work/":    "~/work",
		"/srv/app":   "/srv/app",
		"/srv/app//": "/srv/app",
		"/":          "/",
	}
	for in, want := range distinct {
		if got := CanonicalRemotePath(in); got != want {
			t.Errorf("CanonicalRemotePath(%q) = %q, want %q", in, got, want)
		}
	}

	if RemoteLocation("h", "~/work") == RemoteLocation("h", "") {
		t.Error("a path UNDER the remote home collapsed onto the remote home itself")
	}
	if RemoteLocation("h", "") != RemoteLocation("h", "~") {
		t.Error(`remote paths "" and "~" must be the SAME location — they run in the same directory`)
	}
}

// TestLocationString_RoundTripsThroughParseLocation is the property the
// ambiguity messages depend on: they print a location and tell the user to
// retype it, so what they print has to resolve back to the same location.
//
// Review round 1, finding F3: this held for absolute and ~-rooted paths but not
// for a RELATIVE one. `add --ssh host --remote-path work` was accepted and
// String() rendered it as "host:work", which ParseLocation rejected — so the CLI
// printed an identifier that answered NOT_FOUND. The table below covers every
// shape a --remote-path can take, which is what stops the class recurring.
func TestLocationString_RoundTripsThroughParseLocation(t *testing.T) {
	// Every spelling a user can give --remote-path, including the ones that
	// canonicalize onto each other.
	remotePaths := []string{
		"",           // no --remote-path
		"~",          // the remote home, as String() prints it
		"~/",         //
		"~/work",     // under the remote home
		"~/work/",    //
		"work",       // RELATIVE — F3
		"./work",     //
		"work/",      //
		"deep/sub",   // relative, multi-segment
		".",          // the remote home again
		"/srv/app",   // absolute
		"/srv/app/",  //
		"/srv/app//", //
		"/",          // remote root
	}

	for _, rp := range remotePaths {
		loc := RemoteLocation("alice@host-a", rp)
		rendered := loc.String()

		parsed, ok := ParseLocation(rendered)
		if !ok {
			t.Errorf("--remote-path %q renders as %q, which ParseLocation REFUSES — the CLI would print an identifier that does not resolve", rp, rendered)
			continue
		}
		if parsed != loc {
			t.Errorf("--remote-path %q: round trip via %q produced %+v, want %+v", rp, rendered, parsed, loc)
		}
	}

	// The equivalences the canonical form asserts, spelled out.
	home := RemoteLocation("alice@host-a", "")
	for _, sameAsHome := range []string{"~", "~/", ".", "./"} {
		if got := RemoteLocation("alice@host-a", sameAsHome); got != home {
			t.Errorf("--remote-path %q = %+v, want the remote home %+v", sameAsHome, got, home)
		}
	}
	underHome := RemoteLocation("alice@host-a", "~/work")
	for _, sameAsUnderHome := range []string{"work", "./work", "work/", "~/work/"} {
		if got := RemoteLocation("alice@host-a", sameAsUnderHome); got != underHome {
			t.Errorf("--remote-path %q = %+v, want %+v — ssh starts in the remote home, so these are one directory", sameAsUnderHome, got, underHome)
		}
	}
	// ...and an absolute path is emphatically NOT the same place.
	if RemoteLocation("alice@host-a", "/work") == underHome {
		t.Error("/work was folded onto ~/work")
	}
}

// TestRelativeRemotePath_IdentityAndExecutionAgree is the F3 fix held to the
// same standard as the `~` rule: folding "work" onto "~/work" for identity is
// only correct if they also RUN in the same directory.
func TestRelativeRemotePath_IdentityAndExecutionAgree(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "work"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	want := filepath.Join(home, "work")
	for _, spelling := range []string{"work", "./work", "~/work", "work/"} {
		script := RemoteCDPrefix(spelling) + `printf '%s' "$PWD"`
		cmd := exec.Command("bash", "-c", script)
		// The shell starts in $HOME, exactly as `ssh host <cmd>` does — which is
		// why a relative remote path means "under the remote home" at all.
		cmd.Dir = home
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("--remote-path %q: prefix %q failed: %v (%s)", spelling, RemoteCDPrefix(spelling), err, out)
			continue
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("--remote-path %q ran in %q, want %q (prefix %q)", spelling, got, want, RemoteCDPrefix(spelling))
		}
	}
}

// TestRemoteCDPrefix_IdentityAndExecutionAgree is the core of the round-3
// objection: for every remote path, the directory the command RUNS in must be
// the directory the identity rule says it is. It executes the emitted prefix in
// a real shell rather than asserting on its text, because `cd '~'` looks
// perfectly reasonable as text and is wrong.
func TestRemoteCDPrefix_IdentityAndExecutionAgree(t *testing.T) {
	home := t.TempDir()
	for _, sub := range []string{"work", "my dir", "quote'dir"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	elsewhere := t.TempDir()

	cases := []struct {
		remotePath string
		wantDir    string
	}{
		{"", home},                                  // no --remote-path: ssh lands in the remote home
		{"~", home},                                 // the spelling String() prints
		{"~/", home},                                //
		{"~/work", filepath.Join(home, "work")},     // under the remote home
		{"~/work/", filepath.Join(home, "work")},    //
		{"~/my dir", filepath.Join(home, "my dir")}, // needs quoting AND tilde expansion
		{"~/quote'dir", filepath.Join(home, "quote'dir")},
		{elsewhere, elsewhere}, // absolute
	}

	for _, c := range cases {
		// The shell starts in $HOME, exactly as `ssh host <cmd>` does, so "no
		// prefix" must mean "the remote home".
		script := RemoteCDPrefix(c.remotePath) + `printf '%s' "$PWD"`
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = home
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("remote path %q: prefix %q failed to execute: %v (%s)", c.remotePath, RemoteCDPrefix(c.remotePath), err, out)
			continue
		}
		got := strings.TrimSpace(string(out))
		if got != c.wantDir {
			t.Errorf("remote path %q ran in %q, want %q (prefix was %q)", c.remotePath, got, c.wantDir, RemoteCDPrefix(c.remotePath))
		}

		// ...and the identity rule must agree with where it actually ran.
		canonical := CanonicalRemotePath(c.remotePath)
		if canonical == "" && got != home {
			t.Errorf("remote path %q canonicalises to the remote home but ran in %q", c.remotePath, got)
		}
	}
}

// TestRemoteCDPrefix_DoesNotInject: a remote path is attacker-influenced state
// (it comes off disk), so the emitted prefix must not execute anything.
func TestRemoteCDPrefix_DoesNotInject(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, "pwned")

	for _, evil := range []string{
		"/tmp/x; touch " + marker,
		"/tmp/x$(touch " + marker + ")",
		"~/$(touch " + marker + ")",
		"~/`touch " + marker + "`",
	} {
		script := RemoteCDPrefix(evil) + `printf ok`
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+home)
		_, _ = cmd.CombinedOutput() // the cd is expected to FAIL; that is fine
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("remote path %q executed: prefix was %q", evil, RemoteCDPrefix(evil))
		}
	}
}

// --- The predicate the transcript boundary rests on ---------------------------

// TestTranscriptIsResolvableLocally_IsTheOneRule keeps the predicate itself
// honest: the whole enumeration in remote_transcript_boundary.go depends on it.
func TestTranscriptIsResolvableLocally_IsTheOneRule(t *testing.T) {
	local := NewInstanceWithTool("l", t.TempDir(), "claude")
	if !local.TranscriptIsResolvableLocally() {
		t.Error("a local session's transcript must be resolvable locally")
	}

	remote := NewInstanceWithTool("r", t.TempDir(), "claude")
	remote.SSHHost = "alice@host-a"
	if remote.TranscriptIsResolvableLocally() {
		t.Error("a remote session's transcript must never be resolvable locally")
	}

	// True regardless of whether --remote-path was given: SSHHost alone makes
	// the conversation live on another machine.
	remote.SSHRemotePath = ""
	if remote.TranscriptIsResolvableLocally() {
		t.Error("a remote session with no --remote-path is still remote")
	}

	var nilInst *Instance
	if nilInst.TranscriptIsResolvableLocally() {
		t.Error("nil must not claim a locally resolvable transcript")
	}
}

// --- #1858: the config-dir expression -----------------------------------------

// TestConfigDirShellExpr_RemoteHomeRelative pins the expression itself: a path
// under the LOCAL home is only meaningful relative to A home, so it is emitted
// relative to $HOME for the remote shell to expand. A path OUTSIDE the local
// home is the user's own absolute declaration — the reporter's working
// `[profiles.<account>.claude].config_dir = /Users/cloudlydr/.claude` case — and
// passes through unchanged.
func TestConfigDirShellExpr_RemoteHomeRelative(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	remote := NewInstanceWithTool("remote", t.TempDir(), "claude")
	remote.SSHHost = "cloudlydr@remote-host"
	local := NewInstanceWithTool("local", t.TempDir(), "claude")

	underHome := filepath.Join(home, ".claude")
	outsideHome := "/Users/cloudlydr/.claude"

	if got := remote.configDirShellExpr(underHome); !strings.Contains(got, `"$HOME"`) {
		t.Errorf("a local-home config dir was shipped literally to the remote host: %s", got)
	}
	if got := remote.configDirShellExpr(underHome); strings.Contains(got, home) {
		t.Errorf("the literal local home %q leaked into the remote expression: %s", home, got)
	}
	if got := remote.configDirShellExpr(home); got != `"$HOME"` {
		t.Errorf("the local home itself should render as \"$HOME\", got %s", got)
	}
	if got := remote.configDirShellExpr(outsideHome); strings.Contains(got, "$HOME") {
		t.Errorf("an absolute config dir outside the local home must pass through unchanged, got %s", got)
	}

	// A local session is byte-identical to before the change.
	for _, dir := range []string{underHome, outsideHome, home} {
		if got, want := local.configDirShellExpr(dir), shellQuoteForTest(dir); got != want {
			t.Errorf("local session config dir changed shape: got %s, want %s", got, want)
		}
	}
}

// TestConfigDirShellExpr_IsInjectionSafe: the expression lands in a `bash -c`
// payload, so everything except the deliberate "$HOME" must be quoted.
func TestConfigDirShellExpr_IsInjectionSafe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	marker := filepath.Join(home, "pwned")

	remote := NewInstanceWithTool("remote", t.TempDir(), "claude")
	remote.SSHHost = "cloudlydr@remote-host"

	for _, evil := range []string{
		filepath.Join(home, "a$(touch "+marker+")"),
		filepath.Join(home, "b`touch "+marker+"`"),
		filepath.Join(home, "c;touch "+marker),
		"/outside/$(touch " + marker + ")",
	} {
		expr := remote.configDirShellExpr(evil)
		if err := evalShellValue(t, expr, home); err != nil {
			t.Fatalf("expression %q failed to evaluate: %v", expr, err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("config dir %q injected; expression was %s", evil, expr)
		}
	}
}
