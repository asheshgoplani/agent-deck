package web

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// RemoteFleetLoader provides a read-only snapshot of configured SSH remotes.
// It is exported so embedders can supply a cached or otherwise customized
// scanner without coupling the web package to its concrete implementation.
type RemoteFleetLoader interface {
	Scan(context.Context) (session.RemoteFleetSnapshot, error)
}

const remoteFleetCacheTTL = 5 * time.Second

type remoteFleetFlight struct {
	done     chan struct{}
	snapshot session.RemoteFleetSnapshot
	err      error
	waiters  int
}

// remoteFleetCache prevents multiple web clients from multiplying SSH work.
// A fresh successful snapshot is reused briefly; concurrent cache misses share
// one scan and receive the same result.
type remoteFleetCache struct {
	loader RemoteFleetLoader
	ttl    time.Duration
	now    func() time.Time

	mu       sync.Mutex
	snapshot session.RemoteFleetSnapshot
	expires  time.Time
	inFlight *remoteFleetFlight
}

func newRemoteFleetCache(loader RemoteFleetLoader) *remoteFleetCache {
	return &remoteFleetCache{loader: loader, ttl: remoteFleetCacheTTL, now: time.Now}
}

func (c *remoteFleetCache) Scan(ctx context.Context) (session.RemoteFleetSnapshot, error) {
	if c == nil || c.loader == nil {
		return session.RemoteFleetSnapshot{}, errors.New("remote fleet cache is not configured")
	}
	now := c.now
	if now == nil {
		now = time.Now
	}

	c.mu.Lock()
	if !c.expires.IsZero() && now().Before(c.expires) {
		snapshot := c.snapshot
		c.mu.Unlock()
		return snapshot, nil
	}
	if c.inFlight != nil {
		flight := c.inFlight
		flight.waiters++
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return session.RemoteFleetSnapshot{}, ctx.Err()
		case <-flight.done:
			return flight.snapshot, flight.err
		}
	}
	flight := &remoteFleetFlight{done: make(chan struct{})}
	c.inFlight = flight
	c.mu.Unlock()

	flight.snapshot, flight.err = c.loader.Scan(ctx)
	c.mu.Lock()
	if flight.err == nil {
		c.snapshot = flight.snapshot
		c.expires = now().Add(c.ttl)
	}
	c.inFlight = nil
	close(flight.done)
	c.mu.Unlock()
	return flight.snapshot, flight.err
}

func (s *Server) handleRemotes(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if s.remoteFleet == nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "remote fleet is unavailable")
		return
	}
	snapshot, err := s.remoteFleet.Scan(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "remote fleet scan failed")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
