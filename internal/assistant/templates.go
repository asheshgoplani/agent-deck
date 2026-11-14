package assistant

// templates.go is the ONE place that knows each assistant's config format
// and the context prompt. The CLI (`agent-deck connect`), the web form
// (/connect) and the docs all render from here, so a format fix lands
// everywhere at once.

import (
	"encoding/json"
	"fmt"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

// DefaultKind is used when no assistant is named: one generic block that
// tells the assistant to pick MCP or HTTP by itself.
const DefaultKind = "any"

// DefaultURL is where `agent-deck web` listens by default.
const DefaultURL = "http://127.0.0.1:8420"

// ServerKey is the MCP server name every snippet registers. It is the name
// the research pass verified with Hermes, OpenClaw, Claude Code and Codex
// (no dash, so tool prefixes such as mcp_agentdeck_list_sessions stay clean).
const ServerKey = "agentdeck"

// SetupPromptURL is the paste-in self-setup prompt (the primary door).
const SetupPromptURL = "https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/docs/assistants/SETUP-PROMPT.md"

// skillURLBase is where the ready skill files live (docs/assistants/skills).
const skillURLBase = "https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/docs/assistants/skills/"

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

	snippet func(c *Connection) string
	// pathHint says where the config goes.
	pathHint string
	// clientTest is the assistant's own check, if it has one.
	clientTest string
	// skillPath is where the ready skill file goes ("" = no skill files).
	skillPath string
	// guideHint says where to keep the context prompt when there is no
	// skill file.
	guideHint string
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
	TestCommand    string        `json:"test_command"`
	SkillPath      string        `json:"skill_path,omitempty"`
	SkillURL       string        `json:"skill_url,omitempty"`
	SkillContent   string        `json:"skill_content,omitempty"`
	SetupPrompt    string        `json:"setup_prompt"`
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

// cloudNote explains the public HTTPS address a cloud-side client needs.
func cloudNote(who string) string {
	return who + " calls the URL from its own cloud, so localhost does not work. Only with the owner's consent: " +
		"run `agent-deck web --no-tui --token-file OWNER_TOKEN_FILE --allowed-host HOST` (the owner token keeps the owner pages locked), " +
		"expose it over HTTPS (for example `tailscale funnel 8420`) and re-run this command with --url https://HOST."
}

// Kinds returns every assistant kind, DefaultKind first.
func Kinds() []Kind {
	return []Kind{
		{ID: "any", Label: "Any assistant (it sets itself up)", Transport: "both", snippet: snippetAny,
			pathHint:  "the assistant picks: its MCP server config if it has one, otherwise the HTTP API",
			guideHint: "your assistant's memory, skills or standing instructions"},
		{ID: "hermes", Label: "Hermes Agent", Transport: "stdio", snippet: snippetHermes,
			pathHint:   "run the command, or merge the YAML into ~/.hermes/config.yaml",
			clientTest: "hermes mcp test " + ServerKey,
			skillPath:  "~/.hermes/skills/agent-deck/SKILL.md"},
		{ID: "openclaw", Label: "OpenClaw", Transport: "stdio", snippet: snippetOpenClaw,
			pathHint:   "run the command, or merge the JSON into ~/.openclaw/openclaw.json",
			clientTest: "openclaw mcp probe",
			skillPath:  "~/.openclaw/workspace/skills/agent-deck/SKILL.md"},
		{ID: "grok", Label: "Grok (xAI API, remote MCP)", Transport: "http", NeedsWeb: true, snippet: snippetGrok,
			pathHint:  "the \"tools\" array of POST https://api.x.ai/v1/responses",
			guideHint: "the system message (\"instructions\") of your request",
			Note:      cloudNote("xAI") + " xAI has no ask-first switch, so the rules below and the token scopes are the only guard."},
		{ID: "claude", Label: "Claude Code / Claude Desktop", Transport: "stdio", snippet: snippetClaude,
			pathHint:   "Claude Code: run the command. Claude Desktop: claude_desktop_config.json, then restart the app",
			clientTest: "claude mcp list",
			skillPath:  "~/.claude/skills/agent-deck/SKILL.md"},
		{ID: "chatgpt", Label: "ChatGPT (via Codex CLI or the Responses API)", Transport: "both", snippet: snippetChatGPT,
			pathHint:  "Codex CLI: run the command. Responses API: the \"tools\" array of your request",
			guideHint: "Codex: ~/.agents/skills/agent-deck/SKILL.md. Responses API: the \"instructions\" of your request",
			Note: "ChatGPT web and desktop apps cannot send a fixed token yet (they only do OAuth), so use Codex CLI " +
				"(same ChatGPT login) or the Responses API. " + cloudNote("The Responses API")},
		{ID: "codex", Label: "Codex CLI", Transport: "stdio", snippet: snippetCodex,
			pathHint:   "run the command, or merge the TOML into ~/.codex/config.toml",
			clientTest: "codex mcp list",
			skillPath:  "~/.agents/skills/agent-deck/SKILL.md"},
		{ID: "slack", Label: "Slack bot (HTTP API)", Transport: "http", NeedsWeb: true, snippet: snippetBot,
			pathHint:  "your bot's environment, then call the API from the bot",
			guideHint: "the system prompt of the model behind your bot",
			Note:      "Want a ready Slack bot instead? The conductor has one: agent-deck conductor setup NAME (docs/conductor/slack.md)."},
		{ID: "telegram", Label: "Telegram bot (HTTP API)", Transport: "http", NeedsWeb: true, snippet: snippetBot,
			pathHint:  "your bot's environment, then call the API from the bot",
			guideHint: "the system prompt of the model behind your bot",
			Note:      "Want a ready Telegram bot instead? The conductor has one: agent-deck conductor setup NAME (docs/conductor/telegram.md)."},
		{ID: "mcp", Label: "Any MCP client (generic)", Transport: "both", snippet: snippetMCP,
			pathHint:  "your MCP client's server list (the mcpServers JSON shape is the most common)",
			guideHint: "your client's system prompt or instructions"},
		{ID: "http", Label: "Any HTTP client (generic)", Transport: "http", NeedsWeb: true, snippet: snippetHTTP,
			pathHint:  "your program's environment, then call the API",
			guideHint: "the system prompt of the model that drives your program"},
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

// SkillKinds lists the kinds that keep their guide in a SKILL.md file; a
// ready file for each lives in docs/assistants/skills/<kind>/SKILL.md.
func SkillKinds() []string {
	var out []string
	for _, k := range Kinds() {
		if k.skillPath != "" {
			out = append(out, k.ID)
		}
	}
	return out
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
		Name:        rec.Name,
		Assistant:   kind.ID,
		Scopes:      rec.Scopes,
		Profile:     rec.Profile,
		TokenFile:   tokenFile,
		Token:       token,
		MCPStdio:    MCPStdio{Command: command, Args: []string{"connect", "serve", "--token-file", tokenFile}},
		MCPHTTP:     HTTPEndpoint{URL: baseURL + MCPPath, Headers: auth},
		HTTPAPI:     HTTPAPIConfig{Base: baseURL + strings.TrimSuffix(APIPathPrefix, "/"), Headers: auth},
		SetupPrompt: SetupPromptURL,
	}
	c.ConfigPathHint = kind.pathHint
	c.ConfigSnippet = kind.snippet(c)
	c.RaiseScopeHint = raiseScopeHint(rec)
	c.ContextPrompt = ContextPrompt(c, kind)
	c.TestCommand = testCommand(c, rec, kind)
	if kind.skillPath != "" {
		c.SkillPath = kind.skillPath
		c.SkillURL = skillURLBase + kind.ID + "/SKILL.md"
		c.SkillContent = SkillFile(kind.ID)
	}
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

// deckCommand is "agent-deck" plus -p for a non-default profile.
func deckCommand(profile string) string {
	if profile != "" && profile != "default" {
		return "agent-deck -p " + shellescape.Quote(profile)
	}
	return "agent-deck"
}

func testCommand(c *Connection, rec Record, kind Kind) string {
	cmd := deckCommand(rec.Profile) + " connect test --name " + shellescape.Quote(rec.Name)
	if kind.Transport == "http" {
		cmd += " --url " + shellescape.Quote(baseOf(c))
	}
	return cmd
}

// baseOf is the web server base URL of c.
func baseOf(c *Connection) string {
	return strings.TrimSuffix(c.HTTPAPI.Base, strings.TrimSuffix(APIPathPrefix, "/"))
}

// --- helpers ---------------------------------------------------------------

// kv is one key of an ordered JSON object.
type kv struct {
	K string
	V any
}

// obj is a JSON object that keeps its key order, so snippets read the way
// the assistants' own docs write them (command before args, type first).
type obj []kv

// MarshalJSON writes the keys in order.
func (o obj) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(p.K)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(p.V)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func jsonIndent(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func jsonCompact(v any) string {
	b, _ := json.Marshal(v)
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
	b, _ := json.Marshal(s) // a JSON string is a valid TOML (and YAML) double-quoted string
	return string(b)
}

func tomlArray(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = tomlString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func stdioServer(c *Connection) obj {
	return obj{{"command", c.MCPStdio.Command}, {"args", c.MCPStdio.Args}}
}

func mcpServersJSON(c *Connection) string {
	return jsonIndent(obj{{"mcpServers", obj{{ServerKey, stdioServer(c)}}}})
}

// allowedTools lists the tools c's scopes can call, in Tools() order.
func allowedTools(c *Connection) []string {
	var out []string
	for _, t := range Tools() {
		if t.Scope == "" || HasScope(c.Scopes, t.Scope) {
			out = append(out, t.Name)
		}
	}
	return out
}

// readTools lists the tools that never change anything.
func readTools() []string {
	var out []string
	for _, t := range Tools() {
		if !t.Write {
			out = append(out, t.Name)
		}
	}
	return out
}

func curlExamples(c *Connection, envStyle bool) string {
	base, tok := c.HTTPAPI.Base, c.Token
	if envStyle {
		base, tok = "$AGENTDECK_URL"+strings.TrimSuffix(APIPathPrefix, "/"), "$"+TokenEnvVar
	}
	h := `-H "Authorization: Bearer ` + tok + `"`
	return strings.Join([]string{
		`curl -fsS ` + h + ` "` + base + `/sessions"`,
		`curl -fsS ` + h + ` "` + base + `/sessions/SESSION_ID/output"`,
		`curl -fsS ` + h + ` -H 'Content-Type: application/json' -d '{"message":"..."}' "` + base + `/sessions/SESSION_ID/send"   # needs scope send`,
	}, "\n")
}

// --- per-assistant snippets --------------------------------------------------

const serverDescription = "agent-deck: terminal session manager for AI coding agents"

func snippetAny(c *Connection) string {
	return "Assistant: follow " + SetupPromptURL + " with this connection; use the first option you support.\n\n" +
		"A) MCP over stdio (you run on this machine; the token stays in its file):\n" +
		mcpServersJSON(c) + "\n\n" +
		"B) MCP over HTTP (needs `agent-deck web` running; cloud assistants need a public HTTPS address):\n" +
		"URL: " + c.MCPHTTP.URL + "\n" +
		"Header: Authorization: " + c.MCPHTTP.Headers["Authorization"] + "\n\n" +
		"C) Plain HTTP API (same token, JSON in and out):\n" + curlExamples(c, false)
}

// Hermes: verified with `hermes mcp add` / `hermes mcp test` (v0.23.0).
// `--args` must be the last option. trust: untrusted makes Hermes ask
// before any tool without readOnlyHint (send, approve, launch). `hermes mcp
// add` has no trust option and Hermes treats a server without a trust key
// as fully trusted, so the command line sets it with `hermes config set`
// (mcp_servers takes any sub-key).
func snippetHermes(c *Connection) string {
	return "hermes mcp add " + ServerKey + " --command " + shellescape.Quote(c.MCPStdio.Command) + " --args " +
		quoteArgs(c.MCPStdio.Args[0], c.MCPStdio.Args[1:]) +
		" && hermes config set mcp_servers." + ServerKey + ".trust untrusted\n\n" +
		"# or merge into ~/.hermes/config.yaml:\n" +
		"mcp_servers:\n" +
		"  " + ServerKey + ":\n" +
		"    command: " + tomlString(c.MCPStdio.Command) + "\n" +
		"    args: " + tomlArray(c.MCPStdio.Args) + "\n" +
		"    trust: untrusted   # asks you before any tool that is not read-only\n"
}

// OpenClaw: verified with `openclaw mcp set` / `openclaw mcp probe`
// (2026.11.1); servers live under mcp.servers in openclaw.json.
func snippetOpenClaw(c *Connection) string {
	return "openclaw mcp set " + ServerKey + " " + shellescape.Quote(jsonCompact(stdioServer(c))) + "\n\n" +
		"# or merge into ~/.openclaw/openclaw.json:\n" +
		jsonIndent(obj{{"mcp", obj{{"servers", obj{{ServerKey, stdioServer(c)}}}}}})
}

// Claude Code: `claude mcp add -s user` (verified, 2.1.319). Claude Desktop:
// stdio only (its Connectors UI has no static bearer header) and it needs
// an absolute command path because it does not use the shell PATH.
func snippetClaude(c *Connection) string {
	return "# Claude Code (all projects):\n" +
		"claude mcp add -s user " + ServerKey + " -- " + quoteArgs(c.MCPStdio.Command, c.MCPStdio.Args) + "\n\n" +
		"# Claude Desktop: merge into claude_desktop_config.json and restart the app\n" +
		"#   macOS:   ~/Library/Application Support/Claude/claude_desktop_config.json\n" +
		"#   Windows: %APPDATA%\\Claude\\claude_desktop_config.json\n" +
		"#   Linux:   ~/.config/Claude/claude_desktop_config.json\n" +
		mcpServersJSON(c)
}

// Codex CLI: verified with `codex mcp add` (0.162.1).
func snippetCodex(c *Connection) string {
	return "codex mcp add " + ServerKey + " -- " + quoteArgs(c.MCPStdio.Command, c.MCPStdio.Args) + "\n\n" +
		"# or merge into ~/.codex/config.toml:\n" +
		"[mcp_servers." + ServerKey + "]\n" +
		"command = " + tomlString(c.MCPStdio.Command) + "\n" +
		"args = " + tomlArray(c.MCPStdio.Args) + "\n"
}

// Grok: xAI Responses API remote MCP tool (docs.x.ai remote-mcp-tools).
// There is no require_approval, so allowed_tools is cut to the scopes.
func snippetGrok(c *Connection) string {
	return jsonIndent(obj{
		{"type", "mcp"},
		{"server_url", c.MCPHTTP.URL},
		{"server_label", ServerKey},
		{"server_description", serverDescription},
		{"allowed_tools", allowedTools(c)},
		{"authorization", c.Token},
	})
}

// ChatGPT: the web app's developer mode only does OAuth or no auth, so the
// block points at Codex CLI and the Responses API mcp tool instead.
func snippetChatGPT(c *Connection) string {
	return "# Codex CLI (signs in with your ChatGPT account):\n" +
		"codex mcp add " + ServerKey + " -- " + quoteArgs(c.MCPStdio.Command, c.MCPStdio.Args) + "\n\n" +
		"# OpenAI Responses API: add to \"tools\" (send it with every request; the token is not stored):\n" +
		jsonIndent(obj{
			{"type", "mcp"},
			{"server_label", ServerKey},
			{"server_description", serverDescription},
			{"server_url", c.MCPHTTP.URL},
			{"authorization", c.Token},
			{"allowed_tools", allowedTools(c)},
			{"require_approval", obj{{"never", obj{{"tool_names", readTools()}}}}},
		})
}

func snippetBot(c *Connection) string {
	return "# In the bot's environment:\n" +
		"AGENTDECK_URL=" + baseOf(c) + "\n" +
		TokenEnvVar + "=" + c.Token + "\n\n" +
		"# The bot calls (JSON in, JSON out):\n" + curlExamples(c, true) + "\n\n" +
		"# Python:\n" +
		"# requests.get(os.environ[\"AGENTDECK_URL\"] + \"/api/assistant/v1/sessions\",\n" +
		"#              headers={\"Authorization\": \"Bearer \" + os.environ[\"" + TokenEnvVar + "\"]}).json()\n"
}

func snippetMCP(c *Connection) string {
	return "# stdio (preferred; the client starts agent-deck, the token stays in its file):\n" + mcpServersJSON(c) + "\n\n" +
		"# or Streamable HTTP (needs `agent-deck web` running):\n" +
		jsonIndent(obj{{"mcpServers", obj{{ServerKey, obj{
			{"type", "http"}, {"url", c.MCPHTTP.URL}, {"headers", c.MCPHTTP.Headers},
		}}}}})
}

func snippetHTTP(c *Connection) string {
	base := c.HTTPAPI.Base
	return snippetBot(c) + "\n" +
		"# All routes (Authorization: Bearer token on every call):\n" +
		"# GET  " + base + "/whoami\n" +
		"# GET  " + base + "/sessions[?group=G&status=S]\n" +
		"# GET  " + base + "/sessions/ID\n" +
		"# GET  " + base + "/sessions/ID/output\n" +
		"# POST " + base + "/sessions/ID/send     {\"message\": \"...\"}           (send)\n" +
		"# POST " + base + "/sessions/ID/approve  {\"choice\": \"once\"}          (approve)\n" +
		"# POST " + base + "/sessions             {\"path\",\"title\",\"tool\",\"message\"}  (launch)\n"
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

// rules are the safety rules every connection carries. example is a
// command that grants more scope, shown in the scope rule.
func rules(example string) []string {
	return []string{
		"Call list_sessions first and refer to sessions by id.",
		"Read before acting: check session_status or read_output before you send or approve anything.",
		"ASK THE USER and wait for a clear yes before send_message, approve or launch_session; show the exact text you will send.",
		"Never stop, delete, restart or kill sessions. There is no tool for it; do not use a shell or any other way to do it.",
		"Never read, print or change credentials, tokens or agent-deck's own files (~/.agent-deck, ~/.local/share/agent-deck).",
		"If a tool answers \"scope missing\", do not work around it: tell the user the exact command from the answer (for example: " + example + ").",
		"Keep reports short: one line per session (title, status, what it is waiting for).",
	}
}

const aboutAgentDeck = "agent-deck is a terminal session manager for AI coding agents (Claude Code, Codex, Gemini, OpenCode and others): " +
	"each session is one agent working in its own terminal. Through the agent-deck tools you can see those sessions and, when allowed, drive them.\n"

func writeGuide(b *strings.Builder, toolsHeader, example string) {
	b.WriteString(aboutAgentDeck)
	b.WriteString("\n" + toolsHeader + "\n")
	for _, l := range toolLines {
		b.WriteString("- " + l + "\n")
	}
	b.WriteString("\nRules:\n")
	for i, r := range rules(example) {
		fmt.Fprintf(b, "%d. %s\n", i+1, r)
	}
}

// ContextPrompt is the short guide the assistant keeps.
func ContextPrompt(c *Connection, kind Kind) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are connected to agent-deck on my machine as %q with scopes: %s.\n", c.Name, ScopeList(c.Scopes))
	rec := Record{Name: c.Name, Assistant: kind.ID, Scopes: c.Scopes, Profile: c.Profile}
	writeGuide(&b, "Tools (MCP server \""+ServerKey+"\"; the HTTP API at "+c.HTTPAPI.Base+" mirrors them):", RaiseScopeCommand(rec, ScopeSend))
	return b.String()
}

// SkillFile is the ready SKILL.md for kindID (docs/assistants/skills). It
// carries no name or token: whoami tells the assistant who it is.
func SkillFile(kindID string) string {
	var b strings.Builder
	b.WriteString("---\nname: agent-deck\n")
	b.WriteString("description: See and, when allowed, drive the user's agent-deck sessions (AI coding agents in terminals) " +
		"through the " + ServerKey + " MCP tools. Use when the user asks about their agent-deck sessions or coding agents, " +
		"what is running or waiting, or wants to message, approve or launch a session.\n")
	if kindID == "hermes" {
		b.WriteString("version: 1.0.0\n")
	}
	if kindID == "openclaw" {
		b.WriteString(`metadata: { "openclaw": { "requires": { "bins": ["agent-deck"] } } }` + "\n")
	}
	b.WriteString("---\n\n# agent-deck\n\n")
	b.WriteString("You reach agent-deck through the MCP server \"" + ServerKey + "\" (your client may prefix the tool names). " +
		"Call whoami to see your connection name and scopes.\n")
	writeGuide(&b, "Tools:", "agent-deck connect "+kindID+" --scopes read,send")
	b.WriteString("\nSetup, scopes and troubleshooting: " + strings.TrimSuffix(SetupPromptURL, "SETUP-PROMPT.md") + "README.md\n")
	return b.String()
}

// ServerInstructions is the MCP initialize instructions text for rec.
func ServerInstructions(rec Record) string {
	return "agent-deck manages terminal sessions of AI coding agents. This connection (" + rec.Name + ") has scopes: " +
		ScopeList(rec.Scopes) + ". Rules: " + strings.Join(rules(RaiseScopeCommand(rec, ScopeSend)), " ")
}

func renderBlock(c *Connection, kind Kind) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== agent-deck connect: %s (name %q, scopes %s) ===\n", kind.Label, c.Name, ScopeList(c.Scopes))
	fmt.Fprintf(&b, "Token file: %s (0600). Revoke: %s connect revoke %s\n", c.TokenFile, deckCommand(c.Profile), shellescape.Quote(c.Name))
	if kind.NeedsWeb {
		b.WriteString("Needs `agent-deck web` running and reachable at " + baseOf(c) + ".\n")
	}
	if kind.Note != "" {
		b.WriteString(kind.Note + "\n")
	}
	b.WriteString("\n--- 1. Config: " + c.ConfigPathHint + " ---\n")
	b.WriteString(strings.TrimRight(c.ConfigSnippet, "\n") + "\n")
	b.WriteString("\n--- 2. Test ---\n")
	if kind.clientTest != "" {
		b.WriteString(kind.clientTest + "\n")
	}
	b.WriteString(c.TestCommand + "\n")
	if c.SkillPath != "" {
		b.WriteString("\n--- 3. Guide: save the ready skill " + c.SkillURL + " as " + c.SkillPath + ", or keep this text ---\n")
	} else {
		b.WriteString("\n--- 3. Guide: keep this text in " + kind.guideHint + " ---\n")
	}
	b.WriteString(c.ContextPrompt)
	b.WriteString("\n" + c.RaiseScopeHint + "\n")
	b.WriteString("=== end ===\n")
	return b.String()
}
