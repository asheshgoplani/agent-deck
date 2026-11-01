package provider

// The example azure-vm provider (examples/providers/azure-vm) runs for real
// against a fake az, ssh and curl on PATH (internal/provider/testdata/
// fake-az*.sh). Nothing reaches Azure. The fakes record every argv, so the
// tests check that no secret is ever in one, that every resource is tagged,
// and that list/status/destroy touch only tagged resources.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const (
	azFakeSecret = "FAKE-sp-secret-0123456789abcdefXYZ"
	azFakeToken  = "fake-oauth-AZFAKEtokenForTests_0123456789abcdefABCDEF"
	azAppID      = "11111111-2222-3333-4444-555555555555"
	azTenant     = "66666666-7777-8888-9999-000000000000"
	azRG         = "agent-deck-sandboxes"
)

// azWorld is one test's fake Azure: a bin dir with az/ssh/curl, the fake
// state dir, the provider config file and the provider's local state dir.
type azWorld struct {
	root, bin, state, conf, local, tmp string
	extraEnv                           []string
}

func azProviderPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "examples", "providers", "azure-vm", "provider"))
	if err != nil {
		t.Fatal(err)
	}
	// No skip when it is missing: the example is part of this repo.
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("example azure-vm provider missing: %v", err)
	}
	return p
}

func azNeedTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "python3", "ssh-keygen", "od", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
}

func newAzWorld(t *testing.T, conf string) *azWorld {
	t.Helper()
	azNeedTools(t)
	root := t.TempDir()
	w := &azWorld{root: root, bin: filepath.Join(root, "bin"), state: filepath.Join(root, "fake"),
		local: filepath.Join(root, "local-state"), tmp: filepath.Join(root, "tmp"),
		conf: filepath.Join(root, "azure-vm.conf")}
	for _, d := range []string{w.bin, w.state, w.tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, src := range map[string]string{"az": "fake-az.sh", "ssh": "fake-az-ssh.sh", "curl": "fake-az-curl.sh"} {
		abs, err := filepath.Abs(filepath.Join("testdata", src))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(abs, filepath.Join(w.bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(w.conf, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *azWorld) env() []string {
	return append([]string{
		"PATH=" + w.bin + ":" + os.Getenv("PATH"),
		"HOME=" + w.root,
		"TMPDIR=" + w.tmp,
		"FAKE_AZ_STATE=" + w.state,
		"AGENTDECK_AZURE_CONFIG=" + w.conf,
		"AGENTDECK_AZURE_STATE_DIR=" + w.local,
	}, w.extraEnv...)
}

func (w *azWorld) run(t *testing.T, action, request string) (stdout, stderr string, err error) {
	t.Helper()
	// #nosec G204 -- the example provider script from this repo.
	cmd := exec.Command(azProviderPath(t), action)
	cmd.Env = w.env()
	cmd.Stdin = strings.NewReader(request)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

func (w *azWorld) file(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.state, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

type azResource struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Type string            `json:"type"`
	RG   string            `json:"resourceGroup"`
	Tags map[string]string `json:"tags"`
}

func (w *azWorld) resources(t *testing.T) []azResource {
	t.Helper()
	raw := w.file(t, "resources.json")
	if raw == "" {
		return nil
	}
	var rs []azResource
	if err := json.Unmarshal([]byte(raw), &rs); err != nil {
		t.Fatal(err)
	}
	return rs
}

func (w *azWorld) seed(t *testing.T, rs []azResource) {
	t.Helper()
	b, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.state, "resources.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func azSecretFile(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

const azCreateReq = `{"protocol":1,"action":"create","name":"az1","labels":{"agent-deck/remote":"az1","agent-deck/profile":"work","agent-deck/create-id":"c0ffee12"}}`

func azBaseConf(extra string) string {
	return "resource_group = " + azRG + "\nssh_source = 100.64.0.0/10\n" + extra
}

func azCreated(t *testing.T, stdout string) Sandbox {
	t.Helper()
	var resp struct {
		Protocol int     `json:"protocol"`
		Sandbox  Sandbox `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("provider stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if resp.Protocol != 1 {
		t.Fatalf("protocol = %d, want 1", resp.Protocol)
	}
	if err := resp.Sandbox.Validate(); err != nil {
		t.Fatalf("sandbox fails agent-deck validation: %v\n%s", err, stdout)
	}
	return resp.Sandbox
}

func TestAzureExampleProvider_CreateTagsEveryResourceAndKeepsSecretsOutOfArgv(t *testing.T) {
	sp := azSecretFile(t, azFakeSecret+"\n", 0o600)
	tok := azSecretFile(t, azFakeToken+"\n", 0o600)
	w := newAzWorld(t, azBaseConf("sp_app_id = "+azAppID+"\nsp_tenant = "+azTenant+
		"\nsp_secret_file = "+sp+"\nclaude_token_file = "+tok+"\nproject_name = shop\n"))
	stdout, stderr, err := w.run(t, "create", azCreateReq)
	if err != nil {
		t.Fatalf("create failed: %v\nstderr:\n%s", err, stderr)
	}
	sb := azCreated(t, stdout)

	// No secret in any argv, output or cloud-init custom-data.
	for name, text := range map[string]string{
		"az argv": w.file(t, "argv.log"), "ssh argv": w.file(t, "ssh.log"),
		"provider stdout": stdout, "provider stderr": stderr, "cloud-init custom-data": w.file(t, "custom-data"),
	} {
		if strings.Contains(text, "FAKE-sp-secret") || strings.Contains(text, "AZFAKEtoken") {
			t.Fatalf("a secret leaked into the %s", name)
		}
	}
	// The SP secret reached az login on stdin, in a private config dir.
	if strings.TrimSpace(w.file(t, "login.stdin")) != azFakeSecret {
		t.Fatal("az login did not get the service principal secret on stdin (not shown)")
	}
	if !strings.Contains(w.file(t, "argv.log"), "login --service-principal --username "+azAppID+" --tenant "+azTenant+" --password @-") {
		t.Fatalf("az login argv is not the stdin form:\n%s", w.file(t, "argv.log"))
	}
	for _, line := range strings.Split(strings.TrimSpace(w.file(t, "env.log")), "\n") {
		dir := strings.TrimPrefix(line, "AZURE_CONFIG_DIR=")
		if dir == "" || !strings.HasPrefix(dir, w.tmp+string(os.PathSeparator)) {
			t.Fatalf("az ran with AZURE_CONFIG_DIR %q, want a private dir under TMPDIR", dir)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("private az config dir %s (holds the login) was not removed", dir)
		}
	}
	// The Claude token went to the setup script over ssh stdin.
	sshIn := w.file(t, "ssh.stdin")
	if !strings.Contains(sshIn, "claude_token="+azFakeToken) || !strings.Contains(sshIn, "project_name=shop") {
		t.Fatal("setup over ssh did not get the token and project on stdin (not shown)")
	}
	if !strings.Contains(w.file(t, "ssh.log"), "/usr/local/bin/agent-deck-sandbox-setup") {
		t.Fatalf("setup script was not run over ssh:\n%s", w.file(t, "ssh.log"))
	}

	// Every resource carries our tags, including the OS disk.
	rs := w.resources(t)
	types := map[string]bool{}
	for _, r := range rs {
		types[r.Type] = true
		want := map[string]string{"agent-deck-provider": "azure-vm", "agent-deck-sandbox": sb.ID,
			"agent-deck-remote": "az1", "agent-deck-profile": "work", "agent-deck-create-id": "c0ffee12"}
		for k, v := range want {
			if r.Tags[k] != v {
				t.Fatalf("%s %s tag %s = %q, want %q (tags %v)", r.Type, r.Name, k, r.Tags[k], v, r.Tags)
			}
		}
	}
	for _, typ := range []string{"Microsoft.Compute/virtualMachines", "Microsoft.Compute/disks",
		"Microsoft.Network/networkInterfaces", "Microsoft.Network/publicIPAddresses",
		"Microsoft.Network/networkSecurityGroups", "Microsoft.Network/virtualNetworks"} {
		if !types[typ] {
			t.Fatalf("no %s was made: %+v", typ, rs)
		}
	}
	// ssh only from the configured CIDR.
	if got := strings.TrimSpace(w.file(t, "rules.log")); got != "100.64.0.0/10" {
		t.Fatalf("nsg ssh rule source = %q, want 100.64.0.0/10", got)
	}
	// The sandbox: VM's public IP, a 0600 key in a private dir, pinned host key.
	if sb.Host != "192.0.2.10" || sb.User != "dev" || sb.Auth.Type != "key" {
		t.Fatalf("sandbox = %+v", sb)
	}
	if !strings.HasPrefix(sb.Auth.KeyPath, w.local+string(os.PathSeparator)+sb.ID+".") {
		t.Fatalf("key %s not in a per-sandbox dir under %s", sb.Auth.KeyPath, w.local)
	}
	if len(sb.HostKeys) != 1 || !strings.HasPrefix(sb.HostKeys[0], "ssh-ed25519 AAAA") {
		t.Fatalf("host key not returned from run-command: %v", sb.HostKeys)
	}
	if sb.Labels["agent-deck/create-id"] != "c0ffee12" || sb.Labels["agent-deck/remote"] != "az1" {
		t.Fatalf("labels = %v, want the request's labels back", sb.Labels)
	}
	// cloud-init installs and holds the setup script, and no secret.
	cd := w.file(t, "custom-data")
	for _, want := range []string{"#cloud-config", "/usr/local/bin/agent-deck-sandbox-setup", "sandbox-init.sh, dev, stable"} {
		if !strings.Contains(cd, want) {
			t.Fatalf("custom-data lacks %q:\n%s", want, cd)
		}
	}
}

func TestAzureExampleProvider_ListAndStatusSeeOnlyTaggedVMs(t *testing.T) {
	w := newAzWorld(t, azBaseConf(""))
	stdout, stderr, err := w.run(t, "create", azCreateReq)
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, stderr)
	}
	sb := azCreated(t, stdout)
	rs := w.resources(t)
	vmPrefix := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/" + azRG + "/providers/Microsoft.Compute/virtualMachines/"
	rs = append(rs,
		azResource{ID: vmPrefix + "adk-00000000aa", Name: "adk-00000000aa", Type: "Microsoft.Compute/virtualMachines", RG: azRG, Tags: map[string]string{}},
		azResource{ID: vmPrefix + "someone-else", Name: "someone-else", Type: "Microsoft.Compute/virtualMachines", RG: azRG, Tags: map[string]string{"agent-deck-provider": "other"}},
	)
	w.seed(t, rs)

	stdout, stderr, err = w.run(t, "list", `{"protocol":1,"action":"list"}`)
	if err != nil {
		t.Fatalf("list failed: %v\n%s", err, stderr)
	}
	var list struct {
		Sandboxes []Sandbox `json:"sandboxes"`
	}
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sandboxes) != 1 || list.Sandboxes[0].ID != sb.ID {
		t.Fatalf("list = %+v, want only %s", list.Sandboxes, sb.ID)
	}
	if err := list.Sandboxes[0].Validate(); err != nil {
		t.Fatalf("listed sandbox fails validation: %v", err)
	}
	if !strings.Contains(w.file(t, "argv.log"), "vm list --show-details --resource-group "+azRG) {
		t.Fatalf("list did not stay in the configured resource group:\n%s", w.file(t, "argv.log"))
	}

	if _, _, err := w.run(t, "status", `{"protocol":1,"action":"status","id":"`+sb.ID+`"}`); err != nil {
		t.Fatalf("status of our VM failed: %v", err)
	}
	_, stderr, err = w.run(t, "status", `{"protocol":1,"action":"status","id":"adk-00000000aa"}`)
	if err == nil || !strings.Contains(stderr, "missing tags") {
		t.Fatalf("status of an untagged VM: err %v, stderr %s; want a refusal", err, stderr)
	}
}

func TestAzureExampleProvider_DestroyDeletesEveryResourceAfterCheckingTags(t *testing.T) {
	w := newAzWorld(t, azBaseConf(""))
	stdout, stderr, err := w.run(t, "create", azCreateReq)
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, stderr)
	}
	sb := azCreated(t, stdout)
	keyDir := filepath.Dir(sb.Auth.KeyPath)
	if err := os.WriteFile(filepath.Join(w.state, "argv.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err = w.run(t, "destroy", `{"protocol":1,"action":"destroy","id":"`+sb.ID+`"}`)
	if err != nil || strings.TrimSpace(stdout) != `{"protocol":1,"ok":true}` {
		t.Fatalf("destroy: %v %q\n%s", err, stdout, stderr)
	}
	if rs := w.resources(t); len(rs) != 0 {
		t.Fatalf("resources left after destroy: %+v", rs)
	}
	if _, err := os.Stat(keyDir); !os.IsNotExist(err) {
		t.Fatalf("local key dir %s not removed", keyDir)
	}
	// Each delete comes right after a show of the same resource, VM first.
	lines := strings.Split(strings.TrimSpace(w.file(t, "argv.log")), "\n")
	deletes := 0
	for i, l := range lines {
		if !strings.HasPrefix(l, "resource delete --ids ") {
			continue
		}
		id := strings.Fields(l)[3]
		if i == 0 || !strings.HasPrefix(lines[i-1], "resource show --ids "+id+" ") {
			t.Fatalf("delete of %s not preceded by a tag check:\n%s", id, strings.Join(lines, "\n"))
		}
		if deletes == 0 && !strings.Contains(id, "/virtualMachines/") {
			t.Fatalf("first delete is %s, want the VM", id)
		}
		deletes++
	}
	if deletes == 0 {
		t.Fatalf("no deletes:\n%s", strings.Join(lines, "\n"))
	}

	// Destroy is idempotent.
	if _, stderr, err := w.run(t, "destroy", `{"protocol":1,"action":"destroy","id":"`+sb.ID+`"}`); err != nil {
		t.Fatalf("second destroy failed: %v\n%s", err, stderr)
	}
}

// TestAzureExampleProvider_DestroyRechecksTagsBeforeEachDelete: a resource
// that loses its tags between the listing and its delete is left alone.
func TestAzureExampleProvider_DestroyRechecksTagsBeforeEachDelete(t *testing.T) {
	w := newAzWorld(t, azBaseConf(""))
	stdout, stderr, err := w.run(t, "create", azCreateReq)
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, stderr)
	}
	sb := azCreated(t, stdout)
	w.extraEnv = []string{"FAKE_AZ_UNTAG_AFTER_LIST=" + sb.ID + "-ip"}
	_, stderr, err = w.run(t, "destroy", `{"protocol":1,"action":"destroy","id":"`+sb.ID+`"}`)
	if err == nil || !strings.Contains(stderr, "no longer carries") {
		t.Fatalf("destroy deleted past a resource that lost its tags: err %v\n%s", err, stderr)
	}
	for _, r := range w.resources(t) {
		if r.Name == sb.ID+"-ip" {
			return
		}
	}
	t.Fatal("the public IP that lost its tags was deleted")
}

func TestAzureExampleProvider_DestroyRefusesAnUntaggedVM(t *testing.T) {
	w := newAzWorld(t, azBaseConf(""))
	prefix := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/" + azRG + "/providers/"
	w.seed(t, []azResource{
		{ID: prefix + "Microsoft.Compute/virtualMachines/adk-00000000bb", Name: "adk-00000000bb", Type: "Microsoft.Compute/virtualMachines", RG: azRG, Tags: map[string]string{"owner": "someone"}},
		{ID: prefix + "Microsoft.Network/publicIPAddresses/adk-00000000bb-ip", Name: "adk-00000000bb-ip", Type: "Microsoft.Network/publicIPAddresses", RG: azRG,
			Tags: map[string]string{"agent-deck-provider": "azure-vm", "agent-deck-sandbox": "adk-00000000bb"}},
	})
	_, stderr, err := w.run(t, "destroy", `{"protocol":1,"action":"destroy","id":"adk-00000000bb"}`)
	if err == nil || !strings.Contains(stderr, "refusing") {
		t.Fatalf("destroy of an untagged VM: err %v, stderr %s; want a refusal", err, stderr)
	}
	if strings.Contains(w.file(t, "argv.log"), "resource delete") {
		t.Fatalf("something was deleted:\n%s", w.file(t, "argv.log"))
	}
	if len(w.resources(t)) != 2 {
		t.Fatal("resources changed")
	}
	// An id that is not ours never reaches az.
	_, stderr, err = w.run(t, "destroy", `{"protocol":1,"action":"destroy","id":"prod-db-1"}`)
	if err == nil || !strings.Contains(stderr, "not an azure-vm sandbox id") {
		t.Fatalf("destroy of a foreign id: err %v, stderr %s", err, stderr)
	}
}

func TestAzureExampleProvider_RollsBackWhenCreateFails(t *testing.T) {
	cases := []struct{ name, env, want string }{
		{"vm create fails", "FAKE_AZ_FAIL=vm create", "creating the VM failed"},
		{"cloud-init never ready", "FAKE_AZ_READY=no", "cloud-init did not finish"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newAzWorld(t, azBaseConf(""))
			w.extraEnv = []string{tc.env}
			_, stderr, err := w.run(t, "create", azCreateReq)
			if err == nil {
				t.Fatal("create succeeded")
			}
			if !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "rollback done") {
				t.Fatalf("stderr does not say what failed and that it rolled back:\n%s", stderr)
			}
			if rs := w.resources(t); len(rs) != 0 {
				t.Fatalf("resources left after a failed create: %+v", rs)
			}
			if !strings.Contains(w.file(t, "argv.log"), "network nsg create") {
				t.Fatal("nothing was created, so rollback was not exercised")
			}
			left, _ := filepath.Glob(filepath.Join(w.local, "adk-*"))
			if len(left) != 0 {
				t.Fatalf("local key dirs left: %v", left)
			}
		})
	}
}

func TestAzureExampleProvider_RefusesSSHOpenToTheInternet(t *testing.T) {
	for _, src := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/4", "*", "Internet"} {
		t.Run(src, func(t *testing.T) {
			w := newAzWorld(t, "resource_group = "+azRG+"\nssh_source = "+src+"\n")
			_, stderr, err := w.run(t, "create", azCreateReq)
			if err == nil {
				t.Fatalf("create accepted ssh_source %s", src)
			}
			if !strings.Contains(stderr, "ssh_source") {
				t.Fatalf("stderr does not name ssh_source: %s", stderr)
			}
			if w.file(t, "argv.log") != "" {
				t.Fatalf("az was called:\n%s", w.file(t, "argv.log"))
			}
		})
	}
}

func TestAzureExampleProvider_AutoSSHSourceIsYourIPAndYourAzLogin(t *testing.T) {
	w := newAzWorld(t, "resource_group = "+azRG+"\n")
	w.extraEnv = []string{"FAKE_PUBLIC_IP=203.0.113.7"}
	stdout, stderr, err := w.run(t, "create", azCreateReq)
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, stderr)
	}
	azCreated(t, stdout)
	if got := strings.TrimSpace(w.file(t, "rules.log")); got != "203.0.113.7/32" {
		t.Fatalf("nsg ssh rule source = %q, want 203.0.113.7/32", got)
	}
	// Without sp_app_id: no login, and az uses your own config dir.
	if strings.Contains(w.file(t, "argv.log"), "login") {
		t.Fatal("az login was run without a service principal configured")
	}
	if strings.Contains(w.file(t, "env.log"), w.tmp) {
		t.Fatal("AZURE_CONFIG_DIR was redirected without a service principal configured")
	}
}

func TestAzureExampleProvider_RefusesAnUnsafeSecretFile(t *testing.T) {
	sp := azSecretFile(t, azFakeSecret+"\n", 0o644)
	w := newAzWorld(t, azBaseConf("sp_app_id = "+azAppID+"\nsp_tenant = "+azTenant+"\nsp_secret_file = "+sp+"\n"))
	_, stderr, err := w.run(t, "create", azCreateReq)
	if err == nil || !strings.Contains(stderr, "must be 0600 or 0400") {
		t.Fatalf("create with a 0644 secret file: err %v, stderr %s", err, stderr)
	}
	if strings.Contains(stderr, "FAKE-sp-secret") || strings.Contains(w.file(t, "argv.log"), "create") {
		t.Fatal("secret shown or resources made although the secret file was refused")
	}
}

// TestAzureExampleProvider_ProvisionAndDestroyRemote drives the example
// through agent-deck's own client: Provision validates the reply and writes
// the sandbox's ssh files, DestroyRemote removes everything.
func TestAzureExampleProvider_ProvisionAndDestroyRemote(t *testing.T) {
	w := newAzWorld(t, azBaseConf(""))
	freshHome(t)
	for _, kv := range w.env() {
		k, v, _ := strings.Cut(kv, "=")
		if k != "HOME" {
			t.Setenv(k, v)
		}
	}
	if err := session.SaveUserConfig(&session.UserConfig{Providers: map[string]session.ProviderConfig{
		"azure": {Command: azProviderPath(t), TimeoutSeconds: 30, CreateTimeoutSeconds: 60},
	}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, err := Provision(ctx, ProvisionOptions{Provider: "azure", RemoteName: "az-e2e", Profile: "_test",
		WaitSSH: func(context.Context, string, session.RemoteConfig) error { return nil },
		Install: func(context.Context, string, session.RemoteConfig) error { return nil },
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !IsSandbox(p.Remote) || p.Sandbox.Host != "192.0.2.10" {
		t.Fatalf("provisioned = %+v", p)
	}
	if len(w.resources(t)) == 0 {
		t.Fatal("no resources after Provision")
	}
	if err := DestroyRemote(ctx, p.RemoteName); err != nil {
		t.Fatalf("DestroyRemote: %v", err)
	}
	if rs := w.resources(t); len(rs) != 0 {
		t.Fatalf("resources left after DestroyRemote: %+v", rs)
	}
}

// TestAzureExampleSetup_ReadyProjectAndLogin runs the VM-side setup script
// against a temp HOME: demo project as a git repo, onboarding done, token
// in a 0600 env file agent-deck sources.
func TestAzureExampleSetup_ReadyProjectAndLogin(t *testing.T) {
	azNeedTools(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	home := t.TempDir()
	script, err := filepath.Abs(filepath.Join("..", "..", "examples", "providers", "azure-vm", "sandbox-setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// The script copies the demo from /opt; give it a copied-in project instead.
	if err := os.MkdirAll(filepath.Join(home, "shop"), 0o700); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- the example setup script from this repo.
	cmd := exec.Command(script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	cmd.Stdin = strings.NewReader("project_name=shop\nproject_copied=1\nclaude_token=" + azFakeToken + "\n")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("setup failed: %v\n%s", err, errb.String())
	}
	if strings.Contains(errb.String(), "AZFAKEtoken") {
		t.Fatal("setup printed the token")
	}
	envFile := filepath.Join(home, ".config", "agent-deck-sandbox", "claude.env")
	st, err := os.Stat(envFile)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("claude.env: %v mode %v, want 0600", err, st)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !strings.Contains(string(raw), `"hasCompletedOnboarding":true`) || !strings.Contains(string(raw), filepath.Join(home, "shop")) {
		t.Fatalf(".claude.json does not skip onboarding and trust the project: %s", raw)
	}
}
