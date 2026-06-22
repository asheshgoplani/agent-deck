package session

import (
	"encoding/json"
	"time"
)

// Antigravity tool_data fields live in the statedb extras zone (like
// idle_timeout_secs) so we can round-trip without changing the positional
// MarshalToolData / UnmarshalToolData signatures.

type antigravityToolData struct {
	AntigravityConversationID string `json:"antigravity_conversation_id,omitempty"`
	AntigravityDetectedAt     int64  `json:"antigravity_detected_at,omitempty"`
	AntigravityYoloMode       *bool  `json:"antigravity_yolo_mode,omitempty"`
	AntigravityModel          string `json:"antigravity_model,omitempty"`
}

// WriteAntigravityToToolData merges Antigravity session fields into tool_data.
func WriteAntigravityToToolData(td json.RawMessage, inst *Instance) json.RawMessage {
	if inst == nil {
		return td
	}
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}

	merge := func(key string, value any) {
		if value == nil {
			delete(m, key)
			return
		}
		switch v := value.(type) {
		case string:
			if v == "" {
				delete(m, key)
				return
			}
		case int64:
			if v == 0 {
				delete(m, key)
				return
			}
		case *bool:
			if v == nil {
				delete(m, key)
				return
			}
		}
		raw, _ := json.Marshal(value)
		m[key] = raw
	}

	merge("antigravity_conversation_id", inst.AntigravityConversationID)
	if !inst.AntigravityDetectedAt.IsZero() {
		merge("antigravity_detected_at", inst.AntigravityDetectedAt.Unix())
	} else {
		delete(m, "antigravity_detected_at")
	}
	merge("antigravity_yolo_mode", inst.AntigravityYoloMode)
	merge("antigravity_model", inst.AntigravityModel)

	out, _ := json.Marshal(m)
	return out
}

// ReadAntigravityFromToolData extracts Antigravity fields from tool_data.
func ReadAntigravityFromToolData(td json.RawMessage) (
	conversationID string,
	detectedAt time.Time,
	yoloMode *bool,
	model string,
) {
	if len(td) == 0 {
		return
	}
	var blob antigravityToolData
	if err := json.Unmarshal(td, &blob); err != nil {
		return
	}
	conversationID = blob.AntigravityConversationID
	if blob.AntigravityDetectedAt > 0 {
		detectedAt = time.Unix(blob.AntigravityDetectedAt, 0)
	}
	yoloMode = blob.AntigravityYoloMode
	model = blob.AntigravityModel
	return
}
