// Package harness is the core table of the coding-agent harnesses agent-deck
// runs: binary name, the official install and login commands, and docs.
// Clients (the Mac app's Harnesses view, `agent-deck harness list`) read it
// from here and never hardcode it. It is versioned with the release and each
// entry can be overridden under [harnesses.<name>] in config.toml.
package harness

import "sort"

// Entry is one harness's static facts.
type Entry struct {
	Name           string   `json:"name"`
	DisplayName    string   `json:"display_name"`
	Binary         string   `json:"binary"`
	VersionArgs    []string `json:"-"`
	InstallCommand string   `json:"install_command"`
	LoginCommand   string   `json:"login_command"`
	DocsURL        string   `json:"docs_url"`
	// Images reports whether a running session accepts an image through
	// `session send --image` (docs/macapp-core.md).
	Images bool `json:"images"`
}

// Override is the [harnesses.<name>] config table; empty fields keep the
// built-in value.
type Override struct {
	Binary         string `toml:"binary,omitempty"`
	InstallCommand string `toml:"install_command,omitempty"`
	LoginCommand   string `toml:"login_command,omitempty"`
	DocsURL        string `toml:"docs_url,omitempty"`
}

var table = []Entry{
	{Name: "claude", DisplayName: "Claude Code", Binary: "claude", VersionArgs: []string{"--version"},
		InstallCommand: "curl -fsSL https://claude.ai/install.sh | bash",
		LoginCommand:   "claude /login",
		DocsURL:        "https://docs.anthropic.com/en/docs/claude-code/setup", Images: true},
	{Name: "codex", DisplayName: "Codex", Binary: "codex", VersionArgs: []string{"--version"},
		InstallCommand: "npm install -g @openai/codex",
		LoginCommand:   "codex login",
		DocsURL:        "https://github.com/openai/codex"},
	{Name: "gemini", DisplayName: "Gemini CLI", Binary: "gemini", VersionArgs: []string{"--version"},
		InstallCommand: "npm install -g @google/gemini-cli",
		LoginCommand:   "gemini",
		DocsURL:        "https://github.com/google-gemini/gemini-cli", Images: true},
	{Name: "opencode", DisplayName: "OpenCode", Binary: "opencode", VersionArgs: []string{"--version"},
		InstallCommand: "curl -fsSL https://opencode.ai/install | bash",
		LoginCommand:   "opencode auth login",
		DocsURL:        "https://opencode.ai/docs"},
	{Name: "pi", DisplayName: "Pi", Binary: "pi", VersionArgs: []string{"--version"},
		InstallCommand: "npm install -g @mariozechner/pi-coding-agent",
		LoginCommand:   "pi /login",
		DocsURL:        "https://github.com/badlogic/pi-mono"},
	{Name: "hermes", DisplayName: "Hermes Agent", Binary: "hermes", VersionArgs: []string{"--version"},
		InstallCommand: "curl -fsSL https://raw.githubusercontent.com/NousResearch/hermes-agent/main/scripts/install.sh | bash",
		LoginCommand:   "hermes setup",
		DocsURL:        "https://github.com/NousResearch/hermes-agent"},
}

// All returns the table with overrides applied, in table order.
func All(overrides map[string]Override) []Entry {
	out := make([]Entry, 0, len(table))
	for _, e := range table {
		out = append(out, apply(e, overrides[e.Name]))
	}
	return out
}

// Lookup returns one entry with its override applied.
func Lookup(name string, overrides map[string]Override) (Entry, bool) {
	for _, e := range table {
		if e.Name == name {
			return apply(e, overrides[name]), true
		}
	}
	return Entry{}, false
}

// Names lists the harness names, sorted.
func Names() []string {
	out := make([]string, 0, len(table))
	for _, e := range table {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

func apply(e Entry, o Override) Entry {
	if o.Binary != "" {
		e.Binary = o.Binary
	}
	if o.InstallCommand != "" {
		e.InstallCommand = o.InstallCommand
	}
	if o.LoginCommand != "" {
		e.LoginCommand = o.LoginCommand
	}
	if o.DocsURL != "" {
		e.DocsURL = o.DocsURL
	}
	return e
}
