package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// maxStatusLineBytes caps the statusLine payload read from stdin.
//
// The documented payload is a few hundred bytes of session metadata; 1 MiB is
// three orders of magnitude of headroom and still bounds the allocation for a
// process that a user's Claude Code invokes on every status refresh. It is a
// var rather than a const so the oversized-input test can exercise the cap
// without allocating a megabyte of padding per run.
var maxStatusLineBytes int64 = 1 << 20

// claudeLabel is the display name. Spelled once so the CLI and the cache agree.
const claudeLabel = "Claude"

// statusLinePayload decodes ONLY the rate_limits block.
//
// Everything else Claude pipes in — transcript_path, cwd, the model, the
// session id — is deliberately absent from this struct rather than being
// decoded and then ignored: a field that does not exist cannot be accidentally
// logged, persisted, or added to an error string later.
type statusLinePayload struct {
	RateLimits *struct {
		FiveHour   *statusLineWindow `json:"five_hour"`
		SevenDay   *statusLineWindow `json:"seven_day"`
		SpendLimit *statusLineWindow `json:"spend_limit"`
	} `json:"rate_limits"`
}

type statusLineWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	// ResetsAt is epoch SECONDS here. Z.ai reports MILLISECONDS for the same
	// concept, which is why the two providers do not share a decoder.
	ResetsAt *int64 `json:"resets_at"`
}

// ParseStatusLine extracts a Claude quota snapshot from a statusLine payload.
//
// ok is false, with no error, when the payload carries no rate_limits block.
// That is the normal state before the session's first API response and the
// permanent state for API-key, Bedrock and Vertex auth, so treating it as a
// failure would make the common case look broken.
func ParseStatusLine(r io.Reader) (Snapshot, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxStatusLineBytes+1))
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("reading statusLine payload: %w", err)
	}
	if int64(len(raw)) > maxStatusLineBytes {
		return Snapshot{}, false, errors.New("statusLine payload exceeds size cap")
	}

	var payload statusLinePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Snapshot{}, false, fmt.Errorf("decoding statusLine payload: %w", err)
	}
	if payload.RateLimits == nil {
		return Snapshot{}, false, nil
	}

	snapshot := Snapshot{
		ID:        ProviderClaude,
		Label:     claudeLabel,
		UpdatedAt: time.Now().Unix(),
	}
	// Windows are appended in this fixed order so the rendering is stable
	// across refreshes; Claude drops a window from the block once its reset
	// passes, so a missing one is expected and simply contributes nothing.
	for _, candidate := range []struct {
		kind   WindowKind
		label  string
		window *statusLineWindow
	}{
		{WindowFiveHour, "5h", payload.RateLimits.FiveHour},
		{WindowSevenDay, "7d", payload.RateLimits.SevenDay},
		{WindowSpendLimit, "spend", payload.RateLimits.SpendLimit},
	} {
		if candidate.window == nil {
			continue
		}
		snapshot.Windows = append(snapshot.Windows, Window{
			Kind:  candidate.kind,
			Label: candidate.label,
			// Not clamped: spend_limit exceeds 100 in overage and that is the
			// reading the user most needs to see.
			UsedPercentage: candidate.window.UsedPercentage,
			ResetsAt:       candidate.window.ResetsAt,
		})
	}
	if len(snapshot.Windows) == 0 {
		return Snapshot{}, false, nil
	}
	return snapshot, true, nil
}
