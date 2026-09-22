package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// Slice 1 of docs/CORE-PLAN.md moved session start/stop/restart, list and
// group list behind internal/core. These tests hold the registry path to the
// legacy handlers byte for byte: help goldens recorded from origin/main, and
// a twin-sandbox run of the same script through both paths of one binary.

func TestJSONModeFlagParsesLikeBoolPlusEnvelope(t *testing.T) {
	cases := []struct {
		args []string
		want jsonMode
	}{
		{nil, jsonOff},
		{[]string{"--json"}, jsonLegacy},
		{[]string{"-json"}, jsonLegacy},
		{[]string{"--json=true"}, jsonLegacy},
		{[]string{"--json=1"}, jsonLegacy},
		{[]string{"--json=false"}, jsonOff},
		{[]string{"--json=envelope"}, jsonEnvelope},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		var m jsonModeFlag
		fs.Var(&m, "json", "Output as JSON")
		if err := fs.Parse(c.args); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if m.mode != c.want {
			t.Errorf("%v: mode %d, want %d", c.args, m.mode, c.want)
		}
	}
}

func TestJSONModeFlagErrorsAndHelpMatchFsBool(t *testing.T) {
	newPair := func() (*flag.FlagSet, *flag.FlagSet, *strings.Builder, *strings.Builder) {
		var legacyOut, newOut strings.Builder
		legacy := flag.NewFlagSet("t", flag.ContinueOnError)
		legacy.SetOutput(&legacyOut)
		legacy.Bool("json", false, "Output as JSON")
		legacy.Bool("q", false, "Minimal output (short)")
		registry := flag.NewFlagSet("t", flag.ContinueOnError)
		registry.SetOutput(&newOut)
		var m jsonModeFlag
		registry.Var(&m, "json", "Output as JSON")
		registry.Bool("q", false, "Minimal output (short)")
		return legacy, registry, &legacyOut, &newOut
	}

	legacy, registry, legacyOut, newOut := newPair()
	legacy.PrintDefaults()
	registry.PrintDefaults()
	if legacyOut.String() != newOut.String() {
		t.Fatalf("PrintDefaults differs:\nlegacy:\n%s\nregistry:\n%s", legacyOut, newOut)
	}

	legacy, registry, legacyOut, newOut = newPair()
	legacyErr := legacy.Parse([]string{"--json=bogus"})
	newErr := registry.Parse([]string{"--json=bogus"})
	if legacyErr == nil || newErr == nil || legacyErr.Error() != newErr.Error() || legacyOut.String() != newOut.String() {
		t.Fatalf("invalid value handling differs:\nlegacy %v\n%s\nregistry %v\n%s", legacyErr, legacyOut, newErr, newOut)
	}

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var m jsonModeFlag
	fs.Var(&m, "json", "")
	if got := normalizeArgs(fs, []string{"alpha", "--json", "beta"}); strings.Join(got, " ") != "--json alpha beta" {
		t.Fatalf("normalizeArgs treated --json as taking a value: %v", got)
	}
}

func TestLegacyErrorCodeMapping(t *testing.T) {
	cases := map[string]struct {
		code string
		exit int
	}{
		core.CodeNotFound:       {ErrCodeNotFound, 2},
		core.CodeAmbiguous:      {ErrCodeAmbiguous, 1},
		core.CodeStorage:        {ErrCodeNotFound, 1},
		core.CodeNoActive:       {ErrCodeNotFound, 1},
		core.CodeInvalid:        {ErrCodeInvalidOperation, 1},
		core.CodeAlreadyRunning: {ErrCodeInvalidOperation, 1},
		core.CodeNotRunning:     {ErrCodeInvalidOperation, 1},
		core.CodeSpawnFailed:    {ErrCodeInvalidOperation, 1},
		core.CodeMissingArg:     {ErrCodeInvalidOperation, 1},
		core.CodeInvalidInput:   {ErrCodeInvalidOperation, 1},
	}
	for in, want := range cases {
		code, exit := legacyErrorCode(in)
		if code != want.code || exit != want.exit {
			t.Errorf("legacyErrorCode(%s) = %s,%d want %s,%d", in, code, exit, want.code, want.exit)
		}
	}
}

// TestCoreRegistryRemotePolicyMatchesRemoteExec keeps the registry's Remote
// field in step with the string allowlist in remote_exec.go.
func TestCoreRegistryRemotePolicyMatchesRemoteExec(t *testing.T) {
	for _, d := range coreRegistry().Defs() {
		_, err := remoteCommandArgs(append(append([]string{}, d.CLI...), "x"))
		allowed := err == nil
		if allowed != (d.Remote == core.RemoteAllow) {
			t.Errorf("%s (%s): remote_exec allows=%v, registry Remote=%s", d.ID, d.CLIPath(), allowed, d.Remote)
		}
	}
}

// TestCoreCommandHelpGolden pins --help of the five registry commands to the
// bytes origin/main printed (testdata/core-help, recorded with
// scripts/core-capture/capture.sh), on both the registry and legacy paths.
func TestCoreCommandHelpGolden(t *testing.T) {
	cases := map[string][]string{
		"session-start":   {"session", "start", "--help"},
		"session-stop":    {"session", "stop", "-h"},
		"session-restart": {"session", "restart", "--help"},
		"list":            {"list", "--help"},
		"group-list":      {"group", "list", "--help"},
	}
	for name, args := range cases {
		wantOut := readGolden(t, filepath.Join("testdata", "core-help", name+".stdout"))
		wantErr := readGolden(t, filepath.Join("testdata", "core-help", name+".stderr"))
		for _, legacy := range []bool{false, true} {
			var extra []string
			if legacy {
				extra = []string{envCoreRegistry + "=0"}
			}
			stdout, stderr, code := runAgentDeckEnv(t, t.TempDir(), "", extra, args...)
			if code != 0 || stdout != wantOut || stderr != wantErr {
				t.Errorf("%s (legacy=%v): exit %d\n--- stdout ---\n%s\n--- want ---\n%s\n--- stderr ---\n%s\n--- want ---\n%s",
					name, legacy, code, stdout, wantOut, stderr, wantErr)
			}
		}
	}
}

func readGolden(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// equivalenceScript is the case list run through both paths. Every case runs
// against the same evolving state in each sandbox.
var equivalenceScript = [][]string{
	{"list"}, {"ls"}, {"list", "--json"}, {"list", "--all"}, {"list", "--all", "--json"},
	{"list", "--include-superseded"}, {"list", "--bogus"},
	{"group"}, {"group", "list"}, {"group", "list", "--json"}, {"group", "list", "-q"},
	{"session", "start", "nosuch"}, {"session", "start", "nosuch", "--json"},
	{"session", "start", "--json=bogus", "alpha"},
	{"session", "stop", "alpha"}, {"session", "stop", "alpha", "--json"}, {"session", "stop"},
	{"session", "restart"}, {"session", "restart", "--json"}, {"session", "restart", "nosuch", "--json"},
	{"session", "restart", "--all"}, {"session", "restart", "--all", "--json"},
	{"session", "start", "alpha"}, {"session", "start", "alpha"}, {"session", "start", "alpha", "--json"},
	{"session", "start", "beta", "--json"}, {"session", "start", "gamma", "-q"},
	{"group", "list"}, {"group", "list", "--json"},
	{"session", "restart", "alpha"}, {"session", "restart", "alpha", "--json"},
	{"session", "restart", "alpha", "--force"}, {"session", "restart", "alpha", "--force", "--json"},
	{"session", "restart", "beta", "--env", "FOO=bar", "--json"},
	{"session", "restart", "--all", "--json"}, {"session", "restart", "--all"},
	{"list", "--json"},
	{"session", "stop", "alpha"}, {"session", "stop", "beta", "--json"}, {"session", "stop", "gamma", "-q"},
	{"session", "stop", "gamma", "--json"},
	{"list", "--json"}, {"group", "list", "--json"},
}

var (
	equivTmuxSuffix = regexp.MustCompile(`(agentdeck_[A-Za-z0-9-]+)_[0-9a-f]{8}`)
	equivStartedAgo = regexp.MustCompile(`started [0-9]+s ago`)
	equivTimestamp  = regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})`)
)

func scrubEquivalence(s, home, tmuxDir string) string {
	s = strings.ReplaceAll(s, home, "<HOME>")
	s = strings.ReplaceAll(s, tmuxDir, "<TMUX>")
	s = equivTmuxSuffix.ReplaceAllString(s, "${1}_<RAND>")
	s = equivStartedAgo.ReplaceAllString(s, "started Ns ago")
	return equivTimestamp.ReplaceAllString(s, "<TS>")
}

// TestCoreRegistryMatchesLegacyHandlers is the slice-1 done proof inside the
// repo: one seeded store is cloned into two sandboxes (each with its own
// tmux server), the script runs through the legacy handlers in one and the
// registry in the other, and every case's stdout, stderr and exit status must
// match byte for byte after scrubbing only time and randomness. It includes
// `session restart <id> --json`, the golden named in CORE-PLAN.md.
func TestCoreRegistryMatchesLegacyHandlers(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	seed := t.TempDir()
	projects := filepath.Join(seed, "proj")
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := os.MkdirAll(filepath.Join(projects, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	seedTmux := shortTempDir(t, "adsd")
	seedEnv := []string{"TMUX_TMPDIR=" + seedTmux}
	for _, s := range []struct{ title, group string }{{"alpha", "work"}, {"beta", "work/api"}, {"gamma", "misc"}} {
		if _, stderr, code := runAgentDeckEnv(t, seed, "", seedEnv, "add", filepath.Join(projects, s.title), "-t", s.title, "-c", "bash", "-g", s.group); code != 0 {
			t.Fatalf("seed add %s: exit %d: %s", s.title, code, stderr)
		}
	}

	type sandbox struct {
		home, tmux string
		env        []string
	}
	mk := func(prefix string, legacy bool) sandbox {
		sb := sandbox{home: t.TempDir(), tmux: shortTempDir(t, prefix)}
		if err := copyTree(seed, sb.home); err != nil {
			t.Fatal(err)
		}
		sb.env = []string{"TMUX_TMPDIR=" + sb.tmux}
		if legacy {
			sb.env = append(sb.env, envCoreRegistry+"=0")
		}
		t.Cleanup(func() {
			for _, title := range []string{"alpha", "beta", "gamma"} {
				runAgentDeckEnv(t, sb.home, "", sb.env, "session", "stop", title)
			}
			testutil.KillTmuxServersUnder(sb.tmux)
		})
		return sb
	}
	legacy := mk("adlg", true)
	registry := mk("adrg", false)

	for _, args := range equivalenceScript {
		// Stored paths point into the seed dir, identical in both clones.
		lOut, lErr, lCode := runAgentDeckEnv(t, legacy.home, "", legacy.env, args...)
		rOut, rErr, rCode := runAgentDeckEnv(t, registry.home, "", registry.env, args...)
		lOut, lErr = scrubEquivalence(lOut, legacy.home, legacy.tmux), scrubEquivalence(lErr, legacy.home, legacy.tmux)
		rOut, rErr = scrubEquivalence(rOut, registry.home, registry.tmux), scrubEquivalence(rErr, registry.home, registry.tmux)
		if lCode != rCode || lOut != rOut || lErr != rErr {
			t.Errorf("%q differs\nexit legacy=%d registry=%d\n--- legacy stdout ---\n%s\n--- registry stdout ---\n%s\n--- legacy stderr ---\n%s\n--- registry stderr ---\n%s",
				strings.Join(args, " "), lCode, rCode, lOut, rOut, lErr, rErr)
		}
	}
}

// TestCoreEnvelopeFlag checks the new --json=envelope output on the registry
// path: one envelope object, schema id, ok flag, stable error code.
func TestCoreEnvelopeFlag(t *testing.T) {
	home := t.TempDir()
	env := []string{"TMUX_TMPDIR=" + shortTempDir(t, "adev")}

	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "list", "--json=envelope")
	var ok core.Envelope
	if code != 0 || json.Unmarshal([]byte(stdout), &ok) != nil {
		t.Fatalf("list --json=envelope: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if !ok.OK || ok.Schema != "agent-deck/session.list/v1" || len(ok.RequestID) != 16 || ok.Warnings == nil || ok.Error != nil {
		t.Fatalf("list envelope = %+v", ok)
	}

	stdout, _, code = runAgentDeckEnv(t, home, "", env, "session", "start", "nosuch", "--json=envelope")
	var bad core.Envelope
	if code != 2 || json.Unmarshal([]byte(stdout), &bad) != nil {
		t.Fatalf("start nosuch --json=envelope: exit %d\n%s", code, stdout)
	}
	if bad.OK || bad.Error == nil || bad.Error.Code != core.CodeNotFound || bad.Schema != "agent-deck/session.start/v1" {
		t.Fatalf("error envelope = %+v", bad)
	}
}

// shortTempDir returns a short temp dir (tmux socket paths are length
// limited) removed at cleanup.
func shortTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// copyTree copies the regular files and directories under src into dst.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
