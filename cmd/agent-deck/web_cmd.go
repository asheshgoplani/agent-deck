package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/genui"
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
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	listenAddr := fs.String("listen", "127.0.0.1:8420", "Listen address for web server")
	readOnly := fs.Bool("read-only", false, "Run in read-only mode (input disabled)")
	token := fs.String("token", "", "Bearer token for API/WS access")
	insecureBind := fs.Bool("insecure-bind", false, "Allow binding a non-loopback address with no --token (UNSAFE: exposes an unauthenticated RCE surface to the network)")
	pushEnabled := fs.Bool("push", false, "Enable web push notifications (auto-generates VAPID keys per profile)")
	pushVAPIDSubject := fs.String("push-vapid-subject", "mailto:agentdeck@localhost", "VAPID subject used for web push notifications")
	pushTestEvery := fs.Duration("push-test-every", 0, "Send periodic push test notifications at this interval (e.g. 10s, 1m); 0 disables")
	genuiComposerSession := fs.String("genui-composer-session", "", "Route the generative command center's intent→spec composition to this managed session (the no-API-key dogfood path). When empty, a deterministic no-LLM stub composer is used.")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck web [options]")
		fmt.Println()
		fmt.Println("Start the TUI with web UI server running alongside.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println("  --no-tui")
		fmt.Println("    \tRun in headless mode (HTTP server only, no bubbletea TUI).")
		fmt.Println("    \tSkips ~60 MB of TUI RSS overhead. Sessions remain manageable")
		fmt.Println("    \tvia the web UI; storage is the source of truth.")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck web")
		fmt.Println("  agent-deck -p work web --listen 127.0.0.1:9000")
		fmt.Println("  agent-deck web --read-only")
		fmt.Println("  agent-deck web --push")
		fmt.Println("  agent-deck web --push --push-test-every 10s")
		fmt.Println("  agent-deck web --no-tui                 # headless, perf win")
		fmt.Println("  agent-deck web --no-tui --listen 127.0.0.1:9000")
		fmt.Println("  agent-deck web --listen 0.0.0.0:8420 --token secret  # expose to LAN (token REQUIRED)")
		fmt.Println()
		fmt.Println("Security: the server binds loopback (127.0.0.1) by default. Binding a")
		fmt.Println("non-loopback address without --token is refused — it would expose an")
		fmt.Println("unauthenticated remote-code-execution surface. Override with --insecure-bind")
		fmt.Println("(unsafe) only when you understand the risk.")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		return nil, fmt.Errorf("flag parsing: %w", err)
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *pushTestEvery < 0 {
		return nil, fmt.Errorf("--push-test-every must be >= 0")
	}
	if *pushTestEvery > 0 && !*pushEnabled {
		return nil, fmt.Errorf("--push-test-every requires --push")
	}

	// Report #1: refuse an unauthenticated non-loopback bind before the TUI
	// boots. Fails fast with an actionable error rather than silently exposing
	// an unauthenticated RCE surface (terminal bridge + session-create API).
	if err := web.CheckBindSecurity(*listenAddr, *token, *insecureBind); err != nil {
		return nil, err
	}

	effectiveProfile := session.GetEffectiveProfile(profile)

	resolvedPushSubject := *pushVAPIDSubject
	resolvedPushPublic := ""
	resolvedPushPrivate := ""
	if *pushEnabled {
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

	// genui-1: by default the generative command center composes with the
	// deterministic no-LLM stub (NewServer's default). When --genui-composer-
	// session is set, route composition to that managed claude session — the
	// fleet IS the compute, so there is no billed API call and no API key.
	var genuiComposer genui.SpecComposer
	if s := strings.TrimSpace(*genuiComposerSession); s != "" {
		genuiComposer = genui.NewSessionComposer(s)
	}

	server := web.NewServer(web.Config{
		ListenAddr:          *listenAddr,
		Profile:             effectiveProfile,
		ReadOnly:            *readOnly,
		WebMutations:        resolveMutationsEnabled(*readOnly),
		Token:               *token,
		InsecureBind:        *insecureBind,
		MenuData:            menuData,
		PushVAPIDPublicKey:  resolvedPushPublic,
		PushVAPIDPrivateKey: resolvedPushPrivate,
		PushVAPIDSubject:    resolvedPushSubject,
		PushTestInterval:    *pushTestEvery,
		GenuiComposer:       genuiComposer,
	})

	if mutator != nil {
		server.SetMutator(mutator)
	}

	return server, nil
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
