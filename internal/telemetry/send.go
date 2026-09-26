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
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is PostHog Cloud EU. It is a config value
// ([telemetry] endpoint) so a hostname we own can front it later without a
// code change; consent binds to the endpoint, so changing it re-asks.
// Nothing is uploaded without a project key (config.go).
const DefaultEndpoint = "https://eu.i.posthog.com"

// batchPath is PostHog's batch capture path, appended to the endpoint.
const batchPath = "/batch/"

// sendTimeout bounds the whole request: dial, TLS, write, response.
const sendTimeout = 5 * time.Second

const maxResponseBytes = 1024

var endpoint = DefaultEndpoint

// SetEndpoint overrides the endpoint (from config.toml).
func SetEndpoint(u string) {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		u = DefaultEndpoint
	}
	endpoint = u
}

// Endpoint returns the effective endpoint.
func Endpoint() string { return endpoint }

// batchURL is where uploads are POSTed.
func batchURL() string { return strings.TrimRight(endpoint, "/") + batchPath }

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

// endpointUndeployed reports a .invalid host, which never causes a request.
func endpointUndeployed() bool {
	parsed, err := url.Parse(endpoint)
	return err != nil || strings.HasSuffix(parsed.Hostname(), ".invalid")
}

// httpClient never follows redirects, ignores proxy env, keeps no cookies
// and has a hard timeout.
var httpClient = &http.Client{
	Timeout:   sendTimeout,
	Transport: &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("telemetry: redirects are not followed")
	},
}

// postResult is the outcome of one request.
type postResult struct {
	status     int
	retryAfter time.Duration
	err        error
}

// post is the only function that talks to the network. The response body is
// read up to 1 KiB and ignored.
func post(ctx context.Context, body []byte, timeout time.Duration) postResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, batchURL(), bytes.NewReader(body))
	if err != nil {
		return postResult{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent-deck/"+safeVersion(processVersion))
	resp, err := httpClient.Do(req)
	if err != nil {
		return postResult{err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	r := postResult{status: resp.StatusCode}
	if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
		r.retryAfter = time.Duration(secs) * time.Second
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		r.err = fmt.Errorf("telemetry: endpoint returned %d", resp.StatusCode)
	}
	return r
}
