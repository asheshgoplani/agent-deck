package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// ProtocolVersion is the newest MCP revision served. initialize echoes a
// client's requested revision when it is one of supportedProtocolVersions.
const ProtocolVersion = "2025-06-18"

var supportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// ServerName is reported in initialize.
const ServerName = "agent-deck"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes. codeUnauthorized is server-defined.
const (
	codeParse         = -32700
	codeInvalidReq    = -32600
	codeMethodMissing = -32601
	codeInvalidParams = -32602
	codeUnauthorized  = -32001
)

// TokenSource returns the current raw token for a connection. The stdio
// server re-reads its token file on every request, so a rotated token
// (`connect <name> --scopes ...`) is picked up without a restart.
type TokenSource func() (string, error)

// MCPHandler answers MCP JSON-RPC messages for one transport.
type MCPHandler struct {
	svc       *Service
	transport string
}

// NewMCPHandler returns a handler for transport (TransportStdio or
// TransportHTTP).
func NewMCPHandler(svc *Service, transport string) *MCPHandler {
	return &MCPHandler{svc: svc, transport: transport}
}

// Handle answers one JSON-RPC message. It returns nil for a notification
// (nothing is ever written back for one). The token is authenticated on
// every message, so a revoke takes effect on the next call.
func (h *MCPHandler) Handle(ctx context.Context, tokens TokenSource, line []byte) []byte {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return marshalResp(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeParse, "parse error: " + err.Error()}})
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	if req.Method == "" {
		if isNotification {
			// A response from the client (we never send requests) or junk.
			return nil
		}
		return marshalResp(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidReq, "missing method"}})
	}
	if isNotification {
		return nil
	}
	rec, err := h.authenticate(tokens)
	if err != nil {
		return marshalResp(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeUnauthorized, err.Error()}})
	}
	result, rerr := h.dispatch(ctx, rec, req)
	return marshalResp(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr})
}

func (h *MCPHandler) authenticate(tokens TokenSource) (Record, error) {
	if tokens == nil {
		return Record{}, ErrUnknownToken
	}
	tok, err := tokens()
	if err != nil {
		return Record{}, err
	}
	return h.svc.Authenticate(tok)
}

func marshalResp(r rpcResponse) []byte {
	b, err := json.Marshal(r)
	if err != nil {
		b, _ = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: r.ID, Error: &rpcError{-32603, "internal error"}})
	}
	return b
}

func (h *MCPHandler) dispatch(ctx context.Context, rec Record, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := ProtocolVersion
		for _, v := range supportedProtocolVersions {
			if p.ProtocolVersion == v {
				version = v
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": ServerName, "version": h.svc.Version},
			"instructions":    ServerInstructions(rec),
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": Tools()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{codeInvalidParams, "tools/call needs params.name"}
		}
		if _, ok := LookupTool(p.Name); !ok {
			return nil, &rpcError{codeInvalidParams, "unknown tool: " + p.Name}
		}
		res := h.svc.Call(ctx, rec, p.Name, p.Arguments, h.transport)
		out := map[string]any{
			"content": []map[string]any{{"type": "text", "text": ResultText(res)}},
			"isError": res.Err != nil,
		}
		if res.Err != nil {
			out["structuredContent"] = map[string]any{"error": res.Err}
		} else if m, ok := res.Data.(map[string]any); ok {
			out["structuredContent"] = m
		}
		return out, nil
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	}
	return nil, &rpcError{codeMethodMissing, "method not found: " + req.Method}
}

// maxLine bounds one stdio request line.
const maxLine = 4 << 20

// ServeStdio runs newline-delimited JSON-RPC over in/out until in closes or
// ctx is done. Requests are answered one at a time.
func (h *MCPHandler) ServeStdio(ctx context.Context, tokens TokenSource, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		resp := h.Handle(ctx, tokens, []byte(line))
		if resp == nil {
			continue
		}
		if _, err := out.Write(append(resp, '\n')); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
