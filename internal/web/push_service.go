package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const (
	pushSubscriptionsFileName = "web_push_subscriptions.json"
	defaultPushPollInterval   = 3 * time.Second
)

type pushSubscription struct {
	Endpoint       string               `json:"endpoint"`
	ExpirationTime any                  `json:"expirationTime,omitempty"`
	Keys           pushSubscriptionKeys `json:"keys"`
	ClientFocused  *bool                `json:"clientFocused,omitempty"`
	FocusUpdatedAt time.Time            `json:"focusUpdatedAt,omitempty"`
}

type pushSubscriptionKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

func (s pushSubscription) normalize() pushSubscription {
	s.Endpoint = strings.TrimSpace(s.Endpoint)
	s.Keys.P256DH = strings.TrimSpace(s.Keys.P256DH)
	s.Keys.Auth = strings.TrimSpace(s.Keys.Auth)
	return s
}

func (s pushSubscription) validate() error {
	sub := s.normalize()
	if sub.Endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}
	if sub.Keys.P256DH == "" {
		return fmt.Errorf("keys.p256dh is required")
	}
	if sub.Keys.Auth == "" {
		return fmt.Errorf("keys.auth is required")
	}
	return nil
}

type pushSubscriptionFile struct {
	UpdatedAt     time.Time          `json:"updatedAt"`
	Subscriptions []pushSubscription `json:"subscriptions"`
}

type pushSubscriptionStore interface {
	List(ctx context.Context) ([]pushSubscription, error)
	Upsert(ctx context.Context, sub pushSubscription) error
	UpdateFocusByEndpoint(ctx context.Context, endpoint string, focused bool) error
	RemoveByEndpoint(ctx context.Context, endpoint string) error
	Count(ctx context.Context) (int, error)
}

type pushSubscriptionFileStore struct {
	path string
	mu   sync.Mutex
}

func newPushSubscriptionFileStore(profile string) (*pushSubscriptionFileStore, error) {
	profileDir, err := session.GetProfileDir(session.GetEffectiveProfile(profile))
	if err != nil {
		return nil, fmt.Errorf("resolve profile dir: %w", err)
	}
	return &pushSubscriptionFileStore{
		path: filepath.Join(profileDir, pushSubscriptionsFileName),
	}, nil
}

func (s *pushSubscriptionFileStore) List(_ context.Context) ([]pushSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	out := make([]pushSubscription, len(data.Subscriptions))
	copy(out, data.Subscriptions)
	return out, nil
}

func (s *pushSubscriptionFileStore) Count(ctx context.Context) (int, error) {
	subs, err := s.List(ctx)
	if err != nil {
		return 0, err
	}
	return len(subs), nil
}

func (s *pushSubscriptionFileStore) Upsert(_ context.Context, sub pushSubscription) error {
	sub = sub.normalize()
	if err := sub.validate(); err != nil {
		return err
	}
	if sub.ClientFocused != nil && sub.FocusUpdatedAt.IsZero() {
		sub.FocusUpdatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readLocked()
	if err != nil {
		return err
	}

	updated := false
	for i := range data.Subscriptions {
		if data.Subscriptions[i].Endpoint != sub.Endpoint {
			continue
		}
		// Preserve last known focus state unless caller explicitly sends one.
		if sub.ClientFocused == nil && data.Subscriptions[i].ClientFocused != nil {
			sub.ClientFocused = data.Subscriptions[i].ClientFocused
			sub.FocusUpdatedAt = data.Subscriptions[i].FocusUpdatedAt
		}
		data.Subscriptions[i] = sub
		updated = true
		break
	}
	if !updated {
		data.Subscriptions = append(data.Subscriptions, sub)
	}
	data.UpdatedAt = time.Now().UTC()

	return s.writeLocked(data)
}

func (s *pushSubscriptionFileStore) UpdateFocusByEndpoint(_ context.Context, endpoint string, focused bool) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readLocked()
	if err != nil {
		return err
	}

	found := false
	for i := range data.Subscriptions {
		if data.Subscriptions[i].Endpoint != endpoint {
			continue
		}
		focusedCopy := focused
		data.Subscriptions[i].ClientFocused = &focusedCopy
		data.Subscriptions[i].FocusUpdatedAt = time.Now().UTC()
		found = true
		break
	}
	if !found {
		return nil
	}

	data.UpdatedAt = time.Now().UTC()
	return s.writeLocked(data)
}

func (s *pushSubscriptionFileStore) RemoveByEndpoint(_ context.Context, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readLocked()
	if err != nil {
		return err
	}

	filtered := make([]pushSubscription, 0, len(data.Subscriptions))
	for _, sub := range data.Subscriptions {
		if sub.Endpoint == endpoint {
			continue
		}
		filtered = append(filtered, sub)
	}

	data.Subscriptions = filtered
	data.UpdatedAt = time.Now().UTC()
	return s.writeLocked(data)
}

func (s *pushSubscriptionFileStore) readLocked() (*pushSubscriptionFile, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &pushSubscriptionFile{
				UpdatedAt:     time.Now().UTC(),
				Subscriptions: []pushSubscription{},
			}, nil
		}
		return nil, fmt.Errorf("read push subscriptions: %w", err)
	}

	var data pushSubscriptionFile
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse push subscriptions: %w", err)
	}
	if data.Subscriptions == nil {
		data.Subscriptions = []pushSubscription{}
	}
	return &data, nil
}

func (s *pushSubscriptionFileStore) writeLocked(data *pushSubscriptionFile) error {
	if data == nil {
		data = &pushSubscriptionFile{Subscriptions: []pushSubscription{}}
	}
	if data.Subscriptions == nil {
		data.Subscriptions = []pushSubscription{}
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("mkdir push subscription dir: %w", err)
	}

	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal push subscriptions: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write temp push subscriptions: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename push subscriptions: %w", err)
	}
	return nil
}

type webPushSender interface {
	Send(payload []byte, sub pushSubscription) (int, error)
}

type vapidPushSender struct {
	subject    string
	publicKey  string
	privateKey string
}

func (s *vapidPushSender) Send(payload []byte, sub pushSubscription) (int, error) {
	sub = sub.normalize()
	resp, err := webpush.SendNotification(payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys: webpush.Keys{
			P256dh: sub.Keys.P256DH,
			Auth:   sub.Keys.Auth,
		},
	}, &webpush.Options{
		Subscriber:      s.subject,
		VAPIDPublicKey:  s.publicKey,
		VAPIDPrivateKey: s.privateKey,
		TTL:             3600,
	})
	if resp != nil {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
	}

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}

	if err != nil {
		return status, err
	}
	if status >= 400 {
		return status, fmt.Errorf("push gateway status %d", status)
	}
	return status, nil
}

type pushTransition struct {
	Profile string
	Session *MenuSession
	Status  string
}

type pushMessage struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Tag        string `json:"tag,omitempty"`
	Renotify   bool   `json:"renotify,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	Session    string `json:"session,omitempty"`
	Status     string `json:"status,omitempty"`
	Profile    string `json:"profile,omitempty"`
	Path       string `json:"path,omitempty"`
	Timestamp  string `json:"timestamp"`
	RequireInt bool   `json:"requireInteraction,omitempty"`
}

type pushServiceAPI interface {
	Start(ctx context.Context)
	TriggerSync()
	Enabled() bool
	PublicKey() string
	Subject() string
	SubscriptionCount(ctx context.Context) (int, error)
	UpsertSubscription(ctx context.Context, sub pushSubscription) error
	UpdateSubscriptionFocus(ctx context.Context, endpoint string, focused bool) error
	RemoveSubscriptionByEndpoint(ctx context.Context, endpoint string) error
}

type pushService struct {
	enabled bool

	publicKey  string
	privateKey string
	subject    string
	profile    string
	token      string

	menuData MenuDataLoader
	store    pushSubscriptionStore
	sender   webPushSender

	pollInterval time.Duration
	testEvery    time.Duration

	startOnce sync.Once
	triggerCh chan struct{}

	mu          sync.Mutex
	initialized bool
	lastStatus  map[string]string
}

func newPushService(cfg Config, menuData MenuDataLoader) (pushServiceAPI, error) {
	publicKey := strings.TrimSpace(cfg.PushVAPIDPublicKey)
	privateKey := strings.TrimSpace(cfg.PushVAPIDPrivateKey)

	if publicKey == "" && privateKey == "" {
		return nil, nil
	}
	if publicKey == "" || privateKey == "" {
		return nil, fmt.Errorf("both push vapid public and private keys are required")
	}

	subject := strings.TrimSpace(cfg.PushVAPIDSubject)
	if subject == "" {
		subject = "mailto:agentdeck@localhost"
	}

	store, err := newPushSubscriptionFileStore(cfg.Profile)
	if err != nil {
		return nil, err
	}

	return &pushService{
		enabled:      true,
		publicKey:    publicKey,
		privateKey:   privateKey,
		subject:      subject,
		profile:      session.GetEffectiveProfile(cfg.Profile),
		token:        strings.TrimSpace(cfg.Token),
		menuData:     menuData,
		store:        store,
		sender:       &vapidPushSender{subject: subject, publicKey: publicKey, privateKey: privateKey},
		pollInterval: defaultPushPollInterval,
		testEvery:    cfg.PushTestInterval,
		triggerCh:    make(chan struct{}, 1),
		lastStatus:   make(map[string]string),
	}, nil
}

func (p *pushService) Start(ctx context.Context) {
	if p == nil || !p.enabled {
		return
	}
	p.startOnce.Do(func() {
		go p.run(ctx)
	})
}

func (p *pushService) TriggerSync() {
	if p == nil || !p.enabled {
		return
	}
	select {
	case p.triggerCh <- struct{}{}:
	default:
	}
}

func (p *pushService) Enabled() bool {
	return p != nil && p.enabled
}

func (p *pushService) PublicKey() string {
	if p == nil {
		return ""
	}
	return p.publicKey
}

func (p *pushService) Subject() string {
	if p == nil {
		return ""
	}
	return p.subject
}

func (p *pushService) SubscriptionCount(ctx context.Context) (int, error) {
	if p == nil || p.store == nil {
		return 0, nil
	}
	return p.store.Count(ctx)
}

func (p *pushService) UpsertSubscription(ctx context.Context, sub pushSubscription) error {
	if p == nil || !p.enabled || p.store == nil {
		return fmt.Errorf("push service is not configured")
	}
	return p.store.Upsert(ctx, sub)
}

func (p *pushService) RemoveSubscriptionByEndpoint(ctx context.Context, endpoint string) error {
	if p == nil || !p.enabled || p.store == nil {
		return fmt.Errorf("push service is not configured")
	}
	return p.store.RemoveByEndpoint(ctx, endpoint)
}

func (p *pushService) UpdateSubscriptionFocus(ctx context.Context, endpoint string, focused bool) error {
	if p == nil || !p.enabled || p.store == nil {
		return fmt.Errorf("push service is not configured")
	}
	return p.store.UpdateFocusByEndpoint(ctx, endpoint, focused)
}

var pushLog = logging.ForComponent(logging.CompWeb)

func (p *pushService) run(ctx context.Context) {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	var testTicker *time.Ticker
	var testTick <-chan time.Time
	if p.testEvery > 0 {
		testTicker = time.NewTicker(p.testEvery)
		testTick = testTicker.C
		pushLog.Info("push_test_enabled", slog.String("interval", p.testEvery.String()))
		defer testTicker.Stop()
	}

	// Prime baseline to avoid startup notification flood.
	p.syncOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.syncOnce(ctx)
		case <-p.triggerCh:
			p.syncOnce(ctx)
		case <-testTick:
			p.sendTestPush(ctx)
		}
	}
}

func (p *pushService) syncOnce(ctx context.Context) {
	snapshot, err := p.menuData.LoadMenuSnapshot()
	if err != nil {
		pushLog.Error("push_snapshot_load_failed", slog.String("error", err.Error()))
		return
	}

	current := make(map[string]string)
	sessions := make(map[string]*MenuSession)
	for _, item := range snapshot.Items {
		if item.Type != MenuItemTypeSession || item.Session == nil {
			continue
		}
		sessionCopy := *item.Session
		current[item.Session.ID] = strings.ToLower(string(item.Session.Status))
		sessions[item.Session.ID] = &sessionCopy
	}

	transitions := make([]pushTransition, 0)

	p.mu.Lock()
	if !p.initialized {
		p.lastStatus = current
		p.initialized = true
		p.mu.Unlock()
		return
	}

	for sessionID, status := range current {
		prev := strings.ToLower(strings.TrimSpace(p.lastStatus[sessionID]))
		if prev == status {
			continue
		}
		if status != "waiting" && status != "error" && status != "idle" {
			continue
		}

		sessionMeta := sessions[sessionID]
		if sessionMeta == nil {
			continue
		}
		transitions = append(transitions, pushTransition{
			Profile: snapshot.Profile,
			Session: sessionMeta,
			Status:  status,
		})
		pushLog.Debug("push_transition",
			slog.String("session", sessionID),
			slog.String("profile", snapshot.Profile),
			slog.String("from", prev),
			slog.String("to", status))
	}

	p.lastStatus = current
	p.mu.Unlock()

	for _, tr := range transitions {
		p.notifySubscribers(ctx, tr)
	}
}

func (p *pushService) notifySubscribers(ctx context.Context, tr pushTransition) {
	if p == nil || p.store == nil || p.sender == nil || tr.Session == nil {
		return
	}

	subs, err := p.store.List(ctx)
	if err != nil {
		pushLog.Error("push_list_subscriptions_failed", slog.String("error", err.Error()))
		return
	}
	if len(subs) == 0 {
		return
	}
	pushLog.Debug("push_notifying",
		slog.String("session", tr.Session.ID),
		slog.String("status", tr.Status),
		slog.Int("subscribers", len(subs)))

	msg := pushMessage{
		Title:      pushTitleForStatus(tr),
		Body:       pushBodyForStatus(tr),
		Tag:        fmt.Sprintf("agentdeck-%s-%s", tr.Session.ID, tr.Status),
		Renotify:   true,
		SessionID:  tr.Session.ID,
		Session:    tr.Session.Title,
		Status:     tr.Status,
		Profile:    tr.Profile,
		Path:       p.routePath("/s/" + url.PathEscape(tr.Session.ID)),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		RequireInt: tr.Status == "error",
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		pushLog.Error("push_marshal_failed", slog.String("error", err.Error()))
		return
	}

	for _, sub := range subs {
		if !shouldNotifySubscription(sub, tr) {
			pushLog.Debug("push_skipped",
				slog.String("endpoint", endpointForLog(sub.Endpoint)),
				slog.String("session", tr.Session.ID),
				slog.String("status", tr.Status),
				slog.String("reason", "focused_state"),
				slog.String("state", focusStateForLog(sub)))
			continue
		}
		statusCode, err := p.sender.Send(payload, sub)
		if err == nil {
			pushLog.Debug("push_sent",
				slog.String("endpoint", endpointForLog(sub.Endpoint)),
				slog.Int("http_status", statusCode),
				slog.String("session", tr.Session.ID),
				slog.String("status_change", tr.Status))
			continue
		}

		pushLog.Error("push_send_failed",
			slog.String("endpoint", sub.Endpoint),
			slog.Int("http_status", statusCode),
			slog.String("session", tr.Session.ID),
			slog.String("status_change", tr.Status),
			slog.String("error", err.Error()))
		if statusCode == http.StatusGone || statusCode == http.StatusNotFound {
			_ = p.store.RemoveByEndpoint(ctx, sub.Endpoint)
		}
	}
}

func (p *pushService) sendTestPush(ctx context.Context) {
	if p == nil || p.store == nil || p.sender == nil {
		return
	}

	subs, err := p.store.List(ctx)
	if err != nil {
		pushLog.Error("push_test_list_failed", slog.String("error", err.Error()))
		return
	}
	if len(subs) == 0 {
		pushLog.Debug("push_test_skipped_no_subs")
		return
	}

	now := time.Now().UTC()
	msg := pushMessage{
		Title:     "Agent Deck: Push Test",
		Body:      fmt.Sprintf("Periodic test notification (%s)", now.Format(time.RFC3339)),
		Tag:       fmt.Sprintf("agentdeck-test-%d", now.UnixNano()),
		Renotify:  true,
		Status:    "test",
		Path:      p.routePath("/"),
		Timestamp: now.Format(time.RFC3339),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		pushLog.Error("push_test_marshal_failed", slog.String("error", err.Error()))
		return
	}

	pushLog.Debug("push_test_sending", slog.Int("subscribers", len(subs)))
	for _, sub := range subs {
		if !shouldNotifySubscription(sub, pushTransition{Status: "test"}) {
			pushLog.Debug("push_test_skipped",
				slog.String("endpoint", endpointForLog(sub.Endpoint)),
				slog.String("reason", "focused_state"),
				slog.String("state", focusStateForLog(sub)))
			continue
		}
		statusCode, err := p.sender.Send(payload, sub)
		if err == nil {
			pushLog.Debug("push_test_sent",
				slog.String("endpoint", endpointForLog(sub.Endpoint)),
				slog.Int("http_status", statusCode))
			continue
		}

		pushLog.Error("push_test_send_failed",
			slog.String("endpoint", sub.Endpoint),
			slog.Int("http_status", statusCode),
			slog.String("error", err.Error()))
		if statusCode == http.StatusGone || statusCode == http.StatusNotFound {
			_ = p.store.RemoveByEndpoint(ctx, sub.Endpoint)
		}
	}
}

func pushTitleForStatus(tr pushTransition) string {
	sessionName := ""
	if tr.Session != nil {
		sessionName = strings.TrimSpace(tr.Session.Title)
		if sessionName == "" {
			sessionName = strings.TrimSpace(tr.Session.ID)
		}
	}
	if sessionName == "" {
		sessionName = "Session"
	}

	if tr.Status == "error" {
		return fmt.Sprintf("Agent Deck: %s (error)", sessionName)
	}
	if tr.Status == "idle" {
		return fmt.Sprintf("Agent Deck: %s (idle)", sessionName)
	}
	return fmt.Sprintf("Agent Deck: %s (waiting)", sessionName)
}

func pushBodyForStatus(tr pushTransition) string {
	sessionName := strings.TrimSpace(tr.Session.Title)
	if sessionName == "" {
		sessionName = tr.Session.ID
	}
	if tr.Status == "" {
		return fmt.Sprintf("%s changed status.", sessionName)
	}
	return fmt.Sprintf("%s changed to %s.", sessionName, tr.Status)
}

func shouldNotifySubscription(sub pushSubscription, tr pushTransition) bool {
	_ = tr
	if sub.ClientFocused == nil {
		return false
	}
	return !*sub.ClientFocused
}

func endpointForLog(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err == nil && u.Host != "" {
		return u.Host
	}
	endpoint = strings.TrimSpace(endpoint)
	if len(endpoint) <= 48 {
		return endpoint
	}
	return endpoint[:48] + "..."
}

func focusStateForLog(sub pushSubscription) string {
	if sub.ClientFocused == nil {
		return "unknown"
	}
	if *sub.ClientFocused {
		return "focused"
	}
	return "unfocused"
}

func (p *pushService) routePath(basePath string) string {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" {
		basePath = "/"
	}
	if p == nil || strings.TrimSpace(p.token) == "" {
		return basePath
	}

	u := &url.URL{Path: basePath}
	query := u.Query()
	query.Set("token", p.token)
	u.RawQuery = query.Encode()
	return u.String()
}
