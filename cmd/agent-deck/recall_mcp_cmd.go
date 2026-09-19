package main

import (
	"context"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/recall/mcp"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

// `agent-deck recall mcp` (docs/recall.md "MCP"): the stdio MCP server an
// agent attaches like any other MCP. `mcp list` offers it as the built-in
// `recall` entry while [recall] is enabled (session.RecallMCPDef), so
// `agent-deck mcp attach <session> recall` is all it takes.

func handleRecallMCP(profile string, args []string) {
	fs := newRecallFlagSet("recall mcp")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `Usage: agent-deck recall mcp

Serve recall_search, recall_show and recall_context to an MCP client over
stdio (newline-delimited JSON-RPC 2.0). Attach it to a session with
'agent-deck mcp attach <session> recall' (listed by 'mcp list' while
[recall] enabled = true), or configure it by hand:
  command = "agent-deck", args = ["recall", "mcp"]
The server reads the same index the CLI does and exits when stdin closes.`)
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	// Errors go to stderr as one line: stdout is the protocol channel.
	out := NewCLIOutput(false, false)
	env := openRecallEnv(profile, out)
	defer env.close()
	mcp.SinceParser = func(s string) (int64, error) {
		t, err := parseRecallSince(s)
		if err != nil {
			return 0, err
		}
		return t.Unix(), nil
	}
	ctx, cancel := interruptibleContext()
	defer cancel()
	srv := mcp.New(&recallMCPHandler{env: env}, Version, os.Stdin, os.Stdout)
	if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "recall mcp:", err)
		os.Exit(1)
	}
}

// recallMCPHandler answers the tools from the open env; a search runs the
// same bounded sweep the CLI runs before it.
type recallMCPHandler struct {
	env *recallEnv
}

func (h *recallMCPHandler) Search(ctx context.Context, opts query.SearchOptions) (query.SearchResult, string, error) {
	note := h.env.interactiveSweep(ctx)
	res, err := query.New(h.env.st, h.env.stateDB).Search(ctx, opts)
	return res, note.String(), err
}

func (h *recallMCPHandler) Show(ctx context.Context, ref string, turns int) (query.Detail, error) {
	return query.New(h.env.st, h.env.stateDB).Show(ctx, ref, turns)
}

func (h *recallMCPHandler) Context(ctx context.Context, ref, tier string, budget int) (query.ContextResult, error) {
	return query.New(h.env.st, h.env.stateDB).Context(ctx, ref, tier, budget)
}
