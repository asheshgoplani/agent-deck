package watcher

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// ntfyLog is the package-level logger for the ntfy and slack adapters.
var ntfyLog = logging.ForComponent(logging.CompWatcher)

// DefaultNtfyServer is the public ntfy server the ntfy and slack adapters use
// when Settings["server"] is empty. Anyone who knows a topic on it can read
// and publish to that topic unless the topic is access-protected.
const DefaultNtfyServer = "https://ntfy.sh"

// minNtfyTopicLen is the shortest topic GuessableNtfyTopic accepts.
const minNtfyTopicLen = 16

// publicNtfyTopicWarning is the text of PublicNtfyTopicWarning; %[1]s is the
// adapter type. It never includes the topic itself.
const publicNtfyTopicWarning = "%[1]s watcher: this topic is easy to guess and the public server " + DefaultNtfyServer +
	" has no access token set, so anyone who guesses the topic can read its events and inject fake ones. " +
	"Fix one of: use a random topic (agent-deck watcher create %[1]s --name <name> --random-topic); " +
	"set an access token ($NTFY_TOKEN or --token-file <path> with chmod 600, or token = \"...\" under [source] in watcher.toml); " +
	"or use your own ntfy server (--server https://ntfy.example.com, or server = \"...\" under [source])."

// GuessableNtfyTopic reports whether topic is easy to guess: shorter than 16
// characters, or with no digit in it (word-only names like
// "my-private-topic"). The rule is deliberately simple; RandomNtfyTopic always
// produces a topic that passes it.
func GuessableNtfyTopic(topic string) bool {
	return len(topic) < minNtfyTopicLen || !strings.ContainsAny(topic, "0123456789")
}

// IsPublicNtfyServer reports whether server is the public ntfy.sh (an empty
// server means the default, which is ntfy.sh).
func IsPublicNtfyServer(server string) bool {
	if strings.TrimSpace(server) == "" {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), "ntfy.sh")
}

// PublicNtfyTopicWarning returns the warning to show for an ntfy or slack
// watcher that subscribes to a guessable topic on the public ntfy.sh with no
// access token, or "" when none applies.
func PublicNtfyTopicWarning(adapterType, server, topic, token string) string {
	if token != "" || !IsPublicNtfyServer(server) || !GuessableNtfyTopic(topic) {
		return ""
	}
	return fmt.Sprintf(publicNtfyTopicWarning, adapterType)
}

// RandomNtfyTopic returns a fresh topic with 128 bits of randomness that
// GuessableNtfyTopic does not flag.
func RandomNtfyTopic() (string, error) {
	for {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("generate topic: %w", err)
		}
		topic := "agentdeck-" + hex.EncodeToString(b)
		if !GuessableNtfyTopic(topic) {
			return topic, nil
		}
	}
}

// warnPublicNtfyTopic logs PublicNtfyTopicWarning at Setup, so a watcher that
// predates the create-time check still says so at startup.
func warnPublicNtfyTopic(adapterType, server, topic, token string) {
	if msg := PublicNtfyTopicWarning(adapterType, server, topic, token); msg != "" {
		ntfyLog.Warn(msg)
	}
}
