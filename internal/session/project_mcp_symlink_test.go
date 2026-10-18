package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project directory may be a cloned repository, so its MCP config files may
// be symlinks the user never made. Writing through a link that leaves the
// project would overwrite an unrelated file (e.g. ~/.claude.json) with MCP
// definitions, env values included. These writers must refuse that and leave
// the outside file as it was; a link that stays inside the project still works.

const outsideSeed = `{"keep":"me"}`

func symlinkProjectFixture(t *testing.T, link string) (proj, outside string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	proj = filepath.Join(tmp, "proj")
	outside = filepath.Join(tmp, "victim.json")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(proj, link)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(outsideSeed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(proj, link)); err != nil {
		t.Fatal(err)
	}
	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {Command: "echo", Args: []string{"meow"}, Env: map[string]string{"TOKEN": "secret"}},
	}}
	t.Cleanup(resetUserConfigCache(t, cfg))
	return proj, outside
}

func assertOutsideUntouched(t *testing.T, outside string) {
	t.Helper()
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != outsideSeed {
		t.Fatalf("file outside the project was rewritten: %s", got)
	}
}

func TestWriteMCPJsonFromConfig_RefusesSymlinkOutsideProject(t *testing.T) {
	proj, outside := symlinkProjectFixture(t, ".mcp.json")
	if err := WriteMCPJsonFromConfig(proj, []string{"cat"}); err == nil {
		t.Error("expected an error for a .mcp.json link that leaves the project")
	}
	assertOutsideUntouched(t, outside)
}

func TestWriteCursorProjectMCP_RefusesSymlinkOutsideProject(t *testing.T) {
	proj, outside := symlinkProjectFixture(t, filepath.Join(".cursor", "mcp.json"))
	if err := WriteCursorProjectMCP(proj, []string{"cat"}); err == nil {
		t.Error("expected an error for a .cursor/mcp.json link that leaves the project")
	}
	assertOutsideUntouched(t, outside)
}

func TestWriteCursorProjectMCP_RefusesSymlinkedDirOutsideProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	proj := filepath.Join(tmp, "proj")
	outsideDir := filepath.Join(tmp, "elsewhere")
	for _, d := range []string{proj, outsideDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outsideDir, filepath.Join(proj, ".cursor")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetUserConfigCache(t, &UserConfig{MCPs: map[string]MCPDef{"cat": {Command: "echo"}}}))

	if err := WriteCursorProjectMCP(proj, []string{"cat"}); err == nil {
		t.Error("expected an error for a .cursor directory link that leaves the project")
	}
	for _, name := range []string{"mcp.json", "mcp.json.lock"} {
		if _, err := os.Lstat(filepath.Join(outsideDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s was created outside the project (stat err %v)", name, err)
		}
	}
}

// The lock beside .mcp.json is opened with O_CREATE; a planted lock link must
// not create a file outside the project either.
func TestWriteMCPJsonFromConfig_RefusesLockSymlinkOutsideProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(tmp, "planted")
	if err := os.Symlink(planted, filepath.Join(proj, ".mcp.json.lock")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetUserConfigCache(t, &UserConfig{MCPs: map[string]MCPDef{"cat": {Command: "echo"}}}))

	if err := WriteMCPJsonFromConfig(proj, []string{"cat"}); err == nil {
		t.Error("expected an error for a .mcp.json.lock link that leaves the project")
	}
	if _, err := os.Lstat(planted); !os.IsNotExist(err) {
		t.Fatalf("lock link target created outside the project (stat err %v)", err)
	}
}

func TestWriteOpenCodeProjectMCP_RefusesSymlinkOutsideProject(t *testing.T) {
	proj, outside := symlinkProjectFixture(t, "opencode.json")
	opencodeMCPConfigDirOverride = filepath.Dir(proj)
	defer func() { opencodeMCPConfigDirOverride = "" }()
	if err := WriteOpenCodeProjectMCP(proj, []string{"cat"}); err == nil {
		t.Error("expected an error for an opencode.json link that leaves the project")
	}
	assertOutsideUntouched(t, outside)
}

func TestWriteMCPJsonFromConfig_SymlinkInsideProjectStillWritten(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(proj, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(proj, "config", "mcp.json")
	if err := os.WriteFile(real, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("config", "mcp.json"), filepath.Join(proj, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetUserConfigCache(t, &UserConfig{MCPs: map[string]MCPDef{"cat": {Command: "echo"}}}))

	if err := WriteMCPJsonFromConfig(proj, []string{"cat"}); err != nil {
		t.Fatalf("in-project link: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(proj, ".mcp.json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected .mcp.json to stay a symlink (err %v)", err)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"cat"`) {
		t.Fatalf("link target not updated: %s", data)
	}
}
