package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePushService struct {
	enabled bool
	public  string
	subject string
	subs    map[string]pushSubscription
	focus   map[string]bool
}

func newFakePushService(enabled bool) *fakePushService {
	return &fakePushService{
		enabled: enabled,
		public:  "test-public-key",
		subject: "mailto:test@example.com",
		subs:    make(map[string]pushSubscription),
		focus:   make(map[string]bool),
	}
}

func (f *fakePushService) Start(_ context.Context) {}

func (f *fakePushService) TriggerSync() {}

func (f *fakePushService) Enabled() bool {
	return f.enabled
}

func (f *fakePushService) PublicKey() string {
	return f.public
}

func (f *fakePushService) Subject() string {
	return f.subject
}

func (f *fakePushService) SubscriptionCount(_ context.Context) (int, error) {
	return len(f.subs), nil
}

func (f *fakePushService) UpsertSubscription(_ context.Context, sub pushSubscription) error {
	f.subs[sub.Endpoint] = sub
	return nil
}

func (f *fakePushService) UpdateSubscriptionFocus(_ context.Context, endpoint string, focused bool) error {
	f.focus[endpoint] = focused
	return nil
}

func (f *fakePushService) RemoveSubscriptionByEndpoint(_ context.Context, endpoint string) error {
	delete(f.subs, endpoint)
	return nil
}

func TestPushConfigEndpointDisabledWhenNoService(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.push = nil

	req := httptest.NewRequest(http.MethodGet, "/api/push/config", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"enabled":false`) {
		t.Fatalf("expected enabled=false payload, got: %s", rr.Body.String())
	}
}

func TestPushConfigEndpointEnabled(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	push := newFakePushService(true)
	push.subs["https://push.example/sub"] = pushSubscription{
		Endpoint: "https://push.example/sub",
		Keys: pushSubscriptionKeys{
			P256DH: "k1",
			Auth:   "k2",
		},
	}
	srv.push = push

	req := httptest.NewRequest(http.MethodGet, "/api/push/config", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"enabled":true`) {
		t.Fatalf("expected enabled=true payload, got: %s", body)
	}
	if !strings.Contains(body, `"vapidPublicKey":"test-public-key"`) {
		t.Fatalf("expected public key in payload, got: %s", body)
	}
	if !strings.Contains(body, `"subscriptionCount":1`) {
		t.Fatalf("expected subscription count in payload, got: %s", body)
	}
}

func TestPushSubscribeAndUnsubscribeEndpoints(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	push := newFakePushService(true)
	srv.push = push

	subscribeBody := `{"endpoint":"https://push.example/sub-1","keys":{"p256dh":"key-a","auth":"key-b"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(subscribeBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if _, ok := push.subs["https://push.example/sub-1"]; !ok {
		t.Fatalf("expected subscription to be stored")
	}

	unsubscribeBody := `{"endpoint":"https://push.example/sub-1"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/push/unsubscribe", strings.NewReader(unsubscribeBody))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rr2.Code, rr2.Body.String())
	}
	if _, ok := push.subs["https://push.example/sub-1"]; ok {
		t.Fatalf("expected subscription to be removed")
	}
}

func TestPushPresenceEndpoint(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	push := newFakePushService(true)
	srv.push = push

	subscribeBody := `{"endpoint":"https://push.example/sub-2","keys":{"p256dh":"key-a","auth":"key-b"}}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(subscribeBody))
	req1.Header.Set("Content-Type", "application/json")
	rr1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr1.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/push/presence", strings.NewReader(`{"endpoint":"https://push.example/sub-2","focused":false}`))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rr2.Code, rr2.Body.String())
	}
	if focused, ok := push.focus["https://push.example/sub-2"]; !ok || focused {
		t.Fatalf("expected push presence focused=false to be recorded")
	}
}

func TestPushSubscribeUnauthorizedWhenTokenEnabled(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
	})
	srv.push = newFakePushService(true)

	req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Fatalf("expected UNAUTHORIZED body, got: %s", rr.Body.String())
	}
}
