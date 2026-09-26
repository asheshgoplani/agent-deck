package ui

import (
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	tea "github.com/charmbracelet/bubbletea"
)

// telemetryUploadInterval is how often the TUI checks for an upload. The
// telemetry package decides whether one is due (every 6 hours, completed
// hours only, never on the consent day); this is only the check cadence.
const telemetryUploadInterval = time.Hour

// telemetryUploadTickMsg re-arms the hourly upload check.
type telemetryUploadTickMsg struct{}

// homeTelemetry is the TUI's opt-in telemetry state. Every call below is a
// no-op without consent: the telemetry package checks consent, TTY and all
// kill switches itself.
type homeTelemetry struct {
	sampler   *telemetry.Sampler
	startedAt time.Time
	started   bool
	closeOnce sync.Once

	attachTool string
	attachAt   time.Time
}

func telemetryUploadTick() tea.Cmd {
	return tea.Tick(telemetryUploadInterval, func(time.Time) tea.Msg { return telemetryUploadTickMsg{} })
}

// telemetrySummary snapshots the loaded fleet on the TUI goroutine.
func (h *Home) telemetrySummary() session.TelemetrySummary {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	groups := 0
	if h.groupTree != nil {
		groups = h.groupTree.GroupCount()
	}
	return session.SummarizeForTelemetry(h.instances, groups)
}

// telemetryFirstLoad records this TUI's start once the fleet is loaded,
// starts hourly sampling, and snapshots the environment (once per day).
func (h *Home) telemetryFirstLoad() tea.Cmd {
	if h.tel.started {
		return nil
	}
	h.tel.started = true
	h.tel.startedAt = time.Now()
	h.tel.sampler = telemetry.NewSampler()
	alone := h.tel.sampler != nil
	sum := h.telemetrySummary()
	return func() tea.Msg {
		telemetry.TUIStarted(session.TelemetryFleet(sum), alone)
		telemetry.EnvSnapshot(session.TelemetryEnv())
		return nil
	}
}

// telemetryGranted records the consent events right after a durable grant.
func (h *Home) telemetryGranted(msg telemetryGrantedMsg) tea.Cmd {
	sum := h.telemetrySummary()
	var fleet *telemetry.FleetCounts
	if msg.source == telemetry.SourceTUIFirstRun {
		f := session.TelemetryFleet(sum)
		fleet = &f
	}
	if h.tel.sampler == nil {
		h.tel.sampler = telemetry.NewSampler()
	}
	return func() tea.Msg {
		telemetry.AfterConsent(msg.source, msg.previous, session.TelemetryBaseline(sum), fleet)
		telemetry.EnvSnapshot(session.TelemetryEnv())
		return nil
	}
}

// telemetrySample feeds the status poll to the hourly sampler.
func (h *Home) telemetrySample() {
	if h.tel.sampler == nil {
		return
	}
	h.tel.sampler.Observe(func() []telemetry.SessionSample {
		h.instancesMu.RLock()
		defer h.instancesMu.RUnlock()
		out := make([]telemetry.SessionSample, 0, len(h.instances))
		for _, inst := range h.instances {
			out = append(out, telemetry.SessionSample{
				Tool:   inst.GetToolThreadSafe(),
				Status: telemetry.SessionStatus(inst.GetStatusThreadSafe()),
			})
		}
		return out
	})
}

// telemetryAttachStart remembers the attached session's tool and start.
func (h *Home) telemetryAttachStart(inst *session.Instance) {
	h.tel.attachTool = inst.GetToolThreadSafe()
	h.tel.attachAt = time.Now()
}

// telemetryAttachEnd records the attach that just returned.
func (h *Home) telemetryAttachEnd() tea.Cmd {
	if h.tel.attachAt.IsZero() {
		return nil
	}
	tool, d := h.tel.attachTool, time.Since(h.tel.attachAt)
	h.tel.attachAt = time.Time{}
	return func() tea.Msg {
		telemetry.Attached(tool, telemetry.AttachTUI, d)
		return nil
	}
}

// CloseTelemetry flushes the current activity hour and app.exit to the
// local spool. It never touches the network and runs at most once.
func (h *Home) CloseTelemetry(kind telemetry.ExitKind) {
	h.tel.closeOnce.Do(func() {
		if !h.tel.started {
			return
		}
		h.tel.sampler.Close()
		telemetry.TUIExited(time.Since(h.tel.startedAt), kind)
	})
}

// togglePrivacyFromSettings handles the Settings Privacy row: turning off is
// immediate; turning on opens the same consent question as the first run.
func (h *Home) togglePrivacyFromSettings() tea.Cmd {
	st := telemetry.LoadState()
	if ok, _ := telemetry.Enabled(st); ok {
		if err := telemetry.Disable(Version, time.Now()); err != nil {
			h.err = err
			h.errTime = time.Now()
		}
		return nil
	}
	if h.telemetryDialog != nil && h.telemetryDialog.ShowFromSettings(Version, st) {
		h.telemetryDialog.SetSize(h.width, h.height)
	}
	return nil
}
