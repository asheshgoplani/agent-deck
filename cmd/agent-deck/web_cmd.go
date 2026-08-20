package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

// buildWebServer parses web-specific flags and returns a ready-to-start server.
// The caller is responsible for calling server.Start() and server.Shutdown().
//
// mutator is wired via Server.SetMutator. Pass nil only in tests that don't
// exercise mutation handlers — production callers MUST pass a real mutator
// or every POST/PATCH/DELETE will 503 with NOT_IMPLEMENTED. See
// TestBuildWebServer_WiresMutator for the regression guard on this contract.
func buildWebServer(profile string, args []string, menuData web.MenuDataLoader, mutator web.SessionMutator) (*web.Server, error) {
	options, err := parseWebCommandOptions(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		return nil, err
	}
	return buildWebServerFromOptions(profile, options, menuData, mutator)
}

type webCommandOptions struct {
	listenAddr       string
	readOnly         bool
	token            string
	tokenFile        string
	insecureBind     bool
	pushEnabled      bool
	pushVAPIDSubject string
	pushTestEvery    time.Duration
	noTUI            bool
}

func parseWebCommandOptions(args []string) (webCommandOptions, error) {
	var options webCommandOptions
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.StringVar(&options.listenAddr, "listen", "127.0.0.1:8420", "Listen address for web server")
	fs.BoolVar(&options.readOnly, "read-only", false, "Run in read-only mode (input disabled)")
	fs.StringVar(&options.token, "token", "", "Bearer token for API/WS access")
	fs.StringVar(&options.tokenFile, "token-file", "", "Read bearer token for API/WS access from a 0600 file (keeps the secret out of the process argv)")
	fs.BoolVar(&options.insecureBind, "insecure-bind", false, "Allow binding a non-loopback address with no --token or --token-file (UNSAFE: exposes an unauthenticated RCE surface to the network)")
	fs.BoolVar(&options.pushEnabled, "push", false, "Enable web push notifications (auto-generates VAPID keys per profile)")
	fs.StringVar(&options.pushVAPIDSubject, "push-vapid-subject", "mailto:agentdeck@localhost", "VAPID subject used for web push notifications")
	fs.DurationVar(&options.pushTestEvery, "push-test-every", 0, "Send periodic push test notifications at this interval (e.g. 10s, 1m); 0 disables")
	fs.BoolVar(&options.noTUI, "no-tui", false, "Run in headless mode (HTTP server only, no bubbletea TUI)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck web [options]")
		fmt.Println()
		fmt.Println("Start the TUI with web UI server running alongside.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck web")
		fmt.Println("  agent-deck -p work web --listen 127.0.0.1:9000")
		fmt.Println("  agent-deck web --read-only")
		fmt.Println("  agent-deck web --push")
		fmt.Println("  agent-deck web --push --push-test-every 10s")
		fmt.Println("  agent-deck web --no-tui                 # headless, perf win")
		fmt.Println("  agent-deck web --no-tui --listen 127.0.0.1:9000")
		fmt.Println("  agent-deck web --listen 0.0.0.0:8420 --token secret  # expose to LAN (token REQUIRED)")
		fmt.Println("  agent-deck web --listen 0.0.0.0:8420 --token-file ~/.config/agent-deck/web-token")
		fmt.Println()
		fmt.Println("Security: the server binds loopback (127.0.0.1) by default. Binding a")
		fmt.Println("non-loopback address without --token or --token-file is refused — it would")
		fmt.Println("expose an unauthenticated remote-code-execution surface. Override with")
		fmt.Println("--insecure-bind (unsafe) only when you understand the risk.")
		fmt.Println("--token-file must be a regular file that is not group- or world-accessible")
		fmt.Println("(chmod 600); it keeps the secret out of argv, where any local user can read")
		fmt.Println("it from /proc. MCP administration over HTTP is only wired when a token is")
		fmt.Println("configured — an unauthenticated server keeps those routes unavailable.")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return webCommandOptions{}, err
	}
	if fs.NArg() > 0 {
		return webCommandOptions{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if options.pushTestEvery < 0 {
		return webCommandOptions{}, fmt.Errorf("--push-test-every must be >= 0")
	}
	if options.pushTestEvery > 0 && !options.pushEnabled {
		return webCommandOptions{}, fmt.Errorf("--push-test-every requires --push")
	}
	return options, nil
}

func buildWebServerFromOptions(profile string, options webCommandOptions, menuData web.MenuDataLoader, mutator web.SessionMutator) (*web.Server, error) {
	resolvedToken, err := resolveWebToken(options.token, options.tokenFile)
	if err != nil {
		return nil, err
	}

	// Report #1: refuse an unauthenticated non-loopback bind before the TUI
	// boots. Fails fast with an actionable error rather than silently exposing
	// an unauthenticated RCE surface (terminal bridge + session-create API).
	if err := web.CheckBindSecurity(options.listenAddr, resolvedToken, options.insecureBind); err != nil {
		return nil, err
	}

	// #1790/#1822 F1: route through the guarded resolver, not a bare
	// GetEffectiveProfile. The only current caller (main.go) already passes
	// an already-guarded value here, but re-deriving with GetEffectiveProfile
	// is a latent bypass for any other/future caller (this function is
	// unexported but its contract should not depend on caller discipline
	// alone) — EnsurePushVAPIDKeys below and NewSessionDataService inside
	// web.NewServer both go on to create on-disk profile state from this.
	effectiveProfile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve profile: %w", err)
	}

	resolvedPushSubject := options.pushVAPIDSubject
	resolvedPushPublic := ""
	resolvedPushPrivate := ""
	if options.pushEnabled {
		var generated bool
		var err error
		resolvedPushPublic, resolvedPushPrivate, generated, err = web.EnsurePushVAPIDKeys(effectiveProfile, resolvedPushSubject)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare web push keys: %w", err)
		}
		if generated {
			fmt.Println("Push keys: generated new VAPID keypair for profile")
		} else {
			fmt.Println("Push keys: using existing VAPID keypair for profile")
		}
	}

	confirmLinkOpen := session.GetWebConfirmLinkOpen()
	server := web.NewServer(web.Config{
		ListenAddr:          options.listenAddr,
		Profile:             effectiveProfile,
		ReadOnly:            options.readOnly,
		WebMutations:        resolveMutationsEnabled(options.readOnly),
		Token:               resolvedToken,
		InsecureBind:        options.insecureBind,
		TrustedDomains:      session.GetWebTrustedDomains(),
		ConfirmLinkOpen:     &confirmLinkOpen,
		MenuData:            menuData,
		PushVAPIDPublicKey:  resolvedPushPublic,
		PushVAPIDPrivateKey: resolvedPushPrivate,
		PushVAPIDSubject:    resolvedPushSubject,
		PushTestInterval:    options.pushTestEvery,
	})

	if mutator != nil {
		server.SetMutator(mutator)
	}
	// The MCP routes (/api/mcps and /api/sessions/{id}/mcps...) read and
	// rewrite .mcp.json and the Claude configs, i.e. they change which
	// programs an agent session will launch. Server.authorize() short-circuits
	// to "allowed" whenever no token is configured, so the ONLY thing keeping
	// those routes off an unauthenticated listener is refusing to wire the
	// production manager here. Keep this gate token-conditioned: with no
	// token — loopback default or --insecure-bind alike — the routes stay
	// registered but answer 503, which is the pre-existing behaviour and adds
	// no unauthenticated surface. See TestBuildWebServer_MCPRoutesAuth for the
	// endpoint-by-endpoint regression matrix.
	if resolvedToken != "" {
		server.SetMCPManager(web.NewDefaultMCPManager())
	}

	return server, nil
}

// maxWebTokenFileSize bounds a --token-file read. A bearer token is a short
// single-line secret; refusing to slurp an arbitrarily large file keeps a
// mistyped path (a log, a core dump, /dev/zero) from being read into memory
// and silently installed as the credential.
const maxWebTokenFileSize = 4096

// resolveWebToken folds --token and --token-file into the single bearer token
// the server authorizes against. Every failure path returns an error rather
// than an empty token: an empty token disables authorization entirely
// (Server.authorize allows everything when Config.Token is ""), so anything
// less than a clean read has to fail closed and stop the server from booting.
// Errors name the offending path but never echo file contents.
// validateInlineWebToken applies to --token the checks --token-file already
// performs. The two inputs feed the same field and deserve the same floor: a
// token carrying whitespace or control bytes can never match a Bearer header,
// so it boots a server nobody can authenticate to while still satisfying the
// non-loopback bind check — a lockout that looks like working auth.
//
// An empty token is the documented "authorization disabled" setting and passes
// through untouched. A token that TRIMS to empty is deliberately an error
// rather than a fall-through to that setting: `--token "   "` is an operator
// setting a token, and silently converting it into no-auth would be the one
// outcome this function exists to prevent.
func validateInlineWebToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	resolved := strings.TrimSpace(token)
	if resolved == "" {
		return "", fmt.Errorf("--token is only whitespace; pass a real token, or omit the flag to run without authorization")
	}
	if strings.ContainsFunc(resolved, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return "", fmt.Errorf("--token must be a single value with no whitespace or control characters")
	}
	return resolved, nil
}

func resolveWebToken(token, tokenFile string) (string, error) {
	if token != "" && tokenFile != "" {
		return "", fmt.Errorf("--token and --token-file are mutually exclusive")
	}
	if tokenFile == "" {
		return validateInlineWebToken(token)
	}

	f, err := os.Open(tokenFile)
	if err != nil {
		return "", fmt.Errorf("read --token-file: %w", err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("read --token-file: %w", err)
	}
	// Reject directories, devices and FIFOs: a non-regular source has no
	// stable contents to authorize against, and reading one can block the
	// whole boot.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("--token-file %s is not a regular file", tokenFile)
	}
	// The point of --token-file is to keep the secret off argv, where any
	// local user can read it from /proc. A group- or world-accessible file
	// gives that away again, so refuse it with an actionable fix.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("--token-file %s is group- or world-accessible (mode %#o); restrict it with: chmod 600 %s", tokenFile, perm, tokenFile)
	}
	if info.Size() > maxWebTokenFileSize {
		return "", fmt.Errorf("--token-file %s is larger than %d bytes; expected a single-line bearer token", tokenFile, maxWebTokenFileSize)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxWebTokenFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read --token-file: %w", err)
	}
	if len(data) > maxWebTokenFileSize {
		return "", fmt.Errorf("--token-file %s is larger than %d bytes; expected a single-line bearer token", tokenFile, maxWebTokenFileSize)
	}

	resolved := strings.TrimSpace(string(data))
	if resolved == "" {
		return "", fmt.Errorf("--token-file %s is empty", tokenFile)
	}
	// A token carrying interior whitespace or control bytes can never match a
	// Bearer header (headers cannot carry them), so it would boot a server
	// nobody can authenticate to while still satisfying the non-loopback bind
	// check — a lockout that looks like working auth. Reject it up front.
	if strings.ContainsFunc(resolved, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return "", fmt.Errorf("--token-file %s must contain a single line with no whitespace or control characters", tokenFile)
	}
	return resolved, nil
}

// resolveMutationsEnabled applies precedence: --read-only forces mutations off;
// otherwise the value comes from config.toml `[web].mutations_enabled`, which
// defaults to true when unset.
func resolveMutationsEnabled(readOnly bool) bool {
	if readOnly {
		return false
	}
	return session.GetWebMutationsEnabled()
}

// extractNoTuiFlag pulls --no-tui out of args before buildWebServer's flag
// set sees it. The TUI-vs-headless decision is made at the bootstrap layer
// in main.go (it controls whether bubbletea ever boots), so it lives outside
// the per-server flag set.
//
// Supports: --no-tui, --no-tui=true, --no-tui=false. Returns the parsed
// boolean and args with all --no-tui tokens removed (always a non-nil slice).
func extractNoTuiFlag(args []string) (bool, []string) {
	noTui := false
	remaining := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a == "--no-tui":
			noTui = true
		case strings.HasPrefix(a, "--no-tui="):
			v := strings.TrimPrefix(a, "--no-tui=")
			noTui = v == "true" || v == "1"
		default:
			remaining = append(remaining, a)
		}
	}
	return noTui, remaining
}
