// Package watcher's Gmail adapter delivers normalized Events from a Gmail account
// via the Gmail Pub/Sub watch + streaming pull flow:
//
//  1. users.Watch() registers a push target (Google Cloud Pub/Sub topic) that Gmail
//     publishes change notifications to. The watch expires after ~7 days.
//  2. Subscription.Receive subscribes to that topic via the cloud.google.com/go/pubsub
//     client and blocks until ctx is cancelled. Google's client handles lease
//     extension, flow control, and reconnects natively.
//  3. Each envelope is shaped {emailAddress, historyId}. The handler calls
//     users.history.list(startHistoryId=persistedWatchHistoryID) to fetch the new
//     message IDs, then users.messages.get(format=metadata) to fetch From/Subject/
//     snippet/internalDate, normalizes those to Event, and sends on the channel.
//  4. On success the Pub/Sub message is Acked. On 404 from history.list (stale
//     historyId past Gmail's retention window) the handler falls back to the
//     envelope's historyId, logs a warning, and Acks — dropping some events is
//     acceptable per CONTEXT.md D-18. Malformed envelopes are Acked (not Nacked)
//     so they cannot redeliver forever.
//
// Plan 17-02 implements Setup, Listen (with a STUB renewalLoop that only waits on
// ctx.Done), Teardown, HealthCheck (minimal), processHistory, registerWatch,
// normalizeGmailMessage, the label filter, and the persistingTokenSource wrapper.
// The full renewalLoop body and expanded HealthCheck coverage land in Plan 17-03.
package watcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// OAuth scopes are hard-coded Go constants, NEVER read from AdapterConfig.Settings
// (mitigates T-17-10 Elevation of Privilege — scope escalation via untrusted config).
const (
	gmailScope  = "https://www.googleapis.com/auth/gmail.readonly"
	pubsubScope = "https://www.googleapis.com/auth/pubsub"

	// metaWriteThrottle is the minimum interval between WatchHistoryID meta.json
	// writes. Bursts of Gmail deliveries would otherwise thrash meta.json. D-18.
	metaWriteThrottle = 5 * time.Second
)

// GmailAdapter implements WatcherAdapter against the Gmail Pub/Sub watch API.
// All fields are private; construction uses NewGmailAdapter().
type GmailAdapter struct {
	name            string
	account         string
	topic           string              // projects/{proj}/topics/{t}
	subscr          string              // projects/{proj}/subscriptions/{s}
	labels          map[string]struct{} // parsed from Settings["labels"] (nil = no filter)
	credentialsPath string
	tokenPath       string

	// Clients constructed in Setup
	gmailSvc     *gmail.Service
	pubsubClient *pubsub.Client
	subscription *pubsub.Subscription
	tokenSrc     oauth2.TokenSource

	mu             sync.Mutex // guards watchExpiry, watchHistoryID, lastMetaWrite, lastHealthErr
	watchExpiry    time.Time
	watchHistoryID uint64
	lastMetaWrite  time.Time
	lastHealthErr  error

	// Test seams. Default to time.Now / time.After in NewGmailAdapter.
	// Tests assign these directly after construction.
	nowFunc   func() time.Time
	afterFunc func(time.Duration) <-chan time.Time
}

// NewGmailAdapter constructs a GmailAdapter with default time functions.
// Tests inject fakes by assigning a.nowFunc / a.afterFunc directly.
func NewGmailAdapter() *GmailAdapter {
	return &GmailAdapter{
		nowFunc:   time.Now,
		afterFunc: time.After,
	}
}

// Setup loads OAuth credentials + token, builds gmail.Service and pubsub.Client
// (both authenticating with the same user token via persistingTokenSource),
// verifies the subscription exists, loads existing WatcherMeta, and conditionally
// calls users.Watch() if no watch is present or the existing watch expires in
// under 2 hours (D-11 threshold).
//
// Settings keys:
//   - topic (required):         projects/{projectID}/topics/{topic}
//   - subscription (required):  projects/{projectID}/subscriptions/{sub}
//   - credentials_path:         defaults to <watcher_dir>/credentials.json
//   - token_path:               defaults to <watcher_dir>/token.json
//   - labels:                   optional comma-separated Gmail label filter
//   - account:                  optional informational Gmail address
func (a *GmailAdapter) Setup(ctx context.Context, config AdapterConfig) error {
	if a.nowFunc == nil {
		a.nowFunc = time.Now
	}
	if a.afterFunc == nil {
		a.afterFunc = time.After
	}

	a.name = config.Name
	a.topic = config.Settings["topic"]
	a.subscr = config.Settings["subscription"]
	if a.topic == "" || a.subscr == "" {
		return errors.New("gmail: adapter requires Settings[topic] and Settings[subscription]")
	}
	a.account = config.Settings["account"]

	// Parse optional label filter
	if lblStr := strings.TrimSpace(config.Settings["labels"]); lblStr != "" {
		a.labels = make(map[string]struct{})
		for _, part := range strings.Split(lblStr, ",") {
			if p := strings.TrimSpace(part); p != "" {
				a.labels[p] = struct{}{}
			}
		}
	}

	// Resolve credential and token paths, defaulting to the watcher's dir.
	watcherDir, err := session.WatcherNameDir(a.name)
	if err != nil {
		return fmt.Errorf("gmail: resolve watcher dir: %w", err)
	}
	a.credentialsPath = config.Settings["credentials_path"]
	if a.credentialsPath == "" {
		a.credentialsPath = joinPath(watcherDir, "credentials.json")
	}
	a.tokenPath = config.Settings["token_path"]
	if a.tokenPath == "" {
		a.tokenPath = joinPath(watcherDir, "token.json")
	}

	// Load OAuth client credentials (Google "installed app" JSON).
	credsData, err := os.ReadFile(a.credentialsPath)
	if err != nil {
		return fmt.Errorf("gmail: credentials.json not found at %s (run `agent-deck watcher oauth login` once to bootstrap): %w", a.credentialsPath, err)
	}
	oauthConfig, err := google.ConfigFromJSON(credsData, gmailScope, pubsubScope)
	if err != nil {
		return fmt.Errorf("gmail: parse credentials.json: %w", err)
	}

	// Load persisted user token.
	tokenData, err := os.ReadFile(a.tokenPath)
	if err != nil {
		return fmt.Errorf("gmail: token.json not found at %s: %w", a.tokenPath, err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(tokenData, &tok); err != nil {
		return fmt.Errorf("gmail: malformed token.json: %w", err)
	}

	// Build persist-on-refresh TokenSource wrapper. Both Gmail and Pub/Sub
	// clients authenticate with this same source.
	a.tokenSrc = newPersistingTokenSource(ctx, oauthConfig, &tok, a.tokenPath)

	// Build Gmail client.
	a.gmailSvc, err = gmail.NewService(ctx, option.WithTokenSource(a.tokenSrc))
	if err != nil {
		return fmt.Errorf("gmail: build service: %w", err)
	}

	// Build Pub/Sub client.
	projectID, err := parseProjectID(a.topic)
	if err != nil {
		return err
	}
	a.pubsubClient, err = pubsub.NewClient(ctx, projectID, option.WithTokenSource(a.tokenSrc))
	if err != nil {
		return fmt.Errorf("gmail: build pubsub client: %w", err)
	}
	a.subscription = a.pubsubClient.Subscription(subscriptionIDFromName(a.subscr))

	// Verify the subscription exists (HealthCheck parity).
	exists, err := a.subscription.Exists(ctx)
	if err != nil {
		return fmt.Errorf("gmail: check subscription: %w", err)
	}
	if !exists {
		return fmt.Errorf("gmail: subscription %s does not exist", a.subscr)
	}

	// Load existing WatcherMeta, import WatchExpiry + WatchHistoryID into memory.
	if meta, _ := session.LoadWatcherMeta(a.name); meta != nil {
		if meta.WatchExpiry != "" {
			if t, err := time.Parse(time.RFC3339, meta.WatchExpiry); err == nil {
				a.mu.Lock()
				a.watchExpiry = t
				a.mu.Unlock()
			}
		}
		if meta.WatchHistoryID != "" {
			if id, err := strconv.ParseUint(meta.WatchHistoryID, 10, 64); err == nil {
				a.mu.Lock()
				a.watchHistoryID = id
				a.mu.Unlock()
			}
		}
	}

	// D-11 threshold check: register a fresh watch when missing or within 2 hours.
	a.mu.Lock()
	expiry := a.watchExpiry
	a.mu.Unlock()
	if expiry.IsZero() || expiry.Sub(a.nowFunc()) < 2*time.Hour {
		if err := a.registerWatch(ctx); err != nil {
			return fmt.Errorf("gmail: initial users.Watch failed: %w", err)
		}
	}

	return nil
}

// Listen blocks on Subscription.Receive until ctx is cancelled. Each Pub/Sub
// envelope is decoded, used to drive a users.history.list + users.messages.get
// fan-out, and Acked on success (or on a 404 stale-history fallback). Transient
// Gmail errors result in Nack so Pub/Sub redelivers.
//
// A sync.WaitGroup ensures the renewal goroutine is joined before Listen returns
// — even though the renewalLoop body is a STUB in Plan 17-02 (it only waits on
// ctx.Done), the WaitGroup is in place so Plan 17-03 can drop in the real body
// without changing the lifecycle contract. This prevents Pitfall 3 (renewal
// goroutine outliving Listen and surfacing as a goleak false positive).
func (a *GmailAdapter) Listen(ctx context.Context, events chan<- Event) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.renewalLoop(ctx)
	}()

	err := a.subscription.Receive(ctx, func(msgCtx context.Context, m *pubsub.Message) {
		var envelope struct {
			EmailAddress string `json:"emailAddress"`
			HistoryID    uint64 `json:"historyId,string"`
		}
		if jerr := json.Unmarshal(m.Data, &envelope); jerr != nil {
			// Malformed envelopes cannot succeed on retry — Ack (not Nack) to
			// break the redelivery loop. Pitfall 8.
			slog.Warn("gmail: malformed envelope",
				slog.String("err", jerr.Error()))
			m.Ack()
			return
		}

		a.mu.Lock()
		startID := a.watchHistoryID
		a.mu.Unlock()
		if startID == 0 {
			startID = envelope.HistoryID
		}

		if perr := a.processHistory(msgCtx, startID, envelope.HistoryID, events); perr != nil {
			if isStaleHistoryError(perr) {
				// 404 from history.list — Gmail's history retention lapsed.
				// Fall back to the envelope's historyId, persist, and Ack.
				// Some events are dropped in this edge case; acceptable per D-18.
				slog.Warn("gmail: stale historyId, resuming from envelope",
					slog.Uint64("envelope_id", envelope.HistoryID))
				a.mu.Lock()
				a.watchHistoryID = envelope.HistoryID
				a.mu.Unlock()
				a.persistHistoryIDThrottled()
				m.Ack()
				return
			}
			slog.Error("gmail: history list failed",
				slog.String("err", perr.Error()))
			m.Nack()
			return
		}
		m.Ack()
	})

	// Wait for the renewal goroutine to exit before returning, so goleak cannot
	// observe it outliving Listen.
	wg.Wait()

	// Subscription.Receive returns nil on ctx.Done; only non-nil on permanent
	// errors (auth failures, subscription deleted, etc).
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("gmail: subscription receive: %w", err)
	}
	return nil
}

// renewalLoop is a STUB in Plan 17-02. It only blocks on ctx.Done so Listen can
// Wait on its completion without hanging tests. Plan 17-03 replaces this body
// with the real implementation (renew 1h before expiry, retry every 15m on
// failure) per RESEARCH.md Pattern 5.
func (a *GmailAdapter) renewalLoop(ctx context.Context) {
	<-ctx.Done()
}

// Teardown closes the pubsub client so gRPC connections are released (required
// for goleak). It does NOT call users.Stop() per D-34 — stopping the watch
// would burn 50 quota units on every restart and Google treats users.Watch as
// idempotent on the next Setup.
func (a *GmailAdapter) Teardown() error {
	if a.pubsubClient != nil {
		return a.pubsubClient.Close()
	}
	return nil
}

// HealthCheck reports the adapter's ability to reach Gmail + Pub/Sub. Plan 17-02
// covers the minimal path: returns a cached lastHealthErr (set by renewalLoop in
// Plan 17-03), exercises the token source, checks the Pub/Sub subscription, and
// flags a lapsed watch expiry. Plan 17-03 adds explicit unit coverage per branch.
func (a *GmailAdapter) HealthCheck() error {
	a.mu.Lock()
	lastErr := a.lastHealthErr
	expiry := a.watchExpiry
	a.mu.Unlock()
	if lastErr != nil {
		return lastErr
	}
	if a.tokenSrc != nil {
		if _, err := a.tokenSrc.Token(); err != nil {
			return fmt.Errorf("gmail: token source: %w", err)
		}
	}
	if a.subscription != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exists, err := a.subscription.Exists(ctx)
		if err != nil {
			return fmt.Errorf("gmail: subscription check: %w", err)
		}
		if !exists {
			return fmt.Errorf("gmail: subscription %s does not exist", a.subscr)
		}
	}
	if !expiry.IsZero() && expiry.Before(time.Now()) {
		return fmt.Errorf("gmail: watch expiry has lapsed at %s", expiry.Format(time.RFC3339))
	}
	return nil
}

// registerWatch calls users.Stop() (ignoring errors — idempotent per D-12) then
// users.Watch() with a WatchRequest{TopicName: a.topic}. On success it persists
// WatchExpiry (RFC3339 UTC) and WatchHistoryID (uint64 string) to meta.json via
// session.SaveWatcherMeta. Used by both Setup (initial registration) and the
// renewalLoop body in Plan 17-03.
func (a *GmailAdapter) registerWatch(ctx context.Context) error {
	// Stop is idempotent and can fail if no prior watch exists — ignore error.
	_ = a.gmailSvc.Users.Stop("me").Context(ctx).Do()

	req := &gmail.WatchRequest{TopicName: a.topic}
	resp, err := a.gmailSvc.Users.Watch("me", req).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("gmail: users.Watch: %w", err)
	}

	expiry := time.UnixMilli(resp.Expiration).UTC()
	a.mu.Lock()
	a.watchExpiry = expiry
	a.watchHistoryID = resp.HistoryId
	a.mu.Unlock()

	// Load existing meta (preserve CreatedAt) or create fresh.
	meta, _ := session.LoadWatcherMeta(a.name)
	if meta == nil {
		meta = &session.WatcherMeta{
			Name:      a.name,
			Type:      "gmail",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}
	if meta.Type == "" {
		meta.Type = "gmail"
	}
	meta.WatchExpiry = expiry.Format(time.RFC3339)
	meta.WatchHistoryID = strconv.FormatUint(resp.HistoryId, 10)
	if err := session.SaveWatcherMeta(meta); err != nil {
		return fmt.Errorf("gmail: persist watch meta: %w", err)
	}
	return nil
}

// processHistory drives users.history.list(startHistoryId) + users.messages.get
// for each messageAdded entry, fans normalized Events out on the events channel,
// and updates the in-memory watchHistoryID on success. Pagination is handled via
// NextPageToken. Per-message errors are logged and skipped; a higher-level 404
// on history.list is propagated to the caller for stale-history fallback.
func (a *GmailAdapter) processHistory(ctx context.Context, startID, envelopeID uint64, events chan<- Event) error {
	_ = envelopeID // retained for future logging; currently unused

	call := a.gmailSvc.Users.History.List("me").
		StartHistoryId(startID).
		HistoryTypes("messageAdded").
		MaxResults(100).
		Context(ctx)

	maxSeen := startID
	for {
		resp, err := call.Do()
		if err != nil {
			return err
		}
		for _, h := range resp.History {
			if h.Id > maxSeen {
				maxSeen = h.Id
			}
			for _, ma := range h.MessagesAdded {
				if ma == nil || ma.Message == nil {
					continue
				}
				msg, err := a.gmailSvc.Users.Messages.Get("me", ma.Message.Id).
					Format("metadata").
					MetadataHeaders("From", "Subject", "Date").
					Context(ctx).
					Do()
				if err != nil {
					slog.Warn("gmail: fetch message failed",
						slog.String("id", ma.Message.Id),
						slog.String("err", err.Error()))
					continue
				}
				if !a.passesLabelFilter(msg.LabelIds) {
					continue
				}
				evt := normalizeGmailMessage(msg)
				// Pitfall 5: never block indefinitely on a full channel.
				select {
				case events <- evt:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		call.PageToken(resp.NextPageToken)
	}

	a.mu.Lock()
	if maxSeen > a.watchHistoryID {
		a.watchHistoryID = maxSeen
	}
	a.mu.Unlock()
	a.persistHistoryIDThrottled()
	return nil
}

// persistHistoryIDThrottled writes the current watchHistoryID + watchExpiry to
// meta.json via session.SaveWatcherMeta, but only if metaWriteThrottle has
// elapsed since the last write. Bursts of Gmail deliveries would otherwise
// thrash meta.json (D-18).
func (a *GmailAdapter) persistHistoryIDThrottled() {
	a.mu.Lock()
	sinceLast := a.nowFunc().Sub(a.lastMetaWrite)
	if !a.lastMetaWrite.IsZero() && sinceLast < metaWriteThrottle {
		a.mu.Unlock()
		return
	}
	a.lastMetaWrite = a.nowFunc()
	hid := a.watchHistoryID
	expiry := a.watchExpiry
	a.mu.Unlock()

	meta, _ := session.LoadWatcherMeta(a.name)
	if meta == nil {
		meta = &session.WatcherMeta{
			Name:      a.name,
			Type:      "gmail",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}
	if meta.Type == "" {
		meta.Type = "gmail"
	}
	if !expiry.IsZero() {
		meta.WatchExpiry = expiry.Format(time.RFC3339)
	}
	meta.WatchHistoryID = strconv.FormatUint(hid, 10)
	if err := session.SaveWatcherMeta(meta); err != nil {
		slog.Warn("gmail: persist history id failed",
			slog.String("err", err.Error()))
	}
}

// passesLabelFilter returns true when the message should be emitted. An empty
// filter (a.labels == nil) passes everything. D-17.
func (a *GmailAdapter) passesLabelFilter(labelIDs []string) bool {
	if len(a.labels) == 0 {
		return true
	}
	for _, id := range labelIDs {
		if _, ok := a.labels[id]; ok {
			return true
		}
	}
	return false
}

// normalizeGmailMessage converts a gmail.Message to a watcher Event. Per D-15,
// Sender is the email-only (display name stripped), Subject is the raw header,
// Body is the Snippet (~200 chars from metadata format), and Timestamp is the
// Gmail internalDate (milliseconds since epoch) converted to UTC. RawPayload
// holds the marshaled gmail.Message for debugging.
func normalizeGmailMessage(m *gmail.Message) Event {
	var from, subject string
	if m != nil && m.Payload != nil {
		for _, h := range m.Payload.Headers {
			switch strings.ToLower(h.Name) {
			case "from":
				from = parseEmailOnly(h.Value)
			case "subject":
				subject = h.Value
			}
		}
	}
	var rawJSON []byte
	if m != nil {
		rawJSON, _ = m.MarshalJSON()
	}
	var ts time.Time
	if m != nil {
		ts = time.UnixMilli(m.InternalDate).UTC()
	}
	var body string
	if m != nil {
		body = m.Snippet
	}
	return Event{
		Source:     "gmail",
		Sender:     from,
		Subject:    subject,
		Body:       body,
		Timestamp:  ts,
		RawPayload: json.RawMessage(rawJSON),
	}
}

// parseEmailOnly strips the display name from "Name <email@host>" header values
// and returns just the email address. Falls back to the input on parse error.
func parseEmailOnly(headerValue string) string {
	addr, err := mail.ParseAddress(headerValue)
	if err != nil {
		return strings.TrimSpace(headerValue)
	}
	return addr.Address
}

// parseProjectID extracts the project ID from a Pub/Sub topic name of the form
// projects/{projectID}/topics/{topic}.
func parseProjectID(topic string) (string, error) {
	parts := strings.Split(topic, "/")
	if len(parts) < 2 || parts[0] != "projects" {
		return "", fmt.Errorf("gmail: invalid topic format %q (want projects/PROJECT/topics/TOPIC)", topic)
	}
	return parts[1], nil
}

// subscriptionIDFromName extracts the trailing ID from a full subscription
// resource name of the form projects/{projectID}/subscriptions/{sub}.
func subscriptionIDFromName(name string) string {
	parts := strings.Split(name, "/")
	return parts[len(parts)-1]
}

// isStaleHistoryError returns true when err is a Google API 404 — the signal
// that Gmail's history retention window has lapsed. Pitfall 6 / D-18.
func isStaleHistoryError(err error) bool {
	var gErr *googleapi.Error
	if errors.As(err, &gErr) && gErr.Code == 404 {
		return true
	}
	return false
}

// joinPath concatenates dir + "/" + file without importing path/filepath for a
// single use. Keeps imports tight; path.Join would mangle Windows separators.
func joinPath(dir, file string) string {
	if dir == "" {
		return file
	}
	if strings.HasSuffix(dir, "/") {
		return dir + file
	}
	return dir + "/" + file
}

// persistingTokenSource wraps an oauth2.TokenSource and writes the token back to
// disk whenever Token() returns a token different from the last persisted one.
// Safe for concurrent use. Pitfall 4.
type persistingTokenSource struct {
	inner oauth2.TokenSource
	path  string
	mu    sync.Mutex
	last  *oauth2.Token
}

// newPersistingTokenSource wraps a cfg.TokenSource with ReuseTokenSource (which
// caches valid tokens and calls the underlying source on refresh) and a
// persistence observer that writes refreshed tokens back to disk atomically
// via writeTokenAtomic.
func newPersistingTokenSource(ctx context.Context, cfg *oauth2.Config, initial *oauth2.Token, path string) oauth2.TokenSource {
	inner := oauth2.ReuseTokenSource(initial, cfg.TokenSource(ctx, initial))
	return &persistingTokenSource{
		inner: inner,
		path:  path,
		last:  initial,
	}
}

// Token returns the current OAuth token, persisting any change to disk. Never
// passes the *oauth2.Token value to slog — tokens are secrets. V7 / T-17-06.
func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	t, err := p.inner.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil || t.AccessToken != p.last.AccessToken || !t.Expiry.Equal(p.last.Expiry) {
		if werr := writeTokenAtomic(p.path, t); werr != nil {
			slog.Warn("gmail: failed to persist refreshed token",
				slog.String("path", p.path),
				slog.String("err", werr.Error()))
		}
		p.last = t
	}
	return t, nil
}

// writeTokenAtomic marshals t to JSON and writes it via temp+rename with mode
// 0600. Token files contain the long-lived refresh token — file mode is
// security-critical per RESEARCH.md §Security Domain. T-17-05.
func writeTokenAtomic(path string, t *oauth2.Token) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// newGmailAdapterForTest constructs a GmailAdapter with a custom Gmail endpoint
// and a pre-built pubsub client. Used by gmail_test.go to bypass the full Setup
// flow (which requires real OAuth + a live Gmail API). Go does not enforce
// test-only visibility, so the helper lives next to the production code with
// a `ForTest` suffix documenting intent — the same pattern ntfy.go uses.
func newGmailAdapterForTest(name, gmailEndpoint string, ps *pubsub.Client, sub *pubsub.Subscription) *GmailAdapter {
	a := NewGmailAdapter()
	a.name = name
	a.pubsubClient = ps
	a.subscription = sub
	if gmailEndpoint != "" {
		svc, _ := gmail.NewService(context.Background(),
			option.WithEndpoint(gmailEndpoint),
			option.WithoutAuthentication())
		a.gmailSvc = svc
	}
	return a
}
