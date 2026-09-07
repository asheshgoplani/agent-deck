package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrNotConfigured means the provider has nothing to talk to: the user has not
// configured this provider on this machine. It is a sentinel because the CLI
// renders it differently from a failure — "not configured" is a fact about the
// setup, while a timeout or a 401 is a problem to look at.
var ErrNotConfigured = errors.New("provider not configured")

// zaiMonitorPath is the endpoint the vendor's own coding plugin calls.
const zaiMonitorPath = "/api/monitor/usage/quota/limit"

// zaiHostSuffixes is the set of hosts this package is willing to send a token
// to. It exists because ANTHROPIC_BASE_URL is a general-purpose Anthropic-compat
// setting: pointed at some other gateway, an unguarded fetch would replay the
// user's credential to a host that has nothing to do with Z.ai. The guard means
// the worst case of a mis-set variable is "not configured", not a leak.
//
// It is a var, not a const, for one reason: the httptest stub in zai_test.go
// serves from 127.0.0.1 and has to be reachable without weakening the
// production default. Nothing in production writes to it.
var zaiHostSuffixes = []string{"z.ai"}

// zaiAnthropicPathSuffix is what the Anthropic-compat base URL carries and the
// monitor endpoint does not. It is stripped rather than the host being
// reassembled from scratch so that a regional or self-hosted deployment keeps
// whatever host the user configured.
const zaiAnthropicPathSuffix = "/api/anthropic"

// maxZaiBodyBytes caps the monitor response.
//
// The observed body is ~400 bytes; 256 KiB is ample headroom while still
// bounding what a compromised or confused endpoint can make agent-deck
// allocate. A var so the cap test can shrink it instead of generating a
// quarter-megabyte fixture.
var maxZaiBodyBytes int64 = 256 << 10

// DefaultZaiTimeout matches the repo's existing outbound norm (see
// internal/credrefresh and internal/update, both 5-10s). This request is
// user-triggered and never runs on a render path, so the ceiling only has to be
// short enough that `agent-deck usage` stays interactive.
const DefaultZaiTimeout = 10 * time.Second

// zaiResponse is the monitor payload. Decoding is deliberately tolerant:
// unknown fields are ignored and every optional field is a pointer or a
// zero-tolerant scalar, because this endpoint is the vendor's own and has
// already been observed returning a `type` its own plugin does not expect.
type zaiResponse struct {
	Data struct {
		Level  string     `json:"level"`
		Limits []zaiLimit `json:"limits"`
	} `json:"data"`
}

type zaiLimit struct {
	// Unit and Number discriminate the window: unit is a time-unit enum and
	// number the multiplier. Live evidence (2026-09-07) covers unit=3 (hour,
	// number=5 -> the 5-hour window) and unit=6 (week, number=1 -> the weekly
	// window) and NOTHING else, so the rest of the enum is not extrapolated.
	//
	// `type` is NOT used and is not decoded at all: the live payload returned
	// "CREDIT_LIMIT" for BOTH windows while the vendor's own plugin still
	// branches on TOKENS_LIMIT/TIME_LIMIT, so it neither discriminates nor is
	// stable. Array order is not relied on either.
	Unit   int `json:"unit"`
	Number int `json:"number"`
	// Usage is the BUDGET and CurrentValue the consumption — the field names
	// invite exactly the opposite reading, so nothing here is named after them.
	Usage        float64 `json:"usage"`
	CurrentValue float64 `json:"currentValue"`
	// Percentage is percent consumed, and is a pointer so that "0% consumed"
	// (a window with no activity, which is what the live 5-hour window showed)
	// is distinguishable from "the field was absent".
	Percentage *float64 `json:"percentage"`
	// NextResetTime is epoch MILLISECONDS and OPTIONAL — the live 5-hour window
	// omitted it. Claude's equivalent is in seconds; do not share a decoder.
	NextResetTime *int64 `json:"nextResetTime"`
}

// FetchZai reads the Z.ai / GLM Coding Plan quota from the monitor endpoint on
// the host the user configured in ANTHROPIC_BASE_URL.
//
// Credentials come from the process environment only. The repo has no parser
// for a profile's env_file — those entries are `source`d by the spawned shell
// (internal/session/env.go) and never parsed in Go — and adding a shell-syntax
// parser (quoting, export, command substitution) to reach them would be new,
// fragile surface for a marginal gain. Inside an agent-deck-launched session
// the env_file has already been sourced, so this reads the live credential
// exactly where the user runs it. Widening it is a documented follow-up.
//
// os.Environ() is lint-banned outside an allowlist this package is not on;
// os.Getenv is the supported reader and is all that is needed.
func FetchZai(ctx context.Context, client *http.Client) (Snapshot, error) {
	endpoint, err := zaiQuotaEndpoint(os.Getenv("ANTHROPIC_BASE_URL"))
	if err != nil {
		return Snapshot{}, err
	}
	token := strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN"))
	if token == "" {
		return Snapshot{}, fmt.Errorf("ANTHROPIC_AUTH_TOKEN is not set: %w", ErrNotConfigured)
	}

	// #nosec G704 -- the destination is not attacker-controlled: endpoint is
	// built by zaiQuotaEndpoint from the user's own ANTHROPIC_BASE_URL, which
	// must parse as http/https AND resolve to a host on zaiHostSuffixes before
	// any request is built. There is deliberately no default host, so an
	// unconfigured machine reaches nothing at all.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("building quota request: %w", err)
	}
	// The vendor's own plugin sends the bare token with no "Bearer" prefix. The
	// endpoint accepted both when tested, so the vendor's shape is used: it is
	// the one guaranteed to keep working.
	request.Header.Set("Authorization", token)
	request.Header.Set("Accept", "application/json")

	// #nosec G704 -- same constrained destination as the request above.
	response, err := client.Do(request)
	if err != nil {
		// url.Error stringifies the request URL, which carries no credential
		// (the token is a header), but the message is still reduced to the
		// bare cause so nothing from the transport layer is passed through
		// into a field that gets persisted and printed.
		return Snapshot{}, errors.New("quota request failed: " + zaiTransportReason(err))
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		// Status only. The response body is NOT included: a server that echoes
		// the Authorization header back would otherwise push the user's token
		// into an error string that is cached on disk and printed.
		return Snapshot{}, fmt.Errorf("quota endpoint returned %d %s",
			response.StatusCode, http.StatusText(response.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxZaiBodyBytes+1))
	if err != nil {
		return Snapshot{}, errors.New("reading quota response: " + zaiTransportReason(err))
	}
	if int64(len(body)) > maxZaiBodyBytes {
		return Snapshot{}, errors.New("quota response exceeds size cap")
	}

	var decoded zaiResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Snapshot{}, fmt.Errorf("decoding quota response: %w", err)
	}

	snapshot := Snapshot{
		ID:        ProviderZai,
		Label:     "Z.ai",
		Plan:      decoded.Data.Level,
		UpdatedAt: time.Now().Unix(),
	}
	for _, limit := range decoded.Data.Limits {
		snapshot.Windows = append(snapshot.Windows, limit.toWindow())
	}
	return snapshot, nil
}

// zaiTransportReason reduces a transport error to its innermost cause. A
// *url.Error prints the method and URL, which is noise in a one-line status
// rendering and is the only place a caller-supplied string could reach the
// message at all.
func zaiTransportReason(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		if urlErr.Timeout() {
			return "timeout"
		}
		return urlErr.Err.Error()
	}
	return err.Error()
}

// toWindow maps one monitor limit onto the shared model.
func (l zaiLimit) toWindow() Window {
	window := Window{Kind: WindowOther, Label: fmt.Sprintf("unit%dx%d", l.Unit, l.Number)}
	switch {
	case l.Unit == 3 && l.Number == 5:
		window.Kind, window.Label = WindowFiveHour, "5h"
	case l.Unit == 6 && l.Number == 1:
		window.Kind, window.Label = WindowSevenDay, "7d"
	}

	switch {
	case l.Percentage != nil:
		// Preferred: the provider's own arithmetic, so a plan whose windows are
		// not a simple ratio still reports what the vendor dashboard reports.
		window.UsedPercentage = *l.Percentage
	case l.Usage > 0:
		window.UsedPercentage = l.CurrentValue / l.Usage * 100
	}

	if l.NextResetTime != nil {
		// Milliseconds here, seconds in the model. Nothing is synthesised when
		// the field is absent: a window with no consumption has no open window
		// to reset, and inventing "now + 5h" would be a promise.
		seconds := *l.NextResetTime / 1000
		window.ResetsAt = &seconds
	}
	return window
}

// zaiQuotaEndpoint derives the monitor URL from the user-configured
// Anthropic-compat base URL, mirroring what the vendor's plugin does.
//
// There is deliberately NO default host. The repo's security lens requires that
// an outbound destination in non-test code be user-configured rather than
// hardcoded, and a fallback host would also mean a user who never set up Z.ai
// still generates traffic to it.
func zaiQuotaEndpoint(baseURL string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", fmt.Errorf("ANTHROPIC_BASE_URL is not set: %w", ErrNotConfigured)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("ANTHROPIC_BASE_URL %q is not a usable URL: %w", trimmed, ErrNotConfigured)
	}
	if !isZaiHost(parsed.Hostname()) {
		return "", fmt.Errorf("ANTHROPIC_BASE_URL host %q is not a Z.ai endpoint: %w",
			parsed.Hostname(), ErrNotConfigured)
	}

	path := strings.TrimSuffix(strings.TrimSuffix(parsed.Path, "/"), zaiAnthropicPathSuffix)
	endpoint := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: path + zaiMonitorPath}
	return endpoint.String(), nil
}

// isZaiHost matches on a dot-boundary so a lookalike host ("notz.ai") does not
// satisfy a plain suffix check.
func isZaiHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, suffix := range zaiHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}
