package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Tests for `agent-deck provider connect azure` against the fake az in
// testdata/fake-az-connect (see its README.md). Nothing here reaches Azure:
// PATH and azConnectLookPath point at the fake, HOME/XDG are per test.

const (
	azTestUser    = "dev.tester@outlook.com"
	azTestTenant  = "a1b2c3d4-0000-4000-8000-00000000000a"
	azTestSub1    = "11111111-1111-4111-8111-111111111111"
	azTestSub1Nm  = "Azure subscription 1"
	azTestSub2    = "22222222-2222-4222-8222-222222222222"
	azTestSub2Nm  = "Pay-As-You-Go Dev"
	azTestRG      = "agent-deck-sandboxes"
	azTestCode    = "FAKE1234X"
	azTestURL     = "https://microsoft.com/devicelogin"
	azTestGivenID = "0f0e0d0c-0b0a-4908-8706-050403020100"
)

func azTestScope(sub string) string { return "/subscriptions/" + sub + "/resourceGroups/" + azTestRG }

type azTestEnv struct {
	t       *testing.T
	home    string
	state   string
	cfgDir  string
	dataDir string
	fakeAz  string
	stdout  *bytes.Buffer
	stderr  *bytes.Buffer
	sleeps  []time.Duration
}

func newAzConnectEnv(t *testing.T, scenario string) *azTestEnv {
	t.Helper()
	fakeDir, err := filepath.Abs(filepath.Join("testdata", "fake-az-connect"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	e := &azTestEnv{
		t:       t,
		home:    home,
		state:   t.TempDir(),
		cfgDir:  filepath.Join(home, ".config", "agent-deck"),
		dataDir: filepath.Join(home, ".local", "share", "agent-deck"),
		fakeAz:  filepath.Join(fakeDir, "az"),
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AGENTDECK_AZURE_CONFIG", "")
	t.Setenv("AZURE_CONFIG_DIR", "")
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_AZ_STATE", e.state)
	t.Setenv("FAKE_AZ_SCENARIO", scenario)
	t.Setenv("FAKE_AZ_LOGIN_WAIT", "0")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	origDeps := providerDeps
	origSleep, origLook, origGOOS := azConnectSleep, azConnectLookPath, azConnectGOOS
	origWindow, origLogin := azConnectCheckWindow, azConnectLoginTimeout
	t.Cleanup(func() {
		providerDeps = origDeps
		azConnectSleep, azConnectLookPath, azConnectGOOS = origSleep, origLook, origGOOS
		azConnectCheckWindow, azConnectLoginTimeout = origWindow, origLogin
	})
	providerDeps.isTTY = func() bool { return false }
	providerDeps.stdin = strings.NewReader("")
	providerDeps.stdout = e.stdout
	providerDeps.stderr = e.stderr
	azConnectSleep = func(d time.Duration) { e.sleeps = append(e.sleeps, d) }
	azConnectLookPath = func(name string) (string, error) {
		if name != "az" {
			return "", fmt.Errorf("unexpected lookup of %q", name)
		}
		return e.fakeAz, nil
	}
	azConnectGOOS = "linux"
	return e
}

func (e *azTestEnv) scenario(s string) { e.t.Setenv("FAKE_AZ_SCENARIO", s) }

func (e *azTestEnv) reset() {
	e.stdout.Reset()
	e.stderr.Reset()
	e.sleeps = nil
	session.ClearUserConfigCache()
}

func (e *azTestEnv) run(args ...string) int {
	e.t.Helper()
	e.reset()
	return runProviderConnect("", args)
}

func (e *azTestEnv) mustConnect(args ...string) {
	e.t.Helper()
	if code := e.run(append([]string{"azure"}, args...)...); code != 0 {
		e.t.Fatalf("connect exit %d\nstdout:\n%s\nstderr:\n%s", code, e.stdout, e.stderr)
	}
}

func (e *azTestEnv) secretPath() string { return filepath.Join(e.cfgDir, "azure-sp-secret") }
func (e *azTestEnv) confPath() string   { return filepath.Join(e.cfgDir, "azure-vm.conf") }
func (e *azTestEnv) bundleDir() string  { return filepath.Join(e.dataDir, "providers", "azure-vm") }

func (e *azTestEnv) stateFile(name string) string {
	data, err := os.ReadFile(filepath.Join(e.state, name))
	if err != nil {
		return ""
	}
	return string(data)
}

func (e *azTestEnv) stateExists(name string) bool {
	_, err := os.Stat(filepath.Join(e.state, name))
	return err == nil
}

// calls returns the fake's argv.log, one []string per az call.
func (e *azTestEnv) calls() [][]string {
	var out [][]string
	for _, l := range strings.Split(strings.TrimRight(e.stateFile("argv.log"), "\n"), "\n") {
		if l != "" {
			out = append(out, strings.Split(l, "\x1f"))
		}
	}
	return out
}

// callsWith returns the calls starting with prefix.
func (e *azTestEnv) callsWith(prefix ...string) [][]string {
	var out [][]string
	for _, c := range e.calls() {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], " ") == strings.Join(prefix, " ") {
			out = append(out, c)
		}
	}
	return out
}

func azArg(call []string, flag string) string {
	for i := 0; i+1 < len(call); i++ {
		if call[i] == flag {
			return call[i+1]
		}
	}
	return ""
}

// confValues parses azure-vm.conf.
func (e *azTestEnv) confValues() map[string]string {
	e.t.Helper()
	data, err := os.ReadFile(e.confPath())
	if err != nil {
		e.t.Fatalf("reading azure-vm.conf: %v", err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		if k, v := azConfKey(l); k != "" {
			m[k] = v
		}
	}
	return m
}

func (e *azTestEnv) appID() string {
	e.t.Helper()
	id := e.confValues()["sp_app_id"]
	if id == "" {
		e.t.Fatal("no sp_app_id in azure-vm.conf")
	}
	return id
}

func azMode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return st.Mode().Perm()
}

func azExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// jsonEvents parses stdout as JSON lines; every line must be a JSON object.
func (e *azTestEnv) jsonEvents() []map[string]any {
	e.t.Helper()
	var evs []map[string]any
	for _, l := range strings.Split(strings.TrimRight(e.stdout.String(), "\n"), "\n") {
		if l == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			e.t.Fatalf("stdout line is not JSON: %q (%v)", l, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func azEventsOf(evs []map[string]any, name string) []map[string]any {
	var out []map[string]any
	for _, ev := range evs {
		if ev["event"] == name {
			out = append(out, ev)
		}
	}
	return out
}

// assertNoSecret checks the secret is nowhere it must not be.
func (e *azTestEnv) assertNoSecret(secret string) {
	e.t.Helper()
	if secret == "" {
		e.t.Fatal("empty secret to look for")
	}
	for name, s := range map[string]string{
		"stdout":   e.stdout.String(),
		"stderr":   e.stderr.String(),
		"argv.log": e.stateFile("argv.log"),
		"env.log":  e.stateFile("env.log"),
	} {
		if strings.Contains(s, secret) {
			e.t.Fatalf("the secret appears in %s", name)
		}
	}
	if conf, err := os.ReadFile(e.confPath()); err == nil && strings.Contains(string(conf), secret) {
		e.t.Fatal("the secret appears in azure-vm.conf")
	}
	if cfg, err := os.ReadFile(filepath.Join(e.cfgDir, "config.toml")); err == nil && strings.Contains(string(cfg), secret) {
		e.t.Fatal("the secret appears in config.toml")
	}
}

func azWantKind(t *testing.T, e *azTestEnv, kind string) map[string]any {
	t.Helper()
	errs := azEventsOf(e.jsonEvents(), "error")
	if len(errs) != 1 {
		t.Fatalf("want one error event, got %d\nstdout:\n%s", len(errs), e.stdout)
	}
	if errs[0]["kind"] != kind {
		t.Fatalf("error kind = %v, want %s (message %v)", errs[0]["kind"], kind, errs[0]["message"])
	}
	return errs[0]
}

// ---- connect: happy path ----

func TestProviderConnectAzure_HappyPathEndToEnd(t *testing.T) {
	e := newAzConnectEnv(t, "logged_out")
	e.mustConnect()
	out, errOut := e.stdout.String(), e.stderr.String()

	// login: intro, link and code
	for _, want := range []string{
		azTextLoginIntro,
		azTextDeviceCode(azTestURL, azTestCode),
		fmt.Sprintf(azTextUsingLoginFmt, azTestUser),
		fmt.Sprintf(azTextSubscriptionFmt, azTestSub1Nm, azTestSub1),
		fmt.Sprintf(azTextGroupCreatedFmt, azTestRG, "westeurope"),
		fmt.Sprintf(azTextRoleCleanFmt, azTestRG),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q\nstdout:\n%s", want, out)
		}
	}
	logins := e.callsWith("login", "--use-device-code")
	if len(logins) != 1 || !strings.Contains(strings.Join(logins[0], " "), "--allow-no-subscriptions") {
		t.Fatalf("device-code login calls: %q", logins)
	}

	// one subscription picked, never made the user's default
	if e.stateExists("default_sub") {
		t.Fatal("connect changed the user's default subscription (az account set)")
	}
	for _, c := range e.callsWith("account", "set") {
		t.Fatalf("connect ran az account set as the user: %q", c)
	}

	// group created with the tag
	gdir := filepath.Join("groups", azTestSub1, azTestRG)
	if loc := strings.TrimSpace(e.stateFile(filepath.Join(gdir, "location"))); loc != "westeurope" {
		t.Fatalf("group location = %q", loc)
	}
	if !e.stateExists(filepath.Join(gdir, "tagged")) {
		t.Fatal("group created without the agent-deck tag")
	}

	// SP: Contributor on the group only
	creates := e.callsWith("ad", "sp", "create-for-rbac")
	if len(creates) != 1 {
		t.Fatalf("create-for-rbac calls: %d", len(creates))
	}
	if azArg(creates[0], "--role") != "Contributor" || azArg(creates[0], "--scopes") != azTestScope(azTestSub1) ||
		azArg(creates[0], "--name") != "agent-deck-sandboxes-sp" {
		t.Fatalf("create-for-rbac args: %q", creates[0])
	}
	app := e.appID()
	if scopes := strings.TrimSpace(e.stateFile(filepath.Join("sps", app, "scopes"))); scopes != azTestScope(azTestSub1) {
		t.Fatalf("SP scopes = %q", scopes)
	}
	if role := strings.TrimSpace(e.stateFile(filepath.Join("sps", app, "role"))); role != "Contributor" {
		t.Fatalf("SP role = %q", role)
	}

	// every call of connect itself carries --only-show-errors and an output format
	for _, c := range e.calls() {
		if len(c) > 1 && c[0] == "login" && c[1] == "--service-principal" {
			break // the provider's own calls follow
		}
		j := " " + strings.Join(c, " ") + " "
		if !strings.Contains(j, " --only-show-errors ") || !strings.Contains(j, " -o ") {
			t.Errorf("az call without --only-show-errors / -o: %q", c)
		}
	}

	// secret file: 0600, the issued secret, no newline
	issued := e.stateFile("issued_secret")
	if issued == "" {
		t.Fatal("the fake issued no secret")
	}
	if m := azMode(t, e.secretPath()); m != 0o600 {
		t.Fatalf("secret file mode %o", m)
	}
	if got, _ := os.ReadFile(e.secretPath()); string(got) != issued {
		t.Fatal("secret file does not hold the issued secret")
	}

	// azure-vm.conf
	if m := azMode(t, e.confPath()); m != 0o600 {
		t.Fatalf("azure-vm.conf mode %o", m)
	}
	conf := e.confValues()
	want := map[string]string{
		"resource_group": azTestRG, "location": "westeurope", "subscription": azTestSub1,
		"sp_tenant": azTestTenant, "sp_secret_file": e.secretPath(), "vm_size": "Standard_B2s",
	}
	for k, v := range want {
		if conf[k] != v {
			t.Errorf("azure-vm.conf %s = %q, want %q", k, conf[k], v)
		}
	}
	raw, _ := os.ReadFile(e.confPath())
	if !strings.HasPrefix(string(raw), azTextConfFileHeader+"\n") {
		t.Errorf("azure-vm.conf does not start with the header:\n%s", raw)
	}

	// bundled provider + config.toml
	bundled := filepath.Join(e.bundleDir(), "provider")
	if m := azMode(t, bundled); m != 0o700 {
		t.Fatalf("bundled provider mode %o", m)
	}
	if m := azMode(t, e.bundleDir()); m != 0o700 {
		t.Fatalf("bundle dir mode %o", m)
	}
	for _, f := range []string{"sandbox-init.sh", "sandbox-setup.sh"} {
		if m := azMode(t, filepath.Join(e.bundleDir(), f)); m != 0o600 {
			t.Errorf("bundled %s mode %o", f, m)
		}
	}
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	pc, ok := cfg.Providers["azure"]
	if !ok || pc.Command != bundled || pc.CreateTimeoutSeconds != 900 || pc.TimeoutSeconds != 600 {
		t.Fatalf("[providers.azure] = %+v (ok=%v)", pc, ok)
	}

	// the check ran the bundled provider as the SP, secret on stdin
	if got := e.stateFile("sp_login_secret"); got != issued {
		t.Fatal("the provider's SP login did not get the issued secret on stdin")
	}
	spLogins := e.callsWith("login", "--service-principal")
	if len(spLogins) == 0 || azArg(spLogins[0], "--password") != "@-" {
		t.Fatalf("SP login calls: %q", spLogins)
	}
	wantDone := azTextConnected(false, azTestSub1Nm, azTestSub1, azTestRG, "westeurope", app, e.secretPath(), e.confPath(), 0)
	if !strings.Contains(out, wantDone) {
		t.Fatalf("stdout lacks the CONNECTED block\nwant:\n%s\nstdout:\n%s\nstderr:\n%s", wantDone, out, errOut)
	}
	e.assertNoSecret(issued)
}

func TestProviderConnectAzure_SecretOnlyOnStdinAndNeverPrinted(t *testing.T) {
	e := newAzConnectEnv(t, "logged_out")
	e.mustConnect("--json")
	issued := e.stateFile("issued_secret")
	e.assertNoSecret(issued)
	if e.stderr.Len() != 0 {
		t.Fatalf("--json wrote to stderr: %s", e.stderr)
	}
	if e.stateFile("sp_login_secret") != issued {
		t.Fatal("secret did not reach az on stdin")
	}
	for _, c := range e.callsWith("login", "--service-principal") {
		for i, a := range c {
			if (a == "--password" || a == "-p") && (i+1 >= len(c) || c[i+1] != "@-") {
				t.Fatalf("SP login password not from stdin: %q", c)
			}
		}
	}
	// --reset-secret issues a new one, also never shown
	e.mustConnect("--reset-secret")
	second := e.stateFile("issued_secret")
	if second == issued {
		t.Fatal("--reset-secret did not issue a new secret")
	}
	if got, _ := os.ReadFile(e.secretPath()); string(got) != second {
		t.Fatal("secret file not updated after --reset-secret")
	}
	e.assertNoSecret(issued)
	e.assertNoSecret(second)
	if n := len(e.callsWith("ad", "sp", "create-for-rbac")); n != 2 {
		t.Fatalf("create-for-rbac calls after --reset-secret: %d", n)
	}
}

func TestProviderConnectAzure_IdempotentRerun(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	app := e.appID()
	secret, _ := os.ReadFile(e.secretPath())
	confBefore, _ := os.ReadFile(e.confPath())

	e.mustConnect()
	if n := len(e.callsWith("ad", "sp", "create-for-rbac")); n != 1 {
		t.Fatalf("create-for-rbac ran %d times", n)
	}
	if n := len(e.callsWith("group", "create")); n != 1 {
		t.Fatalf("group create ran %d times", n)
	}
	out := e.stdout.String()
	if !strings.Contains(out, azTextAlreadyConnectedFirst) || strings.Contains(out, azTextConnectedFirst+"\n") {
		t.Fatalf("second run output:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf(azTextSPKeptFmt, app)) {
		t.Fatalf("second run does not say it keeps the SP:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf(azTextGroupExistsFmt, azTestRG, "westeurope")) {
		t.Fatalf("second run does not say the group exists:\n%s", out)
	}
	if after, _ := os.ReadFile(e.secretPath()); !bytes.Equal(after, secret) {
		t.Fatal("secret changed on re-run")
	}
	if after, _ := os.ReadFile(e.confPath()); !bytes.Equal(after, confBefore) {
		t.Fatalf("azure-vm.conf changed on re-run:\n%s\n---\n%s", confBefore, after)
	}
}

func TestProviderConnectAzure_KeepsExtraConfKeys(t *testing.T) {
	e := newAzConnectEnv(t, "")
	if err := os.MkdirAll(e.cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	orig := "# my own notes\nproject_git = git@github.com:me/proj.git\nresource_group = old-group\nvm_size = Standard_B1ms\nclaude_token_file = /x/token\n"
	if err := os.WriteFile(e.confPath(), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	e.mustConnect()
	raw, _ := os.ReadFile(e.confPath())
	lines := strings.Split(string(raw), "\n")
	if lines[0] != "# my own notes" || lines[1] != "project_git = git@github.com:me/proj.git" ||
		lines[2] != "resource_group = "+azTestRG || lines[3] != "vm_size = Standard_B1ms" ||
		lines[4] != "claude_token_file = /x/token" {
		t.Fatalf("existing lines not kept in place:\n%s", raw)
	}
	if m := azMode(t, e.confPath()); m != 0o600 {
		t.Fatalf("azure-vm.conf mode %o", m)
	}
	conf := e.confValues()
	if conf["vm_size"] != "Standard_B1ms" || conf["subscription"] != azTestSub1 || conf["sp_app_id"] == "" {
		t.Fatalf("conf: %v", conf)
	}
	if !strings.Contains(e.stdout.String(), "a Standard_B1ms VM needs 1") {
		t.Fatalf("quota check did not use the conf's vm_size:\n%s", e.stdout)
	}
}

func TestProviderConnectAzure_KeepsOtherProviderCommand(t *testing.T) {
	e := newAzConnectEnv(t, "")
	mine := filepath.Join(e.home, "my-azure-provider")
	bundledSrc, _ := filepath.Abs(filepath.Join("..", "..", "examples", "providers", "azure-vm", "provider"))
	data, err := os.ReadFile(bundledSrc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveUserConfig(&session.UserConfig{Providers: map[string]session.ProviderConfig{"azure": {Command: mine, TimeoutSeconds: 60}}}); err != nil {
		t.Fatal(err)
	}
	e.mustConnect()
	if !strings.Contains(e.stdout.String(), fmt.Sprintf(azTextKeepingCommandFmt, "azure", mine)) {
		t.Fatalf("no keeping note:\n%s", e.stdout)
	}
	session.ClearUserConfigCache()
	cfg, _ := session.LoadUserConfig()
	if cfg.Providers["azure"].Command != mine {
		t.Fatalf("command replaced: %+v", cfg.Providers["azure"])
	}
}

// ---- connect: failures ----

func TestProviderConnectAzure_AzMissingPerOS(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			e := newAzConnectEnv(t, "")
			azConnectLookPath = func(string) (string, error) { return "", errors.New("executable file not found in $PATH") }
			azConnectGOOS = goos
			args := []string{"azure"}
			if goos == "windows" {
				// without it Windows stops earlier with the WSL text
				args = append(args, "--provider-command", `C:\agent-deck\azure-provider.exe`)
			}
			if code := e.run(args...); code != 1 {
				t.Fatalf("exit %d", code)
			}
			if got := e.stderr.String(); got != azTextAzMissing(goos)+"\n" {
				t.Fatalf("stderr:\n%s", got)
			}
			if e.stdout.Len() != 0 {
				t.Fatalf("stdout: %s", e.stdout)
			}
		})
	}
	if !strings.Contains(azTextAzMissing("darwin"), "brew update && brew install azure-cli") ||
		!strings.Contains(azTextAzMissing("linux"), "https://aka.ms/InstallAzureCLIDeb") ||
		!strings.Contains(azTextAzMissing("windows"), "winget install -e --id Microsoft.AzureCLI") {
		t.Fatal("install hints differ from the contract")
	}
	e := newAzConnectEnv(t, "")
	azConnectLookPath = func(string) (string, error) { return "", errors.New("not found") }
	if code := e.run("azure", "--json"); code != 1 {
		t.Fatalf("json exit %d", code)
	}
	ev := azWantKind(t, e, azKindAzMissing)
	if ev["message"] != azTextAzMissing("linux") {
		t.Fatalf("message %q", ev["message"])
	}
}

func TestProviderConnectAzure_NoSubscription(t *testing.T) {
	e := newAzConnectEnv(t, "no_subscription")
	if code := e.run("azure"); code != 1 {
		t.Fatalf("exit %d\n%s", code, e.stderr)
	}
	got := e.stderr.String()
	if !strings.Contains(got, azTextNoSubscription(azTestUser)) || !strings.Contains(got, `"Start free"`) {
		t.Fatalf("stderr:\n%s", got)
	}
	if len(e.callsWith("ad")) != 0 || len(e.callsWith("group")) != 0 {
		t.Fatal("went on after no subscription")
	}
	if code := e.run("azure", "--json"); code != 1 {
		t.Fatalf("json exit %d", code)
	}
	azWantKind(t, e, azKindNoSubscription)
}

func TestProviderConnectAzure_WrongTenantAtLogin(t *testing.T) {
	e := newAzConnectEnv(t, "logged_out,wrong_tenant_login")
	if code := e.run("azure", "--tenant", "contoso.onmicrosoft.com"); code != 1 {
		t.Fatalf("exit %d\n%s", code, e.stderr)
	}
	if got := e.stderr.String(); !strings.Contains(got, azTextWrongTenantLogin("contoso.onmicrosoft.com", "AADSTS50020")) {
		t.Fatalf("stderr:\n%s", got)
	}
	logins := e.callsWith("login", "--use-device-code")
	if len(logins) != 1 || azArg(logins[0], "--tenant") != "contoso.onmicrosoft.com" {
		t.Fatalf("login calls %q", logins)
	}
	// the code was still shown before the failure
	if !strings.Contains(e.stdout.String(), azTextDeviceCode(azTestURL, azTestCode)) {
		t.Fatalf("stdout:\n%s", e.stdout)
	}
}

func TestProviderConnectAzure_WrongTenantSubscriptions(t *testing.T) {
	e := newAzConnectEnv(t, "other_tenant")
	if code := e.run("azure", "--tenant", azTestTenant, "--json"); code != 1 {
		t.Fatalf("exit %d\n%s", code, e.stdout)
	}
	ev := azWantKind(t, e, azKindWrongTenant)
	msg, _ := ev["message"].(string)
	if !strings.HasPrefix(msg, "The subscription you want is not in the directory you logged in to ("+azTestTenant+").") ||
		!strings.Contains(msg, "contoso.onmicrosoft.com") {
		t.Fatalf("message:\n%s", msg)
	}
}

func TestProviderConnectAzure_DisabledSubscription(t *testing.T) {
	e := newAzConnectEnv(t, "disabled_sub")
	if code := e.run("azure"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	got := e.stderr.String()
	for _, want := range []string{
		"None of your subscriptions can be used right now:",
		"Free Trial (33333333-3333-4333-8333-333333333333): Disabled",
		"Azure for Students (44444444-4444-4444-8444-444444444444): Warned",
		"portal.azure.com > Subscriptions > your subscription > Upgrade.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr lacks %q:\n%s", want, got)
		}
	}
	e.run("azure", "--json")
	azWantKind(t, e, azKindSubscriptionDisabled)
}

func TestProviderConnectAzure_MultiSubNonTTYExit3(t *testing.T) {
	e := newAzConnectEnv(t, "multi_sub")
	if code := e.run("azure"); code != 3 {
		t.Fatalf("exit %d", code)
	}
	list := fmt.Sprintf("  %s (%s)\n  %s (%s)", azTestSub1Nm, azTestSub1, azTestSub2Nm, azTestSub2)
	if got := e.stderr.String(); got != azTextChooseSubscription(list)+"\n" {
		t.Fatalf("stderr:\n%q", got)
	}

	if code := e.run("azure", "--json"); code != 3 {
		t.Fatalf("json exit %d", code)
	}
	evs := e.jsonEvents()
	subs := azEventsOf(evs, "subscriptions")
	if len(subs) != 1 {
		t.Fatalf("subscriptions events: %v", evs)
	}
	arr, _ := subs[0]["subscriptions"].([]any)
	if len(arr) != 2 {
		t.Fatalf("subscriptions: %v", subs[0])
	}
	first, _ := arr[0].(map[string]any)
	if first["id"] != azTestSub1 || first["name"] != azTestSub1Nm || first["tenant_id"] != azTestTenant ||
		first["state"] != "Enabled" || first["default"] != true {
		t.Fatalf("first subscription: %v", first)
	}
	second, _ := arr[1].(map[string]any)
	if second["id"] != azTestSub2 || second["default"] != false {
		t.Fatalf("second subscription: %v", second)
	}
	azWantKind(t, e, azKindChooseSubscription)
	if len(e.callsWith("ad")) != 0 {
		t.Fatal("created an SP without a chosen subscription")
	}
}

func TestProviderConnectAzure_MultiSubTTYMenu(t *testing.T) {
	e := newAzConnectEnv(t, "multi_sub")
	providerDeps.isTTY = func() bool { return true }
	providerDeps.stdin = strings.NewReader("2\n")
	e.mustConnect()
	errOut := e.stderr.String()
	if !strings.Contains(errOut, azTextSubscriptionMenuIntro) || !strings.Contains(errOut, "  2) "+azTestSub2Nm+" ("+azTestSub2+")") ||
		!strings.Contains(errOut, fmt.Sprintf(azTextSubscriptionMenuAsk, 2)) {
		t.Fatalf("menu:\n%s", errOut)
	}
	if e.confValues()["subscription"] != azTestSub2 {
		t.Fatal("menu choice 2 not used")
	}
	creates := e.callsWith("ad", "sp", "create-for-rbac")
	if len(creates) != 1 || azArg(creates[0], "--scopes") != azTestScope(azTestSub2) {
		t.Fatalf("create-for-rbac: %q", creates)
	}
	if e.stateExists("default_sub") {
		t.Fatal("default subscription changed")
	}

	e2 := newAzConnectEnv(t, "multi_sub")
	providerDeps.isTTY = func() bool { return true }
	providerDeps.stdin = strings.NewReader("7\n")
	if code := e2.run("azure"); code != 1 || !strings.Contains(e2.stderr.String(), azTextSubscriptionMenuBad) {
		t.Fatalf("bad choice: exit %d\n%s", code, e2.stderr)
	}
}

func TestProviderConnectAzure_SubscriptionByNameAndID(t *testing.T) {
	e := newAzConnectEnv(t, "multi_sub")
	e.mustConnect("--subscription", azTestSub2Nm)
	if e.confValues()["subscription"] != azTestSub2 {
		t.Fatal("--subscription by name not used")
	}
	for _, c := range e.callsWith("group") {
		if azArg(c, "--subscription") != azTestSub2 {
			t.Fatalf("call without --subscription %s: %q", azTestSub2, c)
		}
	}

	e2 := newAzConnectEnv(t, "multi_sub")
	if code := e2.run("azure", "--subscription", "Nope"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	want := azTextSubscriptionNotFound("Nope", fmt.Sprintf("  %s (%s)\n  %s (%s)", azTestSub1Nm, azTestSub1, azTestSub2Nm, azTestSub2))
	if e2.stderr.String() != want+"\n" {
		t.Fatalf("stderr:\n%s", e2.stderr)
	}
}

func TestProviderConnectAzure_NoSPPermission(t *testing.T) {
	for _, scen := range []string{"no_sp_permission", "no_role_permission"} {
		t.Run(scen, func(t *testing.T) {
			e := newAzConnectEnv(t, scen)
			if code := e.run("azure"); code != 1 {
				t.Fatalf("exit %d\n%s", code, e.stderr)
			}
			got := e.stderr.String()
			for _, want := range []string{
				"Your account may not create a service principal (a robot login for agent-deck) here.",
				`Ask the owner of subscription "` + azTestSub1Nm + `" to do one of these:`,
				"agent-deck provider connect azure --subscription " + azTestSub1 + " --resource-group " + azTestRG + "\n",
				"az ad sp create-for-rbac --name agent-deck-sandboxes-sp --role Contributor --scopes " + azTestScope(azTestSub1),
				"--sp-app-id <appId> --sp-secret-stdin",
				`"Owner" or "User Access Administrator" role on resource group ` + azTestRG,
				`"Application Developer" role in Microsoft Entra ID`,
			} {
				if !strings.Contains(got, want) {
					t.Errorf("stderr lacks %q:\n%s", want, got)
				}
			}
			if azExists(e.secretPath()) || azExists(e.confPath()) {
				t.Fatal("files written after a failed SP creation")
			}
			// under no_role_permission the failed run left an app with that
			// name, which a re-run treats as sp_name_taken; so the JSON kind
			// is checked on fresh state
			if scen == "no_role_permission" {
				e.run("azure", "--json")
				azWantKind(t, e, azKindSPNameTaken)
			}
			j := newAzConnectEnv(t, scen)
			j.run("azure", "--json")
			azWantKind(t, j, azKindNoPermissionSP)
		})
	}
}

func TestProviderConnectAzure_NoGroupPermission(t *testing.T) {
	e := newAzConnectEnv(t, "no_group_permission")
	if code := e.run("azure"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	got := e.stderr.String()
	for _, want := range []string{
		`Your account may not create resource group ` + azTestRG + ` in subscription "` + azTestSub1Nm + `".`,
		"Azure said: ",
		"AuthorizationFailed",
		"Ask the subscription owner to create it (az group create --name " + azTestRG + " --location westeurope --subscription " + azTestSub1 + ")",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr lacks %q:\n%s", want, got)
		}
	}
	if len(e.callsWith("ad")) != 0 {
		t.Fatal("went on after the group failed")
	}
	e.run("azure", "--json")
	azWantKind(t, e, azKindNoPermissionGroup)
}

func TestProviderConnectAzure_BadLocation(t *testing.T) {
	e := newAzConnectEnv(t, "")
	if code := e.run("azure", "--location", "marsnorth"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if got := e.stderr.String(); got != azTextBadLocation("marsnorth")+"\n" {
		t.Fatalf("stderr:\n%s", got)
	}
}

func TestProviderConnectAzure_QuotaFull(t *testing.T) {
	e := newAzConnectEnv(t, "quota_full")
	if code := e.run("azure"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	got := e.stderr.String()
	if !strings.HasPrefix(got, "Region westeurope has no room for a Standard_B2s VM in this subscription: ") ||
		!strings.Contains(got, "allows 4 vCPUs, 3 in use, 2 needed.") ||
		!strings.Contains(got, "--vm-size Standard_B1s") {
		t.Fatalf("stderr:\n%s", got)
	}
	if len(e.callsWith("ad")) != 0 {
		t.Fatal("SP created despite no quota")
	}
	// one vCPU fits
	e.mustConnect("--vm-size", "Standard_B1s")
	e.run("azure", "--json", "--vm-size", "Standard_B2ms")
	azWantKind(t, e, azKindQuota)
}

func TestProviderConnectAzure_QuotaUnreadableContinues(t *testing.T) {
	e := newAzConnectEnv(t, "usage_error")
	e.mustConnect()
	if !strings.Contains(e.stderr.String(), fmt.Sprintf(azTextQuotaUnreadableFmt, "westeurope")) {
		t.Fatalf("stderr:\n%s", e.stderr)
	}
}

func TestProviderConnectAzure_RegistersProviders(t *testing.T) {
	e := newAzConnectEnv(t, "unregistered")
	e.mustConnect()
	for _, ns := range []string{"Microsoft.Compute", "Microsoft.Network"} {
		if !e.stateExists(filepath.Join("registered", ns)) {
			t.Errorf("%s not registered", ns)
		}
		found := false
		for _, c := range e.callsWith("provider", "register") {
			if azArg(c, "--namespace") == ns && azArg(c, "--subscription") == azTestSub1 && strings.Contains(strings.Join(c, " "), "--wait") {
				found = true
			}
		}
		if !found {
			t.Errorf("no provider register --wait for %s", ns)
		}
		if !strings.Contains(e.stdout.String(), fmt.Sprintf(azTextRegisteringFmt, ns)) {
			t.Errorf("no progress line for %s", ns)
		}
	}

	d := newAzConnectEnv(t, "unregistered,register_denied")
	if code := d.run("azure"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if got := d.stderr.String(); got != azTextNoPermissionRegister("Microsoft.Compute", azTestSub1)+"\n" {
		t.Fatalf("stderr:\n%s", got)
	}
	d.run("azure", "--json")
	azWantKind(t, d, azKindNoPermissionRegister)
}

func TestProviderConnectAzure_SPPropagationDelayRetried(t *testing.T) {
	e := newAzConnectEnv(t, "sp_propagation_delay=2")
	e.mustConnect()
	if len(e.sleeps) != 2 {
		t.Fatalf("sleeps = %v", e.sleeps)
	}
	for _, d := range e.sleeps {
		if d != 10*time.Second {
			t.Fatalf("retry pause %v", d)
		}
	}
	if n := strings.Count(e.stdout.String()+e.stderr.String(), azTextWaitingForSP); n != 1 {
		t.Fatalf("waiting line shown %d times", n)
	}
	if !strings.Contains(e.stdout.String(), azTextConnectedFirst) {
		t.Fatalf("stdout:\n%s", e.stdout)
	}
	if n := len(e.callsWith("login", "--service-principal")); n != 3 {
		t.Fatalf("SP logins = %d", n)
	}
}

func TestProviderConnectAzure_CheckFailsAfterWindow(t *testing.T) {
	e := newAzConnectEnv(t, "sp_propagation_delay=100")
	azConnectCheckWindow = 30 * time.Second
	if code := e.run("azure"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	got := e.stderr.String()
	if !strings.Contains(got, "Setup is saved, but the check failed: agent-deck could not list VMs in "+azTestRG+" as the service principal.") ||
		!strings.Contains(got, "AADSTS700016") || !strings.Contains(got, "Run the check again later with: agent-deck provider test azure") {
		t.Fatalf("stderr:\n%s", got)
	}
	if len(e.sleeps) != 3 {
		t.Fatalf("sleeps %v (window 30s / 10s)", e.sleeps)
	}
	if !azExists(e.secretPath()) || !azExists(e.confPath()) {
		t.Fatal("setup not saved before the check")
	}
	e.assertNoSecret(e.stateFile("issued_secret"))
	e.run("azure", "--json")
	azWantKind(t, e, azKindCheckFailed)
}

func TestProviderConnectAzure_GivenSPFromStdin(t *testing.T) {
	e := newAzConnectEnv(t, "")
	const secret = "given-Secret~value.42"
	providerDeps.stdin = strings.NewReader(secret + "\n")
	e.mustConnect("--sp-app-id", azTestGivenID, "--sp-secret-stdin")
	if n := len(e.callsWith("ad", "sp", "create-for-rbac")); n != 0 {
		t.Fatalf("create-for-rbac ran %d times", n)
	}
	if got, _ := os.ReadFile(e.secretPath()); string(got) != secret {
		t.Fatal("secret file does not hold the stdin secret")
	}
	if m := azMode(t, e.secretPath()); m != 0o600 {
		t.Fatalf("secret mode %o", m)
	}
	conf := e.confValues()
	if conf["sp_app_id"] != azTestGivenID || conf["sp_tenant"] != azTestTenant {
		t.Fatalf("conf: %v", conf)
	}
	if e.stateFile("sp_login_secret") != secret {
		t.Fatal("the check did not log in with the given secret")
	}
	e.assertNoSecret(secret)

	for _, in := range []string{"", "\n", "one\ntwo\n"} {
		f := newAzConnectEnv(t, "")
		providerDeps.stdin = strings.NewReader(in)
		if code := f.run("azure", "--sp-app-id", azTestGivenID, "--sp-secret-stdin"); code != 2 {
			t.Fatalf("stdin %q: exit %d", in, code)
		}
		if azExists(f.secretPath()) {
			t.Fatalf("stdin %q: secret file written", in)
		}
	}
	g := newAzConnectEnv(t, "")
	if code := g.run("azure", "--sp-app-id", azTestGivenID); code != 2 {
		t.Fatalf("--sp-app-id alone: exit %d", code)
	}
}

func TestProviderConnectAzure_UnknownTarget(t *testing.T) {
	e := newAzConnectEnv(t, "")
	if code := e.run("gcp"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(e.stderr.String(), `unknown provider to connect "gcp" (known: azure)`) {
		t.Fatalf("stderr:\n%s", e.stderr)
	}
	if code := e.run(); code != 2 {
		t.Fatalf("no target: exit %d", code)
	}
	if len(e.calls()) != 0 {
		t.Fatal("az called for a usage error")
	}
}

// ---- --json ----

func TestProviderConnectAzure_JSONEventShapes(t *testing.T) {
	e := newAzConnectEnv(t, "logged_out")
	e.mustConnect("--json")
	if e.stderr.Len() != 0 {
		t.Fatalf("stderr with --json: %s", e.stderr)
	}
	evs := e.jsonEvents()
	steps := map[string]bool{"az": true, "login": true, "subscription": true, "providers": true, "group": true, "region": true, "quota": true, "sp": true, "store": true, "check": true}
	statuses := map[string]bool{"start": true, "ok": true, "skip": true, "warn": true}
	seen := map[string]bool{}
	for _, ev := range evs {
		switch ev["event"] {
		case "step":
			s, _ := ev["step"].(string)
			st, _ := ev["status"].(string)
			if !steps[s] || !statuses[st] {
				t.Errorf("bad step event %v", ev)
			}
			if _, ok := ev["message"].(string); !ok {
				t.Errorf("step event without message string: %v", ev)
			}
			if st == "ok" {
				seen[s] = true
			}
		case "device_code":
			if ev["url"] != azTestURL || ev["code"] != azTestCode || len(ev) != 3 {
				t.Errorf("device_code event %v", ev)
			}
		case "done":
		default:
			t.Errorf("unexpected event %v", ev)
		}
	}
	for s := range steps {
		if !seen[s] {
			t.Errorf("no ok step event for %s", s)
		}
	}
	if len(azEventsOf(evs, "device_code")) != 1 {
		t.Fatal("want one device_code event")
	}
	done := azEventsOf(evs, "done")
	if len(done) != 1 || evs[len(evs)-1]["event"] != "done" {
		t.Fatalf("done event: %v", done)
	}
	d := done[0]
	if d["action"] != "connect" || d["resource_group"] != azTestRG || d["subscription"] != azTestSub1 ||
		d["sp_app_id"] != e.appID() || d["config"] != e.confPath() || d["provider"] != "azure" {
		t.Fatalf("done event %v", d)
	}
	for _, human := range []string{azTextConnectedFirst, azTextLoginIntro} {
		if strings.Contains(e.stdout.String(), human+"\n") {
			t.Fatalf("human text %q in --json stdout", human)
		}
	}

	// error event carries the exact human text
	f := newAzConnectEnv(t, "no_subscription")
	if code := f.run("azure", "--json"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	ev := azWantKind(t, f, azKindNoSubscription)
	if ev["message"] != azTextNoSubscription(azTestUser) || len(ev) != 3 {
		t.Fatalf("error event %v", ev)
	}
	if f.stderr.Len() != 0 {
		t.Fatalf("stderr with --json: %s", f.stderr)
	}
}

// ---- disconnect ----

func TestProviderConnectAzure_DisconnectRefusesNonTTYWithoutYes(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	if code := e.run("azure", "--disconnect"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if got := e.stderr.String(); got != azTextDisconnectNeedsYes+"\n" {
		t.Fatalf("stderr:\n%s", got)
	}
	if !azExists(e.secretPath()) || !azExists(e.confPath()) || len(e.callsWith("ad", "app", "delete")) != 0 {
		t.Fatal("disconnect changed something without --yes")
	}
}

func TestProviderConnectAzure_DisconnectTTYConfirm(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	app := e.appID()
	providerDeps.isTTY = func() bool { return true }
	providerDeps.stdin = strings.NewReader("n\n")
	if code := e.run("azure", "--disconnect"); code != 1 {
		t.Fatalf("answer n: exit %d", code)
	}
	if !strings.Contains(e.stderr.String(), fmt.Sprintf(azTextDisconnectConfirm, app)) || !azExists(e.secretPath()) {
		t.Fatalf("answer n:\n%s", e.stderr)
	}
	providerDeps.stdin = strings.NewReader("y\n")
	if code := e.run("azure", "--disconnect"); code != 0 {
		t.Fatalf("answer y: exit %d\n%s", code, e.stderr)
	}
	if azExists(e.secretPath()) {
		t.Fatal("secret kept after confirmed disconnect")
	}
}

func TestProviderConnectAzure_DisconnectRefusesWithSandboxes(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	sb, err := os.ReadFile(filepath.Join("testdata", "fake-az-connect", "recorded", "vm-list-one-sandbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.state, "vms.json"), sb, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := e.run("azure", "--disconnect", "--yes"); code != 1 {
		t.Fatalf("exit %d\n%s", code, e.stderr)
	}
	if got := e.stderr.String(); !strings.Contains(got, azTextSandboxesExist(1, azTestRG, "adk-0123456789")) {
		t.Fatalf("stderr:\n%s", got)
	}
	if len(e.callsWith("ad", "app", "delete")) != 0 || !azExists(e.secretPath()) {
		t.Fatal("disconnect went on with a sandbox left")
	}
	e.run("azure", "--disconnect", "--yes", "--json")
	azWantKind(t, e, azKindSandboxesExist)

	if code := e.run("azure", "--disconnect", "--yes", "--force"); code != 0 {
		t.Fatalf("--force: exit %d\n%s", code, e.stderr)
	}
	if azExists(e.secretPath()) {
		t.Fatal("--force did not disconnect")
	}
}

func TestProviderConnectAzure_DisconnectRemovesEverything(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	app := e.appID()
	if code := e.run("azure", "--disconnect", "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.stderr)
	}
	ras := e.callsWith("role", "assignment", "delete")
	if len(ras) != 1 || azArg(ras[0], "--assignee") != app || azArg(ras[0], "--scope") != azTestScope(azTestSub1) ||
		azArg(ras[0], "--subscription") != azTestSub1 {
		t.Fatalf("role assignment delete: %q", ras)
	}
	apps := e.callsWith("ad", "app", "delete")
	if len(apps) != 1 || azArg(apps[0], "--id") != app {
		t.Fatalf("ad app delete: %q", apps)
	}
	if !e.stateExists(filepath.Join("deleted", app)) {
		t.Fatal("app not deleted in the fake")
	}
	if azExists(e.secretPath()) || azExists(e.confPath()) || azExists(e.bundleDir()) {
		t.Fatal("local files left after disconnect")
	}
	session.ClearUserConfigCache()
	cfg, _ := session.LoadUserConfig()
	if _, ok := cfg.Providers["azure"]; ok {
		t.Fatal("[providers.azure] left after disconnect")
	}
	if len(e.callsWith("group", "delete")) != 0 || !e.stateExists(filepath.Join("groups", azTestSub1, azTestRG, "location")) {
		t.Fatal("resource group not kept")
	}
	out := e.stdout.String()
	if !strings.Contains(out, fmt.Sprintf(azTextGroupKeptFmt, azTestRG, azTestRG, azTestSub1)) || !strings.HasSuffix(out, azTextDisconnected+"\n") {
		t.Fatalf("stdout:\n%s", out)
	}

	if code := e.run("azure", "--disconnect", "--yes"); code != 0 || e.stdout.String() != azTextNotConnected+"\n" {
		t.Fatalf("second disconnect: exit %d\n%s", code, e.stdout)
	}
}

func TestProviderConnectAzure_DisconnectKeepsUserKeysAndOtherCommand(t *testing.T) {
	e := newAzConnectEnv(t, "")
	if err := os.MkdirAll(e.cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.confPath(), []byte("project_git = git@github.com:me/proj.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.mustConnect()
	if code := e.run("azure", "--disconnect", "--yes", "--json"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.stdout)
	}
	raw, err := os.ReadFile(e.confPath())
	if err != nil || strings.TrimSpace(string(raw)) != "project_git = git@github.com:me/proj.git" {
		t.Fatalf("azure-vm.conf after disconnect (%v):\n%s", err, raw)
	}
	done := azEventsOf(e.jsonEvents(), "done")
	if len(done) != 1 || done[0]["action"] != "disconnect" || done[0]["subscription"] != azTestSub1 || done[0]["resource_group"] != azTestRG {
		t.Fatalf("done: %v", done)
	}
}

func TestProviderConnectAzure_DisconnectNoPermissionKeepsFiles(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	app := e.appID()
	e.scenario("no_sp_permission")
	if code := e.run("azure", "--disconnect", "--yes"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if got := e.stderr.String(); !strings.Contains(got, "Your account may not remove service principal "+app) {
		t.Fatalf("stderr:\n%s", got)
	}
	if !azExists(e.secretPath()) || e.confValues()["sp_app_id"] != app {
		t.Fatal("local files removed although Azure refused")
	}
	e.run("azure", "--disconnect", "--yes", "--json")
	azWantKind(t, e, azKindNoPermissionSP)
}

func TestProviderConnectAzure_DisconnectAliasArgs(t *testing.T) {
	// handleProvider exits on failure, so only the success path goes through it.
	e := newAzConnectEnv(t, "")
	e.reset()
	handleProvider("", []string{"disconnect", "azure", "--yes"})
	if e.stdout.String() != azTextNotConnected+"\n" {
		t.Fatalf("stdout:\n%s", e.stdout)
	}
}

func TestProviderConnectAzure_WiderScopeWarns(t *testing.T) {
	e := newAzConnectEnv(t, "wide_scope")
	e.mustConnect()
	got := e.stderr.String()
	if !strings.Contains(got, "Warning: service principal "+e.appID()+" has roles outside resource group "+azTestRG+":") ||
		!strings.Contains(got, "User Access Administrator on /subscriptions/"+azTestSub1) {
		t.Fatalf("stderr:\n%s", got)
	}
	if strings.Contains(e.stdout.String(), fmt.Sprintf(azTextRoleCleanFmt, azTestRG)) {
		t.Fatal("said the role is clean despite a wider one")
	}
}

func TestProviderConnectAzure_LoginTimeout(t *testing.T) {
	e := newAzConnectEnv(t, "logged_out")
	t.Setenv("FAKE_AZ_LOGIN_WAIT", "30")
	azConnectLoginTimeout = 500 * time.Millisecond
	start := time.Now()
	if code := e.run("azure"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("login timeout took %v", d)
	}
	if got := e.stderr.String(); got != azTextLoginFailed(azTextLoginTimedOut)+"\n" {
		t.Fatalf("stderr:\n%s", got)
	}
	if !strings.Contains(e.stdout.String(), azTextDeviceCode(azTestURL, azTestCode)) {
		t.Fatalf("code not shown before the wait:\n%s", e.stdout)
	}
}

func TestProviderConnectAzure_ProviderCommandFlag(t *testing.T) {
	e := newAzConnectEnv(t, "")
	src, _ := filepath.Abs(filepath.Join("..", "..", "examples", "providers", "azure-vm", "provider"))
	e.mustConnect("--provider-command", src, "--name", "az2")
	session.ClearUserConfigCache()
	cfg, _ := session.LoadUserConfig()
	if pc := cfg.Providers["az2"]; pc.Command != src || pc.CreateTimeoutSeconds != 900 {
		t.Fatalf("[providers.az2] = %+v", pc)
	}
	if azExists(e.bundleDir()) {
		t.Fatal("bundle written although --provider-command was given")
	}
}

// ---- review fixes ----

const (
	azTestOtherTenant = "b2c3d4e5-0000-4000-8000-00000000000b"
	azTestTakenApp    = "f0f0f0f0-0000-4000-8000-000000000900"
)

// Finding 1: an SP from --sp-app-id belongs to its owner; disconnect must
// not delete it. Ownership is recorded as sp_created = true.
func TestProviderConnectAzure_DisconnectKeepsGivenSP(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	if got := e.confValues()["sp_created"]; got != "true" {
		t.Fatalf("own SP: sp_created = %q, want true", got)
	}
	providerDeps.stdin = strings.NewReader("given-Secret~value.42\n")
	e.mustConnect("--sp-app-id", azTestGivenID, "--sp-secret-stdin")
	if _, ok := e.confValues()["sp_created"]; ok {
		t.Fatal("sp_created kept after switching to a given SP")
	}
	before := len(e.calls())
	providerDeps.isTTY = func() bool { return true }
	providerDeps.stdin = strings.NewReader("y\n")
	if code := e.run("azure", "--disconnect"); code != 0 {
		t.Fatalf("disconnect exit %d\n%s", code, e.stderr)
	}
	if !strings.Contains(e.stderr.String(), fmt.Sprintf(azTextDisconnectConfirmLocal, azTestGivenID)) {
		t.Fatalf("confirm text:\n%s", e.stderr)
	}
	for _, c := range e.calls()[before:] {
		j := strings.Join(c, " ")
		if strings.HasPrefix(j, "ad app delete") || strings.HasPrefix(j, "role assignment delete") {
			t.Fatalf("disconnect deleted a given SP: %q", c)
		}
	}
	if !strings.Contains(e.stdout.String(), fmt.Sprintf(azTextSPNotOursFmt, azTestGivenID)) {
		t.Fatalf("stdout:\n%s", e.stdout)
	}
	if azExists(e.secretPath()) || azExists(e.confPath()) {
		t.Fatal("local files left")
	}
}

// Finding 2: create-for-rbac on a display name someone else uses would
// revoke their secret.
func TestProviderConnectAzure_SPNameTakenRefused(t *testing.T) {
	e := newAzConnectEnv(t, "sp_name_taken")
	const takenPw = "Taken8Q~secret-used-on-another-machine" // the fake's seed
	if code := e.run("azure", "--json"); code != 1 {
		t.Fatalf("exit %d\n%s", code, e.stdout)
	}
	ev := azWantKind(t, e, azKindSPNameTaken)
	if ev["message"] != azTextSPNameTaken("agent-deck-sandboxes-sp", azTestTakenApp) {
		t.Fatalf("message:\n%v", ev["message"])
	}
	if n := len(e.callsWith("ad", "sp", "create-for-rbac")); n != 0 {
		t.Fatalf("create-for-rbac ran %d times on a taken name", n)
	}
	if got := e.stateFile(filepath.Join("sps", azTestTakenApp, "password")); got != takenPw {
		t.Fatal("the other app's secret was replaced")
	}
	if azExists(e.secretPath()) || azExists(e.confPath()) {
		t.Fatal("files written after the refusal")
	}
	// --reset-secret takes it over on purpose, but it does not become ours
	e.mustConnect("--reset-secret")
	if e.appID() != azTestTakenApp {
		t.Fatalf("sp_app_id = %s", e.appID())
	}
	if _, ok := e.confValues()["sp_created"]; ok {
		t.Fatal("a taken-over app was recorded as created by connect")
	}

	// a name that is only a prefix of the taken one is not taken
	f := newAzConnectEnv(t, "sp_name_taken")
	f.mustConnect("--sp-name", "agent-deck")
	if f.appID() == azTestTakenApp {
		t.Fatal("prefix name reused the taken app")
	}
	// our own SP: re-created without --reset-secret when its secret is gone
	g := newAzConnectEnv(t, "")
	g.mustConnect()
	app := g.appID()
	if err := os.Remove(g.secretPath()); err != nil {
		t.Fatal(err)
	}
	g.mustConnect()
	if g.appID() != app || len(g.callsWith("ad", "sp", "create-for-rbac")) != 2 {
		t.Fatalf("own SP not re-created: app %s, calls %d", g.appID(), len(g.callsWith("ad", "sp", "create-for-rbac")))
	}
}

// Finding 3: a subscription in another directory than the az login stops
// with an az login --tenant instruction.
func TestProviderConnectAzure_LoginTenantDiffersStops(t *testing.T) {
	for _, args := range [][]string{{"azure", "--json"}, {"azure", "--json", "--tenant", azTestTenant}} {
		e := newAzConnectEnv(t, "login_other_tenant")
		if code := e.run(args...); code != 1 {
			t.Fatalf("%v: exit %d\n%s", args, code, e.stdout)
		}
		ev := azWantKind(t, e, azKindWrongTenant)
		if ev["message"] != azTextWrongTenantAccount(azTestSub1Nm, azTestSub1, azTestTenant, azTestOtherTenant) {
			t.Fatalf("%v: message:\n%v", args, ev["message"])
		}
		if len(e.callsWith("group")) != 0 || len(e.callsWith("ad")) != 0 || len(e.callsWith("provider")) != 0 {
			t.Fatalf("%v: went on after the tenant mismatch", args)
		}
	}
}

// Finding 4: values reaching az argv are validated.
func TestProviderConnectAzure_RejectsBadValues(t *testing.T) {
	for _, args := range [][]string{
		{"azure", "--resource-group", "-x"},
		{"azure", "--resource-group", "bad name!"},
		{"azure", "--tenant", "@evil"},
		{"azure", "--tenant", "a b"},
	} {
		e := newAzConnectEnv(t, "")
		if code := e.run(args...); code != 2 {
			t.Fatalf("%q: exit %d", args, code)
		}
		if n := len(e.calls()); n != 0 {
			t.Fatalf("%q: %d az calls", args, n)
		}
	}
	writeConf := func(e *azTestEnv, conf string) {
		if err := os.MkdirAll(e.cfgDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.confPath(), []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.secretPath(), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, conf := range []string{
		"resource_group = agent-deck-sandboxes\nsubscription = " + azTestSub1 + "\nsp_app_id = --help\nsp_created = true\n",
		"resource_group = -x\nsubscription = " + azTestSub1 + "\nsp_app_id = " + azTestGivenID + "\nsp_created = true\n",
		"resource_group = agent-deck-sandboxes\nsubscription = @x\nsp_app_id = " + azTestGivenID + "\nsp_created = true\n",
	} {
		e := newAzConnectEnv(t, "")
		writeConf(e, conf)
		if code := e.run("azure", "--disconnect", "--yes", "--json"); code != 1 {
			t.Fatalf("disconnect with %q: exit %d\n%s", conf, code, e.stdout)
		}
		azWantKind(t, e, azKindBadConfig)
		if n := len(e.calls()); n != 0 {
			t.Fatalf("disconnect with %q: %d az calls", conf, n)
		}
	}
	e := newAzConnectEnv(t, "")
	writeConf(e, "resource_group = agent-deck-sandboxes\nsubscription = "+azTestSub1+"\nsp_app_id = -x\nsp_tenant = "+azTestTenant+"\n")
	if code := e.run("azure", "--json"); code != 1 {
		t.Fatalf("connect with a bad sp_app_id: exit %d\n%s", code, e.stdout)
	}
	azWantKind(t, e, azKindBadConfig)
	for _, c := range e.calls() {
		for _, a := range c {
			if a == "-x" {
				t.Fatalf("bad conf value reached az: %q", c)
			}
		}
	}
}

// Finding 5: --sp-secret-stdin reads one line and does not wait for EOF.
func TestAzureConnect_ReadSecretOneLine(t *testing.T) {
	newAzConnectEnv(t, "")
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	go func() { _, _ = pw.Write([]byte("pipe-Secret~1\n")) }()
	type res struct {
		s   string
		err error
	}
	done := make(chan res, 1)
	c := &azConnect{in: bufio.NewReader(pr), errOut: io.Discard}
	go func() { s, err := c.readSecret(); done <- res{s, err} }()
	select {
	case r := <-done:
		if r.err != nil || r.s != "pipe-Secret~1" {
			t.Fatalf("got %q, %v", r.s, r.err)
		}
	case <-time.After(5 * time.Second):
		_ = pw.Close()
		t.Fatal("readSecret blocked after the first line (it must not wait for EOF)")
	}
	c = &azConnect{in: bufio.NewReader(strings.NewReader("no-newline-secret")), errOut: io.Discard}
	if s, err := c.readSecret(); err != nil || s != "no-newline-secret" {
		t.Fatalf("EOF without newline: %q, %v", s, err)
	}
	c = &azConnect{in: bufio.NewReader(strings.NewReader(strings.Repeat("x", int(azConnectSecretMax)+10) + "\n")), errOut: io.Discard}
	if _, err := c.readSecret(); err == nil || err.Error() != azTextSecretTooLong {
		t.Fatalf("too long: %v", err)
	}
}

// Finding 6: a kept SP whose check fails points at --reset-secret.
func TestProviderConnectAzure_KeptSPCheckFailsHintsReset(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	if strings.Contains(e.stderr.String(), azTextCheckFailedResetHint) {
		t.Fatal("hint on a passing run")
	}
	app := e.appID()
	if err := os.WriteFile(filepath.Join(e.state, "sps", app, "password"), []byte("reset-elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := e.run("azure"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	got := e.stderr.String()
	if !strings.Contains(got, "AADSTS7000215") || !strings.HasSuffix(got, "Run the check again later with: agent-deck provider test azure\n"+azTextCheckFailedResetHint+"\n") {
		t.Fatalf("stderr:\n%s", got)
	}
}

// Finding 7: the conf is written with sp_secret_pending before the secret,
// so a failed secret write does not leave a pair the next run keeps.
func TestProviderConnectAzure_SecretPendingMarker(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	oldSecret, _ := os.ReadFile(e.secretPath())
	// the secret write fails: its path is a directory
	if err := os.Remove(e.secretPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(e.secretPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := e.run("azure", "--reset-secret", "--json"); code != 1 {
		t.Fatalf("exit %d\n%s", code, e.stdout)
	}
	azWantKind(t, e, azKindStoreFailed)
	if got := e.confValues()["sp_secret_pending"]; got != "true" {
		t.Fatalf("sp_secret_pending = %q after a failed secret write", got)
	}
	// the old (now revoked) secret is back in place
	if err := os.Remove(e.secretPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.secretPath(), oldSecret, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := e.run("azure"); code != 0 {
		t.Fatalf("rerun after a failed secret write: exit %d\n%s", code, e.stderr)
	}
	if _, ok := e.confValues()["sp_secret_pending"]; ok {
		t.Fatal("sp_secret_pending left after a good run")
	}
	if got, _ := os.ReadFile(e.secretPath()); string(got) != e.stateFile("issued_secret") {
		t.Fatal("secret file does not hold the newest secret")
	}
}

// Finding 8: disconnect keeps a bundle another entry uses, and a custom
// command.
func TestProviderConnectAzure_DisconnectKeepsSharedBundleAndCustomCommand(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	bundled := filepath.Join(e.bundleDir(), "provider")
	cfg.Providers["az2"] = session.ProviderConfig{Command: bundled}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if code := e.run("azure", "--disconnect", "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.stderr)
	}
	if !azExists(bundled) {
		t.Fatal("bundled provider removed although [providers.az2] uses it")
	}
	session.ClearUserConfigCache()
	cfg, _ = session.LoadUserConfig()
	if _, ok := cfg.Providers["azure"]; ok {
		t.Fatal("[providers.azure] kept")
	}
	if cfg.Providers["az2"].Command != bundled {
		t.Fatal("[providers.az2] changed")
	}

	f := newAzConnectEnv(t, "")
	mine := filepath.Join(f.home, "my-azure-provider")
	src, _ := filepath.Abs(filepath.Join("..", "..", "examples", "providers", "azure-vm", "provider"))
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveUserConfig(&session.UserConfig{Providers: map[string]session.ProviderConfig{"azure": {Command: mine, TimeoutSeconds: 60}}}); err != nil {
		t.Fatal(err)
	}
	f.mustConnect()
	if code := f.run("azure", "--disconnect", "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, f.stderr)
	}
	session.ClearUserConfigCache()
	cfg, _ = session.LoadUserConfig()
	if pc, ok := cfg.Providers["azure"]; !ok || pc.Command != mine || pc.TimeoutSeconds != 60 {
		t.Fatalf("custom [providers.azure] not kept: %+v (ok=%v)", pc, ok)
	}
}

// Finding 9: the cached config is not mutated in place.
func TestProviderConnectAzure_DoesNotMutateCachedConfig(t *testing.T) {
	e := newAzConnectEnv(t, "")
	c := &azConnect{opts: azConnectOptions{name: "azure"}, out: io.Discard, errOut: io.Discard, bundleDir: e.bundleDir()}
	cached, err := session.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.writeProviderConfig(); err != nil {
		t.Fatal(err)
	}
	if _, ok := cached.Providers["azure"]; ok {
		t.Fatal("writeProviderConfig changed the cached config's Providers map")
	}
	session.ClearUserConfigCache()
	cached, err = session.LoadUserConfig()
	if err != nil || cached.Providers["azure"].Command == "" {
		t.Fatalf("entry not saved: %v", err)
	}
	if err := c.removeLocal(&azConfFile{}, cached); err != nil {
		t.Fatal(err)
	}
	if _, ok := cached.Providers["azure"]; !ok {
		t.Fatal("removeLocal changed the cached config's Providers map")
	}
}

// Finding 10: quota families of multi-letter series.
func TestAzureConnect_QuotaFamily(t *testing.T) {
	for _, tc := range []struct{ size, family string }{
		{"Standard_B2s", "standardBSFamily"},
		{"Standard_B1ms", "standardBSFamily"},
		{"Standard_B2als_v2", "standardBasv2Family"},
		{"Standard_B2ts_v2", "standardBsv2Family"},
		{"Standard_B2pls_v2", "standardBpsv2Family"},
		{"Standard_D2s_v5", "standardDSv5Family"},
		{"Standard_D2as_v5", "standardDASv5Family"},
		{"Standard_E4as_v5", "standardEASv5Family"},
		{"Standard_D4s_v3", "standardDSv3Family"},
		{"Standard_D2ads_v5", "standardDADSv5Family"},
		{"Standard_D2pls_v5", "standardDPLSv5Family"},
		{"Standard_F2s_v2", "standardFSv2Family"},
		{"Standard_DS2_v2", "standardDSv2Family"},
		{"Standard_D2_v3", "standardDv3Family"},
		{"Standard_NC6s_v3", "standardNCSv3Family"},
		{"Standard_A2_v2", "standardAv2Family"},
		{"standard_d2as_v5", "standardDASv5Family"},
		{"Basic_A1", ""},
	} {
		if got := azQuotaFamily(tc.size); got != tc.family {
			t.Errorf("azQuotaFamily(%q) = %q, want %q", tc.size, got, tc.family)
		}
	}
	for size, want := range map[string]int{"Standard_B1s": 1, "Standard_B2s": 2, "Standard_E4as_v5": 4, "Standard_D16s_v3": 16, "Basic_A1": 2} {
		if got := vmSizeVCPUs(size); got != want {
			t.Errorf("vmSizeVCPUs(%q) = %d, want %d", size, got, want)
		}
	}
}

// Finding 11: Windows needs WSL (the bundled provider is bash).
func TestProviderConnectAzure_WindowsNeedsWSL(t *testing.T) {
	e := newAzConnectEnv(t, "")
	azConnectGOOS = "windows"
	if code := e.run("azure"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if got := e.stderr.String(); got != azTextWindowsUseWSL+"\n" {
		t.Fatalf("stderr:\n%s", got)
	}
	if n := len(e.calls()); n != 0 {
		t.Fatalf("%d az calls", n)
	}
	e.run("azure", "--json")
	azWantKind(t, e, azKindUnsupportedOS)
	src, _ := filepath.Abs(filepath.Join("..", "..", "examples", "providers", "azure-vm", "provider"))
	e.mustConnect("--provider-command", src)
}

// Finding 12a: a re-run that changes nothing does not say it saved.
func TestProviderConnectAzure_RerunDoesNotSaySaved(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	saved := fmt.Sprintf(azTextStoredFmt, e.confPath())
	if !strings.Contains(e.stdout.String(), saved) {
		t.Fatalf("first run does not say it saved:\n%s", e.stdout)
	}
	e.mustConnect()
	if strings.Contains(e.stdout.String(), saved) {
		t.Fatalf("re-run says it saved although nothing changed:\n%s", e.stdout)
	}
}

// Finding 12b: the quota check runs before the group is created.
func TestProviderConnectAzure_QuotaFailLeavesNoGroup(t *testing.T) {
	e := newAzConnectEnv(t, "quota_full")
	if code := e.run("azure"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if n := len(e.callsWith("group", "create")); n != 0 {
		t.Fatalf("group create ran %d times before a quota failure", n)
	}
	if e.stateExists(filepath.Join("groups", azTestSub1, azTestRG)) {
		t.Fatal("stray resource group after a quota failure")
	}
	// an existing group: its own location is used for the quota check
	f := newAzConnectEnv(t, "")
	gd := filepath.Join(f.state, "groups", azTestSub1, azTestRG)
	if err := os.MkdirAll(gd, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(gd, "name"), []byte(azTestRG), 0o600)
	_ = os.WriteFile(filepath.Join(gd, "location"), []byte("northeurope"), 0o600)
	f.mustConnect("--location", "eastus")
	us := f.callsWith("vm", "list-usage")
	if len(us) != 1 || azArg(us[0], "--location") != "northeurope" {
		t.Fatalf("list-usage calls: %q", us)
	}
	if len(f.callsWith("group", "create")) != 0 {
		t.Fatal("existing group re-created")
	}
}

// Finding 12c: --json check event carries the check text, done says changed.
func TestProviderConnectAzure_JSONCheckAndChanged(t *testing.T) {
	e := newAzConnectEnv(t, "")
	for i, wantChanged := range []bool{true, false} {
		e.mustConnect("--json")
		evs := e.jsonEvents()
		var checkMsg any
		for _, ev := range evs {
			if ev["event"] == "step" && ev["step"] == "check" && ev["status"] == "ok" {
				checkMsg = ev["message"]
			}
		}
		if checkMsg != azTextCheckPassed(azTestRG, 0) {
			t.Fatalf("run %d: check ok message %q", i+1, checkMsg)
		}
		done := azEventsOf(evs, "done")
		if len(done) != 1 || done[0]["changed"] != wantChanged {
			t.Fatalf("run %d: done %v, want changed=%v", i+1, done, wantChanged)
		}
	}
}

// Finding 12d: the --yes refusal and the confirm come before any az call.
func TestProviderConnectAzure_DisconnectRefusalBeforeAnyCall(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	before := len(e.calls())
	if code := e.run("azure", "--disconnect"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if n := len(e.calls()) - before; n != 0 {
		t.Fatalf("%d az calls before the --yes refusal", n)
	}
	providerDeps.isTTY = func() bool { return true }
	providerDeps.stdin = strings.NewReader("n\n")
	if code := e.run("azure", "--disconnect"); code != 1 {
		t.Fatalf("answer n: exit %d", code)
	}
	if n := len(e.calls()) - before; n != 0 {
		t.Fatalf("%d az calls before the confirm answer", n)
	}
}

// ---- region and size (smoke test on a new subscription, 2026-11-05) ----

// azTestMissingRegNetwork is az's real stderr when Microsoft.Network is not
// registered (smoke/attempt2/create.err).
const azTestMissingRegNetwork = "ERROR: (MissingSubscriptionRegistration) The subscription is not registered to use namespace 'Microsoft.Network'. See https://aka.ms/rps-not-found for how to register subscriptions.\n" +
	"Code: MissingSubscriptionRegistration\n" +
	"Message: The subscription is not registered to use namespace 'Microsoft.Network'. See https://aka.ms/rps-not-found for how to register subscriptions.\n" +
	"Exception Details:\t(MissingSubscriptionRegistration) The subscription is not registered to use namespace 'Microsoft.Network'. See https://aka.ms/rps-not-found for how to register subscriptions.\n" +
	"\tCode: MissingSubscriptionRegistration\n" +
	"\tMessage: The subscription is not registered to use namespace 'Microsoft.Network'. See https://aka.ms/rps-not-found for how to register subscriptions.\n" +
	"\tTarget: Microsoft.Network\n"

// probeLocations returns the --location of every probe NSG create.
func (e *azTestEnv) probeLocations() []string {
	e.t.Helper()
	var locs []string
	for _, c := range e.callsWith("network", "nsg", "create") {
		if azArg(c, "--name") != azProbeNSG || azArg(c, "--resource-group") != azTestRG ||
			azArg(c, "--tags") != "agent-deck=probe" || azArg(c, "--subscription") != azTestSub1 {
			e.t.Fatalf("probe call args: %q", c)
		}
		locs = append(locs, azArg(c, "--location"))
	}
	return locs
}

// leftProbes lists the probe NSGs still in the fake's group.
func (e *azTestEnv) leftProbes() []string {
	left, _ := filepath.Glob(filepath.Join(e.state, "groups", azTestSub1, azTestRG, "nsgs", "*"))
	return left
}

func TestProviderConnectAzure_RegionClosedFallsBack(t *testing.T) {
	e := newAzConnectEnv(t, "region_closed=westeurope")
	e.mustConnect()
	note := fmt.Sprintf(azTextRegionUsingFmt, "westeurope", "swedencentral")
	if !strings.Contains(e.stderr.String(), note+"\n") {
		t.Fatalf("stderr lacks %q\nstderr:\n%s", note, e.stderr)
	}
	if got := e.probeLocations(); strings.Join(got, ",") != "westeurope,swedencentral" {
		t.Fatalf("probe locations = %q", got)
	}
	if n := len(e.callsWith("network", "nsg", "delete")); n != 1 {
		t.Fatalf("probe deletes = %d, want 1 (the refused create made nothing)", n)
	}
	if left := e.leftProbes(); len(left) != 0 {
		t.Fatalf("probe NSGs left behind: %v", left)
	}
	// the group was made first, in the asked region; it stays there
	calls := e.calls()
	firstProbe, groupCreate := -1, -1
	for i, c := range calls {
		j := strings.Join(c, " ")
		if groupCreate < 0 && strings.HasPrefix(j, "group create") {
			groupCreate = i
		}
		if firstProbe < 0 && strings.HasPrefix(j, "network nsg create") {
			firstProbe = i
		}
	}
	if groupCreate < 0 || firstProbe < groupCreate {
		t.Fatalf("probe ran before the group existed (group create #%d, probe #%d)", groupCreate, firstProbe)
	}
	if loc := e.stateFile(filepath.Join("groups", azTestSub1, azTestRG, "location")); loc != "westeurope" {
		t.Fatalf("group location = %q", loc)
	}
	conf := e.confValues()
	if conf["location"] != "swedencentral" || conf["vm_size"] != "Standard_B2s" {
		t.Fatalf("azure-vm.conf location = %q, vm_size = %q", conf["location"], conf["vm_size"])
	}
	// the quota is read for the region the VMs go to
	us := e.callsWith("vm", "list-usage")
	if len(us) == 0 || azArg(us[len(us)-1], "--location") != "swedencentral" {
		t.Fatalf("list-usage calls: %q", us)
	}

	// a re-run keeps the stored setup and does not probe again
	before := len(e.calls())
	e.mustConnect()
	for _, c := range e.calls()[before:] {
		j := strings.Join(c, " ")
		if strings.HasPrefix(j, "network nsg") || strings.HasPrefix(j, "vm list-skus") {
			t.Fatalf("re-run probed again: %q", c)
		}
	}
	if !strings.Contains(e.stdout.String(), azTextAlreadyConnectedFirst) {
		t.Fatalf("re-run output:\n%s", e.stdout)
	}
	if got := e.confValues()["location"]; got != "swedencentral" {
		t.Fatalf("re-run changed location to %q", got)
	}

	// --json: the note is a region warn event
	j := newAzConnectEnv(t, "region_closed=westeurope")
	j.mustConnect("--json")
	var warned, ok bool
	for _, ev := range azEventsOf(j.jsonEvents(), "step") {
		if ev["step"] == "region" && ev["status"] == "warn" && ev["message"] == note {
			warned = true
		}
		if ev["step"] == "region" && ev["status"] == "ok" {
			ok = true
		}
	}
	if !warned || !ok {
		t.Fatalf("region events (warn %v, ok %v):\n%s", warned, ok, j.stdout)
	}
}

func TestProviderConnectAzure_RegionClosedGivenLocation(t *testing.T) {
	e := newAzConnectEnv(t, "region_closed=westeurope+swedencentral")
	if code := e.run("azure", "--location", "westeurope"); code != 1 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, e.stdout, e.stderr)
	}
	want := azTextRegionClosed("westeurope", []string{"northeurope", "germanywestcentral"}, []string{"swedencentral", "northeurope", "germanywestcentral"})
	if got := e.stderr.String(); got != want+"\n" {
		t.Fatalf("stderr:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(want, "agent-deck provider connect azure --location northeurope") {
		t.Fatalf("REGION_CLOSED lacks the re-run command:\n%s", want)
	}
	if got := e.probeLocations(); strings.Join(got, ",") != "westeurope,swedencentral,northeurope,germanywestcentral" {
		t.Fatalf("probe locations = %q (at most 3 more after the asked one)", got)
	}
	if left := e.leftProbes(); len(left) != 0 {
		t.Fatalf("probe NSGs left behind: %v", left)
	}
	if len(e.callsWith("ad")) != 0 {
		t.Fatal("service principal work after a closed region")
	}
	e.run("azure", "--location", "westeurope", "--json")
	azWantKind(t, e, azKindRegionClosed)

	// nothing works: the text says so and lists what was tried
	f := newAzConnectEnv(t, "region_closed=westeurope+swedencentral+northeurope+germanywestcentral")
	if code := f.run("azure", "--location", "westeurope"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	want = azTextRegionClosed("westeurope", nil, []string{"swedencentral", "northeurope", "germanywestcentral"})
	if got := f.stderr.String(); got != want+"\n" {
		t.Fatalf("stderr:\n%s\nwant:\n%s", got, want)
	}
}

func TestProviderConnectAzure_SizeMissingPicksNext(t *testing.T) {
	for _, scen := range []string{"sku_missing=Standard_B2s", "sku_restricted=Standard_B2s"} {
		t.Run(scen, func(t *testing.T) {
			e := newAzConnectEnv(t, scen)
			e.mustConnect()
			note := fmt.Sprintf(azTextSizeUsingFmt, "Standard_B2s", "westeurope", "Standard_B2als_v2", 2)
			if !strings.Contains(e.stderr.String(), note+"\n") {
				t.Fatalf("stderr lacks %q\nstderr:\n%s", note, e.stderr)
			}
			if got := e.confValues()["vm_size"]; got != "Standard_B2als_v2" {
				t.Fatalf("vm_size = %q", got)
			}
			skus := e.callsWith("vm", "list-skus")
			if len(skus) == 0 {
				t.Fatal("no vm list-skus call")
			}
			for _, c := range skus {
				if azArg(c, "--location") != "westeurope" || azArg(c, "--resource-type") != "virtualMachines" ||
					!strings.HasPrefix(azArg(c, "--size"), "Standard_") || azArg(c, "--subscription") != azTestSub1 {
					t.Fatalf("list-skus args: %q", c)
				}
			}
			us := e.callsWith("vm", "list-usage")
			if len(us) != 1 {
				t.Fatalf("list-usage calls: %q", us)
			}
			if !strings.Contains(e.stdout.String(), fmt.Sprintf(azTextQuotaOKFmt, "westeurope", 10, "Standard_B2als_v2", 2)) {
				t.Fatalf("quota not checked for the picked size:\n%s", e.stdout)
			}
		})
	}
}

func TestProviderConnectAzure_SizeGivenUnavailable(t *testing.T) {
	offered := []string{"Standard_B2als_v2", "Standard_B2as_v2", "Standard_B2s_v2", "Standard_D2as_v5", "Standard_D2s_v5"}
	e := newAzConnectEnv(t, "sku_missing=Standard_B2s")
	if code := e.run("azure", "--vm-size", "Standard_B2s"); code != 1 {
		t.Fatalf("exit %d\nstderr:\n%s", code, e.stderr)
	}
	want := azTextSizeUnavailable("Standard_B2s", "westeurope", offered, e.confPath())
	if got := e.stderr.String(); got != want+"\n" {
		t.Fatalf("stderr:\n%s\nwant:\n%s", got, want)
	}
	if n := len(e.callsWith("group", "create")); n != 0 {
		t.Fatalf("group create ran %d times before a size failure", n)
	}
	e.run("azure", "--vm-size", "Standard_B2s", "--json")
	azWantKind(t, e, azKindSizeUnavailable)

	// vm_size in azure-vm.conf counts as asked for
	f := newAzConnectEnv(t, "sku_missing=Standard_B2s")
	if err := os.MkdirAll(f.cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.confPath(), []byte("vm_size = Standard_B2s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := f.run("azure"); code != 1 {
		t.Fatalf("conf vm_size: exit %d\nstderr:\n%s", code, f.stderr)
	}
	if got, want := f.stderr.String(), azTextSizeUnavailable("Standard_B2s", "westeurope", offered, f.confPath()); got != want+"\n" {
		t.Fatalf("conf vm_size: stderr:\n%s", got)
	}
	// --vm-size with an offered size replaces the conf's vm_size
	f.mustConnect("--vm-size", "Standard_D2s_v5")
	if got := f.confValues()["vm_size"]; got != "Standard_D2s_v5" {
		t.Fatalf("vm_size after --vm-size = %q", got)
	}
}

func TestProviderConnectAzure_SkusUnreadableContinues(t *testing.T) {
	e := newAzConnectEnv(t, "skus_error")
	e.mustConnect()
	prefix := strings.SplitN(azTextSkusUnreadableFmt, "%", 2)[0]
	if !strings.Contains(e.stderr.String(), prefix+"westeurope (") ||
		!strings.Contains(e.stderr.String(), "; continuing with Standard_B2s.") {
		t.Fatalf("stderr:\n%s", e.stderr)
	}
	if got := e.confValues()["vm_size"]; got != "Standard_B2s" {
		t.Fatalf("vm_size = %q", got)
	}

	// a listing that takes too long is cut off, then the same warning
	old := azConnectSkuTimeout
	azConnectSkuTimeout = 300 * time.Millisecond
	t.Cleanup(func() { azConnectSkuTimeout = old })
	f := newAzConnectEnv(t, "skus_wait=60")
	start := time.Now()
	f.mustConnect()
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("connect took %v with a hanging list-skus", d)
	}
	if !strings.Contains(f.stderr.String(), prefix+"westeurope (") {
		t.Fatalf("stderr:\n%s", f.stderr)
	}
}

func TestProviderConnectAzure_RerunSkipsRegionChecks(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	if got := e.probeLocations(); strings.Join(got, ",") != "westeurope" {
		t.Fatalf("first run probes = %q", got)
	}
	before := len(e.calls())
	e.mustConnect()
	for _, c := range e.calls()[before:] {
		j := strings.Join(c, " ")
		if strings.HasPrefix(j, "network nsg") || strings.HasPrefix(j, "vm list-skus") {
			t.Fatalf("re-run probed again: %q", c)
		}
	}
	// not the stored setup (new secret): checked again
	before = len(e.calls())
	e.mustConnect("--reset-secret")
	n := 0
	for _, c := range e.calls()[before:] {
		if strings.HasPrefix(strings.Join(c, " "), "network nsg create") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("--reset-secret run probed %d times, want 1", n)
	}
}

// Smoke attempt 2: a new subscription has Microsoft.Network unregistered.
// The fake answers with az's real text, and connect registers both
// namespaces before any call that needs them.
func TestProviderConnectAzure_UnregisteredNetworkRealTextAndOrder(t *testing.T) {
	e := newAzConnectEnv(t, "unregistered")
	// #nosec G204 -- the repo's fake az
	cmd := exec.Command(e.fakeAz, "network", "nsg", "create", "--name", "x-nsg", "--resource-group", azTestRG,
		"--location", "swedencentral", "--subscription", azTestSub1, "--only-show-errors", "-o", "none")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("nsg create succeeded with Microsoft.Network unregistered")
	}
	if got := stderr.String(); got != azTestMissingRegNetwork {
		t.Fatalf("fake stderr:\n%q\nwant:\n%q", got, azTestMissingRegNetwork)
	}

	before := len(e.calls())
	e.mustConnect()
	registered := map[string]int{}
	firstUse := -1
	for i, c := range e.calls()[before:] {
		if len(c) >= 2 && c[0] == "provider" && c[1] == "register" {
			registered[azArg(c, "--namespace")] = i
		}
		if firstUse < 0 && len(c) > 0 && (c[0] == "group" || c[0] == "network" || c[0] == "vm") {
			firstUse = i
		}
	}
	for _, ns := range []string{"Microsoft.Compute", "Microsoft.Network"} {
		i, ok := registered[ns]
		if !ok || firstUse < 0 || i > firstUse {
			t.Fatalf("%s registered at call #%d (ok=%v), first group/network/vm call #%d", ns, i, ok, firstUse)
		}
	}
	probes := 0
	for _, c := range e.calls()[before:] {
		if strings.HasPrefix(strings.Join(c, " "), "network nsg create --name "+azProbeNSG) {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("%d region probes after registering, want 1", probes)
	}
}

func TestAzureConnect_SkuOffered(t *testing.T) {
	const listing = `[
  {"name": "Standard_B2s", "resourceType": "virtualMachines", "locations": ["westeurope"], "restrictions": []},
  {"name": "Standard_B2ms", "resourceType": "virtualMachines", "locations": ["swedencentral"], "restrictions": [
    {"type": "Location", "reasonCode": "NotAvailableForSubscription", "values": ["swedencentral"],
     "restrictionInfo": {"locations": ["swedencentral"], "zones": null}}]},
  {"name": "Standard_B2als_v2", "resourceType": "virtualMachines", "locations": ["SwedenCentral"], "restrictions": [
    {"type": "Zone", "reasonCode": "NotAvailableForSubscription", "values": ["swedencentral"],
     "restrictionInfo": {"locations": ["swedencentral"], "zones": ["1"]}}]},
  {"name": "Standard_D2s_v5", "resourceType": "virtualMachines", "locations": ["swedencentral"], "restrictions": [
    {"type": "Location", "reasonCode": "NotAvailableForSubscription", "values": ["northeurope"],
     "restrictionInfo": {"locations": ["northeurope"], "zones": null}}]},
  {"name": "Standard_D2as_v5", "resourceType": "disks", "locations": ["swedencentral"], "restrictions": []}
]`
	var skus []azSku
	if err := json.Unmarshal([]byte(listing), &skus); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		size, loc string
		want      bool
	}{
		{"Standard_B2s", "westeurope", true},
		{"standard_b2s", "WestEurope", true},
		{"Standard_B2s", "swedencentral", false}, // listed for another region only
		{"Standard_B2ms", "swedencentral", false},
		{"Standard_B2als_v2", "swedencentral", true}, // one zone restricted: the region still works
		{"Standard_D2s_v5", "swedencentral", true},   // restriction for another region
		{"Standard_D2as_v5", "swedencentral", false}, // not a VM size
		{"Standard_B2s_v2", "swedencentral", false},  // missing
	} {
		if got := azSkuOffered(skus, tc.size, tc.loc); got != tc.want {
			t.Errorf("azSkuOffered(%s, %s) = %v, want %v", tc.size, tc.loc, got, tc.want)
		}
	}
}

// azSetConf rewrites one key of azure-vm.conf the way a user edit would.
func (e *azTestEnv) azSetConf(key, value string) {
	e.t.Helper()
	conf, err := azReadConf(e.confPath())
	if err != nil {
		e.t.Fatal(err)
	}
	conf.set(key, value)
	if err := os.WriteFile(e.confPath(), conf.bytes(), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// A disconnect run while az is logged in to another directory must not
// take az's "does not exist" (it only means "not in this directory") as a
// finished delete: it stops before any delete and keeps the secret.
func TestProviderConnectAzure_DisconnectWrongTenantKeepsSecret(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	app := e.appID()
	secret, err := os.ReadFile(e.secretPath())
	if err != nil {
		t.Fatal(err)
	}
	e.scenario("login_other_tenant")
	if code := e.run("azure", "--disconnect", "--yes"); code != 1 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, e.stdout, e.stderr)
	}
	if got, want := e.stderr.String(), azTextDisconnectWrongTenant(app, azTestTenant, azTestOtherTenant); !strings.Contains(got, want) {
		t.Fatalf("stderr:\n%s\nwant:\n%s", got, want)
	}
	if n := len(e.callsWith("ad", "app", "delete")) + len(e.callsWith("role", "assignment", "delete")); n != 0 {
		t.Fatalf("%d delete calls after the tenant mismatch", n)
	}
	if e.stateExists(filepath.Join("deleted", app)) {
		t.Fatal("app deleted in the fake")
	}
	if got, err := os.ReadFile(e.secretPath()); err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("secret file changed or removed (%v)", err)
	}
	if v := e.confValues(); v["sp_app_id"] != app || v["sp_tenant"] != azTestTenant || v[azKeySPCreated] != "true" {
		t.Fatalf("azure-vm.conf changed: %v", v)
	}
	e.run("azure", "--disconnect", "--yes", "--json")
	azWantKind(t, e, azKindWrongTenant)

	// Back in the right directory the same disconnect goes through.
	e.scenario("")
	if code := e.run("azure", "--disconnect", "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.stderr)
	}
	if !e.stateExists(filepath.Join("deleted", app)) || azExists(e.secretPath()) {
		t.Fatal("disconnect in the right directory did not finish")
	}
}

// The subscription in azure-vm.conf must be reachable from the az login,
// in the service principal's directory, before anything is deleted.
func TestProviderConnectAzure_DisconnectSubscriptionNotReachedKeepsSecret(t *testing.T) {
	const unknownSub = "99999999-9999-4999-8999-999999999999"
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	app := e.appID()
	e.azSetConf("subscription", unknownSub)
	// --force: the provider's own sandbox list fails on that subscription too.
	if code := e.run("azure", "--disconnect", "--yes", "--force", "--json"); code != 1 {
		t.Fatalf("exit %d\n%s", code, e.stdout)
	}
	ev := azWantKind(t, e, azKindWrongTenant)
	if msg, _ := ev["message"].(string); !strings.Contains(msg, unknownSub) || !strings.Contains(msg, "az login --tenant "+azTestTenant) {
		t.Fatalf("message:\n%v", ev["message"])
	}
	if n := len(e.callsWith("ad", "app", "delete")) + len(e.callsWith("role", "assignment", "delete")); n != 0 {
		t.Fatalf("%d delete calls for an unreachable subscription", n)
	}
	if !azExists(e.secretPath()) || e.confValues()["sp_app_id"] != app {
		t.Fatal("local files removed")
	}
}

// A secret connect wrote elsewhere (sp_secret_file pointing to an older
// config dir) goes too; anything that may not be connect's stays.
func TestProviderConnectAzure_DisconnectSecretFileElsewhere(t *testing.T) {
	for _, tc := range []struct {
		name   string
		file   string
		mode   os.FileMode
		link   bool
		remove bool
	}{
		{name: "connect's secret in another dir", file: "azure-sp-secret", mode: 0o600, remove: true},
		{name: "another name", file: "my-sp-secret", mode: 0o600},
		{name: "wider mode", file: "azure-sp-secret", mode: 0o644},
		{name: "symlink", file: "azure-sp-secret", mode: 0o600, link: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAzConnectEnv(t, "")
			e.mustConnect()
			other := filepath.Join(t.TempDir(), "old-config")
			if err := os.MkdirAll(other, 0o700); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(other, tc.file)
			target := p
			if tc.link {
				target = filepath.Join(other, "target")
			}
			secret, err := os.ReadFile(e.secretPath())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, secret, tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, tc.mode); err != nil {
				t.Fatal(err)
			}
			if tc.link {
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
			}
			e.azSetConf("sp_secret_file", p)
			// --force: the provider refuses some of these files for its list.
			if code := e.run("azure", "--disconnect", "--yes", "--force"); code != 0 {
				t.Fatalf("exit %d\n%s", code, e.stderr)
			}
			if azExists(e.secretPath()) {
				t.Fatal("the secret in the config dir is left")
			}
			_, err = os.Lstat(p)
			if gone := errors.Is(err, os.ErrNotExist); gone != tc.remove {
				t.Fatalf("%s removed = %v, want %v", p, gone, tc.remove)
			}
			if !tc.remove {
				if !azExists(target) {
					t.Fatal("symlink target removed")
				}
				if got := e.stderr.String(); !strings.Contains(got, fmt.Sprintf(azTextSecretElsewhereKeptFmt, p)) {
					t.Fatalf("stderr does not report the kept file:\n%s", got)
				}
			}
		})
	}
}

// sp_tenant written as a domain name: the subscription's directory decides.
// With neither sp_tenant nor subscription there is nothing to check
// against, so disconnect deletes nothing.
func TestProviderConnectAzure_DisconnectTenantFromSubscriptionOrRefuse(t *testing.T) {
	e := newAzConnectEnv(t, "")
	e.mustConnect()
	app := e.appID()
	e.azSetConf("sp_tenant", "devtesteroutlook.onmicrosoft.com")
	e.scenario("login_other_tenant")
	// --force: the fake's SP login does not take a domain as tenant.
	if code := e.run("azure", "--disconnect", "--yes", "--force", "--json"); code != 1 {
		t.Fatalf("exit %d\n%s", code, e.stdout)
	}
	if ev := azWantKind(t, e, azKindWrongTenant); ev["message"] != azTextDisconnectWrongTenant(app, azTestTenant, azTestOtherTenant) {
		t.Fatalf("message:\n%v", ev["message"])
	}
	if len(e.callsWith("ad", "app", "delete")) != 0 || !azExists(e.secretPath()) {
		t.Fatal("deleted something from the wrong directory")
	}

	conf, err := azReadConf(e.confPath())
	if err != nil {
		t.Fatal(err)
	}
	conf.remove("sp_tenant", "subscription")
	if err := os.WriteFile(e.confPath(), conf.bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	e.scenario("")
	if code := e.run("azure", "--disconnect", "--yes", "--force", "--json"); code != 1 {
		t.Fatalf("exit %d\n%s", code, e.stdout)
	}
	if ev := azWantKind(t, e, azKindBadConfig); ev["message"] != azTextDisconnectTenantUnknown(e.confPath(), app) {
		t.Fatalf("message:\n%v", ev["message"])
	}
	if len(e.callsWith("ad", "app", "delete")) != 0 || !azExists(e.secretPath()) {
		t.Fatal("deleted something without a known directory")
	}
}
