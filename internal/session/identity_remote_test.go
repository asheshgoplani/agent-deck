package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/docker"
)

// ASK-105: SSH and sandboxed sessions used to get no identity block at all
// (the file lives on the controller host). They now carry it inline in the
// launch command and write the file where the harness runs. These tests run
// the real launch command through a stand-in for the hop that ssh/docker add
// (`ssh host CMD` hands CMD to the remote user's shell as `sh -c CMD`;
// `docker exec ... bash -c CMD` runs it in the container), then read the argv
// and environment the harness actually received.

// remoteHarnessRecorder installs a fake harness binary named tool on PATH
// that records one ARG= line per argument plus the identity env and file.
func remoteHarnessRecorder(t *testing.T, bin, tool string) string {
	t.Helper()
	record := filepath.Join(t.TempDir(), "argv.txt")
	fake := "#!/bin/sh\n{ printf 'IDFILE=%s\\n' \"$" + IdentityFileEnv + "\"; for a in \"$@\"; do printf 'ARG=%s\\0' \"$a\"; done; } > \"$RECORD\"\n" +
		"if [ -n \"$" + IdentityFileEnv + "\" ]; then cat \"$" + IdentityFileEnv + "\" > \"$RECORD.file\"; fi\n"
	if err := os.WriteFile(filepath.Join(bin, tool), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	return record
}

// installHopShims puts `ssh` (runs its last argument with sh -c, as sshd does
// with the remote user's shell) and `docker` (drops everything before the
// in-container `bash` and runs the rest) on PATH.
func installHopShims(t *testing.T, bin string) {
	t.Helper()
	ssh := "#!/bin/sh\nfor a in \"$@\"; do last=$a; done\nexec sh -c \"$last\"\n"
	dock := "#!/bin/sh\nwhile [ $# -gt 0 ] && [ \"$1\" != bash ]; do shift; done\nexec \"$@\"\n"
	for name, body := range map[string]string{"ssh": ssh, "docker": dock} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

type remoteRun struct {
	args   []string
	idFile string
	file   string
}

// runLaunch runs cmd the way tmux starts a pane command (sh -c) with a fresh
// runtime dir standing in for the remote/container user's XDG_RUNTIME_DIR.
func runLaunch(t *testing.T, cmd, record, runtimeDir string) remoteRun {
	t.Helper()
	work := t.TempDir()
	run := exec.Command("sh", "-c", cmd)
	run.Dir = work
	run.Env = append(os.Environ(), "RECORD="+record, "XDG_RUNTIME_DIR="+runtimeDir, IdentityFileEnv+"=")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("launch command failed: %v\n%s\n%s", err, out, cmd)
	}
	if strings.Contains(string(out), "command not found") {
		t.Errorf("shell evaluated part of the payload: %s", out)
	}
	if _, err := os.Stat(filepath.Join(work, "MUST_NOT_EXIST")); err == nil {
		t.Fatalf("command substitution in a field executed:\n%s", cmd)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("harness never ran: %v\n%s\n%s", err, out, cmd)
	}
	var r remoteRun
	head, rest, _ := strings.Cut(string(data), "\n")
	r.idFile = strings.TrimPrefix(head, "IDFILE=")
	for _, a := range strings.Split(rest, "\x00") {
		if a != "" {
			r.args = append(r.args, strings.TrimPrefix(a, "ARG="))
		}
	}
	if b, err := os.ReadFile(record + ".file"); err == nil {
		r.file = string(b)
	}
	return r
}

func (r remoteRun) flagValue(flag string) (string, bool) {
	for k, a := range r.args {
		if a == flag && k+1 < len(r.args) {
			return r.args[k+1], true
		}
	}
	return "", false
}

func remoteTestEnv(t *testing.T) (bin string) {
	identityTestEnv(t)
	bin = t.TempDir()
	installHopShims(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

func sshIdentityInstance(tool, title string) *Instance {
	inst := identityTestInstance(tool)
	inst.Title = title
	inst.SSHHost = "worker@lab-box"
	inst.Command = tool
	return inst
}

// assertRemoteFile checks the file the launch wrote where the harness runs:
// exported, under the runtime dir (never the project dir), exact content,
// private directory.
func assertRemoteFile(t *testing.T, r remoteRun, runtimeDir, id, want string) {
	t.Helper()
	wantPath := filepath.Join(runtimeDir, "agent-deck", "identity", id, identityFileName)
	if r.idFile != wantPath {
		t.Errorf("%s = %q, want %q", IdentityFileEnv, r.idFile, wantPath)
	}
	if r.file != want {
		t.Errorf("remote identity file content differs from the block:\n got %q\nwant %q", r.file, want)
	}
	if st, err := os.Stat(filepath.Dir(wantPath)); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("remote identity dir must be 0700: %v %v", st, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(wantPath), ".*")); len(matches) != 0 {
		t.Errorf("tmp file left behind: %v", matches)
	}
}

func TestIdentityRemote_SSHClaudeGetsInlineBlockAndRemoteFile(t *testing.T) {
	bin := remoteTestEnv(t)
	record := remoteHarnessRecorder(t, bin, "claude")
	for _, title := range adversarialTitles {
		inst := sshIdentityInstance("claude", title)
		launch, _, err := inst.prepareCommand(inst.buildClaudeCommand("claude"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(launch, "ssh ") {
			t.Fatalf("expected an ssh launch, got:\n%s", launch)
		}
		localFile, _ := inst.IdentityFilePath()
		if strings.Contains(launch, localFile) || strings.Contains(launch, "--append-system-prompt-file") {
			t.Errorf("title %q: ssh launch must not reference the controller-local file:\n%s", title, launch)
		}
		if strings.Contains(launch, "\n") {
			t.Errorf("title %q: launch command must stay on one line", title)
		}
		runtimeDir := t.TempDir()
		r := runLaunch(t, launch, record, runtimeDir)
		want := inst.BuildIdentityPrompt()
		got, ok := r.flagValue("--append-system-prompt")
		if !ok {
			t.Fatalf("title %q: claude got no --append-system-prompt: %q", title, r.args)
		}
		// $(...) strips the trailing newline; everything else is byte-exact.
		if got != strings.TrimRight(want, "\n") {
			t.Errorf("title %q: inline block not delivered byte for byte:\n got %q\nwant %q", title, got, want)
		}
		assertRemoteFile(t, r, runtimeDir, inst.ID, want)
	}
}

func TestIdentityRemote_SSHCodexGetsDeveloperInstructionsWithoutLocalMerge(t *testing.T) {
	bin := remoteTestEnv(t)
	record := remoteHarnessRecorder(t, bin, "codex")
	// A developer_instructions in THIS host's codex config does not describe
	// the remote's codex and must not leak into the remote override.
	codexHome := filepath.Join(os.Getenv("HOME"), ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("developer_instructions = \"CONTROLLER-ONLY\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	inst := sshIdentityInstance("codex", "codex 'remote' \"one\" 日本")
	launch, _, err := inst.prepareCommand(inst.buildCodexCommand("codex"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	r := runLaunch(t, launch, record, runtimeDir)
	got, ok := r.flagValue("-c")
	if !ok || !strings.HasPrefix(got, "developer_instructions=") {
		t.Fatalf("codex got no developer_instructions override: %q", r.args)
	}
	want := inst.BuildIdentityPrompt()
	if got != "developer_instructions="+identityTOMLString(want) {
		t.Errorf("override is not exactly the block:\n got %q\nwant %q", got, "developer_instructions="+identityTOMLString(want))
	}
	if strings.Contains(got, "CONTROLLER-ONLY") {
		t.Errorf("controller codex config merged into a remote launch: %q", got)
	}
	assertRemoteFile(t, r, runtimeDir, inst.ID, want)
}

func TestIdentityRemote_SandboxClaudeGetsInlineBlockInsideContainer(t *testing.T) {
	bin := remoteTestEnv(t)
	record := remoteHarnessRecorder(t, bin, "claude")
	for _, title := range adversarialTitles {
		inst := identityTestInstance("claude")
		inst.Title = title
		inst.Command = "claude"
		inst.Sandbox = &SandboxConfig{Enabled: true, Image: "img"}
		cmd := inst.buildClaudeCommand("claude")
		localFile, _ := inst.IdentityFilePath()
		if strings.Contains(cmd, localFile) {
			t.Errorf("title %q: sandbox launch must not reference the host-local file:\n%s", title, cmd)
		}
		// The exact docker exec wrapping prepareCommand uses (ensureSandboxContainer
		// itself needs a docker daemon).
		launch := buildExecCommand(docker.NewContainer("agent-deck-test", "img"), nil, cmd)
		runtimeDir := t.TempDir()
		r := runLaunch(t, launch, record, runtimeDir)
		want := inst.BuildIdentityPrompt()
		if got, ok := r.flagValue("--append-system-prompt"); !ok || got != strings.TrimRight(want, "\n") {
			t.Errorf("title %q: sandboxed claude did not get the block inline:\n got %q\nwant %q", title, got, want)
		}
		assertRemoteFile(t, r, runtimeDir, inst.ID, want)
	}
}

func TestIdentityRemote_SSHPiGetsInlineText(t *testing.T) {
	bin := remoteTestEnv(t)
	record := remoteHarnessRecorder(t, bin, "pi")
	inst := sshIdentityInstance("pi", "pi `x` $(touch MUST_NOT_EXIST)")
	launch, _, err := inst.prepareCommand(inst.buildPiCommand("pi"))
	if err != nil {
		t.Fatal(err)
	}
	r := runLaunch(t, launch, record, t.TempDir())
	want := inst.BuildIdentityPrompt()
	if got, ok := r.flagValue("--append-system-prompt"); !ok || got != strings.TrimRight(want, "\n") {
		t.Errorf("ssh pi did not get the block as text:\n got %q\nwant %q", got, want)
	}
}

// A remote that cannot take the file (runtime dir is not a directory) still
// launches, with the inline block and no stale AGENTDECK_IDENTITY_FILE.
func TestIdentityRemote_FileWriteFailureStillLaunchesInline(t *testing.T) {
	bin := remoteTestEnv(t)
	record := remoteHarnessRecorder(t, bin, "claude")
	inst := sshIdentityInstance("claude", "worker")
	launch, _, err := inst.prepareCommand(inst.buildClaudeCommand("claude"))
	if err != nil {
		t.Fatal(err)
	}
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := runLaunch(t, launch, record, notADir)
	if r.idFile != "" {
		t.Errorf("%s must be unset when the remote write fails, got %q", IdentityFileEnv, r.idFile)
	}
	if _, ok := r.flagValue("--append-system-prompt"); !ok {
		t.Errorf("inline block must not depend on the remote file: %q", r.args)
	}
}

func TestIdentityRemote_OptOutStillWins(t *testing.T) {
	t.Run("per-session --no-identity", func(t *testing.T) {
		identityTestEnv(t)
		inst := sshIdentityInstance("claude", "w")
		inst.IdentityInjectionDisabled = true
		cmd := inst.buildClaudeCommand("claude")
		if strings.Contains(cmd, "--append-system-prompt") || strings.Contains(cmd, IdentityFileEnv) {
			t.Errorf("--no-identity ssh session must carry no block:\n%s", cmd)
		}
	})
	t.Run("global inject_identity=false", func(t *testing.T) {
		identityTestEnv(t)
		off := false
		restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{InjectIdentity: &off}})
		defer restore()
		inst := identityTestInstance("codex")
		inst.Sandbox = &SandboxConfig{Enabled: true}
		cmd := inst.buildCodexCommand("codex")
		if strings.Contains(cmd, "developer_instructions") || strings.Contains(cmd, IdentityFileEnv) {
			t.Errorf("inject_identity=false sandbox session must carry no block:\n%s", cmd)
		}
	})
}

// The block is rendered per spawn, so a rename shows at the next restart.
func TestIdentityRemote_RegeneratedOnEverySpawn(t *testing.T) {
	identityTestEnv(t)
	inst := sshIdentityInstance("claude", "before")
	first := inst.buildClaudeCommand("claude")
	inst.Title = "after-rename"
	second := inst.buildClaudeCommand("claude")
	if !strings.Contains(first, "title: before") || !strings.Contains(second, "title: after-rename") || strings.Contains(second, "title: before") {
		t.Errorf("remote block not regenerated from the record:\n%s\n---\n%s", first, second)
	}
}

func TestIdentityPrintfArg_RoundTripsUnderPosixSh(t *testing.T) {
	for _, s := range append(adversarialTitles, "a\\nb\\\\c\\0101 %s %b\n", "trailing\n\n") {
		out, err := exec.Command("sh", "-c", "printf '%b' "+identityPrintfArg(s)).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != s {
			t.Errorf("round trip of %q gave %q", s, out)
		}
	}
}

// tmux refuses a pane command over ~16 KB ("command too long", tmux 3.4), and
// every quote in a remote launch is re-escaped by each quoting layer. The
// block must ride in the command once and leave room for a long, quote-heavy
// title.
func TestIdentityRemote_LaunchFitsTmuxCommandLimit(t *testing.T) {
	identityTestEnv(t)
	title := strings.Repeat("it's ", 24)
	for _, tool := range []string{"claude", "codex", "pi"} {
		inst := sshIdentityInstance(tool, title)
		inst.Account = "work'acct"
		var cmd string
		switch tool {
		case "claude":
			cmd = inst.buildClaudeCommand("claude")
		case "codex":
			cmd = inst.buildCodexCommand("codex")
		case "pi":
			cmd = inst.buildPiCommand("pi")
		}
		launch, _, err := inst.prepareCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(launch); n > 14000 {
			t.Errorf("%s ssh launch is %d bytes; tmux refuses ~16 KB", tool, n)
		}
		if strings.Count(launch, "Completion sentinel") > 1 && tool != "codex" {
			t.Errorf("%s ssh launch carries the block more than once", tool)
		}
	}
}

// The skills line names the session's own id and the commands to list and
// attach; an SSH session's attached skills live on the remote.
func TestBuildIdentityPrompt_SkillsLineForSSHSession(t *testing.T) {
	identityTestEnv(t)
	inst := sshIdentityInstance("claude", "w")
	got := inst.BuildIdentityPrompt()
	for _, want := range []string{
		"`agent-deck skill list`",
		"Attached to this session now: listed on the remote (`agent-deck skill attached abc12345-1700000000`)",
		"`agent-deck skill attach abc12345-1700000000 <skill>`, then restart.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skills line missing %q:\n%s", want, got)
		}
	}
}

// envHarnessRecorder installs a fake tool that records the named env vars
// (and answers a --version probe like opencode 1.x).
func envHarnessRecorder(t *testing.T, bin, tool string, vars ...string) string {
	t.Helper()
	record := filepath.Join(t.TempDir(), "env.txt")
	var b strings.Builder
	b.WriteString("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.19.3; exit 0; fi\n{\n")
	for _, v := range vars {
		b.WriteString("printf '" + v + "=%s\\0' \"$" + v + "\"\n")
	}
	b.WriteString("} > \"$RECORD\"\n")
	if err := os.WriteFile(filepath.Join(bin, tool), []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return record
}

func runEnvLaunch(t *testing.T, cmd, record string, extraEnv ...string) map[string]string {
	t.Helper()
	run := exec.Command("sh", "-c", cmd)
	run.Dir = t.TempDir()
	run.Env = append(append(os.Environ(), "RECORD="+record, "XDG_RUNTIME_DIR="+t.TempDir(), "OPENCODE_CONFIG_CONTENT=", "COPILOT_CUSTOM_INSTRUCTIONS_DIRS="), extraEnv...)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("launch failed: %v\n%s\n%s", err, out, cmd)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("harness never ran: %v\n%s", err, cmd)
	}
	env := map[string]string{}
	for _, kv := range strings.Split(string(data), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

// opencodeInstructionFile decodes OPENCODE_CONFIG_CONTENT and returns the
// one instructions entry agent-deck added, read back from disk.
func opencodeInstructionFile(t *testing.T, raw string) (path, content string) {
	t.Helper()
	var cfg struct {
		Instructions []string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil || len(cfg.Instructions) != 1 {
		t.Fatalf("OPENCODE_CONFIG_CONTENT = %q, want {\"instructions\":[<file>]} (%v)", raw, err)
	}
	data, err := os.ReadFile(cfg.Instructions[0])
	if err != nil {
		t.Fatalf("instructions file unreadable: %v", err)
	}
	return cfg.Instructions[0], string(data)
}

func TestIdentityOpenCode_InstructionsThroughConfigContent(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		bin := remoteTestEnv(t)
		record := envHarnessRecorder(t, bin, "opencode", "OPENCODE_CONFIG_CONTENT", IdentityFileEnv)
		inst := identityTestInstance("opencode")
		inst.Title = `oc 'q' "dq" $(touch MUST_NOT_EXIST)`
		inst.Command = "opencode"
		env := runEnvLaunch(t, inst.buildOpenCodeCommand("opencode"), record)
		path, content := opencodeInstructionFile(t, env["OPENCODE_CONFIG_CONTENT"])
		if want, _ := inst.IdentityFilePath(); path != want || env[IdentityFileEnv] != want {
			t.Errorf("instructions path %q / env %q, want %q", path, env[IdentityFileEnv], want)
		}
		if content != inst.BuildIdentityPrompt() {
			t.Errorf("instructions file is not the block:\n%s", content)
		}
	})
	t.Run("ssh", func(t *testing.T) {
		bin := remoteTestEnv(t)
		record := envHarnessRecorder(t, bin, "opencode", "OPENCODE_CONFIG_CONTENT", IdentityFileEnv)
		inst := sshIdentityInstance("opencode", "oc remote 'q'")
		launch, _, err := inst.prepareCommand(inst.buildOpenCodeCommand("opencode"))
		if err != nil {
			t.Fatal(err)
		}
		env := runEnvLaunch(t, launch, record)
		path, content := opencodeInstructionFile(t, env["OPENCODE_CONFIG_CONTENT"])
		if path != env[IdentityFileEnv] || !strings.Contains(path, filepath.Join("agent-deck", "identity", inst.ID)) {
			t.Errorf("ssh opencode instructions %q must be the file written where it runs (%q)", path, env[IdentityFileEnv])
		}
		if local, _ := inst.IdentityFilePath(); path == local {
			t.Errorf("ssh opencode must not point at the controller file")
		}
		if content != inst.BuildIdentityPrompt() {
			t.Errorf("remote instructions file is not the block:\n%s", content)
		}
	})
	t.Run("user OPENCODE_CONFIG_CONTENT is never overwritten", func(t *testing.T) {
		bin := remoteTestEnv(t)
		record := envHarnessRecorder(t, bin, "opencode", "OPENCODE_CONFIG_CONTENT")
		inst := identityTestInstance("opencode")
		inst.Command = "opencode"
		mine := `{"model":"x/y"}`
		env := runEnvLaunch(t, inst.buildOpenCodeCommand("opencode"), record, "OPENCODE_CONFIG_CONTENT="+mine)
		if env["OPENCODE_CONFIG_CONTENT"] != mine {
			t.Errorf("user OPENCODE_CONFIG_CONTENT replaced: %q", env["OPENCODE_CONFIG_CONTENT"])
		}
	})
	t.Run("opt-out", func(t *testing.T) {
		identityTestEnv(t)
		inst := identityTestInstance("opencode")
		inst.Command = "opencode"
		inst.IdentityInjectionDisabled = true
		if cmd := inst.buildOpenCodeCommand("opencode"); strings.Contains(cmd, "OPENCODE_CONFIG_CONTENT") {
			t.Errorf("--no-identity opencode must not set OPENCODE_CONFIG_CONTENT:\n%s", cmd)
		}
	})
}

func TestIdentityCopilot_CustomInstructionsDir(t *testing.T) {
	bin := remoteTestEnv(t)
	record := envHarnessRecorder(t, bin, "copilot", "COPILOT_CUSTOM_INSTRUCTIONS_DIRS")
	for _, ssh := range []bool{false, true} {
		inst := identityTestInstance("copilot")
		inst.Command = "copilot"
		launch := inst.buildCopilotCommand("copilot")
		if ssh {
			inst.SSHHost = "worker@lab-box"
			var err error
			if launch, _, err = inst.prepareCommand(inst.buildCopilotCommand("copilot")); err != nil {
				t.Fatal(err)
			}
		}
		env := runEnvLaunch(t, launch, record, "COPILOT_CUSTOM_INSTRUCTIONS_DIRS=/theirs")
		dirs := strings.Split(env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"], ",")
		if len(dirs) != 2 || dirs[0] != "/theirs" {
			t.Fatalf("ssh=%v: COPILOT_CUSTOM_INSTRUCTIONS_DIRS = %q, want /theirs plus the identity dir", ssh, env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"])
		}
		data, err := os.ReadFile(filepath.Join(dirs[1], copilotIdentityFileName))
		if err != nil || string(data) != inst.BuildIdentityPrompt() {
			t.Errorf("ssh=%v: %s in %s is not the block: %v\n%s", ssh, copilotIdentityFileName, dirs[1], err, data)
		}
	}
}

// Review finding: wrapExitToShell strips the first "exec " in command
// position as the harness launcher. A title such as "run exec tests" sits in
// the env prefix's block text, ahead of the launcher, and must neither be
// mangled nor stop the pane from falling back to a shell.
func TestIdentityRemote_ExitToShellWithExecInTitle(t *testing.T) {
	bin := remoteTestEnv(t)
	record := remoteHarnessRecorder(t, bin, "claude")
	marker := filepath.Join(t.TempDir(), "shell-ran")
	fakeShell := filepath.Join(bin, "fake-shell")
	if err := os.WriteFile(fakeShell, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", fakeShell)
	on := true
	inst := sshIdentityInstance("claude", "run exec tests ;exec x")
	inst.ExitToShell = &on
	launch, _, err := inst.prepareCommand(inst.buildClaudeCommand("claude"))
	if err != nil {
		t.Fatal(err)
	}
	r := runLaunch(t, launch, record, t.TempDir())
	if got, _ := r.flagValue("--append-system-prompt"); !strings.Contains(got, "- title: run exec tests ;exec x\n") {
		t.Errorf("block title mangled by the exit-to-shell rewrite:\n%s", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("exit-to-shell fallback never ran (the real launcher kept its exec):\n%s", launch)
	}
}

// Review finding: sandboxed codex carries the block twice (file + override)
// under four quoting layers (account bash -c, docker exec, bash -c inside,
// ignore-suspend); a quote-heavy title went past tmux's limit. ensure-
// SandboxContainer needs a docker daemon, so the layers are applied here
// exactly as prepareCommand applies them after it.
func TestIdentityRemote_SandboxCodexFitsTmuxCommandLimit(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("codex")
	inst.Title = strings.Repeat("it's ", 24)
	inst.Account = "work"
	inst.Sandbox = &SandboxConfig{Enabled: true, Image: "img"}
	cmd := "export AGENTDECK_ACCOUNT=" + "work" + "; " + inst.buildCodexCommand("codex")
	cmd = "bash -c '" + strings.ReplaceAll(cmd, "'", "'\"'\"'") + "'"
	launch := wrapIgnoreSuspend(buildExecCommand(docker.NewContainer("agent-deck-test", "img"), nil, cmd))
	if n := len(launch); n > 14000 {
		t.Errorf("sandboxed codex launch is %d bytes; tmux refuses ~16 KB", n)
	}
}

// Review finding: a fork's claude flags were the parent's, so a local fork
// of an SSH parent got the parent's inline flag (empty locally) and a fork of
// a local parent the parent's identity file. The fork carries its own.
func TestIdentityFork_CarriesTargetIdentity(t *testing.T) {
	identityTestEnv(t)
	for _, ssh := range []bool{false, true} {
		parent := &Instance{ID: "parent99-1700000000", Tool: "claude", Title: "Parent", ClaudeSessionID: "11111111-1111-4111-8111-111111111111", ClaudeDetectedAt: time.Now()}
		if ssh {
			parent.SSHHost = "box"
		}
		parent.markClaudeSessionIDVerified()
		target := &Instance{ID: "child001-1700000000", Tool: "claude", Title: "Child", ProjectPath: t.TempDir()}
		cmd, err := parent.buildClaudeForkCommandForTarget(target, nil)
		if err != nil {
			t.Fatal(err)
		}
		childFile, _ := target.IdentityFilePath()
		parentFile, _ := parent.IdentityFilePath()
		if !strings.Contains(cmd, "--append-system-prompt-file "+childFile) {
			t.Errorf("ssh parent=%v: fork lacks its own identity file:\n%s", ssh, cmd)
		}
		if strings.Contains(cmd, parentFile) || strings.Contains(cmd, identityInlineText()) {
			t.Errorf("ssh parent=%v: fork still carries the parent's identity flag:\n%s", ssh, cmd)
		}
	}
}

// Review finding: without XDG_RUNTIME_DIR the remote file goes under the
// user's own $HOME, never a shared /tmp another user could pre-create.
func TestIdentityRemote_FallbackIsUnderHome(t *testing.T) {
	bin := remoteTestEnv(t)
	record := remoteHarnessRecorder(t, bin, "claude")
	inst := sshIdentityInstance("claude", "w")
	launch, _, err := inst.prepareCommand(inst.buildClaudeCommand("claude"))
	if err != nil {
		t.Fatal(err)
	}
	r := runLaunch(t, launch, record, "")
	want := filepath.Join(os.Getenv("HOME"), ".local", "share", "agent-deck", "identity", inst.ID, identityFileName)
	if r.idFile != want {
		t.Errorf("fallback %s = %q, want %q", IdentityFileEnv, r.idFile, want)
	}
}

// Lander review (MEDIUM): an SSH payload runs in the remote user's LOGIN
// shell. Without an Account or wrapper nothing put it under bash, so a fish
// or csh login shell received POSIX syntax (PATH prelude, identity prefix)
// and every spawn failed. The stand-in login shell below accepts only what a
// non-POSIX shell can run here: an optional `cd X && ` then `bash -c '...'`.
func TestIdentityRemote_SSHPayloadSurvivesNonPosixLoginShell(t *testing.T) {
	bin := remoteTestEnv(t)
	fishy := "#!/bin/sh\nfor a in \"$@\"; do last=$a; done\nrest=$last\ncase \"$rest\" in cd\\ *) rest=${rest#* && };; esac\n" +
		"case \"$rest\" in \"bash -c '\"*) exec sh -c \"$last\";; esac\necho \"fish: Unsupported use of '='\" >&2; exit 127\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fishy), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"claude", "codex", "pi", "opencode"} {
		record := envHarnessRecorder(t, bin, tool, IdentityFileEnv)
		inst := sshIdentityInstance(tool, "fish 'host'")
		inst.Account = "" // no account: nothing else wraps the payload
		inst.SSHRemotePath = "~/proj"
		var cmd string
		switch tool {
		case "claude":
			cmd = inst.buildClaudeCommand("claude")
		case "codex":
			cmd = inst.buildCodexCommand("codex")
		case "pi":
			cmd = inst.buildPiCommand("pi")
		case "opencode":
			cmd = inst.buildOpenCodeCommand("opencode")
		}
		launch, _, err := inst.prepareCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		home := os.Getenv("HOME")
		if err := os.MkdirAll(filepath.Join(home, "proj"), 0o755); err != nil {
			t.Fatal(err)
		}
		run := exec.Command("sh", "-c", launch)
		run.Env = append(os.Environ(), "RECORD="+record, "XDG_RUNTIME_DIR="+t.TempDir())
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("%s: launch failed under a non-POSIX login shell: %v\n%s\n%s", tool, err, out, launch)
		}
		if _, err := os.Stat(record); err != nil {
			t.Fatalf("%s: harness never ran", tool)
		}
	}
}

// writeSkills attaches n skills with long names to the project, growing only
// the block (no other part of the launch carries skill names).
func writeSkills(t *testing.T, project string, n int, name func(int) string) {
	t.Helper()
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &ProjectSkillsManifest{}
	for k := 0; k < n; k++ {
		m.Skills = append(m.Skills, ProjectSkillAttachment{ID: strconv.Itoa(k), Name: name(k), Source: "pool"})
	}
	if err := SaveProjectSkillsManifest(project, m); err != nil {
		t.Fatal(err)
	}
}

func sandboxCodexLaunch(t *testing.T, inst *Instance) string {
	t.Helper()
	cmd := "export AGENTDECK_ACCOUNT=" + "work" + "; " + inst.buildCodexCommand("codex")
	cmd = "bash -c '" + strings.ReplaceAll(cmd, "'", "'\"'\"'") + "'"
	return wrapIgnoreSuspend(buildExecCommand(docker.NewContainer("agent-deck-test", "img"), nil, cmd))
}

// Lander review (MEDIUM): no size cap. Near the limit codex drops its -c
// override (the file stays); a block too large to carry is not injected; the
// launch stays under tmux's limit either way and still starts.
func TestIdentityRemote_SizeCapDegradesInsteadOfBreaking(t *testing.T) {
	skill := func(k int) string { return fmt.Sprintf("skill-%03d-%s", k, strings.Repeat("x", 40)) }
	for _, tc := range []struct {
		name             string
		skills           int
		wantOverride     bool
		wantFile, wantCl bool
	}{
		{"small", 0, true, true, true},
		{"near limit: one copy only", 90, false, true, true},
		{"over the cap: env-only", 200, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identityTestEnv(t)
			inst := identityTestInstance("codex")
			inst.ProjectPath = filepath.Join(t.TempDir(), "proj")
			inst.Sandbox = &SandboxConfig{Enabled: true, Image: "img"}
			if tc.skills > 0 {
				writeSkills(t, inst.ProjectPath, tc.skills, skill)
			}
			launch := sandboxCodexLaunch(t, inst)
			if n := len(launch); n > 15000 {
				t.Errorf("sandboxed codex launch is %d bytes; tmux refuses ~16 KB", n)
			}
			if got := strings.Contains(launch, "developer_instructions="); got != tc.wantOverride {
				t.Errorf("codex override present=%v, want %v", got, tc.wantOverride)
			}
			if got := strings.Contains(launch, identityShellVar+"=$(printf"); got != tc.wantFile {
				t.Errorf("remote file write present=%v, want %v", got, tc.wantFile)
			}
			cl := identityTestInstance("claude")
			cl.ProjectPath, cl.Sandbox = inst.ProjectPath, inst.Sandbox
			if got := strings.Contains(cl.buildClaudeCommand("claude"), "--append-system-prompt"); got != tc.wantCl {
				t.Errorf("sandboxed claude inline flag present=%v, want %v", got, tc.wantCl)
			}
		})
	}
}

// Lander review (low): skill names come from a repo's skills.toml; NUL/CR
// must not reach the block (a raw NUL cannot be passed in argv at all).
func TestBuildIdentityPrompt_SkillNamesAreOneLine(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	writeSkills(t, inst.ProjectPath, 2, func(k int) string { return []string{"bad\x00name\rx", "\n"}[k] })
	got := inst.BuildIdentityPrompt()
	if strings.ContainsAny(got, "\x00\r") || !strings.Contains(got, "Attached to this session now: bad name x (") {
		t.Errorf("skill names not collapsed to one clean line:\n%q", got)
	}
}

// Lander review (low): a cross-harness switch into a sandbox must not embed
// the raw (multi-line) block in the pane command; only codex's single-line
// override is carried.
func TestIdentityNativeArgs_SandboxTargetNoRawBlock(t *testing.T) {
	identityTestEnv(t)
	for _, tool := range []string{"claude", "pi", "omp", "codex"} {
		inst := identityTestInstance(tool)
		inst.Sandbox = &SandboxConfig{Enabled: true}
		args := inst.identityNativeArgs(tool, "")
		for _, a := range args {
			if strings.Contains(a, "\n") {
				t.Errorf("%s: native arg carries raw newlines: %q", tool, a)
			}
		}
		if (tool == "codex") != (len(args) == 2) {
			t.Errorf("%s: args = %q", tool, args)
		}
	}
}

// Lander review (low): a fork whose target runs over SSH carries the block
// inline, with the env export that sets the variable in the same command.
func TestIdentityFork_SSHTargetCarriesInlineBlock(t *testing.T) {
	bin := remoteTestEnv(t)
	record := remoteHarnessRecorder(t, bin, "claude")
	parent := &Instance{ID: "parent99-1700000000", Tool: "claude", Title: "Parent", ClaudeSessionID: "11111111-1111-4111-8111-111111111111", ClaudeDetectedAt: time.Now()}
	parent.markClaudeSessionIDVerified()
	target := &Instance{ID: "child001-1700000000", Tool: "claude", Title: "Child 'x'", ProjectPath: t.TempDir(), SSHHost: "box"}
	cmd, err := parent.buildClaudeForkCommandForTarget(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := runLaunch(t, cmd, record, t.TempDir())
	if got, ok := r.flagValue("--append-system-prompt"); !ok || got != strings.TrimRight(target.BuildIdentityPrompt(), "\n") {
		t.Errorf("ssh fork target did not get its own block inline: %q\n%s", got, cmd)
	}
}
