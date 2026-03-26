package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"

	"github.com/asheshgoplani/agent-deck/internal/calendar"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handleGoogleCalendar(args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printGoogleCalendarUsage()
		return
	}
	switch args[0] {
	case "auth":
		handleGoogleCalendarAuth()
	case "test":
		handleGoogleCalendarTest()
	default:
		fmt.Fprintf(os.Stderr, "Unknown google-calendar subcommand: %s\n", args[0])
		printGoogleCalendarUsage()
		os.Exit(1)
	}
}

func printGoogleCalendarUsage() {
	fmt.Println("Usage: agent-deck google-calendar <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  auth    Authorize Google Calendar access (opens browser)")
	fmt.Println("  test    Test the integration by fetching upcoming events")
}

func handleGoogleCalendarAuth() {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	credPath := cfg.GoogleCalendar.GetCredentialsPath()
	if credPath == "" {
		fmt.Fprintln(os.Stderr, "Error: cannot determine credentials path")
		os.Exit(1)
	}

	oauthCfg, err := calendar.ParseCredentials(credPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n\nPlace your Google Cloud credentials at:\n  %s\n", err, credPath)
		os.Exit(1)
	}

	// Bind the callback listener before building the redirect URL to avoid
	// a TOCTOU race where another process grabs the port between probe and serve.
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot listen on localhost: %v\n", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	redirectURL := fmt.Sprintf("http://localhost:%d/callback", port)
	oauthCfg.RedirectURL = redirectURL

	// Generate a cryptographically random CSRF state token.
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot generate state token: %v\n", err)
		os.Exit(1)
	}
	oauthState := base64.RawURLEncoding.EncodeToString(stateBytes)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != oauthState {
			errCh <- fmt.Errorf("state mismatch in OAuth callback")
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no code in callback")
			fmt.Fprintln(w, "Error: no authorization code received.")
			return
		}
		codeCh <- code
		fmt.Fprintln(w, "Authorization successful! You can close this tab.")
	})
	srv := &http.Server{Handler: mux}
	// Serve returns http.ErrServerClosed on clean shutdown via Shutdown below — expected.
	go srv.Serve(listener) //nolint:errcheck
	// Shutdown error is safe to ignore: this single-use auth server exits seconds
	// after receiving the OAuth code regardless of whether Shutdown returns an error.
	defer srv.Shutdown(context.Background()) //nolint:errcheck

	authURL := oauthCfg.AuthCodeURL(oauthState, oauth2.AccessTypeOffline)
	fmt.Printf("Opening browser for authorization...\n\n")
	openBrowser(authURL)
	fmt.Printf("If the browser didn't open, visit:\n  %s\n\nWaiting for authorization...\n", authURL)

	select {
	case code := <-codeCh:
		tok, err := oauthCfg.Exchange(context.Background(), code)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error exchanging code: %v\n", err)
			os.Exit(1)
		}
		tokenPath := cfg.GoogleCalendar.GetTokenPath()
		if err := calendar.SaveToken(tokenPath, tok); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving token: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nAuthorization successful! Token saved to:\n  %s\n", tokenPath)
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "Authorization failed: %v\n", err)
		os.Exit(1)
	case <-time.After(5 * time.Minute):
		fmt.Fprintln(os.Stderr, "Authorization timed out. Run 'agent-deck google-calendar auth' to try again.")
		os.Exit(1)
	}
}

func handleGoogleCalendarTest() {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	collector, err := calendar.NewCollectorFromConfig(
		cfg.GoogleCalendar.GetCredentialsPath(),
		cfg.GoogleCalendar.GetTokenPath(),
		cfg.GoogleCalendar.CalendarIDs,
		cfg.GoogleCalendar.GetLookahead(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	events, err := collector.Collect(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching events: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		fmt.Println("No upcoming events found.")
		return
	}

	fmt.Printf("Found %d upcoming events:\n\n", len(events))
	for _, e := range events {
		fmt.Printf("  %s  %s\n", e.TimeUntilLabel(), e.Title)
	}
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start() //nolint:errcheck
	case "linux":
		exec.Command("xdg-open", url).Start() //nolint:errcheck
	}
}
