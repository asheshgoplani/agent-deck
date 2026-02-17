package web

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakePushStore struct {
	mu   sync.Mutex
	subs map[string]pushSubscription
}

func newFakePushStore() *fakePushStore {
	return &fakePushStore{
		subs: make(map[string]pushSubscription),
	}
}

func (s *fakePushStore) List(_ context.Context) ([]pushSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]pushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out, nil
}

func (s *fakePushStore) Upsert(_ context.Context, sub pushSubscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[sub.Endpoint] = sub
	return nil
}

func (s *fakePushStore) UpdateFocusByEndpoint(_ context.Context, endpoint string, focused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[endpoint]
	if !ok {
		return nil
	}
	focusedCopy := focused
	sub.ClientFocused = &focusedCopy
	sub.FocusUpdatedAt = time.Now().UTC()
	s.subs[endpoint] = sub
	return nil
}

func (s *fakePushStore) RemoveByEndpoint(_ context.Context, endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, endpoint)
	return nil
}

func (s *fakePushStore) Count(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs), nil
}

type fakePushSender struct {
	mu          sync.Mutex
	payloads    [][]byte
	statusCode  int
	returnError error
}

func (s *fakePushSender) Send(payload []byte, _ pushSubscription) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, append([]byte(nil), payload...))
	return s.statusCode, s.returnError
}

type rotatingPushMenuData struct {
	mu        sync.Mutex
	snapshots []*MenuSnapshot
	index     int
}

func (r *rotatingPushMenuData) LoadMenuSnapshot() (*MenuSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.snapshots) == 0 {
		return &MenuSnapshot{}, nil
	}
	idx := r.index
	if idx >= len(r.snapshots) {
		idx = len(r.snapshots) - 1
	}
	if r.index < len(r.snapshots)-1 {
		r.index++
	}
	return r.snapshots[idx], nil
}

func TestPushServiceNotifiesOnWaitingTransition(t *testing.T) {
	menu := &rotatingPushMenuData{
		snapshots: []*MenuSnapshot{
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-1",
							Title:  "Build Bot",
							Status: "running",
						},
					},
				},
			},
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-1",
							Title:  "Build Bot",
							Status: "waiting",
						},
					},
				},
			},
		},
	}
	store := newFakePushStore()
	focused := false
	_ = store.Upsert(context.Background(), pushSubscription{
		Endpoint:      "https://push.example/sub-a",
		ClientFocused: &focused,
		Keys: pushSubscriptionKeys{
			P256DH: "k1",
			Auth:   "k2",
		},
	})
	sender := &fakePushSender{}

	push := &pushService{
		enabled:      true,
		menuData:     menu,
		store:        store,
		sender:       sender,
		lastStatus:   make(map[string]string),
		pollInterval: defaultPushPollInterval,
	}

	push.syncOnce(context.Background()) // baseline only
	push.syncOnce(context.Background()) // transition

	if len(sender.payloads) != 1 {
		t.Fatalf("expected exactly 1 push payload, got %d", len(sender.payloads))
	}

	var msg pushMessage
	if err := json.Unmarshal(sender.payloads[0], &msg); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if msg.SessionID != "sess-1" {
		t.Fatalf("expected session id sess-1, got %q", msg.SessionID)
	}
	if msg.Status != "waiting" {
		t.Fatalf("expected waiting status, got %q", msg.Status)
	}
	if msg.Path != "/s/sess-1" {
		t.Fatalf("expected path /s/sess-1, got %q", msg.Path)
	}
	if msg.Body != "Build Bot changed to waiting." {
		t.Fatalf("expected simplified body, got %q", msg.Body)
	}
}

func TestPushServiceAddsTokenToNotificationPathWhenConfigured(t *testing.T) {
	menu := &rotatingPushMenuData{
		snapshots: []*MenuSnapshot{
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-token",
							Title:  "Token Bot",
							Status: "running",
						},
					},
				},
			},
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-token",
							Title:  "Token Bot",
							Status: "waiting",
						},
					},
				},
			},
		},
	}
	store := newFakePushStore()
	focused := false
	_ = store.Upsert(context.Background(), pushSubscription{
		Endpoint:      "https://push.example/sub-token",
		ClientFocused: &focused,
		Keys: pushSubscriptionKeys{
			P256DH: "k1",
			Auth:   "k2",
		},
	})
	sender := &fakePushSender{}

	push := &pushService{
		enabled:      true,
		token:        "secret-token",
		menuData:     menu,
		store:        store,
		sender:       sender,
		lastStatus:   make(map[string]string),
		pollInterval: defaultPushPollInterval,
	}

	push.syncOnce(context.Background())
	push.syncOnce(context.Background())

	if len(sender.payloads) != 1 {
		t.Fatalf("expected exactly 1 push payload, got %d", len(sender.payloads))
	}

	var msg pushMessage
	if err := json.Unmarshal(sender.payloads[0], &msg); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if msg.Path != "/s/sess-token?token=secret-token" {
		t.Fatalf("expected tokenized path, got %q", msg.Path)
	}
}

func TestPushServiceRemovesExpiredSubscription(t *testing.T) {
	menu := &rotatingPushMenuData{
		snapshots: []*MenuSnapshot{
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-2",
							Title:  "Deploy Bot",
							Status: "running",
						},
					},
				},
			},
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-2",
							Title:  "Deploy Bot",
							Status: "error",
						},
					},
				},
			},
		},
	}

	store := newFakePushStore()
	focused := false
	_ = store.Upsert(context.Background(), pushSubscription{
		Endpoint:      "https://push.example/sub-expired",
		ClientFocused: &focused,
		Keys: pushSubscriptionKeys{
			P256DH: "k1",
			Auth:   "k2",
		},
	})

	sender := &fakePushSender{
		statusCode:  410,
		returnError: errors.New("gone"),
	}

	push := &pushService{
		enabled:      true,
		menuData:     menu,
		store:        store,
		sender:       sender,
		lastStatus:   make(map[string]string),
		pollInterval: defaultPushPollInterval,
	}

	push.syncOnce(context.Background())
	push.syncOnce(context.Background())

	count, err := store.Count(context.Background())
	if err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected expired subscription to be removed, count=%d", count)
	}
}

func TestPushServiceTriggerSyncProcessesImmediateTransition(t *testing.T) {
	menu := &rotatingPushMenuData{
		snapshots: []*MenuSnapshot{
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-3",
							Title:  "Review Bot",
							Status: "running",
						},
					},
				},
			},
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-3",
							Title:  "Review Bot",
							Status: "waiting",
						},
					},
				},
			},
		},
	}

	store := newFakePushStore()
	focused := false
	_ = store.Upsert(context.Background(), pushSubscription{
		Endpoint:      "https://push.example/sub-trigger",
		ClientFocused: &focused,
		Keys: pushSubscriptionKeys{
			P256DH: "k1",
			Auth:   "k2",
		},
	})

	sender := &fakePushSender{}
	push := &pushService{
		enabled:      true,
		menuData:     menu,
		store:        store,
		sender:       sender,
		pollInterval: time.Hour,
		triggerCh:    make(chan struct{}, 1),
		lastStatus:   make(map[string]string),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go push.run(ctx)

	push.TriggerSync()

	deadline := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(deadline) {
		sender.mu.Lock()
		count := len(sender.payloads)
		sender.mu.Unlock()
		if count == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	sender.mu.Lock()
	count := len(sender.payloads)
	sender.mu.Unlock()
	t.Fatalf("expected 1 push payload after immediate trigger, got %d", count)
}

func TestPushServiceIdleTransitionNotifiesOnlyWhenUnfocused(t *testing.T) {
	menu := &rotatingPushMenuData{
		snapshots: []*MenuSnapshot{
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-idle",
							Title:  "Idle Bot",
							Status: "running",
						},
					},
				},
			},
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-idle",
							Title:  "Idle Bot",
							Status: "idle",
						},
					},
				},
			},
		},
	}

	store := newFakePushStore()
	focusedTrue := true
	focusedFalse := false
	_ = store.Upsert(context.Background(), pushSubscription{
		Endpoint:      "https://push.example/sub-focused",
		ClientFocused: &focusedTrue,
		Keys: pushSubscriptionKeys{
			P256DH: "k1",
			Auth:   "k2",
		},
	})
	_ = store.Upsert(context.Background(), pushSubscription{
		Endpoint:      "https://push.example/sub-unfocused",
		ClientFocused: &focusedFalse,
		Keys: pushSubscriptionKeys{
			P256DH: "k3",
			Auth:   "k4",
		},
	})

	sender := &fakePushSender{}
	push := &pushService{
		enabled:      true,
		menuData:     menu,
		store:        store,
		sender:       sender,
		lastStatus:   make(map[string]string),
		pollInterval: defaultPushPollInterval,
	}

	push.syncOnce(context.Background()) // baseline
	push.syncOnce(context.Background()) // running -> idle

	if len(sender.payloads) != 1 {
		t.Fatalf("expected 1 idle push payload for unfocused subscription, got %d", len(sender.payloads))
	}

	var msg pushMessage
	if err := json.Unmarshal(sender.payloads[0], &msg); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if msg.Status != "idle" {
		t.Fatalf("expected idle push status, got %q", msg.Status)
	}
}

func TestPushServiceSendTestPush(t *testing.T) {
	store := newFakePushStore()
	focused := false
	_ = store.Upsert(context.Background(), pushSubscription{
		Endpoint:      "https://push.example/sub-test",
		ClientFocused: &focused,
		Keys: pushSubscriptionKeys{
			P256DH: "k1",
			Auth:   "k2",
		},
	})

	sender := &fakePushSender{}
	push := &pushService{
		enabled:    true,
		store:      store,
		sender:     sender,
		lastStatus: make(map[string]string),
	}

	push.sendTestPush(context.Background())

	if len(sender.payloads) != 1 {
		t.Fatalf("expected 1 test push payload, got %d", len(sender.payloads))
	}

	var msg pushMessage
	if err := json.Unmarshal(sender.payloads[0], &msg); err != nil {
		t.Fatalf("unmarshal test payload: %v", err)
	}
	if msg.Status != "test" {
		t.Fatalf("expected test push status, got %q", msg.Status)
	}
	if msg.Title == "" {
		t.Fatalf("expected test push title")
	}
}

func TestPushServiceSkipsWhenFocusUnknown(t *testing.T) {
	menu := &rotatingPushMenuData{
		snapshots: []*MenuSnapshot{
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-unknown-focus",
							Title:  "Unknown Focus",
							Status: "running",
						},
					},
				},
			},
			{
				Profile: "work",
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:     "sess-unknown-focus",
							Title:  "Unknown Focus",
							Status: "waiting",
						},
					},
				},
			},
		},
	}

	store := newFakePushStore()
	_ = store.Upsert(context.Background(), pushSubscription{
		Endpoint: "https://push.example/sub-unknown",
		Keys: pushSubscriptionKeys{
			P256DH: "k1",
			Auth:   "k2",
		},
	})

	sender := &fakePushSender{}
	push := &pushService{
		enabled:      true,
		menuData:     menu,
		store:        store,
		sender:       sender,
		lastStatus:   make(map[string]string),
		pollInterval: defaultPushPollInterval,
	}

	push.syncOnce(context.Background())
	push.syncOnce(context.Background())

	if len(sender.payloads) != 0 {
		t.Fatalf("expected no payloads when focus state is unknown, got %d", len(sender.payloads))
	}
}
