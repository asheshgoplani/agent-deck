package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
)

func TestOpenCode2ConstructorAndCapabilities(t *testing.T) {
	isolateOpenCodeConfig(t, "opencode")
	inst := NewInstanceWithGroupAndTool("v2", t.TempDir(), "group", "opencode2")
	if inst.Tool != "opencode" || inst.Command != "opencode2" || inst.GroupPath != "group" {
		t.Fatalf("constructor: tool=%q command=%q group=%q", inst.Tool, inst.Command, inst.GroupPath)
	}
	if !SupportsLaunchModel("opencode2") || !SupportsNativeFork("opencode2") || !ToolSupportsMCPManager("opencode2") || !inst.CanRestart() {
		t.Fatal("alias lost OpenCode capabilities")
	}
	if err := inst.ApplyLaunchModel("openai/gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(inst)
	if err != nil {
		t.Fatal(err)
	}
	var restored Instance
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Tool != "opencode" || restored.Command != "opencode2" || restored.GetOpenCodeOptions().Model != "openai/gpt-5.5" {
		t.Fatal("launcher/options did not survive persistence")
	}
	if got := KnownModelIDsForTool("opencode2"); !reflect.DeepEqual(got, KnownModelIDsForTool("opencode")) {
		t.Fatalf("alias model catalog = %v", got)
	}
	if GetToolIcon("opencode2") != GetToolIcon("opencode") {
		t.Fatal("alias lost OpenCode icon")
	}
}

func TestOpenCode2RegistryVisibility(t *testing.T) {
	isolateOpenCodeConfig(t, "opencode")
	previous := lookPathFn
	lookPathFn = func(name string) (string, error) {
		if name == "opencode2" {
			return "/fake/opencode2", nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPathFn = previous })
	r := InitFiltered(map[string]ToolDef{"opencode2": {Command: "wrong"}}, true, nil)
	if !r.IsBuiltin("opencode2") || r.GetCustom("opencode2") != nil || r.Match("opencode2") != "opencode" {
		t.Fatal("alias must be reserved and resolve to the canonical tool")
	}
	if !r.IsVisible("opencode2") || r.IsVisible("opencode") || r.FilterFallback() {
		t.Fatal("a v2-only installation must show the v2 launcher, not the missing v1 binary")
	}
	for _, hidden := range []string{"opencode", "opencode2"} {
		if InitFiltered(nil, true, []string{hidden}).IsVisible("opencode2") {
			t.Fatalf("hidden %q did not hide the alias", hidden)
		}
	}
	ui := UISettings{HiddenTools: []string{"opencode2"}}
	normalizeUIHiddenTools(&ui, nil)
	if !slices.Contains(ui.HiddenTools, "opencode2") {
		t.Fatal("hidden_tools normalization discarded the launcher")
	}
}

// runOpenCode2Launch executes real shell command construction against a stub
// launcher. API calls and the final TUI invocation are observed as argv, not
// merely matched as substrings in the generated command.
func runOpenCode2Launch(t *testing.T, inst *Instance, command, response string, failAPI bool) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	stub := fmt.Sprintf(`#!/bin/sh
printf 'CALL' >> %s
for arg do printf '\t%%s' "$arg" >> %s; done
printf '\n' >> %s
if [ "$1" = api ]; then
  if [ "$2" = session.create ] || [ "$2" = session.fork ] || [ "$2" = session.list ]; then
    printf '%%s\n' %s
  fi
  exit %d
fi
`, shellescape.Quote(log), shellescape.Quote(log), shellescape.Quote(log), shellescape.Quote(response), map[bool]int{false: 0, true: 1}[failAPI])
	if err := os.WriteFile(filepath.Join(bin, "opencode2"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = inst.ProjectPath
	output, err := cmd.CombinedOutput()
	if failAPI && err == nil {
		t.Fatal("failed API call unexpectedly launched the TUI")
	} else if !failAPI && err != nil {
		t.Fatalf("launch: %v\n%s\ncommand: %s", err, output, command)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(calls)
}

func TestOpenCode2FreshLaunchAppliesOptions(t *testing.T) {
	isolateOpenCodeConfig(t, "opencode")
	inst := NewInstanceWithTool(`title "quoted"`, t.TempDir(), "opencode2")
	if err := inst.SetOpenCodeOptions(&OpenCodeOptions{Model: "openai/gpt-5.5#high", Agent: "build"}); err != nil {
		t.Fatal(err)
	}
	command := inst.buildOpenCodeCommand(inst.Command)
	calls := runOpenCode2Launch(t, inst, command, `{"data":{"id":"ses_child","model":{"id":"gpt-5.5"}}}`, false)
	for _, want := range []string{"api\tsession.create", `"providerID":"openai"`, `"id":"gpt-5.5"`, `"variant":"high"`, `"agent":"build"`, "CALL\t-s\tses_child"} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls missing %q:\n%s", want, calls)
		}
	}
	for _, rejected := range []string{"\t-m\t", "\t--agent\t", "\t--port\t"} {
		if strings.Contains(calls, rejected) {
			t.Errorf("rejected TUI flag %q in calls: %s", rejected, calls)
		}
	}
}

func TestOpenCode2ResumeAppliesOptionsWithoutCreatingSession(t *testing.T) {
	isolateOpenCodeConfig(t, "opencode")
	inst := NewInstanceWithTool("resume", t.TempDir(), "opencode2")
	inst.OpenCodeSessionID = "ses_existing"
	if err := inst.SetOpenCodeOptions(&OpenCodeOptions{Model: "openai/gpt-5.5", Agent: "plan"}); err != nil {
		t.Fatal(err)
	}
	calls := runOpenCode2Launch(t, inst, inst.buildOpenCodeCommand(inst.Command), "", false)
	for _, want := range []string{"session.switchAgent", `{"agent":"plan"}`, "session.switchModel", "sessionID=ses_existing", "CALL\t-s\tses_existing"} {
		if !strings.Contains(calls, want) {
			t.Errorf("resume calls missing %q: %s", want, calls)
		}
	}
	if strings.Contains(calls, "session.create") || strings.Contains(calls, "session.fork") {
		t.Fatal("resume created another session")
	}
}

func TestOpenCode2ContinueAppliesOptionsToLatestSession(t *testing.T) {
	isolateOpenCodeConfig(t, "opencode")
	inst := NewInstanceWithTool("continue", t.TempDir(), "opencode2")
	if err := inst.SetOpenCodeOptions(&OpenCodeOptions{SessionMode: "continue", Model: "openai/gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	calls := runOpenCode2Launch(t, inst, inst.buildOpenCodeCommand(inst.Command), `{"data":[{"id":"ses_latest"}],"cursor":{}}`, false)
	for _, want := range []string{"session.list", "parentID=null", "limit=1", "session.switchModel", "CALL\t-s\tses_latest"} {
		if !strings.Contains(calls, want) {
			t.Errorf("continue calls missing %q: %s", want, calls)
		}
	}
	if strings.Contains(calls, "session.create") {
		t.Fatal("continue created a session instead of retaining the last conversation")
	}
}

func TestOpenCode2NativeMCPConfigPreservesOptions(t *testing.T) {
	isolateOpenCodeConfig(t, "opencode")
	project := t.TempDir()
	file := filepath.Join(project, "opencode.json")
	payload := `{"model":"openai/gpt-5.5","mcp":{"timeout":{"startup":45000},"servers":{"docs":{"type":"remote","url":"https://example.invalid/mcp"}}}}`
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearAllOpenCodeMCPInfoCache()
	t.Cleanup(ClearAllOpenCodeMCPInfoCache)
	info := GetOpenCodeMCPInfo(project)
	if len(info.LocalMCPs) != 1 || info.LocalMCPs[0].Name != "docs" {
		t.Fatalf("native MCP info: %+v", info)
	}
	inst := NewInstanceWithTool("mcp", project, "opencode2")
	if err := inst.WriteLocalMCPConfig(nil); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(out, &config); err != nil {
		t.Fatal(err)
	}
	mcp := config["mcp"].(map[string]any)
	if config["model"] != "openai/gpt-5.5" || mcp["timeout"] == nil || mcp["servers"] == nil {
		t.Fatalf("MCP write discarded native options: %s", out)
	}
	if MCPLocalConfigPathForTool("opencode2", project) != file {
		t.Fatal("alias has no local MCP path")
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if GetOpenCodeConfigDir() != filepath.Join(xdg, "opencode") {
		t.Fatal("OpenCode config ignores XDG_CONFIG_HOME")
	}
}

func TestOpenCode2ForkAppliesOptionsAndStopsOnFailure(t *testing.T) {
	for _, failAPI := range []bool{false, true} {
		t.Run(fmt.Sprint(failAPI), func(t *testing.T) {
			isolateOpenCodeConfig(t, "opencode")
			parent := NewInstanceWithTool("parent", t.TempDir(), "opencode2")
			parent.OpenCodeSessionID = "ses_parent"
			parent.OpenCodeDetectedAt = time.Now()
			workDir := t.TempDir()
			child, command, err := parent.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir("child", "", &OpenCodeOptions{Model: "openai/gpt-5.5", Agent: "plan"}, workDir, parent.ProjectPath, "fork/child")
			if err != nil {
				t.Fatal(err)
			}
			calls := runOpenCode2Launch(t, child, command, `{"data":{"id":"ses_child"}}`, failAPI)
			if failAPI {
				if strings.Contains(calls, "CALL\t-s") {
					t.Fatal("API failure still launched a TUI")
				}
				return
			}
			for _, want := range []string{"session.fork", "session.move", "session.update", "session.switchAgent", "session.switchModel", "CALL\t-s\tses_child"} {
				if !strings.Contains(calls, want) {
					t.Errorf("fork calls missing %q: %s", want, calls)
				}
			}
			if child.Command != "opencode2" || child.GetOpenCodeOptions().Model != "openai/gpt-5.5" {
				t.Fatal("fork lost persistent launcher/options")
			}
		})
	}
}

func TestOpenCode2DiscoveryUsesSelectedLauncherAndNestedAPITimes(t *testing.T) {
	isolateOpenCodeConfig(t, "opencode")
	project := t.TempDir()
	bin := t.TempDir()
	marker := filepath.Join(bin, "args")
	response := fmt.Sprintf(`{"data":[{"id":"ses_v2","parentID":"ses_parent","location":{"directory":%q},"time":{"created":1000,"updated":2000}}],"cursor":{}}`, project)
	stub := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nprintf '%%s\\n' %s\n", shellescape.Quote(marker), shellescape.Quote(response))
	if err := os.WriteFile(filepath.Join(bin, "opencode2"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	inst := NewInstanceWithTool("discovery", project, "opencode2")
	if got := inst.queryOpenCodeSession(); got != "ses_v2" {
		t.Fatalf("v2 session binding = %q", got)
	}
	args, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(args), "session.list") || !strings.Contains(string(args), "directory="+project) {
		t.Fatalf("API args: %s, error %v", args, err)
	}
	// The same directory queried through v1 must not receive v2's cached IDs.
	legacy := NewInstanceWithTool("legacy", project, "opencode")
	if got := legacy.queryOpenCodeSession(); got != "" {
		t.Fatalf("v1 received v2 cached binding: %q", got)
	}
}

func TestOpenCode2ConfiguredLauncherKeepsPathAndEnvironment(t *testing.T) {
	bin := t.TempDir()
	path := filepath.Join(bin, "opencode2")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' \"$TEST_OC_ENV\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	isolateOpenCodeConfig(t, "TEST_OC_ENV=ok "+shellescape.Quote(path))
	inst := NewInstanceWithTool("configured", t.TempDir(), "opencode")
	if !inst.openCodeUsesV2CLI() || inst.OpenCodeCommandName() != "opencode2" {
		t.Fatal("quoted/environment-wrapped v2 launcher was not recognized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd, err := inst.openCodeCLICommand(ctx, inst.ProjectPath, "api", "session.list")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil || string(out) != "ok" {
		t.Fatalf("configured environment = %q, error %v", out, err)
	}
	if got, ok := inst.openCodeLaunchBinary(); !ok || got != path {
		t.Fatalf("probe binary = %q, %v", got, ok)
	}
}
