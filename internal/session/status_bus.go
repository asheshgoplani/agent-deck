package session

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// StatusBusEvent is the data of a `session.status` bus frame: one status
// transition of one session, as written to state.db by whichever process
// owns the status (TUI poller, notify daemon). Clients follow it with
// `events follow --kind session.status` instead of polling `list --json`.
type StatusBusEvent struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Tool        string `json:"tool,omitempty"`
	TmuxSession string `json:"tmux_session,omitempty"`
	Substate    string `json:"substate"`
	ChangedAt   string `json:"changed_at"`
}

// TurnBusEvent is the data of a `session.turn` frame: a turn started (the
// session entered running) or ended (it left running).
type TurnBusEvent struct {
	Phase      string `json:"phase"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	To         string `json:"to,omitempty"`
}

// TranscriptBusEvent is the data of a `session.transcript` frame: the live
// native transcript grew.
type TranscriptBusEvent struct {
	Path          string `json:"path"`
	BytesAppended int64  `json:"bytes_appended"`
	Size          int64  `json:"size"`
}

func init() {
	statedb.SetStatusChangeObserver(publishStatusChange)
}

// busPublish is the bus tap; tests replace it with a recorder.
var busPublish = events.PublishProfile

// turnStarts remembers when this process saw each session enter running,
// for the ended frame's duration.
var turnStarts sync.Map // id -> time.Time

// statusBusProfile maps <profiles dir>/<profile>/state.db to its profile.
// Any other database (tests, in-memory, ad hoc paths) publishes nothing.
func statusBusProfile(dbPath string) string {
	if dbPath == "" || filepath.Base(dbPath) != "state.db" {
		return ""
	}
	dir := filepath.Dir(dbPath)
	if filepath.Base(filepath.Dir(dir)) != "profiles" {
		return ""
	}
	return filepath.Base(dir)
}

func publishStatusChange(c statedb.StatusChange) {
	profile := statusBusProfile(c.DBPath)
	if profile == "" || c.ID == "" {
		return
	}
	busPublish(profile, "session.status", c.ID, StatusBusEvent{
		From:        c.From,
		To:          c.To,
		Tool:        c.Tool,
		TmuxSession: c.TmuxSession,
		ChangedAt:   c.At.UTC().Format(time.RFC3339Nano),
	})
	running := string(StatusRunning)
	switch {
	case c.To == running:
		turnStarts.Store(c.ID, c.At)
		busPublish(profile, "session.turn", c.ID, TurnBusEvent{Phase: "started"})
	case c.From == running:
		ev := TurnBusEvent{Phase: "ended", To: c.To}
		if v, ok := turnStarts.LoadAndDelete(c.ID); ok {
			ev.DurationMs = c.At.Sub(v.(time.Time)).Milliseconds()
		}
		busPublish(profile, "session.turn", c.ID, ev)
	}
}

// transcriptSizes tracks the last observed size of each live transcript
// for session.transcript frames (notify daemon, [macapp] transcript_events).
type transcriptSizes struct {
	mu    sync.Mutex
	sizes map[string]int64 // instance id + path -> size
}

var transcriptGrowth = &transcriptSizes{sizes: map[string]int64{}}

// LiveTranscriptPath is the native transcript an instance writes now:
// Claude Code's JSONL or Codex's live rollout. "" when there is none.
func LiveTranscriptPath(inst *Instance) string {
	switch {
	case inst == nil:
		return ""
	case IsClaudeCompatible(inst.Tool):
		if inst.ClaudeSessionID == "" {
			return ""
		}
		p := ClaudeTranscriptPathForInstance(inst)
		if _, err := os.Stat(p); err != nil {
			return ""
		}
		return p
	case IsCodexCompatible(inst.Tool):
		return CodexRolloutPathForInstance(inst)
	}
	return ""
}

// publish publishes one session.transcript frame for every
// live session whose transcript grew since the previous pass. The first
// observation of a path only records its size.
func (t *transcriptSizes) publish(profile string, instances []*Instance) {
	t.mu.Lock()
	defer t.mu.Unlock()
	seen := map[string]bool{}
	for _, inst := range instances {
		if inst == nil || !isLiveSessionStatus(inst.Status) {
			continue
		}
		path := LiveTranscriptPath(inst)
		if path == "" {
			continue
		}
		key := inst.ID + "\x00" + path
		seen[key] = true
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		prev, known := t.sizes[key]
		t.sizes[key] = info.Size()
		if known && info.Size() > prev {
			busPublish(profile, "session.transcript", inst.ID, TranscriptBusEvent{Path: path, BytesAppended: info.Size() - prev, Size: info.Size()})
		}
	}
	for key := range t.sizes {
		if !seen[key] {
			delete(t.sizes, key)
		}
	}
}
