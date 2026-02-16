package main

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// mockStatusChecker implements statusChecker for testing waitForCompletion.
type mockStatusChecker struct {
	statuses []string // statuses returned in order
	errors   []error  // errors returned in order (nil = no error)
	idx      atomic.Int32
}

func (m *mockStatusChecker) GetStatus() (string, error) {
	i := int(m.idx.Add(1) - 1)
	if i >= len(m.statuses) {
		// Stay on last status if we exceed the list
		i = len(m.statuses) - 1
	}
	var err error
	if i < len(m.errors) {
		err = m.errors[i]
	}
	return m.statuses[i], err
}

func TestWaitForCompletion_ImmediateWaiting(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"waiting"},
	}
	status, err := waitForCompletion(mock, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", status)
	}
}

func TestWaitForCompletion_ActiveThenWaiting(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active", "active", "waiting"},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", status)
	}
}

func TestWaitForCompletion_ActiveThenIdle(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active", "idle"},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "idle" {
		t.Errorf("expected status 'idle', got %q", status)
	}
}

func TestWaitForCompletion_ActiveThenInactive(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active", "inactive"},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "inactive" {
		t.Errorf("expected status 'inactive', got %q", status)
	}
}

func TestWaitForCompletion_TransientErrors(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"", "", "waiting"},
		errors:   []error{fmt.Errorf("tmux error"), fmt.Errorf("tmux error"), nil},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", status)
	}
}

func TestWaitForCompletion_Timeout(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active"}, // Stays active forever
	}
	// Use a very short timeout so the test doesn't block
	_, err := waitForCompletion(mock, 2*time.Second)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
