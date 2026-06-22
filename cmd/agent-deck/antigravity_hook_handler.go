package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// antigravityHookPayload is the JSON Antigravity CLI sends on stdin for hooks.
// Field names are camelCase per official Antigravity hooks documentation.
type antigravityHookPayload struct {
	ConversationID string `json:"conversationId"`
	TranscriptPath string `json:"transcriptPath"`
	HookEventName  string `json:"hookEventName"`
}

// handleAntigravityHook processes Antigravity CLI hook invocations.
// Usage: agent-deck antigravity-hook [PreInvocation|Stop]
// Reads agy JSON from stdin, maps to agent-deck hook status, emits allow decision.
func handleAntigravityHook(args []string) {
	event := "PreInvocation"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		event = strings.TrimSpace(args[0])
	}

	instanceID := os.Getenv("AGENTDECK_INSTANCE_ID")
	if instanceID == "" || !validInstanceID.MatchString(instanceID) || strings.Contains(instanceID, "..") {
		fmt.Println(`{"decision":"allow"}`)
		return
	}

	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxHookPayloadSize))
	if err != nil || len(data) == 0 {
		fmt.Println(`{"decision":"allow"}`)
		return
	}

	var payload antigravityHookPayload
	_ = json.Unmarshal(data, &payload)

	sessionID := strings.TrimSpace(payload.ConversationID)
	status := mapEventToStatus(event)
	if status == "" {
		fmt.Println(`{"decision":"allow"}`)
		return
	}

	// Synthesize hook-handler compatible payload for session ID extraction
	synthetic := hookPayload{
		HookEventName:  event,
		ConversationID: sessionID,
		SessionID:      sessionID,
	}
	if isStopHookEvent(event) {
		writeHookStatusWithScan(instanceID, status, sessionID, event, detectDoneSentinel(mustJSON(synthetic)))
	} else {
		writeHookStatus(instanceID, status, sessionID, event)
	}

	fmt.Println(`{"decision":"allow"}`)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
