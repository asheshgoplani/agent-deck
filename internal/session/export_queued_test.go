package session

import "testing"

// NewTestStorageForExternalTests is newTestStorage for the session_test
// package: a store on a temp path whose directory is not named after its
// profile ("_test").
func NewTestStorageForExternalTests(t *testing.T) *Storage { return newTestStorage(t) }
