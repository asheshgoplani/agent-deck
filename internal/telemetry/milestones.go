package telemetry

import (
	"fmt"
	"math/bits"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
)

// step is a funnel bit (F0..F15, TELEMETRY.md "Funnel steps").
type step int

const (
	stepFirstRun step = iota
	stepConsented
	stepFirstCreated
	stepFirstRunning
	stepFirstAttach
	stepFirstSend
	stepSecondSession
	stepSecondTool
	stepFirstFork
	stepFirstWorktree
	stepFirstMCPAttach
	stepFirstGroup
	stepFirstConductor
	stepFirstRemote
	stepFirstFleet
	stepActivated
)

// activationWindow is how long after first run "activated" can be reached.
const activationWindow = 7 * 24 * time.Hour

func milestoneBit(st step) uint32 { return 1 << uint(st) }

// featureMilestones maps a first successful feature use to its funnel step.
var featureMilestones = map[string]step{
	"fork":            stepFirstFork,
	"worktree_create": stepFirstWorktree,
	"mcp_attach":      stepFirstMCPAttach,
	"group_create":    stepFirstGroup,
	"conductor_start": stepFirstConductor,
	"remote_add":      stepFirstRemote,
	"fleet_launch":    stepFirstFleet,
}

// reach records a funnel step the first time it is reached. Callers hold
// the state lock and save afterwards.
func (s *State) reach(st step, tool, via string, now time.Time) {
	if s.Milestones&milestoneBit(st) != 0 {
		return
	}
	s.Milestones |= milestoneBit(st)
	props := map[string]any{
		"step":           milestoneNames[st],
		"since":          SinceBucket(now.Sub(s.firstSeen(now))),
		"before_consent": false,
	}
	if tool != "" {
		props["tool"] = tool
	}
	if via != "" {
		props["via"] = via
	}
	s.spool("onboard.milestone", props, "", now)
}

func (s *State) firstSeen(now time.Time) time.Time {
	if s.FirstSeenAt.IsZero() {
		return now
	}
	return s.FirstSeenAt
}

// onboardingStep is the furthest of F0..F5 reached, for error events.
func (s *State) onboardingStep() string {
	name := "none"
	for st := stepFirstRun; st <= stepFirstSend; st++ {
		if s.Milestones&milestoneBit(st) != 0 {
			name = milestoneNames[st]
		}
	}
	return name
}

// afterRecord derives funnel steps from a recorded event.
func (s *State) afterRecord(name string, props map[string]any, now time.Time) {
	if name != "session.create" {
		return
	}
	tool, _ := props["tool"].(string)
	via, _ := props["via"].(string)
	f := &s.Funnel
	inc(&f.Created)
	f.ToolsUsed |= ToolBit(tool)
	s.reach(stepFirstCreated, tool, via, now)
	if f.Created >= 2 {
		s.reach(stepSecondSession, tool, via, now)
	}
	if bits.OnesCount32(f.ToolsUsed) >= 2 {
		s.reach(stepSecondTool, tool, via, now)
	}
	if via == string(ViaTUIFork) {
		s.reach(stepFirstFork, tool, via, now)
	}
	if wt, _ := props["worktree"].(bool); wt {
		s.reach(stepFirstWorktree, tool, via, now)
	}
	if now.Sub(s.firstSeen(now)) <= activationWindow {
		inc(&f.ActivationCreates)
		if d := dayOf(now); !contains(f.ActivationDays, d) && len(f.ActivationDays) < 8 {
			f.ActivationDays = append(f.ActivationDays, d)
		}
		if f.ActivationCreates >= 3 && len(f.ActivationDays) >= 2 {
			s.reach(stepActivated, tool, via, now)
		}
	}
}

// Baseline describes an install at the moment of consent, computed from
// existing local state by the caller (sessions DB, config, PATH). Nothing
// here is recorded before consent.
type Baseline struct {
	InstallMethod string
	TmuxOK        bool
	ToolsFound    uint32
	HadConfig     bool
	Sessions      int
	ToolsUsed     []string
	HasWorktree   bool
	HasGroups     bool
	HasConductor  bool
	HasRemote     bool
}

// baselineMilestones returns the funnel bits an install reached before consent.
func (b Baseline) milestones(tuiSeen bool) uint32 {
	var m uint32
	set := func(st step, ok bool) {
		if ok {
			m |= milestoneBit(st)
		}
	}
	set(stepFirstRun, tuiSeen)
	set(stepFirstCreated, b.Sessions >= 1)
	set(stepSecondSession, b.Sessions >= 2)
	set(stepSecondTool, bits.OnesCount32(ToolMask(b.ToolsUsed...)) >= 2)
	set(stepFirstWorktree, b.HasWorktree)
	set(stepFirstGroup, b.HasGroups)
	set(stepFirstConductor, b.HasConductor)
	set(stepFirstRemote, b.HasRemote)
	return m
}

// ConsentSource is where consent was given.
type ConsentSource string

const (
	SourceTUIFirstRun ConsentSource = "tui_first_run"
	SourceTUISettings ConsentSource = "tui_settings"
	SourceCLIOn       ConsentSource = "cli_on"
)

// AfterConsent records, right after a durable grant: telemetry.consent,
// onboard.baseline, the first_run and consented milestones, and (for a TUI
// grant) the app.start of the TUI that asked. prev is State.Previous() from
// before the grant.
func AfterConsent(src ConsentSource, prev string, b Baseline, fleet *FleetCounts) {
	fb, _ := feedback.LoadState()
	tuiSeen := src != SourceCLIOn || fb != nil && !fb.FirstSeenAt.IsZero()
	withState(func(s *State, now time.Time) bool {
		s.spool("telemetry.consent", map[string]any{
			"answer": "yes", "source": string(src), "previous": oneOf(prev, consentPrevious), "prompt_variant": "v2a",
		}, "", now)
		before := b.milestones(tuiSeen)
		s.spool("onboard.baseline", map[string]any{
			"install_method": oneOf(b.InstallMethod, installMethods), "tmux_ok": b.TmuxOK,
			"tools_found": int(b.ToolsFound), "had_config": b.HadConfig,
			"milestones_before": int(before), "sessions_total": CountBucket(b.Sessions),
		}, "", now)
		f := &s.Funnel
		f.Created = max(f.Created, min(b.Sessions, maxCounter))
		f.ToolsUsed |= ToolMask(b.ToolsUsed...)
		if fleet != nil {
			s.appStartLocked(*fleet, now)
		}
		if before&milestoneBit(stepFirstRun) != 0 && s.Milestones&milestoneBit(stepFirstRun) == 0 {
			s.spool("onboard.milestone", map[string]any{
				"step": milestoneNames[stepFirstRun], "since": SinceBucket(now.Sub(s.firstSeen(now))), "before_consent": true,
			}, "", now)
		}
		s.Milestones |= before
		s.reach(stepConsented, "", "", now)
		return true
	})
}

// initFirstSeen seeds the local first-seen facts on a v2 grant: the TUI's
// first launch (feedback state) when known, else now.
func (s *State) initFirstSeen(now time.Time) {
	if s.FirstSeenDay != "" {
		return
	}
	first := now
	if fb, err := feedback.LoadState(); err == nil && fb != nil && !fb.FirstSeenAt.IsZero() && fb.FirstSeenAt.Before(now) {
		first = fb.FirstSeenAt
	}
	s.FirstSeenAt = first
	s.FirstSeenDay = dayOf(first)
	s.PreV2 = s.prevV1 != "" || now.Sub(first) > 24*time.Hour
}

// installAge returns the install_age bucket and install_week for a day.
func (s *State) installAge(day string) (string, string) {
	first, err := time.ParseInLocation(DayFormat, s.FirstSeenDay, time.Local)
	if err != nil {
		first, _ = time.ParseInLocation(DayFormat, day, time.Local)
	}
	d, err := time.ParseInLocation(DayFormat, day, time.Local)
	if err != nil {
		d = first
	}
	days := int(d.Sub(first).Hours()+12) / 24
	y, w := first.ISOWeek()
	return AgeBucket(days), isoWeek(y, w)
}

func isoWeek(y, w int) string { return fmt.Sprintf("%04d-W%02d", y, w) }
