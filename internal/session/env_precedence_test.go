package session

import (
	"strings"
	"testing"
)

func withTestToolConfig(t *testing.T, toolEnv map[string]string) {
	t.Helper()
	userConfigCacheMu.Lock()
	orig := userConfigCache
	userConfigCache = &UserConfig{
		Tools: map[string]ToolDef{"testtool": {Env: toolEnv}},
		MCPs:  make(map[string]MCPDef),
	}
	userConfigCacheMu.Unlock()
	t.Cleanup(func() {
		userConfigCacheMu.Lock()
		userConfigCache = orig
		userConfigCacheMu.Unlock()
	})
}

// TestBuildEnvSourceCommand_PerSessionEnvWinsOverConfig: a per-session env value
// must win over a colliding config-level value ([tools.X].env / env_file). The
// per-session export is emitted AFTER the config env in the same shell, so the
// later assignment wins.
func TestBuildEnvSourceCommand_PerSessionEnvWinsOverConfig(t *testing.T) {
	withTestToolConfig(t, map[string]string{"KEY": "fromconfig"})
	inst := &Instance{Tool: "testtool", Env: []string{"KEY=fromsession"}}

	out := inst.buildEnvSourceCommand()
	cfgIdx := strings.Index(out, "export KEY='fromconfig'")
	sessIdx := strings.Index(out, "export KEY='fromsession'")
	if cfgIdx < 0 || sessIdx < 0 {
		t.Fatalf("both config and session exports must be present: %q", out)
	}
	if sessIdx < cfgIdx {
		t.Fatalf("per-session export must come AFTER config export so it wins; got: %q", out)
	}
}

// TestBuildEnvSourceCommand_SkipsPerSessionEnvForSSHRemotePath: per-session env
// must NOT be injected here for remote-path SSH sessions (remote re-quoting
// mangles inline exports), matching prepareCommand's skip.
func TestBuildEnvSourceCommand_SkipsPerSessionEnvForSSHRemotePath(t *testing.T) {
	withTestToolConfig(t, nil)
	inst := &Instance{Tool: "testtool", Env: []string{"KEY=fromsession"}, SSHRemotePath: "/remote/path"}

	if out := inst.buildEnvSourceCommand(); strings.Contains(out, "fromsession") {
		t.Fatalf("per-session env must be skipped for SSHRemotePath sessions: %q", out)
	}
}
