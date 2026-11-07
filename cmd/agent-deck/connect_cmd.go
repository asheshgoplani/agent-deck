package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/assistant"
	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck connect`: give an outside assistant (Hermes, OpenClaw, Grok,
// Claude, ChatGPT, Codex, Slack/Telegram bots, any MCP or HTTP client) a
// scoped token and one copy-paste block. `mcp` is already the MCP-attach
// command, hence the separate name. Read-only by default.

func printConnectHelp() {
	fmt.Println("Usage: agent-deck connect [assistant] [options]")
	fmt.Println()
	fmt.Println("Connect an assistant bot to agent-deck with one scoped token. Prints one")
	fmt.Println("copy-paste block: the config for that assistant and a short guide it keeps.")
	fmt.Println("Read-only by default; send, approve and launch are opt-in.")
	fmt.Println()
	fmt.Println("Assistants (optional; without one you get a generic block the assistant adapts):")
	for _, k := range assistant.Kinds() {
		fmt.Printf("  %-10s %s\n", k.ID, k.Label)
	}
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --scopes LIST   Comma-separated: read,send,approve,launch (default read)")
	fmt.Println("  --name NAME     Token name (default: the assistant). Reusing a name rotates its token")
	fmt.Println("  --url URL       agent-deck web address for HTTP snippets (default " + assistant.DefaultURL + ")")
	fmt.Println("  --json          Print the connection as JSON (token, config, guide)")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  connect list [--json]                  List connected assistants and their scopes")
	fmt.Println("  connect revoke <name>                  Revoke a token (effective on its next call)")
	fmt.Println("  connect audit [--name N] [-n 50] [--json]  Show recent assistant calls")
	fmt.Println("  connect serve [--token-file F]         MCP server over stdio (token from F or $" + assistant.TokenEnvVar + ")")
	fmt.Println("  connect test [--token-file F] [--name N] [--url U]  Check a token end to end (list_sessions)")
	fmt.Println()
	fmt.Println("Tools: whoami, list_sessions, session_status, read_output (read), send_message (send),")
	fmt.Println("approve (approve), launch_session (launch). There is no stop/remove/restart tool.")
	fmt.Println("Over HTTP (agent-deck web): POST /mcp and /api/assistant/v1/..., bearer token only.")
	fmt.Println("Rate limits per token: 120 read and 10 write calls per minute. Audit log:")
	fmt.Println("assistants-audit.jsonl in the agent-deck data directory (never message bodies).")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck connect                         # generic block, read-only")
	fmt.Println("  agent-deck connect hermes                  # Hermes config + guide")
	fmt.Println("  agent-deck connect claude --scopes read,send")
	fmt.Println("  agent-deck connect telegram --url https://deck.example.ts.net --json")
	fmt.Println("  agent-deck connect revoke hermes")
}

func handleConnect(profile string, args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "help", "--help", "-h":
			printConnectHelp()
			return
		case "list", "ls":
			handleConnectList(args[1:])
			return
		case "revoke":
			handleConnectRevoke(args[1:])
			return
		case "audit":
			handleConnectAudit(args[1:])
			return
		case "serve":
			handleConnectServe(args[1:])
			return
		case "test":
			handleConnectTest(args[1:])
			return
		}
	}
	handleConnectIssue(profile, args)
}

// connectStore opens the token store in the agent-deck data directory.
func connectStore() (*assistant.Store, error) {
	dir, err := session.GetAgentDeckDir()
	if err != nil {
		return nil, fmt.Errorf("resolving agent-deck data directory: %w", err)
	}
	return assistant.NewStore(dir), nil
}

// connectBinaryPath is the agent-deck path written into assistant configs:
// the PATH entry when it is this same binary (a stable symlink survives
// upgrades), else this executable.
func connectBinaryPath() string {
	self, err := os.Executable()
	if err != nil {
		return "agent-deck"
	}
	selfReal, _ := filepath.EvalSymlinks(self)
	if onPath, err := exec.LookPath("agent-deck"); err == nil {
		if abs, err := filepath.Abs(onPath); err == nil {
			if real, err := filepath.EvalSymlinks(abs); err == nil && real == selfReal {
				return abs
			}
		}
	}
	return self
}

func connectFail(jsonOut bool, msg string) {
	if jsonOut {
		b, _ := json.Marshal(map[string]any{"success": false, "error": msg})
		fmt.Println(string(b))
	} else {
		fmt.Fprintln(os.Stderr, "Error: "+msg)
	}
	exitCLI(1)
}

func handleConnectIssue(profile string, args []string) {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	scopes := fs.String("scopes", "", "Comma-separated scopes: read,send,approve,launch (default read)")
	name := fs.String("name", "", "Token name (default: the assistant)")
	baseURL := fs.String("url", "", "agent-deck web base URL for HTTP snippets (default "+assistant.DefaultURL+")")
	jsonOut := fs.Bool("json", false, "Print the connection as JSON")
	fs.Usage = printConnectHelp
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	if fs.NArg() > 1 {
		connectFail(*jsonOut, "connect takes at most one assistant name; see agent-deck connect --help")
	}
	kind := fs.Arg(0)
	if _, ok := assistant.LookupKind(kind); !ok {
		connectFail(*jsonOut, fmt.Sprintf("unknown assistant %q (known: %s; or use mcp/http for any other client)", kind, strings.Join(assistant.KindIDs(), ", ")))
	}
	sc, err := assistant.ParseScopes(*scopes)
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	if *baseURL != "" && !strings.HasPrefix(*baseURL, "http://") && !strings.HasPrefix(*baseURL, "https://") {
		connectFail(*jsonOut, "--url must start with http:// or https://")
	}
	resolved, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		connectFail(*jsonOut, fmt.Sprintf("resolving profile: %v", err))
	}
	store, err := connectStore()
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	conn, err := assistant.Issue(store, assistant.ConnectOptions{
		Kind: kind, Name: *name, Scopes: sc, Profile: resolved, URL: *baseURL, Command: connectBinaryPath(),
	})
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(conn)
		return
	}
	fmt.Print(conn.Block)
}

func handleConnectList(args []string) {
	fs := flag.NewFlagSet("connect list", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck connect list [--json]")
		fmt.Println()
		fmt.Println("List connected assistants: name, assistant, scopes, profile, created.")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	store, err := connectStore()
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	recs, err := store.List()
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	if *jsonOut {
		type row struct {
			Name      string            `json:"name"`
			Assistant string            `json:"assistant"`
			Scopes    []assistant.Scope `json:"scopes"`
			Profile   string            `json:"profile,omitempty"`
			CreatedAt time.Time         `json:"created_at"`
			TokenFile string            `json:"token_file"`
		}
		rows := make([]row, 0, len(recs))
		for _, r := range recs {
			rows = append(rows, row{r.Name, r.Assistant, r.Scopes, r.Profile, r.CreatedAt, store.TokenFile(r.Name)})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return
	}
	if len(recs) == 0 {
		fmt.Println("No assistants connected. Run: agent-deck connect [assistant]")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tASSISTANT\tSCOPES\tPROFILE\tCREATED")
	for _, r := range recs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Assistant, assistant.ScopeList(r.Scopes), r.Profile, r.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	_ = tw.Flush()
}

func handleConnectRevoke(args []string) {
	fs := flag.NewFlagSet("connect revoke", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck connect revoke <name>")
		fmt.Println()
		fmt.Println("Delete an assistant token. A running server refuses it on its next call.")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	if fs.NArg() != 1 {
		fs.Usage()
		exitCLI(2)
	}
	name := fs.Arg(0)
	store, err := connectStore()
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	ok, err := store.Revoke(name)
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	if !ok {
		connectFail(*jsonOut, fmt.Sprintf("no assistant token named %q (see agent-deck connect list)", name))
	}
	if *jsonOut {
		fmt.Printf("{\"success\":true,\"revoked\":%q}\n", name)
		return
	}
	fmt.Printf("Revoked %s.\n", name)
}

func handleConnectAudit(args []string) {
	fs := flag.NewFlagSet("connect audit", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	name := fs.String("name", "", "Only calls by this token name")
	n := fs.Int("n", 50, "Number of entries")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck connect audit [--name N] [-n 50] [--json]")
		fmt.Println()
		fmt.Println("Show recent assistant calls, allowed and refused. Message bodies are never logged.")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	store, err := connectStore()
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	entries, err := assistant.ReadAudit(store.AuditPath(), *name, *n)
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	if *jsonOut {
		if entries == nil {
			entries = []assistant.AuditEntry{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(entries)
		return
	}
	if len(entries) == 0 {
		fmt.Println("No assistant calls logged yet.")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tNAME\tTOOL\tSESSION\tRESULT\tTRANSPORT")
	for _, e := range entries {
		res := "ok"
		if !e.Allowed {
			res = "denied: " + e.Reason
		} else if e.Reason != "" {
			res = e.Reason
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", e.TS.Local().Format("2006-01-02 15:04:05"), e.Name, e.Tool, truncateWidth(e.Session, 32), res, e.Transport)
	}
	_ = tw.Flush()
}

// connectTokenSource returns where serve/test read the token: --token-file
// (re-read on every call, so a rotation is picked up), else the env var.
func connectTokenSource(tokenFile string) (assistant.TokenSource, error) {
	if tokenFile != "" {
		if _, err := assistant.ReadTokenFile(tokenFile); err != nil {
			return nil, err
		}
		return func() (string, error) { return assistant.ReadTokenFile(tokenFile) }, nil
	}
	tok := strings.TrimSpace(os.Getenv(assistant.TokenEnvVar))
	if tok == "" {
		return nil, errors.New("no token: pass --token-file or set " + assistant.TokenEnvVar)
	}
	return func() (string, error) { return tok, nil }, nil
}

func handleConnectServe(args []string) {
	fs := flag.NewFlagSet("connect serve", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	tokenFile := fs.String("token-file", "", "File holding the assistant token (default $"+assistant.TokenEnvVar+")")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck connect serve [--token-file F]")
		fmt.Println()
		fmt.Println("Serve the agent-deck assistant tools over stdio MCP (newline-delimited JSON-RPC 2.0).")
		fmt.Println("An MCP client starts this; the token's scopes decide what it may do. Exits when stdin closes.")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	// Errors go to stderr: stdout is the protocol channel.
	tokens, err := connectTokenSource(*tokenFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect serve:", err)
		exitCLI(1)
	}
	store, err := connectStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect serve:", err)
		exitCLI(1)
	}
	tok, err := tokens()
	if err == nil {
		_, err = store.Authenticate(tok)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect serve:", err)
		exitCLI(1)
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect serve: locating agent-deck binary:", err)
		exitCLI(1)
	}
	svc := assistant.NewService(store, assistant.BinaryExecutor{Path: self}, Version)
	ctx, cancel := interruptibleContext()
	defer cancel()
	h := assistant.NewMCPHandler(svc, assistant.TransportStdio)
	if err := h.ServeStdio(ctx, tokens, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "connect serve:", err)
		exitCLI(1)
	}
}

func handleConnectTest(args []string) {
	fs := flag.NewFlagSet("connect test", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	tokenFile := fs.String("token-file", "", "File holding the assistant token")
	name := fs.String("name", "", "Test the token saved for this name (instead of --token-file)")
	baseURL := fs.String("url", "", "Test over HTTP against this agent-deck web URL (default: local stdio bridge)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck connect test [--token-file F | --name N] [--url U] [--json]")
		fmt.Println()
		fmt.Println("Run list_sessions through the bridge with a token: over stdio (agent-deck connect serve)")
		fmt.Println("or, with --url, over HTTP (POST <url>/mcp). Prints ok and the session count.")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	file := *tokenFile
	if file == "" && *name != "" {
		store, err := connectStore()
		if err != nil {
			connectFail(*jsonOut, err.Error())
		}
		file = store.TokenFile(*name)
	}
	tokens, err := connectTokenSource(file)
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	tok, err := tokens()
	if err != nil {
		connectFail(*jsonOut, err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var count int
	transport := "stdio"
	if *baseURL != "" {
		transport = "http"
		count, err = connectTestHTTP(ctx, strings.TrimRight(*baseURL, "/")+assistant.MCPPath, tok)
	} else {
		count, err = connectTestStdio(ctx, file, tok)
	}
	if err != nil {
		connectFail(*jsonOut, "test failed: "+err.Error())
	}
	if *jsonOut {
		fmt.Printf("{\"success\":true,\"transport\":%q,\"sessions\":%d}\n", transport, count)
		return
	}
	fmt.Printf("ok: list_sessions over %s returned %d session(s)\n", transport, count)
}

var connectTestRequests = []string{
	`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + assistant.ProtocolVersion + `","capabilities":{},"clientInfo":{"name":"agent-deck-connect-test","version":"1"}}}`,
	`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_sessions","arguments":{}}}`,
}

// connectTestCount reads the session count out of a tools/call reply.
func connectTestCount(reply []byte) (int, error) {
	var resp struct {
		Result *struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(reply, &resp); err != nil {
		return 0, fmt.Errorf("bad reply: %w", err)
	}
	if resp.Error != nil {
		return 0, errors.New(resp.Error.Message)
	}
	if resp.Result == nil {
		return 0, errors.New("empty reply")
	}
	if resp.Result.IsError {
		msg := "tool error"
		if len(resp.Result.Content) > 0 {
			msg = resp.Result.Content[0].Text
		}
		return 0, errors.New(msg)
	}
	if c, ok := resp.Result.StructuredContent["count"].(float64); ok {
		return int(c), nil
	}
	return 0, errors.New("reply has no session count")
}

func connectTestStdio(ctx context.Context, tokenFile, tok string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	args := []string{"connect", "serve"}
	env := childenv.ForSelfReexec()
	if tokenFile != "" {
		args = append(args, "--token-file", tokenFile)
	} else {
		env = append(childenv.ForSelfReexec(assistant.TokenEnvVar), assistant.TokenEnvVar+"="+tok)
	}
	cmd := exec.CommandContext(ctx, self, args...) //nolint:gosec // G204: re-exec of this binary with fixed args
	cmd.Env = env
	cmd.Stdin = strings.NewReader(strings.Join(connectTestRequests, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var last []byte
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if bytes.Contains(sc.Bytes(), []byte(`"id":2`)) {
			last = append([]byte(nil), sc.Bytes()...)
		}
	}
	if last == nil {
		return 0, errors.New("no reply to tools/call")
	}
	return connectTestCount(last)
}

func connectTestHTTP(ctx context.Context, url, tok string) (int, error) {
	var last []byte
	for _, body := range connectTestRequests {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusAccepted {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		last = data
	}
	return connectTestCount(last)
}
