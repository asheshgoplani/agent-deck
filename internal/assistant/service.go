package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var assistantLog = logging.ForComponent(logging.CompWeb)

// Error codes a call can fail with.
const (
	CodeUnauthorized = "unauthorized"
	CodeScopeMissing = "scope_missing"
	CodeRateLimited  = "rate_limited"
	CodeInvalid      = "invalid_arguments"
	CodeUnknownTool  = "unknown_tool"
	CodeReadOnly     = "read_only"
	CodeFailed       = "command_failed"
)

// Transports, as written to the audit log.
const (
	TransportStdio = "mcp-stdio"
	TransportHTTP  = "mcp-http"
	TransportAPI   = "http-api"
)

// CallError is a refused or failed call.
type CallError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Hint tells the assistant what to tell its user (for scope_missing:
	// the exact command that grants the scope).
	Hint string `json:"hint,omitempty"`
}

func (e *CallError) Error() string {
	if e.Hint != "" {
		return e.Message + ". " + e.Hint
	}
	return e.Message
}

// Result is a finished call: Data (JSON-encodable) or Text, or Err.
type Result struct {
	Data any
	Text string
	Err  *CallError
}

// Service checks, limits, audits and runs assistant tool calls. It is
// shared by the stdio MCP server, the HTTP MCP endpoint and the JSON API.
type Service struct {
	Store  *Store
	Exec   Executor
	Limits Limits
	// ReadOnly refuses every write tool (send, approve, launch), whatever
	// the token's scopes. `agent-deck web --read-only` sets it.
	ReadOnly bool
	// Version is reported in MCP initialize.
	Version string

	limiter *rateLimiter
	audit   *auditLog
	now     func() time.Time
}

// NewService builds a Service over store with DefaultLimits.
func NewService(store *Store, exec Executor, version string) *Service {
	return NewServiceWithLimits(store, exec, version, DefaultLimits)
}

// NewServiceWithLimits is NewService with explicit rate limits.
func NewServiceWithLimits(store *Store, exec Executor, version string, limits Limits) *Service {
	return &Service{
		Store:   store,
		Exec:    exec,
		Limits:  limits,
		Version: version,
		limiter: newRateLimiter(limits),
		audit:   &auditLog{path: store.AuditPath()},
		now:     time.Now,
	}
}

// Authenticate maps a raw token to its record.
func (s *Service) Authenticate(token string) (Record, error) {
	return s.Store.Authenticate(token)
}

// RaiseScopeCommand is the command that re-issues name's token with scopes
// plus want. Running it rotates the token.
func RaiseScopeCommand(rec Record, want Scope) string {
	scopes := withScope(rec.Scopes, want)
	cmd := "agent-deck"
	if rec.Profile != "" && rec.Profile != "default" {
		cmd += " -p " + rec.Profile
	}
	kind := rec.Assistant
	if kind == "" {
		kind = rec.Name
	}
	cmd += " connect " + kind
	if rec.Name != kind {
		cmd += " --name " + rec.Name
	}
	return cmd + " --scopes " + ScopeList(scopes)
}

// Call runs one tool for rec over transport. Every call, allowed or not,
// writes one audit line (never a message body).
func (s *Service) Call(ctx context.Context, rec Record, name string, rawArgs json.RawMessage, transport string) Result {
	entry := AuditEntry{TS: s.now().UTC(), Name: rec.Name, Tool: name, Transport: transport}
	deny := func(code, msg, hint string) Result {
		entry.Allowed, entry.Reason = false, code
		s.writeAudit(entry)
		return Result{Err: &CallError{Code: code, Message: msg, Hint: hint}}
	}

	tool, ok := LookupTool(name)
	// Every call, refused or not, spends from the token's budget first, so
	// a token cannot flood the audit log with unknown-tool, bad-argument or
	// scope-missing calls.
	if !s.limiter.allow(rec.Name, ok && tool.Write) {
		limit := s.Limits.ReadPerMinute
		if ok && tool.Write {
			limit = s.Limits.WritePerMinute
		}
		return deny(CodeRateLimited, fmt.Sprintf("rate limit reached (%d calls per minute); wait and retry", limit), "")
	}
	if !ok {
		return deny(CodeUnknownTool, fmt.Sprintf("unknown tool %q", name), "")
	}
	args, err := decodeArgs(rawArgs)
	if err != nil {
		return deny(CodeInvalid, err.Error(), "")
	}
	if ref, _ := args.str("session"); ref != "" {
		entry.Session = clip(ref, 256)
	} else if title, _ := args.str("title"); title != "" && tool.Name == ToolLaunchSession {
		entry.Session = clip(title, 256)
	}
	if tool.Scope != "" && !HasScope(rec.Scopes, tool.Scope) {
		return deny(CodeScopeMissing,
			fmt.Sprintf("scope %q missing: this connection (%s) has %s", tool.Scope, rec.Name, ScopeList(rec.Scopes)),
			"Do not work around this. Tell the user they can grant it by running: "+RaiseScopeCommand(rec, tool.Scope))
	}
	if tool.Write && s.ReadOnly {
		return deny(CodeReadOnly, "this agent-deck server is read-only; "+tool.Name+" is disabled", "")
	}

	data, _, err := s.runTool(ctx, rec, tool, args)
	if err != nil {
		var inv invalidArgs
		if errors.As(err, &inv) {
			return deny(CodeInvalid, err.Error(), "")
		}
		entry.Allowed, entry.Reason = true, CodeFailed
		s.writeAudit(entry)
		return Result{Err: &CallError{Code: CodeFailed, Message: err.Error()}}
	}
	entry.Allowed = true
	s.writeAudit(entry)
	if text, ok := data.(string); ok {
		return Result{Text: text}
	}
	return Result{Data: data}
}

func (s *Service) writeAudit(e AuditEntry) {
	if err := s.audit.write(e); err != nil {
		assistantLog.Warn("assistant_audit_write_failed", slog.String("error", err.Error()))
	}
}

// ResultText renders a result as the text an assistant reads.
func ResultText(r Result) string {
	if r.Err != nil {
		return r.Err.Error()
	}
	if r.Data == nil {
		return r.Text
	}
	b, err := json.MarshalIndent(r.Data, "", "  ")
	if err != nil {
		return fmt.Sprint(r.Data)
	}
	return string(b)
}
