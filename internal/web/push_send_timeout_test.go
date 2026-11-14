package web

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// orderedPushStore lists subscriptions in insertion order, so the test can
// put the hung endpoint ahead of the healthy one.
type orderedPushStore struct {
	fakePushStore
	order []pushSubscription
}

func (s *orderedPushStore) List(_ context.Context) ([]pushSubscription, error) {
	return append([]pushSubscription(nil), s.order...), nil
}

func newTestPushKeys(t *testing.T) pushSubscriptionKeys {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate p256dh: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("generate auth: %v", err)
	}
	return pushSubscriptionKeys{
		P256DH: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(auth),
	}
}

// A push endpoint that accepts the request and never answers must not block
// the subscribers after it: the real VAPID sender has to give up on its own.
func TestVapidPushSenderHungEndpointDoesNotBlockLaterSubscribers(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hung.Close()
	defer close(release)

	var mu sync.Mutex
	delivered := make(chan struct{}, 1)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		mu.Lock()
		defer mu.Unlock()
		select {
		case delivered <- struct{}{}:
		default:
		}
	}))
	defer healthy.Close()

	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("generate vapid keys: %v", err)
	}
	focused := false
	store := &orderedPushStore{order: []pushSubscription{
		{Endpoint: hung.URL + "/hung", ClientFocused: &focused, Keys: newTestPushKeys(t)},
		{Endpoint: healthy.URL + "/ok", ClientFocused: &focused, Keys: newTestPushKeys(t)},
	}}
	push := &pushService{
		enabled:    true,
		store:      store,
		sender:     &vapidPushSender{subject: "mailto:test@localhost", publicKey: publicKey, privateKey: privateKey},
		lastStatus: make(map[string]string),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		push.notifySubscribers(context.Background(), pushTransition{
			Profile: "work",
			Session: &MenuSession{ID: "sess-1", Title: "Build Bot", Status: "waiting"},
			Status:  "waiting",
		})
	}()

	bound := 20 * time.Second
	select {
	case <-delivered:
	case <-time.After(bound):
		t.Fatalf("healthy subscriber not notified within %s: hung endpoint blocks the serial send loop", bound)
	}
	select {
	case <-done:
	case <-time.After(bound):
		t.Fatalf("notifySubscribers did not return within %s", bound)
	}
}
