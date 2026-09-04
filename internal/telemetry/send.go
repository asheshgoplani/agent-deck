package telemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultEndpoint is a placeholder. The .invalid TLD (RFC 2606) never
// resolves, so a build shipped without a maintainer-provided endpoint cannot
// deliver anything even when consent is granted. The maintainer must stand
// up a receiver (docs/TELEMETRY-DESIGN.md section 10) and either replace
// this constant or set [telemetry].endpoint in config.toml.
const DefaultEndpoint = "https://telemetry.agent-deck.invalid/v1/ping"

// sendTimeout bounds the whole request: dial, TLS, write, response.
const sendTimeout = 5 * time.Second

// maxResponseBytes caps how much of a response is read before discarding.
const maxResponseBytes = 1024

var endpoint = DefaultEndpoint

// SetEndpoint overrides the endpoint (from config.toml).
func SetEndpoint(u string) {
	u = strings.TrimSpace(u)
	if u == "" {
		u = DefaultEndpoint
	}
	endpoint = u
}

// Endpoint returns the effective endpoint.
func Endpoint() string { return endpoint }

// ValidateEndpoint enforces https, except plain http to a loopback host for
// local testing of a self-hosted receiver.
func ValidateEndpoint(u string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("telemetry: endpoint: %w", err)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("telemetry: endpoint cannot contain credentials, query or fragment")
	}
	if parsed.Host == "" {
		return errors.New("telemetry: endpoint has no host")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return errors.New("telemetry: plain http is only allowed to localhost")
	default:
		return fmt.Errorf("telemetry: unsupported scheme %q", parsed.Scheme)
	}
}

// httpClient never follows redirects and has a hard timeout.
var httpClient = &http.Client{
	Timeout:   sendTimeout,
	Transport: &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("telemetry: redirects are not followed")
	},
}

// nowFn is a test seam.
var nowFn = time.Now

// SendResult describes what MaybeSend did, for tests and for `status`.
type SendResult struct {
	Attempted bool
	Sent      bool
	Reason    string
}

// MaybeSend is the single outbound path.
func MaybeSend(ctx context.Context, version string) SendResult {
	if HardDisabled() {
		return SendResult{Reason: string(HardDisableReason())}
	}
	if !Interactive() {
		return SendResult{Reason: "non-interactive context"}
	}
	if err := ValidateEndpoint(endpoint); err != nil {
		return SendResult{Reason: err.Error()}
	}

	// Hold the cross-process lock through the bounded request. A completed
	// disable cannot be overwritten or followed by an already-reserved send.
	unlock, err := lockState()
	if err != nil {
		return SendResult{Reason: err.Error()}
	}
	defer unlock()
	s := LoadState()
	if enabled, reason := Enabled(s); !enabled {
		return SendResult{Reason: string(reason)}
	}
	if HardDisabled() || !Interactive() {
		return SendResult{Reason: "disabled or non-interactive"}
	}
	parsed, _ := url.Parse(endpoint)
	if strings.HasSuffix(parsed.Hostname(), ".invalid") {
		return SendResult{Reason: "receiver is not deployed"}
	}
	now := nowFn()
	today := dayOf(now)
	if s.LastAttemptDay >= today {
		return SendResult{Reason: "already attempted today"}
	}
	body, err := BuildPayload(s, version, now).Marshal()
	if err != nil || len(body) > 2048 {
		return SendResult{Reason: "invalid payload"}
	}
	s.LastAttemptDay = today
	if err := saveStateLocked(s); err != nil {
		return SendResult{Reason: err.Error()}
	}
	if HardDisabled() || !Interactive() {
		return SendResult{Reason: "disabled before send"}
	}
	if err := post(ctx, body, safeVersion(version)); err != nil {
		return SendResult{Attempted: true, Reason: err.Error()}
	}
	s.LastSentDay = today
	s.LastPayload = body
	s.Counters = nil
	if err := saveStateLocked(s); err != nil {
		return SendResult{Attempted: true, Sent: true, Reason: "sent; could not persist acknowledgement"}
	}
	return SendResult{Attempted: true, Sent: true}
}

func post(ctx context.Context, body []byte, version string) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent-deck/"+version)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("telemetry: endpoint returned %d", resp.StatusCode)
	}
	return nil
}
