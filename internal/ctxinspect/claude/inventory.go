package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Disk-walk bounds. Every lookup below is a bounded glob rather than a
// recursive walk: the panel opens from a keypress and must not pay for the size
// of a plugin cache.
const (
	// maxDiskSkills bounds the number of on-disk skills reported.
	maxDiskSkills = 512
	// maxDiskAgents bounds the number of on-disk agent definitions reported.
	maxDiskAgents = 512
)

// SkillEntry is one skill's catalogue line, split out of the injected listing.
type SkillEntry struct {
	// Name is the skill identifier as the listing names it. Plugin skills are
	// named "plugin:skill".
	Name string
	// Listing is the verbatim catalogue text for this skill, including its
	// leading "- name: " marker.
	Listing string
	// Chars is the character count of Listing.
	Chars int
}

// SplitSkillListing divides the injected skill catalogue into one entry per
// skill, using the harness's own name list to find the boundaries.
//
// Descriptions are free text and routinely contain newlines and list markers,
// so splitting on line shape would mis-attribute them. Locating each name's
// marker in order is exact, and it fails loudly: if any marker is missing or out
// of order the whole split is rejected, and the caller reports the listing as
// one unsplit captured block rather than silently mis-assigning cost between
// skills.
func SplitSkillListing(content string, names []string) ([]SkillEntry, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("the skill listing named no skills")
	}
	starts := make([]int, len(names))
	cursor := 0
	for i, name := range names {
		marker := "- " + name + ":"
		idx := strings.Index(content[cursor:], marker)
		if idx < 0 {
			return nil, fmt.Errorf("skill %q has no %q marker in the listing at or after offset %d", name, marker, cursor)
		}
		starts[i] = cursor + idx
		cursor = starts[i] + len(marker)
	}

	out := make([]SkillEntry, len(names))
	for i, name := range names {
		end := len(content)
		if i+1 < len(names) {
			end = starts[i+1]
		}
		listing := strings.TrimRight(content[starts[i]:end], "\n")
		out[i] = SkillEntry{Name: name, Listing: listing, Chars: len([]rune(listing))}
	}
	return out, nil
}

// skillNamePattern matches the identifier in a catalogue entry's "- name: "
// marker. Names are identifiers — plugin-qualified ones carry an internal colon
// but never a space — so requiring that shape is what stops a description's own
// bullet ("- Also use for: permissions") from being read as a new skill.
var skillNamePattern = regexp.MustCompile(`^- ([A-Za-z0-9][A-Za-z0-9_:.-]{0,79}): `)

// DeriveSkillNames recovers the skill names from a catalogue that did not carry
// them.
//
// Claude Code versions before mid-2026 write the skill_listing attachment with
// only its content and a count; the names array arrived later. Measured on a
// working machine, 2 of 39 recent sessions are on the older shape. Without a
// fallback those sessions lose the entire per-skill breakdown, which is the
// point of the category.
//
// The caller must verify the result against the harness's own skillCount before
// trusting it: this is a reconstruction, and a reconstruction that disagrees
// with the count the harness published is wrong.
func DeriveSkillNames(content string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(content, "\n") {
		m := skillNamePattern.FindStringSubmatch(line)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// DiskSkill is a skill definition found on disk.
type DiskSkill struct {
	// Name matches the identifier the listing would use.
	Name string
	// Path is the absolute path of the SKILL.md file.
	Path string
	// Dir is the skill directory, which is what a user would delete.
	Dir string
	// Size is the size of SKILL.md in bytes — the body's cost if it were
	// invoked, which is what makes potential-versus-actual measurable.
	Size int64
	// Source is "user", "project" or "plugin".
	Source string
}

// DiscoverSkills lists skill definitions on disk for a session.
//
// It covers the three places Claude Code loads them from: the user's config
// directory, the project's .claude directory, and the plugin cache. The result
// is what makes "nine of your twenty skills cost nothing today" a statement
// about measured files rather than a guess.
func DiscoverSkills(configDir, projectPath string) []DiskSkill {
	var out []DiskSkill
	seen := make(map[string]bool)

	add := func(name, path, dir, source string) {
		if len(out) >= maxDiskSkills || name == "" || seen[name] {
			return
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return
		}
		seen[name] = true
		out = append(out, DiskSkill{Name: name, Path: path, Dir: dir, Size: info.Size(), Source: source})
	}

	if d := strings.TrimSpace(configDir); d != "" {
		for _, p := range globPaths(filepath.Join(d, "skills", "*", "SKILL.md")) {
			dir := filepath.Dir(p)
			add(filepath.Base(dir), p, dir, "user")
		}
		// Plugin cache layout: plugins/cache/<marketplace>/<plugin>/<version>/skills/<name>/SKILL.md.
		// The listing names these "<plugin>:<name>".
		for _, p := range globPaths(filepath.Join(d, "plugins", "cache", "*", "*", "*", "skills", "*", "SKILL.md")) {
			dir := filepath.Dir(p)
			plugin := pluginNameFromSkillPath(p)
			name := filepath.Base(dir)
			if plugin != "" {
				name = plugin + ":" + name
			}
			add(name, p, dir, "plugin")
		}
	}
	if p := strings.TrimSpace(projectPath); p != "" {
		for _, sp := range globPaths(filepath.Join(p, ".claude", "skills", "*", "SKILL.md")) {
			dir := filepath.Dir(sp)
			add(filepath.Base(dir), sp, dir, "project")
		}
	}

	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// pluginNameFromSkillPath extracts the plugin directory from a plugin-cache
// skill path, which is the segment the listing prefixes the skill name with.
func pluginNameFromSkillPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "cache" && i+2 < len(parts) {
			return parts[i+2]
		}
	}
	return ""
}

// DiskAgent is an agent definition found on disk.
type DiskAgent struct {
	// Name is the agent identifier, taken from the file name.
	Name string
	// Path is the absolute path of the definition file.
	Path string
	// Source is "user", "project" or "plugin".
	Source string
}

// DiscoverAgents lists agent definitions on disk.
//
// It is how built-in agents are told apart from the user's own without a
// hardcoded list of Claude Code's internals: an agent with a definition file is
// the user's and carries a lever, and an agent with none is shipped inside the
// binary and is marked immovable. A hardcoded list would rot the first time
// Anthropic renames one.
func DiscoverAgents(configDir, projectPath string) []DiskAgent {
	var out []DiskAgent
	seen := make(map[string]bool)

	add := func(path, source string) {
		if len(out) >= maxDiskAgents {
			return
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, DiskAgent{Name: name, Path: path, Source: source})
	}

	if d := strings.TrimSpace(configDir); d != "" {
		for _, p := range globPaths(filepath.Join(d, "agents", "*.md")) {
			add(p, "user")
		}
		for _, p := range globPaths(filepath.Join(d, "plugins", "cache", "*", "*", "*", "agents", "*.md")) {
			add(p, "plugin")
		}
	}
	if p := strings.TrimSpace(projectPath); p != "" {
		for _, ap := range globPaths(filepath.Join(p, ".claude", "agents", "*.md")) {
			add(ap, "project")
		}
	}

	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// globPaths returns sorted matches, treating a malformed pattern as no match
// rather than as an error: every pattern here is built from a literal, so a
// failure would be a programming error and an empty result is the safe outcome.
func globPaths(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}

// AgentEntry is one agent's catalogue line, split out of the injected listing.
type AgentEntry struct {
	// Name is the agent identifier.
	Name string
	// Listing is the verbatim catalogue line.
	Listing string
	// Chars is the character count of Listing.
	Chars int
}

// SplitAgentListing pairs the injected agent lines with their identifiers.
//
// Unlike the skill listing, the agent listing arrives already split into one
// entry per agent, so this only has to pair the parallel arrays and report a
// length mismatch instead of quietly truncating to the shorter one.
func SplitAgentListing(types, lines []string) ([]AgentEntry, error) {
	if len(types) != len(lines) {
		return nil, fmt.Errorf("the agent listing carries %d identifiers and %d lines", len(types), len(lines))
	}
	out := make([]AgentEntry, len(types))
	for i, name := range types {
		out[i] = AgentEntry{Name: name, Listing: lines[i], Chars: len([]rune(lines[i]))}
	}
	return out, nil
}

// mcpServerForTool returns the MCP server a deferred tool belongs to, and
// whether the tool is an MCP tool at all.
//
// Claude Code names MCP tools "mcp__<server>__<tool>", so the server is
// recoverable from the name — which is what lets a deferred tool carry the
// exact agent-deck detach command for the server that contributed it.
func mcpServerForTool(name string) (server string, ok bool) {
	const prefix = "mcp__"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	rest := name[len(prefix):]
	idx := strings.Index(rest, "__")
	if idx <= 0 {
		return "", false
	}
	return rest[:idx], true
}
