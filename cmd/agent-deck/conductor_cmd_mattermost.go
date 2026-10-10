package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// promptMattermostSettings asks for the Mattermost bot's settings during
// `conductor setup`. The bridge talks to the user in a DM with the bot unless
// a channel ID is given.
func promptMattermostSettings(reader *bufio.Reader, out io.Writer) (session.MattermostSettings, error) {
	ask := func(prompt string) string {
		fmt.Fprint(out, prompt)
		answer, _ := reader.ReadString('\n')
		return strings.TrimSpace(answer)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  1. Create a bot account: Main Menu -> Integrations -> Bot Accounts -> Add Bot Account")
	fmt.Fprintln(out, "     (if that menu is missing, ask your Mattermost admin to create one for you)")
	fmt.Fprintln(out, "  2. Copy the bot's access token")
	fmt.Fprintln(out, "  3. Optional: add the bot to a channel to talk there instead of in a DM")
	fmt.Fprintln(out)

	serverURL := strings.TrimRight(ask("Mattermost server URL (https://...): "), "/")
	parsed, err := url.Parse(serverURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" {
		return session.MattermostSettings{}, errors.New("a server URL like https://mattermost.example.com is required")
	}
	allowInsecureHTTP := false
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		fmt.Fprintln(out, "  Plain http sends the bot token and every message unencrypted.")
		if !strings.EqualFold(ask("Use plain http anyway? (y/N): "), "y") {
			return session.MattermostSettings{}, errors.New("use an https:// server URL")
		}
		allowInsecureHTTP = true
	}

	botToken := ask("Bot access token (or $ENV_VAR / keychain:<service>): ")
	if botToken == "" {
		return session.MattermostSettings{}, errors.New("bot token is required")
	}

	user := strings.TrimPrefix(ask("Your Mattermost username: "), "@")
	if user == "" {
		return session.MattermostSettings{}, errors.New("username is required")
	}

	channelID := ask("Channel ID to use instead of a DM (blank for a DM): ")

	return session.MattermostSettings{
		ServerURL:         serverURL,
		BotToken:          botToken,
		User:              user,
		ChannelID:         channelID,
		AllowInsecureHTTP: allowInsecureHTTP,
	}, nil
}

// isLoopbackHost matches the bridge's list of hosts that may use plain http.
func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
