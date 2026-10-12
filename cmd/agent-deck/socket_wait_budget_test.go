package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSocketWaitObservedReply_OneBudgetAndPartialRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendJSONL(t, path, jsonlUser("peer", "", "wrapped peer text"),
		jsonlAssistant("partial", "", "partial reply", "tool_use"))
	start := time.Now()
	resp, _, identityErr, completionErr, responseErr := awaitClaudeObservedReply(
		session.TurnQuery{Path: path, Prompt: "unwrapped text"}, start.Add(300*time.Millisecond))
	if identityErr != nil || completionErr != nil || !errors.Is(responseErr, session.ErrTurnResponseIncomplete) || resp == nil || resp.Content != "partial reply" {
		t.Fatalf("response=%+v identity=%v completion=%v read=%v", resp, identityErr, completionErr, responseErr)
	}
	if elapsed := time.Since(start); elapsed > 600*time.Millisecond {
		t.Fatalf("observed reply exceeded one budget: %s", elapsed)
	}
}
