package assistant

// templates.go is the ONE place that knows each assistant's config format
// and the context prompt. The CLI (`agent-deck connect`), the web form
// (/connect) and the docs all render from here, so a format fix lands
// everywhere at once.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

// DefaultKind is used when no assistant is named: one generic block that
// tells the assistant to pick MCP or HTTP by itself.
const DefaultKind = "any"

// DefaultURL is where `agent-deck web` listens by default.
const DefaultURL = "http://127.0.0.1:8420"

// Kind is one assistant the connect command knows a config format for.
type Kind struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Transport is how the snippet connects: "stdio" (the assistant starts
	// `agent-deck connect serve`), "http" (it calls the web server) or
	// "both".
	Transport string `json:"transport"`
	// NeedsWeb is true when the snippet needs `agent-deck web` running
	// (and, for cloud assistants, reachable from the internet over HTTPS).
	NeedsWeb bool   `json:"needs_web"`
	Note     string `json:"note,omitempty"`

	snippet  func(c *Connection) string
	pathHint string
}

// Connection is everything an assistant needs, as printed by `connect
// --json` and returned by POST /api/connect/tokens.
type Connection struct {
	Name           string        `json:"name"`
	Assistant      string        `json:"assistant"`
	Scopes         []Scope       `json:"scopes"`
	Profile        string        `json:"profile,omitempty"`
	TokenFile      string        `json:"token_file"`
	Token          string        `json:"token"`
	MCPStdio       MCPStdio      `json:"mcp_stdio"`
	MCPHTTP        HTTPEndpoint  `json:"mcp_http"`
	HTTPAPI        HTTPAPIConfig `json:"http_api"`
	ConfigSnippet  string        `json:"config_snippet"`
	ConfigPathHint string        `json:"config_path_hint"`
	ContextPrompt  string        `json:"context_prompt"`
	RaiseScopeHint string        `json:"raise_scope_hint"`
	Block          string        `json:"block"`
}

// MCPStdio is the command an MCP client starts.
type MCPStdio struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// HTTPEndpoint is the MCP-over-HTTP endpoint.
type HTTPEndpoint struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// HTTPAPIConfig is the JSON API base.
type HTTPAPIConfig struct {
	Base    string            `json:"base"`
	Headers map[string]string `json:"headers"`
}

// ConnectOptions configures Issue.
type ConnectOptions struct {
	Kind    string  // assistant kind; "" means DefaultKind
	Name    string  // token name; "" means the kind
	Scopes  []Scope // nil means DefaultScopes
	Profile string  // resolved agent-deck profile the token is bound to
	URL     string  // web server base URL; "" means DefaultURL
	Command string  // absolute path of the agent-deck binary
}

// Kinds returns every assistant kind, DefaultKind first.
func Kinds() []Kind {
	return []Kind{
		{ID: "any", Label: "Any assistant (it sets itself up)", Transport: "both", snippet: snippetAny,
			pathHint: "the assistant picks: MCP config if it supports MCP servers, otherwise the HTTP API"},
		{ID: "hermes", Label: "Hermes Agent", Transport: "stdio", snippet: snippetHermes,
			pathHint: "~/.hermes/config.yaml (merge under mcp_servers), then restart Hermes"},
		{ID: "openclaw", Label: "OpenClaw", Transport: "stdio", snippet: snippetOpenClaw,
			pathHint: "~/.openclaw/openclaw.json (merge under mcp.servers), then restart the gateway"},
		{ID: "grok", Label: "Grok (xAI API remote MCP)", Transport: "http", NeedsWeb: true, snippet: snippetGrok,
			pathHint: "the tools array of your xAI API request",
			Note:     "xAI calls the URL from its servers: expose agent-deck web over HTTPS (for example tailscale funnel) and use that URL."},
		{ID: "claude", Label: "Claude Code / Claude Desktop", Transport: "stdio", snippet: snippetClaude,
			pathHint: "Claude Code: run the command. Claude Desktop: claude_desktop_config.json (Settings > Developer > Edit Config)"},
		{ID: "chatgpt", Label: "ChatGPT (custom connector / Responses API)", Transport: "http", NeedsWeb: true, snippet: snippetChatGPT,
			pathHint: "ChatGPT: Settings > Connectors > Advanced > Developer mode > Create. API: the tools array of a Responses request",
			Note:     "ChatGPT calls the URL from OpenAI's servers: expose agent-deck web over HTTPS (for example tailscale funnel) and use that URL."},
		{ID: "codex", Label: "Codex CLI", Transport: "stdio", snippet: snippetCodex,
			pathHint: "run the command, or merge the TOML into ~/.codex/config.toml"},
		{ID: "slack", Label: "Slack bot (HTTP API)", Transport: "http", NeedsWeb: true, snippet: snippetBot,
			pathHint: "your bot's environment (AGENTDECK_URL, AGENTDECK_TOKEN)"},
		{ID: "telegram", Label: "Telegram bot (HTTP API)", Transport: "http", NeedsWeb: true, snippet: snippetBot,
			pathHint: "your bot's environment (AGENTDECK_URL, AGENTDECK_TOKEN)"},
		{ID: "mcp", Label: "Any MCP client (generic)", Transport: "both", snippet: snippetMCP,
			pathHint: "your MCP client's server list (mcpServers JSON is the common shape)"},
		{ID: "http", Label: "Any HTTP client (generic)", Transport: "http", NeedsWeb: true, snippet: snippetHTTP,
			pathHint: "your program's environment (AGENTDECK_URL, AGENTDECK_TOKEN)"},
	}
}

// KindIDs lists the kind ids, DefaultKind first.
func KindIDs() []string {
	ks := Kinds()
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.ID
	}
	return out
}

// LookupKind finds a kind by id (case-insensitive). "generic" is accepted
// as an alias of DefaultKind.
func LookupKind(id string) (Kind, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" || id == "generic" {
		id = DefaultKind
	}
	for _, k := range Kinds() {
		if k.ID == id {
			return k, true
		}
	}
	return Kind{}, false
}

// Issue creates (or rotates) the token for opts and renders the connection.
func Issue(store *Store, opts ConnectOptions) (*Connection, error) {
	kind, ok := LookupKind(opts.Kind)
	if !ok {
		return nil, fmt.Errorf("unknown assistant %q (known: %s)", opts.Kind, strings.Join(KindIDs(), ", "))
	}
	name := opts.Name
	if name == "" {
		name = kind.ID
		if kind.ID == DefaultKind {
			name = "assistant"
		}
	}
	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	token, rec, err := store.Create(name, kind.ID, scopes, opts.Profile)
	if err != nil {
		return nil, err
	}
	return Render(kind, rec, token, store.TokenFile(name), opts.URL, opts.Command), nil
}

// Render builds the connection for an issued token.
func Render(kind Kind, rec Record, token, tokenFile, baseURL, command string) *Connection {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if command == "" {
		command = "agent-deck"
	}
	auth := map[string]string{"Authorization": "Bearer " + token}
	c := &Connection{
		Name:      rec.Name,
		Assistant: kind.ID,
		Scopes:    rec.Scopes,
		Profile:   rec.Profile,
		TokenFile: tokenFile,
		Token:     token,
		MCPStdio:  MCPStdio{Command: command, Args: []string{"connect", "serve", "--token-file", tokenFile}},
		MCPHTTP:   HTTPEndpoint{URL: baseURL + MCPPath, Headers: auth},
		HTTPAPI:   HTTPAPIConfig{Base: baseURL + strings.TrimSuffix(APIPathPrefix, "/"), Headers: auth},
	}
	c.ConfigPathHint = kind.pathHint
	c.ConfigSnippet = kind.snippet(c)
	c.RaiseScopeHint = raiseScopeHint(rec)
	c.ContextPrompt = ContextPrompt(c, kind)
	c.Block = renderBlock(c, kind)
	return c
}

func raiseScopeHint(rec Record) string {
	all := rec
	all.Scopes = AllScopes
	cmd := RaiseScopeCommand(all, ScopeRead)
	return "To allow more, run: " + cmd + " (pick only the scopes you need; this rotates the token, " +
		"stdio configs pick the new one up from the token file, HTTP configs need the new token)"
}

// --- helpers ---------------------------------------------------------------

func jsonIndent(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func quoteArgs(cmd string, args []string) string {
	parts := []string{shellescape.Quote(cmd)}
	for _, a := range args {
		parts = append(parts, shellescape.Quote(a))
	}
	return strings.Join(parts, " ")
}

func tomlString(s string) string {
	b, _ := json.Marshal(s) // a JSON string is a valid TOML basic string
	return string(b)
}

func tomlArray(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = tomlString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func mcpServersJSON(c *Connection) string {
	return jsonIndent(map[string]any{"mcpServers": map[string]any{
		"agent-deck": map[string]any{"command": c.MCPStdio.Command, "args": c.MCPStdio.Args},
	}})
}

func sortedHeaderLines(h map[string]string) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, h[k])
	}
	return b.String()
}

func curlExamples(c *Connection, envStyle bool) string {
	base, tok := c.HTTPAPI.Base, c.Token
	if envStyle {
		base, tok = "$AGENTDECK_URL/api/assistant/v1", "$AGENTDECK_TOKEN"
	}
	h := `-H "Authorization: Bearer ` + tok + `"`
	return strings.Join([]string{
		`curl -s ` + h + ` ` + base + `/sessions`,
		`curl -s ` + h + ` ` + base + `/sessions/<id>/output`,
		`curl -s ` + h + ` -H 'Content-Type: application/json' -d '{"message":"..."}' ` + base + `/sessions/<id>/send`,
	}, "\n")
}

// --- per-assistant snippets --------------------------------------------------

func snippetAny(c *Connection) string {
	return "If you support MCP servers, add this stdio server (mcpServers shape, adapt to your own config format):\n" +
		mcpServersJSON(c) + "\n\n" +
		"If you can only make HTTP calls, use MCP over HTTP:\n" +
		"POST " + c.MCPHTTP.URL + "\n" + sortedHeaderLines(c.MCPHTTP.Headers) + "\n" +
		"or the plain JSON API (needs `agent-deck web` running):\n" + curlExamples(c, false)
}

func snippetHermes(c *Connection) string {
	return "mcp_servers:\n" +
		"  agent-deck:\n" +
		"    command: " + tomlString(c.MCPStdio.Command) + "\n" +
		"    args: " + tomlArray(c.MCPStdio.Args) + "\n"
}

func snippetOpenClaw(c *Connection) string {
	return jsonIndent(map[string]any{"mcp": map[string]any{"servers": map[string]any{
		"agent-deck": map[string]any{"command": c.MCPStdio.Command, "args": c.MCPStdio.Args},
	}}})
}

func snippetClaude(c *Connection) string {
	return "# Claude Code:\n" +
		"claude mcp add --scope user agent-deck -- " + quoteArgs(c.MCPStdio.Command, c.MCPStdio.Args) + "\n\n" +
		"# Claude Desktop (claude_desktop_config.json):\n" + mcpServersJSON(c)
}

func snippetCodex(c *Connection) string {
	return "codex mcp add agent-deck -- " + quoteArgs(c.MCPStdio.Command, c.MCPStdio.Args) + "\n\n" +
		"# or in ~/.codex/config.toml:\n" +
		"[mcp_servers.agent-deck]\n" +
		"command = " + tomlString(c.MCPStdio.Command) + "\n" +
		"args = " + tomlArray(c.MCPStdio.Args) + "\n"
}

func snippetGrok(c *Connection) string {
	return jsonIndent(map[string]any{
		"type":          "mcp",
		"server_url":    c.MCPHTTP.URL,
		"server_label":  "agent-deck",
		"authorization": c.Token,
	})
}

func snippetChatGPT(c *Connection) string {
	return "# ChatGPT custom connector (Developer mode):\n" +
		"MCP server URL: " + c.MCPHTTP.URL + "\n" +
		"Authentication: bearer token " + c.Token + "\n\n" +
		"# OpenAI Responses API tools entry:\n" +
		jsonIndent(map[string]any{
			"type":             "mcp",
			"server_label":     "agent-deck",
			"server_url":       c.MCPHTTP.URL,
			"authorization":    c.Token,
			"require_approval": "always",
		})
}

func snippetBot(c *Connection) string {
	base := strings.TrimSuffix(c.HTTPAPI.Base, "/api/assistant/v1")
	return "AGENTDECK_URL=" + base + "\n" +
		"AGENTDECK_TOKEN=" + c.Token + "\n\n" +
		"# The bot calls (JSON in, JSON out):\n" + curlExamples(c, true)
}

func snippetMCP(c *Connection) string {
	return "# stdio (the client starts agent-deck):\n" + mcpServersJSON(c) + "\n\n" +
		"# or Streamable HTTP (needs `agent-deck web` running):\n" +
		jsonIndent(map[string]any{"mcpServers": map[string]any{
			"agent-deck": map[string]any{"type": "http", "url": c.MCPHTTP.URL, "headers": c.MCPHTTP.Headers},
		}})
}

func snippetHTTP(c *Connection) string {
	return snippetBot(c)
}

// --- context prompt ----------------------------------------------------------

// toolLines documents the tools for the context prompt.
var toolLines = []string{
	"whoami: your name, scopes and limits",
	"list_sessions {group?, status?}: all sessions with id, title, tool, status, group, path (read)",
	"session_status {session}: one session's details (read)",
	"read_output {session}: the last answer of that session's agent (read)",
	"send_message {session, message}: type a message into a session (send)",
	"approve {session, choice?}: answer a visible approval prompt (approve)",
	"launch_session {path, title, tool?, message?}: start a new session (launch)",
}

// rules are the safety rules every connection carries.
func rules(kindID string) []string {
	cmd := "agent-deck connect " + kindID + " --scopes read,send"
	return []string{
		"Call list_sessions first and refer to sessions by id.",
		"Read before acting: check session_status or read_output before you send or approve anything.",
		"ASK THE USER and wait for a clear yes before send_message, approve or launch_session; show the exact text you will send.",
		"Never stop, delete, restart or kill sessions. There is no tool for it; do not use a shell or any other way to do it.",
		"Never read, print or change credentials, tokens or the ~/.agent-deck directory.",
		"If a tool answers \"scope missing\", do not work around it: tell the user the exact command from the answer (for example: " + cmd + ").",
		"Keep reports short: one line per session (title, status, what it is waiting for).",
	}
}

// ContextPrompt is the short guide the assistant keeps.
func ContextPrompt(c *Connection, kind Kind) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are connected to agent-deck on my machine as %q with scopes: %s.\n", c.Name, ScopeList(c.Scopes))
	b.WriteString("agent-deck is a terminal session manager for AI coding agents (Claude Code, Codex, Gemini, OpenCode and others): " +
		"each session is one agent working in its own terminal. Through the agent-deck tools you can see those sessions and, when allowed, drive them.\n\n")
	b.WriteString("Tools (MCP tool names; the HTTP API at " + c.HTTPAPI.Base + " mirrors them):\n")
	for _, l := range toolLines {
		b.WriteString("- " + l + "\n")
	}
	b.WriteString("\nRules:\n")
	for i, r := range rules(kind.ID) {
		fmt.Fprintf(&b, "%d. %s\n", i+1, r)
	}
	return b.String()
}

// ServerInstructions is the MCP initialize instructions text for rec.
func ServerInstructions(rec Record) string {
	kind := rec.Assistant
	if kind == "" {
		kind = DefaultKind
	}
	return "agent-deck manages terminal sessions of AI coding agents. This connection (" + rec.Name + ") has scopes: " +
		ScopeList(rec.Scopes) + ". Rules: " + strings.Join(rules(kind), " ")
}

func renderBlock(c *Connection, kind Kind) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== agent-deck connect: %s (name %q, scopes %s) ===\n", kind.Label, c.Name, ScopeList(c.Scopes))
	fmt.Fprintf(&b, "Token file: %s (0600). Revoke: agent-deck connect revoke %s\n", c.TokenFile, c.Name)
	if kind.NeedsWeb {
		b.WriteString("Needs `agent-deck web` running and reachable at " + strings.TrimSuffix(c.HTTPAPI.Base, "/api/assistant/v1") + ".\n")
	}
	if kind.Note != "" {
		b.WriteString(kind.Note + "\n")
	}
	b.WriteString("\n--- 1. Config (" + c.ConfigPathHint + ") ---\n")
	b.WriteString(strings.TrimRight(c.ConfigSnippet, "\n") + "\n")
	b.WriteString("\n--- 2. Paste into your assistant (keep it as its agent-deck guide) ---\n")
	b.WriteString(c.ContextPrompt)
	b.WriteString("\n" + c.RaiseScopeHint + "\n")
	b.WriteString("=== end ===\n")
	return b.String()
}
