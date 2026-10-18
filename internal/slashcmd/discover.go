package slashcmd

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one slash command a Claude session can run.
type Entry struct {
	Name        string `json:"name"`
	Source      string `json:"source"` // builtin, user, user-skill, project, project-skill, plugin:<name>
	Description string `json:"description,omitempty"`
	// Verified means session command checks this command's specific outcome
	// (not just that it was submitted).
	Verified bool `json:"verified,omitempty"`
}

// builtins are the Claude Code client commands most useful remotely. The
// dialog-only ones (/config, /status, /resume ...) are left out: they open an
// interactive view that cannot be driven from a channel.
var builtins = []Entry{
	{Name: "/model", Description: "Switch the model: /model <id>, e.g. /model claude-haiku-5-5", Verified: true},
	{Name: "/compact", Description: "Summarize the conversation to free context (optional instructions)", Verified: true},
	{Name: "/clear", Description: "Start a new conversation with empty context", Verified: true},
	{Name: "/exit", Description: "Exit Claude; agent-deck then marks the session stopped", Verified: true},
	{Name: "/effort", Description: "Set the effort level: /effort <low|medium|high|xhigh|max>"},
	{Name: "/init", Description: "Initialize a CLAUDE.md for the project"},
	{Name: "/review", Description: "Review a pull request"},
	{Name: "/loop", Description: "Run a prompt or slash command on an interval"},
}

// Discover lists the slash commands available to a Claude session whose
// config dir is configDir and whose project is projectDir. It reads files
// only; nothing is typed into the session.
func Discover(configDir, projectDir string) []Entry {
	out := append([]Entry{}, builtins...)
	for i := range out {
		out[i].Source = "builtin"
	}
	if configDir != "" {
		out = append(out, commandsIn(filepath.Join(configDir, "commands"), "", "user")...)
		out = append(out, skillsIn(filepath.Join(configDir, "skills"), "", "user-skill")...)
		out = append(out, pluginEntries(configDir)...)
	}
	if projectDir != "" {
		out = append(out, commandsIn(filepath.Join(projectDir, ".claude", "commands"), "", "project")...)
		out = append(out, skillsIn(filepath.Join(projectDir, ".claude", "skills"), "", "project-skill")...)
	}
	seen := map[string]bool{}
	uniq := out[:0]
	for _, e := range out {
		if !seen[e.Name] {
			seen[e.Name] = true
			uniq = append(uniq, e)
		}
	}
	sort.SliceStable(uniq, func(i, j int) bool {
		if (uniq[i].Source == "builtin") != (uniq[j].Source == "builtin") {
			return uniq[i].Source == "builtin"
		}
		return uniq[i].Name < uniq[j].Name
	})
	return uniq
}

// commandsIn reads <dir>/**/*.md; subdirectories become "dir:name".
func commandsIn(dir, prefix, source string) []Entry {
	var out []Entry
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		name := strings.ReplaceAll(strings.TrimSuffix(rel, ".md"), string(filepath.Separator), ":")
		fm := frontmatter(path)
		out = append(out, Entry{Name: "/" + prefix + name, Source: source, Description: fm["description"]})
		return nil
	})
	return out
}

// skillsIn reads <dir>/<skill>/SKILL.md, skipping skills that opt out of
// the slash menu with user-invocable: false.
func skillsIn(dir, prefix, source string) []Entry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, e := range entries {
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		fm := frontmatter(path)
		if fm["user-invocable"] == "false" {
			continue
		}
		name := fm["name"]
		if name == "" {
			name = e.Name()
		}
		out = append(out, Entry{Name: "/" + prefix + name, Source: source, Description: fm["description"]})
	}
	return out
}

// pluginEntries lists commands and skills of enabled plugins as
// "/<plugin>:<name>".
func pluginEntries(configDir string) []Entry {
	var installed struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	var settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if !readJSON(filepath.Join(configDir, "plugins", "installed_plugins.json"), &installed) {
		return nil
	}
	readJSON(filepath.Join(configDir, "settings.json"), &settings)
	keys := make([]string, 0, len(installed.Plugins))
	for k := range installed.Plugins {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Entry
	for _, key := range keys {
		if !settings.EnabledPlugins[key] || len(installed.Plugins[key]) == 0 {
			continue
		}
		plugin, _, _ := strings.Cut(key, "@")
		root := installed.Plugins[key][0].InstallPath
		source := "plugin:" + plugin
		out = append(out, commandsIn(filepath.Join(root, "commands"), plugin+":", source)...)
		out = append(out, skillsIn(filepath.Join(root, "skills"), plugin+":", source)...)
	}
	return out
}

func readJSON(path string, v any) bool {
	data, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(data, v) == nil
}

// frontmatter reads the flat "key: value" pairs of a markdown file's YAML
// front matter (enough for name, description, user-invocable).
func frontmatter(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return out
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}
