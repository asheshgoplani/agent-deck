package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestStatusWorkerConfigSelection(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		err     error
		want    time.Duration
	}{
		{0, nil, 2 * time.Second},
		{5, nil, 5 * time.Second},
		{100, nil, 10 * time.Second},
		{5, errors.New("config unavailable"), 2 * time.Second},
	} {
		got := statusWorkerInterval(func() (*session.UserConfig, error) {
			return &session.UserConfig{Performance: session.PerformanceSettings{StatusIntervalSeconds: tc.seconds}}, tc.err
		})
		if got != tc.want {
			t.Fatalf("seconds=%d error=%v got=%s want=%s", tc.seconds, tc.err, got, tc.want)
		}
	}
}

func TestStatusWorkerCadenceAndImmediateTrigger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home := &Home{ctx: ctx, statusTrigger: make(chan statusUpdateRequest, 1)}
	sweeps := make(chan time.Time, 4)
	requests := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		home.statusWorkerLoop(100*time.Millisecond, func() {
			select {
			case sweeps <- time.Now():
			default:
			}
		},
			func(statusUpdateRequest) { requests <- struct{}{} })
	}()
	home.statusTrigger <- statusUpdateRequest{}
	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("explicit refresh must not wait for a periodic sweep")
	}
	first := <-sweeps
	second := <-sweeps
	if second.Sub(first) < 80*time.Millisecond {
		t.Fatal("configured cadence was not honored")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("status worker failed to stop")
	}
}

func TestStatusCadencePreservesRemoteRowsAndRefreshGate(t *testing.T) {
	home := NewHome()
	defer home.cancel()
	if home.storage != nil {
		defer home.storage.Close()
	}
	home.remoteSessionRefreshSec = 30
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"remote": {{ID: "r1", RemoteName: "remote", Status: "running"}},
	}
	for _, interval := range []int{1, 5, 10} {
		cfg := &session.UserConfig{Performance: session.PerformanceSettings{StatusIntervalSeconds: interval}}
		if cfg.StatusInterval() != time.Duration(interval)*time.Second {
			t.Fatal("local interval not selected")
		}
		now := time.Now()
		home.lastRemoteFetch = now.Add(-10 * time.Second)
		if home.shouldFetchRemoteSessions(now) {
			t.Fatal("local interval accelerated remote polling")
		}
		home.lastRemoteFetch = now.Add(-31 * time.Second)
		if !home.shouldFetchRemoteSessions(now) {
			t.Fatal("remote refresh was suppressed by local interval")
		}
		if home.remoteSessions["remote"][0].ID != "r1" {
			t.Fatal("local cadence removed remote row")
		}
	}
}
