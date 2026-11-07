package provider

// The example docker provider's "ready machine" setup: the project and the
// optional Claude login token. The provider runs for real against a fake
// docker (a script that records its argv and the setup's stdin), and the
// in-container setup script runs for real against a temp HOME.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const fakeToken = "fake-oauth-FAKEtokenForTests_0123456789abcdefABCDEF"

func exampleDockerDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "providers", "docker"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
}

// fakeDocker is a docker stand-in: it logs each argv line to argv.log,
// keeps the label file docker create gets, saves the setup script's stdin
// to setup.stdin and answers inspect with the labels and an ssh port.
const fakeDocker = `#!/bin/sh
s="$FAKE_DOCKER_STATE"
printf '%s\n' "$*" >>"$s/argv.log"
case "$1" in
image) exit 0 ;;
create)
	while [ $# -gt 0 ]; do
		[ "$1" = --label-file ] && cp "$2" "$s/labels"
		shift
	done
	echo fakecid0123456789abcdef0123 ;;
cp | start | rm) cat >/dev/null 2>&1 || true ;;
exec)
	case "$*" in
	*agent-deck-sandbox-setup*)
		cat >"$s/setup.stdin"
		# FAKE_SETUP: also run the real in-container setup script on that
		# stdin, with a HOME of its own, so its stderr reaches the provider.
		if [ -n "${FAKE_SETUP:-}" ]; then
			mkdir -p "$s/home/shop"
			HOME="$s/home" "$FAKE_SETUP" <"$s/setup.stdin"
		fi ;;
	*/home/dev/.local/bin/agent-deck*) cat >"$s/agent-deck.copied" ;;
	*ssh_host_ed25519_key.pub*) echo "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGn3PMci37qtGRr0qkRvnuvhHEkIl6OupFa0wrBJQenu root@x" ;;
	*) cat >/dev/null ;;
	esac ;;
container)
	python3 -I -c '
import json, sys
labels = dict(l.rstrip("\n").split("=", 1) for l in open(sys.argv[1]) if "=" in l)
print(json.dumps([{"Id": "fakecid0123456789abcdef0123", "Config": {"Labels": labels},
  "State": {"Status": "running"},
  "NetworkSettings": {"Ports": {"22/tcp": [{"HostIp": "127.0.0.1", "HostPort": sys.argv[2]}]}}}]))
' "$s/labels" "$FAKE_SSH_PORT" ;;
*) echo "fake docker: unexpected $1" >&2; exit 1 ;;
esac
`

// sshBanner listens on 127.0.0.1 and greets every connection like sshd.
func sshBanner(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("SSH-2.0-fake\r\n"))
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done })
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

type providerRun struct {
	stdout, stderr string
	err            error
	state          string
}

func runExampleCreate(t *testing.T, conf string, extraEnv ...string) providerRun {
	t.Helper()
	env, state := exampleCreateEnv(t, conf, extraEnv...)
	// #nosec G204 -- the example provider script from this repo.
	cmd := exec.Command(filepath.Join(exampleDockerDir(t), "provider"), "create")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(`{"protocol":1,"action":"create","name":"t1","labels":{"agent-deck/remote":"t1"}}`)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return providerRun{stdout: out.String(), stderr: errb.String(), err: err, state: state}
}

// exampleCreateEnv sets up a fake docker, a provider config file and a
// banner-only sshd, and returns the provider's env and the fake docker's
// state dir.
func exampleCreateEnv(t *testing.T, conf string, extraEnv ...string) (env []string, state string) {
	t.Helper()
	needTools(t, "bash", "python3", "ssh-keygen", "timeout", "stat")
	root := t.TempDir()
	bin, state, tmp := filepath.Join(root, "bin"), filepath.Join(root, "state"), filepath.Join(root, "tmp")
	for _, d := range []string{bin, state, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o700); err != nil {
		t.Fatal(err)
	}
	confPath := filepath.Join(root, "docker-sandbox.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return append([]string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + root,
		"TMPDIR=" + tmp,
		"FAKE_DOCKER_STATE=" + state,
		"FAKE_SSH_PORT=" + sshBanner(t),
		"AGENTDECK_SANDBOX_CONFIG=" + confPath,
		"AGENTDECK_SANDBOX_IMAGE=agent-deck-sandbox:test",
		"AGENTDECK_SANDBOX_WAIT=10",
	}, extraEnv...), state
}

func writeToken(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude-token")
	if err := os.WriteFile(p, []byte(fakeToken+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDockerExampleProvider_TokenGoesThroughStdinOnly(t *testing.T) {
	tok := writeToken(t, 0o600)
	run := runExampleCreate(t, "project_name = shop\nclaude_token_file = "+tok+"\n")
	if run.err != nil {
		t.Fatalf("create failed: %v\nstderr:\n%s", run.err, run.stderr)
	}
	argv, err := os.ReadFile(filepath.Join(run.state, "argv.log"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := os.ReadFile(filepath.Join(run.state, "setup.stdin"))
	if err != nil {
		t.Fatalf("the setup script was not run: %v", err)
	}
	for name, text := range map[string]string{"docker argv": string(argv), "provider stdout": run.stdout, "provider stderr": run.stderr} {
		if strings.Contains(text, "FAKEtoken") {
			t.Fatalf("the token leaked into the %s", name)
		}
	}
	if !strings.Contains(string(stdin), "claude_token="+fakeToken) || !strings.Contains(string(stdin), "project_name=shop") {
		t.Fatalf("setup stdin lacks the token or the project name (token not shown)")
	}
	var resp struct {
		Sandbox Sandbox `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &resp); err != nil {
		t.Fatalf("provider stdout is not one JSON object: %v\n%s", err, run.stdout)
	}
	if resp.Sandbox.Labels["project"] != "/home/dev/shop" || resp.Sandbox.Labels["claude-login"] != "token" {
		t.Fatalf("sandbox labels = %v, want project /home/dev/shop and claude-login token", resp.Sandbox.Labels)
	}
}

func TestDockerExampleProvider_RefusesAnUnsafeTokenFile(t *testing.T) {
	tok := writeToken(t, 0o644)
	run := runExampleCreate(t, "claude_token_file = "+tok+"\n")
	if run.err == nil {
		t.Fatal("create succeeded with a group/other-readable token file")
	}
	if !strings.Contains(run.stderr, "must be 0600 or 0400") {
		t.Fatalf("stderr does not say why: %s", run.stderr)
	}
	if strings.Contains(run.stderr, "FAKEtoken") {
		t.Fatal("the token leaked into stderr")
	}
	if argv, _ := os.ReadFile(filepath.Join(run.state, "argv.log")); strings.Contains(string(argv), "create") {
		t.Fatal("a container was created although the token file was refused")
	}
}

func TestDockerExampleProvider_DefaultsToDemoProjectWithoutLogin(t *testing.T) {
	run := runExampleCreate(t, "")
	if run.err != nil {
		t.Fatalf("create failed: %v\nstderr:\n%s", run.err, run.stderr)
	}
	stdin, err := os.ReadFile(filepath.Join(run.state, "setup.stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(stdin); got != "project_name=demo\n" {
		t.Fatalf("setup stdin = %q, want only the demo project", got)
	}
}

func TestDockerExampleProvider_CopiesAgentDeckBinaryIn(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "agent-deck")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho fake agent-deck\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := runExampleCreate(t, "agent_deck_binary = "+bin+"\n")
	if run.err != nil {
		t.Fatalf("create failed: %v\nstderr:\n%s", run.err, run.stderr)
	}
	got, err := os.ReadFile(filepath.Join(run.state, "agent-deck.copied"))
	if err != nil || string(got) != "#!/bin/sh\necho fake agent-deck\n" {
		t.Fatalf("agent-deck binary not copied into ~/.local/bin of the sandbox: %v %q", err, got)
	}
}

// runSetup runs the in-container setup script with a temp HOME holding a
// copied-in project folder.
func runSetup(t *testing.T, stdin string) (home, stderr string, err error) {
	t.Helper()
	needTools(t, "bash")
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "shop"), 0o700); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- the example setup script from this repo.
	cmd := exec.Command(filepath.Join(exampleDockerDir(t), "sandbox-setup.sh"))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	cmd.Stdin = strings.NewReader(stdin)
	// stdout and stderr together: a secret must reach neither.
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &errb, &errb
	err = cmd.Run()
	return home, errb.String(), err
}

func TestDockerExampleSetup_WritesLoginForAgentDeckClaudeSessions(t *testing.T) {
	home, stderr, err := runSetup(t, "project_name=shop\nproject_copied=1\nclaude_token=CLAUDE_CODE_OAUTH_TOKEN="+fakeToken+"\n")
	if err != nil {
		t.Fatalf("setup failed: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "FAKEtoken") {
		t.Fatal("setup printed the token")
	}
	envFile := filepath.Join(home, ".config", "agent-deck-sandbox", "claude.env")
	st, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("claude.env mode = %o, want 600", st.Mode().Perm())
	}
	body, _ := os.ReadFile(envFile)
	if string(body) != "export CLAUDE_CODE_OAUTH_TOKEN="+fakeToken+"\n" {
		t.Fatal("claude.env does not export exactly the token (not shown)")
	}
	cfg, err := os.ReadFile(filepath.Join(home, ".config", "agent-deck", "config.toml"))
	if err != nil || !strings.Contains(string(cfg), `env_file = "~/.config/agent-deck-sandbox/claude.env"`) {
		t.Fatalf("agent-deck config does not point [claude].env_file at the token: %v\n%s", err, cfg)
	}
	var claudeState struct {
		HasCompletedOnboarding        bool `json:"hasCompletedOnboarding"`
		BypassPermissionsModeAccepted bool `json:"bypassPermissionsModeAccepted"`
		Projects                      map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err := json.Unmarshal(raw, &claudeState); err != nil {
		t.Fatalf(".claude.json: %v", err)
	}
	if !claudeState.HasCompletedOnboarding || !claudeState.BypassPermissionsModeAccepted || !claudeState.Projects[filepath.Join(home, "shop")].HasTrustDialogAccepted {
		t.Fatalf(".claude.json does not skip onboarding and trust the project: %s", raw)
	}
}

func TestDockerExampleSetup_NoTokenWritesNoLogin(t *testing.T) {
	home, stderr, err := runSetup(t, "project_name=shop\nproject_copied=1\n")
	if err != nil {
		t.Fatalf("setup failed: %v\n%s", err, stderr)
	}
	for _, p := range []string{".config/agent-deck/config.toml", ".config/agent-deck-sandbox/claude.env"} {
		if _, err := os.Stat(filepath.Join(home, p)); err == nil {
			t.Fatalf("%s written without a token", p)
		}
	}
	if !strings.Contains(stderr, "login none") {
		t.Fatalf("summary does not say there is no login: %s", stderr)
	}
}

func TestDockerExampleSetup_RejectsAMalformedToken(t *testing.T) {
	home, stderr, err := runSetup(t, "project_name=shop\nproject_copied=1\nclaude_token=abc$(touch pwned) "+fakeToken+"\n")
	if err == nil {
		t.Fatal("setup accepted a token with shell syntax and spaces")
	}
	if !strings.Contains(stderr, "does not look like a token") {
		t.Fatalf("setup did not say the token is malformed: %s", stderr)
	}
	if strings.Contains(stderr, "FAKEtoken") {
		t.Fatal("setup printed the token")
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "agent-deck-sandbox", "claude.env")); err == nil {
		t.Fatal("claude.env written for a malformed token")
	}
}

// leakToken is an obviously fake token for the token-leak tests; "FAKE-DEMO"
// is what must never show in any output.
const leakToken = "FAKE-DEMO-TOKEN-0000-not-a-real-key"

// badTokenFiles are token files that do not hold exactly one token line.
// Each used to put the token on a setup line of its own, which the setup
// script then printed as an "unknown setup line".
var badTokenFiles = map[string]string{
	"comment line first": "# my claude token\n" + leakToken + "\n",
	"blank first line":   "\n" + leakToken + "\n",
	"two lines":          leakToken + "\n" + leakToken + "-second\n",
}

func TestDockerExampleSetup_TokenOverSeveralLinesIsNeverPrinted(t *testing.T) {
	for shape, body := range badTokenFiles {
		t.Run(shape, func(t *testing.T) {
			home, out, err := runSetup(t, "project_name=shop\nproject_copied=1\nclaude_token="+body)
			if strings.Contains(out, "FAKE-DEMO") {
				t.Fatal("setup printed the token (output not shown)")
			}
			if err == nil {
				t.Fatalf("setup accepted a token spread over more than one line; output: %s", out)
			}
			if _, err := os.Stat(filepath.Join(home, ".config", "agent-deck-sandbox", "claude.env")); err == nil {
				t.Fatal("claude.env written for a bad token")
			}
		})
	}
}

// The provider refuses a token file that is not exactly one line before it
// creates anything, and the token shows nowhere: not in the provider's
// stdout or stderr, not in docker's argv, not in the error agent-deck shows
// (TUI notice, CLI) and not in agent-deck's debug log. The fake docker runs
// the real setup script, so a token that slipped through would reach the
// provider's stderr the way it does in a real container.
func TestDockerExampleProvider_TokenFileMustBeOneLineAndIsNeverShown(t *testing.T) {
	setup := filepath.Join(exampleDockerDir(t), "sandbox-setup.sh")
	for shape, body := range badTokenFiles {
		t.Run(shape, func(t *testing.T) {
			tok := filepath.Join(t.TempDir(), "claude-token")
			if err := os.WriteFile(tok, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			conf := "project_name = shop\nclaude_token_file = " + tok + "\n"

			run := runExampleCreate(t, conf, "FAKE_SETUP="+setup)
			argv, _ := os.ReadFile(filepath.Join(run.state, "argv.log"))
			for name, text := range map[string]string{"provider stdout": run.stdout, "provider stderr": run.stderr, "docker argv": string(argv)} {
				if strings.Contains(text, "FAKE-DEMO") {
					t.Fatalf("the token leaked into the %s (not shown)", name)
				}
			}
			if run.err == nil {
				t.Fatal("create succeeded with a token file that is not exactly one line")
			}
			if !strings.Contains(run.stderr, "exactly one line") {
				t.Fatalf("stderr does not say why: %s", run.stderr)
			}
			if strings.Contains(string(argv), "create") {
				t.Fatal("a container was created although the token file was refused")
			}

			// The same through agent-deck's provider client, with its
			// debug log on.
			logDir := t.TempDir()
			logging.Init(logging.Config{Debug: true, LogDir: logDir, Level: "debug", Format: "json"})
			t.Cleanup(func() { logging.Shutdown(); logging.Init(logging.Config{}) })
			env, _ := exampleCreateEnv(t, conf, "FAKE_SETUP="+setup)
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				t.Setenv(k, v)
			}
			c := &Client{Name: "docker", Config: session.ProviderConfig{Command: filepath.Join(exampleDockerDir(t), "provider"), TimeoutSeconds: 30, CreateTimeoutSeconds: 30}}
			_, err := c.Create(context.Background(), "t1", map[string]string{"agent-deck/remote": "t1"})
			if err == nil {
				t.Fatal("client create succeeded with a bad token file")
			}
			if strings.Contains(err.Error(), "FAKE-DEMO") {
				t.Fatal("the token leaked into the error agent-deck shows (not shown)")
			}
			logging.Shutdown()
			entries, _ := os.ReadDir(logDir)
			for _, e := range entries {
				data, _ := os.ReadFile(filepath.Join(logDir, e.Name()))
				if bytes.Contains(data, []byte("FAKE-DEMO")) {
					t.Fatalf("the token leaked into the debug log %s (not shown)", e.Name())
				}
			}
		})
	}
}

// Destroy removes the container by the full id it just inspected (and
// checked the label on), not by the id the request named, which docker
// would resolve again as a prefix.
func TestDockerExampleProvider_DestroyRemovesByTheInspectedFullID(t *testing.T) {
	env, state := exampleCreateEnv(t, "")
	keydir := filepath.Join(t.TempDir(), "agent-deck-docker-test")
	if err := os.MkdirAll(keydir, 0o700); err != nil {
		t.Fatal(err)
	}
	labels := "agent-deck.provider=docker\nagent-deck.keydir=" + keydir + "\n"
	if err := os.WriteFile(filepath.Join(state, "labels"), []byte(labels), 0o600); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- the example provider script from this repo.
	cmd := exec.Command(filepath.Join(exampleDockerDir(t), "provider"), "destroy")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(`{"protocol":1,"action":"destroy","id":"fakecid0123"}`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	argv, _ := os.ReadFile(filepath.Join(state, "argv.log"))
	var rm []string
	for _, l := range strings.Split(strings.TrimSpace(string(argv)), "\n") {
		if strings.HasPrefix(l, "rm ") {
			rm = append(rm, l)
		}
	}
	if len(rm) != 1 || rm[0] != "rm -f -- fakecid0123456789abcdef0123" {
		t.Fatalf("docker rm calls = %q, want one rm of the inspected full id fakecid0123456789abcdef0123", rm)
	}
	if _, err := os.Stat(keydir); !os.IsNotExist(err) {
		t.Fatal("the key dir was not removed")
	}
}
