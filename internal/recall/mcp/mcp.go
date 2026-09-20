// Package mcp is `agent-deck recall mcp`: a Model Context Protocol server
// over stdio (newline-delimited JSON-RPC 2.0) exposing three tools,
// recall_search, recall_show and recall_context, so an agent in any
// harness that speaks MCP can query the index directly instead of
// shelling out to the CLI. It is the one MCP server agent-deck has; it
// reads the same index the CLI does and never writes it beyond the
// bounded pre-search sweep the CLI also runs.
//
// The loop is synchronous: one request is read, answered, and the next is
// read, in the caller's goroutine. It ends when stdin closes.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

// ProtocolVersion is the MCP revision this server implements.
const ProtocolVersion = "2025-06-18"

// ServerName is reported in initialize.
const ServerName = "agent-deck-recall"

// Handler is what the tools call: the CLI's env behind an interface, so
// the protocol is testable without recall.db.
type Handler interface {
	// Search runs one search; note is the CLI's index note ("index is
	// behind by ...") or "".
	Search(ctx context.Context, opts query.SearchOptions) (query.SearchResult, string, error)
	Show(ctx context.Context, ref string, turns int) (query.Detail, error)
	Context(ctx context.Context, ref, tier string, budget int) (query.ContextResult, error)
}

// Server serves one stdio connection.
type Server struct {
	h       Handler
	version string
	in      io.Reader
	out     io.Writer
}

// New returns a Server reading requests from in and writing replies to out.
func New(h Handler, version string, in io.Reader, out io.Writer) *Server {
	return &Server{h: h, version: version, in: in, out: out}
}

// maxLine bounds one request line.
const maxLine = 16 << 20

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes.
const (
	codeParse         = -32700
	codeInvalidReq    = -32600
	codeMethodMissing = -32601
	codeInvalidParams = -32602
	codeInternal      = -32603
)

// Serve runs until in is closed or ctx is done.
func (s *Server) Serve(ctx context.Context) error {
	sc := bufio.NewScanner(s.in)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if err := s.writeError(nil, codeParse, "parse error: "+err.Error()); err != nil {
				return err
			}
			continue
		}
		if req.Method == "" {
			if err := s.writeError(req.ID, codeInvalidReq, "missing method"); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			// A notification: nothing is ever written back for one.
			continue
		}
		result, rerr := s.dispatch(ctx, req)
		if err := s.write(response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr}); err != nil {
			return err
		}
	}
	return sc.Err()
}

// writeError replies with a JSON-RPC error; an absent id is written as null.
func (s *Server) writeError(id json.RawMessage, code int, msg string) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return s.write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg}})
}

func (s *Server) write(resp response) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = s.out.Write(append(b, '\n'))
	return err
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": ServerName, "version": s.version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": Tools()}, nil
	case "tools/call":
		return s.call(ctx, req.Params)
	}
	return nil, &rpcError{codeMethodMissing, "method not found: " + req.Method}
}

const instructions = "Recall is agent-deck's index of every conversation on this machine (Claude, Codex, pi, Gemini, OpenCode, Hermes). " +
	"Use recall_search to find sessions, recall_show to read one (tier card first, then an excerpt), and recall_context to render one as context for the current task."

// Tool is one MCP tool description.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Tools lists the three tools with their JSON schemas.
func Tools() []Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	return []Tool{
		{
			Name:        "recall_search",
			Description: "Full-text search over every indexed conversation. Ranks sessions: a title, hint or tag hit outranks body mentions. Returns the same JSON as `agent-deck recall search --json`.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   str("Search terms (AND-ed; AND/OR/NOT and trailing * work)"),
					"harness": str("Only this harness: claude, codex, pi, gemini, opencode, hermes"),
					"profile": str("Only this profile's transcripts"),
					"project": str("Only sessions whose working directory is this path"),
					"since":   str("Only sessions active since: 30d, 12h or YYYY-MM-DD"),
					"session": str("Only the conversation bound to this agent-deck session id"),
					"hint":    map[string]any{"type": "object", "description": "Only sessions with these hints (key: value)", "additionalProperties": map[string]any{"type": "string"}},
					"tag":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only sessions with every one of these tags"},
					"role":    str("Only body hits in user or assistant messages"),
					"phrase":  map[string]any{"type": "boolean", "description": "Verify the literal phrase in the hits' bodies"},
					"limit":   integer("Sessions to return (default 20)"),
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "recall_show",
			Description: "One session: its card, derived summary (stale ones marked), tools, files and decoded messages. `session` is a #number from a search, a harness conversation id (or unique prefix) or an agent-deck session id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session": str("The session reference"),
					"tier":    str("card (no messages), excerpt (the first `turns` messages, default) or raw (every message)"),
					"turns":   integer("Messages for the excerpt tier (default 40)"),
				},
				"required": []string{"session"},
			},
		},
		{
			Name:        "recall_context",
			Description: "Render one session as plain-text context for the current task: card (about 60 tokens), brief (card + derived + files), or excerpt (brief + the newest turns under the token budget). A card pulled from another machine stops at brief.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session": str("The session reference"),
					"tier":    str("card, brief or excerpt (default excerpt)"),
					"budget":  integer("Token budget for the excerpt (default 4000)"),
				},
				"required": []string{"session"},
			},
		},
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) call(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "invalid params: " + err.Error()}
	}
	if len(p.Arguments) == 0 {
		p.Arguments = json.RawMessage("{}")
	}
	var text string
	var err error
	switch p.Name {
	case "recall_search":
		text, err = s.search(ctx, p.Arguments)
	case "recall_show":
		text, err = s.show(ctx, p.Arguments)
	case "recall_context":
		text, err = s.context(ctx, p.Arguments)
	default:
		return nil, &rpcError{codeInvalidParams, "unknown tool: " + p.Name}
	}
	if err != nil {
		var bad *argError
		if errors.As(err, &bad) {
			return nil, &rpcError{codeInvalidParams, err.Error()}
		}
		// A tool failure (not found, index off) is a tool result with
		// isError, as the protocol asks, so the model can read it.
		return toolResult(err.Error(), true), nil
	}
	return toolResult(text, false), nil
}

func toolResult(text string, isError bool) map[string]any {
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if isError {
		res["isError"] = true
	}
	return res
}

type argError struct{ msg string }

func (e *argError) Error() string { return e.msg }

func badArgs(format string, a ...any) error { return &argError{fmt.Sprintf(format, a...)} }

type searchArgs struct {
	Query   string            `json:"query"`
	Harness string            `json:"harness"`
	Profile string            `json:"profile"`
	Project string            `json:"project"`
	Since   string            `json:"since"`
	Session string            `json:"session"`
	Hint    map[string]string `json:"hint"`
	Tag     []string          `json:"tag"`
	Role    string            `json:"role"`
	Phrase  bool              `json:"phrase"`
	Limit   int               `json:"limit"`
}

// SinceParser turns the CLI's --since forms into a time; the CLI sets it.
var SinceParser func(string) (int64, error)

func (s *Server) search(ctx context.Context, raw json.RawMessage) (string, error) {
	var a searchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", badArgs("recall_search: %v", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", badArgs("recall_search: query is required")
	}
	opts := query.SearchOptions{Query: a.Query, Phrase: a.Phrase, Limit: a.Limit}
	opts.Harness, opts.Profile, opts.Project, opts.DeckID = a.Harness, a.Profile, a.Project, a.Session
	opts.Tags, opts.Hints = a.Tag, a.Hint
	switch strings.ToLower(a.Role) {
	case "":
	case "user":
		opts.Role = 1
	case "assistant":
		opts.Role = 2
	default:
		return "", badArgs("recall_search: role must be user or assistant")
	}
	if a.Since != "" {
		if SinceParser == nil {
			return "", badArgs("recall_search: since is not supported here")
		}
		ts, err := SinceParser(a.Since)
		if err != nil {
			return "", badArgs("recall_search: %v", err)
		}
		opts.Since = time.Unix(ts, 0)
	}
	res, note, err := s.h.Search(ctx, opts)
	if err != nil {
		return "", err
	}
	out := map[string]any{"result": res}
	if note != "" {
		out["index"] = note
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type showArgs struct {
	Session string `json:"session"`
	Tier    string `json:"tier"`
	Turns   int    `json:"turns"`
}

func (s *Server) show(ctx context.Context, raw json.RawMessage) (string, error) {
	var a showArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", badArgs("recall_show: %v", err)
	}
	if a.Session == "" {
		return "", badArgs("recall_show: session is required")
	}
	turns := a.Turns
	switch a.Tier {
	case "card":
		turns = -1
	case "", "excerpt":
		if turns <= 0 {
			turns = 40
		}
	case "raw":
		turns = 0
	default:
		return "", badArgs("recall_show: tier must be card, excerpt or raw")
	}
	d, err := s.h.Show(ctx, a.Session, turns)
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type contextArgs struct {
	Session string `json:"session"`
	Tier    string `json:"tier"`
	Budget  int    `json:"budget"`
}

func (s *Server) context(ctx context.Context, raw json.RawMessage) (string, error) {
	var a contextArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", badArgs("recall_context: %v", err)
	}
	if a.Session == "" {
		return "", badArgs("recall_context: session is required")
	}
	if a.Tier == "" {
		a.Tier = query.TierExcerpt
	}
	res, err := s.h.Context(ctx, a.Session, a.Tier, a.Budget)
	if err != nil {
		if errors.Is(err, query.ErrTier) {
			return "", badArgs("%v", err)
		}
		return "", err
	}
	return res.Text, nil
}
