package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// TerminalManager manages multiple Terminal instances, one per session.
// This enables multi-pane layouts where each pane can display a different session.
type TerminalManager struct {
	terminals map[string]*Terminal
	mu        sync.RWMutex
	ctx       context.Context
	sshBridge *SSHBridge
}

// NewTerminalManager creates a new TerminalManager instance.
func NewTerminalManager() *TerminalManager {
	return &TerminalManager{
		terminals: make(map[string]*Terminal),
	}
}

// SetContext sets the Wails runtime context for all terminals.
func (tm *TerminalManager) SetContext(ctx context.Context) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.ctx = ctx

	// Update context for any existing terminals
	for _, t := range tm.terminals {
		t.SetContext(ctx)
	}
}

// SetSSHBridge sets the SSH bridge for all terminals.
func (tm *TerminalManager) SetSSHBridge(bridge *SSHBridge) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.sshBridge = bridge

	// Update SSH bridge for any existing terminals
	for _, t := range tm.terminals {
		t.SetSSHBridge(bridge)
	}
}

// GetOrCreate returns an existing Terminal for the session ID, or creates a new one.
func (tm *TerminalManager) GetOrCreate(sessionID string) *Terminal {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if t, exists := tm.terminals[sessionID]; exists {
		return t
	}

	t := NewTerminal(sessionID)
	if tm.ctx != nil {
		t.SetContext(tm.ctx)
	}
	if tm.sshBridge != nil {
		t.SetSSHBridge(tm.sshBridge)
	}
	tm.terminals[sessionID] = t
	return t
}

// Get returns an existing Terminal for the session ID, or nil if not found.
func (tm *TerminalManager) Get(sessionID string) *Terminal {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.terminals[sessionID]
}

// Close closes and removes a terminal for the given session ID.
func (tm *TerminalManager) Close(sessionID string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	t, exists := tm.terminals[sessionID]
	if !exists {
		return nil
	}

	err := t.Close()
	delete(tm.terminals, sessionID)
	return err
}

// CloseAll closes all managed terminals.
func (tm *TerminalManager) CloseAll() error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	var errs []error
	for id, t := range tm.terminals {
		if err := t.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close terminal %s: %w", id, err))
		}
		delete(tm.terminals, id)
	}

	return errors.Join(errs...)
}

// Count returns the number of active terminals.
func (tm *TerminalManager) Count() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.terminals)
}

// List returns a list of all active session IDs.
func (tm *TerminalManager) List() []string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	ids := make([]string, 0, len(tm.terminals))
	for id := range tm.terminals {
		ids = append(ids, id)
	}
	return ids
}
