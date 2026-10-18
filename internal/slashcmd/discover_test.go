package slashcmd

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover(t *testing.T) {
	cfg := t.TempDir()
	project := t.TempDir()
	plug := t.TempDir()
	off := t.TempDir()

	write(t, filepath.Join(cfg, "commands", "catchup.md"), "---\ndescription: Catch up on the branch\n---\nbody")
	write(t, filepath.Join(cfg, "commands", "git", "sync.md"), "no frontmatter")
	write(t, filepath.Join(cfg, "skills", "disturb-me", "SKILL.md"), "---\nname: disturb-me\ndescription: \"Ping me when done\"\n---\n")
	write(t, filepath.Join(cfg, "skills", "hidden", "SKILL.md"), "---\nname: hidden\nuser-invocable: false\ndescription: x\n---\n")
	write(t, filepath.Join(project, ".claude", "commands", "deploy.md"), "---\ndescription: Deploy it\n---\n")
	write(t, filepath.Join(plug, "skills", "on", "SKILL.md"), "---\nname: on\ndescription: Voice on\n---\n")
	write(t, filepath.Join(plug, "commands", "status.md"), "---\ndescription: Voice status\n---\n")
	write(t, filepath.Join(off, "commands", "nope.md"), "---\ndescription: disabled plugin\n---\n")
	write(t, filepath.Join(cfg, "plugins", "installed_plugins.json"), `{"version":2,"plugins":{
		"voice-mode@claude-code-voice":[{"scope":"user","installPath":"`+plug+`"}],
		"off@mkt":[{"scope":"user","installPath":"`+off+`"}]}}`)
	write(t, filepath.Join(cfg, "settings.json"), `{"enabledPlugins":{"voice-mode@claude-code-voice":true,"off@mkt":false}}`)

	cmds := Discover(cfg, project)
	byName := map[string]Entry{}
	for _, c := range cmds {
		byName[c.Name] = c
	}
	want := map[string]string{
		"/model":             "builtin",
		"/compact":           "builtin",
		"/catchup":           "user",
		"/git:sync":          "user",
		"/disturb-me":        "user-skill",
		"/deploy":            "project",
		"/voice-mode:on":     "plugin:voice-mode",
		"/voice-mode:status": "plugin:voice-mode",
	}
	for name, source := range want {
		got, ok := byName[name]
		if !ok {
			t.Errorf("missing %s", name)
			continue
		}
		if got.Source != source {
			t.Errorf("%s source = %q, want %q", name, got.Source, source)
		}
	}
	if byName["/catchup"].Description != "Catch up on the branch" || byName["/disturb-me"].Description != "Ping me when done" {
		t.Errorf("descriptions not read from frontmatter: %+v %+v", byName["/catchup"], byName["/disturb-me"])
	}
	for _, absent := range []string{"/hidden", "/off:nope"} {
		if _, ok := byName[absent]; ok {
			t.Errorf("%s must not be listed", absent)
		}
	}
	if !byName["/model"].Verified || byName["/catchup"].Verified {
		t.Errorf("verified flag wrong: model=%v catchup=%v", byName["/model"].Verified, byName["/catchup"].Verified)
	}
}
