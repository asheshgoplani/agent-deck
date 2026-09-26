package telemetry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Surface is where an event came from.
type Surface string

const (
	SurfaceTUI Surface = "tui"
	SurfaceCLI Surface = "cli"
	SurfaceWeb Surface = "web"
)

var (
	surface        = SurfaceCLI
	processVersion = "dev"
	nowFn          = time.Now
)

// SetProcess records the running binary's version and surface. Called once
// from main before any event.
func SetProcess(version string, s Surface) {
	processVersion = version
	surface = s
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// newUUID returns a random version 4 UUID for a spool line (a seam for
// deterministic golden tests).
var newUUID = func() string {
	h, err := randomHex(16)
	if err != nil {
		return ""
	}
	return formatUUID(h)
}

// formatUUID shapes 32 hex characters as a version 4 UUID.
func formatUUID(h string) string {
	b, _ := hex.DecodeString(h)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h = hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func actor() string {
	if AgentActor() {
		return "agent"
	}
	return "human"
}

// sessionHash returns the first 16 hex of HMAC-SHA256(local salt, id). The
// salt never leaves the machine, so the value cannot be reversed or joined
// with anything outside this install.
func sessionHash(salt, id string) string {
	if salt == "" || id == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(salt))
	m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil))[:16]
}

// withState runs fn on the loaded state under a non-blocking lock, only when
// recording is allowed and consent is granted, then saves. Contention drops
// the update: recording must never block the UI.
func withState(fn func(s *State, now time.Time) bool) {
	if !canRecord() {
		return
	}
	unlock, err := lockStateWithFlags(syscall.LOCK_EX | syscall.LOCK_NB)
	if err != nil {
		return
	}
	defer unlock()
	s := LoadState()
	if ok, _ := Enabled(s); !ok {
		return
	}
	now := nowFn()
	if fn(s, now) {
		_ = saveStateFast(s)
	}
}

// record validates and spools one event now.
func record(name string, props map[string]any, sessionID string) {
	recordAt(name, props, sessionID, time.Time{})
}

// recordAt spools one event with its time fields taken from at (zero = now).
func recordAt(name string, props map[string]any, sessionID string, at time.Time) {
	if !canRecord() {
		return
	}
	unlock, err := lockStateWithFlags(syscall.LOCK_EX | syscall.LOCK_NB)
	if err != nil {
		return
	}
	defer unlock()
	s := LoadState()
	now := nowFn()
	if at.IsZero() {
		at = now
	}
	if ok, _ := Enabled(s); !ok {
		if LogMode() {
			logWouldRecord(name, props, at)
		}
		return
	}
	if s.spool(name, props, sessionID, at) {
		_ = saveStateFast(s)
	}
}

// spool appends one validated event and its milestone side effects. It
// reports whether state changed. Callers hold the state lock.
func (s *State) spool(name string, props map[string]any, sessionID string, at time.Time) bool {
	def, ok := LookupEvent(name)
	if !ok {
		return false
	}
	r := s.day(dayOf(at))
	level := EffectiveLevel(s)
	if level == LevelBasic && !def.Basic {
		return false
	}
	if _, wants := def.prop("ds_session"); wants && level == LevelFull {
		if h := sessionHash(s.Salt, sessionID); h != "" {
			props["ds_session"] = h
		}
	}
	if Validate(name, props) != nil {
		inc(&r.SchemaDrops)
		return true
	}
	s.markActive(at)
	if r.Emitted >= dailyEventCap {
		inc(&r.Dropped)
		return true
	}
	if appendSpool(s.newLine(name, props, at, level)) != nil {
		return true
	}
	r.Emitted++
	s.afterRecord(name, props, at)
	return true
}

func (s *State) newLine(name string, props map[string]any, at time.Time, level Level) spoolLine {
	s.Seq++
	l := spoolLine{
		E: name, U: newUUID(), D: dayOf(at), S: s.Seq, V: safeVersion(processVersion),
		A: actor(), SF: string(surface), L: string(level), P: props,
	}
	if level == LevelFull {
		h, w := at.Local().Hour(), int(at.Local().Weekday())
		l.H, l.W = &h, &w
	}
	return l
}

// markActive sets the hour bit for the current actor in the day rollup.
func (s *State) markActive(at time.Time) {
	r := s.day(dayOf(at))
	bit := HourBit(at.Local().Hour())
	if actor() == "agent" {
		r.AgentHours |= bit
	} else {
		r.HumanHours |= bit
	}
}

var releaseVersion = regexp.MustCompile(`^v?[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)

// safeVersion keeps release versions and maps every build string to "dev".
func safeVersion(version string) string {
	if releaseVersion.MatchString(version) {
		if version[0] == 'v' {
			return version[1:]
		}
		return version
	}
	return "dev"
}

// minorOf returns "X.Y" of a release version, else "other".
func minorOf(version string) string {
	v := safeVersion(version)
	if v == "dev" {
		return "other"
	}
	parts := strings.SplitN(v, ".", 3)
	return parts[0] + "." + parts[1]
}

// ---------------------------------------------------------------------------
// Typed helpers. Callers never pass free text: every string parameter is a
// named enum type, or a raw name/id that is normalised or hashed here.
// ---------------------------------------------------------------------------

// Feature is a feature.daily enum value (TELEMETRY.md).
type Feature string

// CreateVia is how a session was created.
type CreateVia string

const (
	ViaTUINew    CreateVia = "tui_new"
	ViaTUIFork   CreateVia = "tui_fork"
	ViaTUIQuick  CreateVia = "tui_quick"
	ViaCLIAdd    CreateVia = "cli_add"
	ViaCLILaunch CreateVia = "cli_launch"
	ViaTry       CreateVia = "try"
	ViaFleet     CreateVia = "fleet"
	ViaConductor CreateVia = "conductor"
	ViaWeb       CreateVia = "web"
)

// EndKind is how a session ended.
type EndKind string

const (
	EndStop     EndKind = "stop"
	EndDelete   EndKind = "delete"
	EndToolExit EndKind = "tool_exit"
	EndCrash    EndKind = "crash"
	EndRestart  EndKind = "restart"
)

// SendVia is how a message reached a session.
type SendVia string

const (
	SendTUI       SendVia = "tui"
	SendCLI       SendVia = "cli_send"
	SendConductor SendVia = "conductor"
	SendInbox     SendVia = "inbox"
	SendTelegram  SendVia = "telegram"
	SendWeb       SendVia = "web"
)

// AttachVia is how a session was attached.
type AttachVia string

const (
	AttachTUI    AttachVia = "tui"
	AttachCLI    AttachVia = "cli"
	AttachWeb    AttachVia = "web"
	AttachRemote AttachVia = "remote"
)

// ErrArea and ErrKind classify an error without its message.
type (
	ErrArea string
	ErrKind string
)

const (
	AreaTmux         ErrArea = "tmux"
	AreaSessionStart ErrArea = "session_start"
	AreaSend         ErrArea = "send"
	AreaWorktree     ErrArea = "worktree"
	AreaMCP          ErrArea = "mcp"
	AreaRemote       ErrArea = "remote"
	AreaUpdate       ErrArea = "update"
	AreaConfig       ErrArea = "config"
	AreaHook         ErrArea = "hook"
	AreaDB           ErrArea = "db"
	AreaWeb          ErrArea = "web"
	AreaConductor    ErrArea = "conductor"
	AreaTelemetry    ErrArea = "telemetry"

	KindTmuxMissing    ErrKind = "tmux_missing"
	KindTmuxTooOld     ErrKind = "tmux_too_old"
	KindToolNotFound   ErrKind = "tool_not_found"
	KindToolAuth       ErrKind = "tool_auth"
	KindWorktreeDirty  ErrKind = "worktree_dirty"
	KindMCPSpawnFailed ErrKind = "mcp_spawn_failed"
	KindSSHAuth        ErrKind = "ssh_auth"
	KindSSHUnreachable ErrKind = "ssh_unreachable"
	KindConfigParse    ErrKind = "config_parse"
	KindDBLocked       ErrKind = "db_locked"
	KindTimeout        ErrKind = "timeout"
	KindPermission     ErrKind = "permission"
	KindDiskFull       ErrKind = "disk_full"
	KindPanic          ErrKind = "panic"
	KindOther          ErrKind = "other"
)

// StartKind and ExitKind classify TUI starts and exits.
type (
	StartKind string
	ExitKind  string
)

const (
	StartFirstEver   StartKind = "first_ever"
	StartNormal      StartKind = "normal"
	StartAfterUpdate StartKind = "after_update"
	StartAfterCrash  StartKind = "after_crash"

	ExitQuit          ExitKind = "quit"
	ExitSignal        ExitKind = "signal"
	ExitUpdateRestart ExitKind = "update_restart"
	ExitPanic         ExitKind = "panic"
)

// UpdateKind and UpdateOutcome describe an update attempt.
type (
	UpdateKind    string
	UpdateOutcome string
)

const (
	UpdateAuto        UpdateKind    = "auto"
	UpdateManual      UpdateKind    = "manual"
	UpdateTimer       UpdateKind    = "timer"
	UpdateRemoteSweep UpdateKind    = "remote_sweep"
	UpdateOK          UpdateOutcome = "ok"
	UpdateError       UpdateOutcome = "error"
	UpdateRolledBack  UpdateOutcome = "rolled_back"
)

// FleetCounts is the configured fleet size at a TUI start.
type FleetCounts struct {
	Sessions, Groups, Profiles, Remotes, Conductors int
}

// TUIStarted records app.start for a TUI start, and the first_run milestone.
func TUIStarted(fleet FleetCounts) {
	withState(func(s *State, now time.Time) bool {
		s.appStartLocked(fleet, now)
		s.reach(stepFirstRun, "", "", now)
		return true
	})
}

// appStartLocked records a TUI app.start. The start kind comes from local
// state: first ever, after a TUI that never recorded its exit, after an
// update, or normal.
func (s *State) appStartLocked(fleet FleetCounts, now time.Time) {
	kind := StartNormal
	switch {
	case s.Milestones&milestoneBit(stepFirstRun) == 0 && !s.PreV2:
		kind = StartFirstEver
	case s.TUIOpen:
		kind = StartAfterCrash
	case s.LastVersion != "" && s.LastVersion != safeVersion(processVersion):
		kind = StartAfterUpdate
	}
	s.TUIOpen = true
	s.LastVersion = safeVersion(processVersion)
	inc(&s.day(dayOf(now)).TUIStarts)
	s.spool("app.start", map[string]any{
		"start_kind": string(kind), "sessions_total": CountBucket(fleet.Sessions),
		"groups": CountBucket(fleet.Groups), "profiles": CountBucket(fleet.Profiles),
		"remotes": CountBucket(fleet.Remotes), "conductors": CountBucket(fleet.Conductors),
	}, "", now)
}

// TUIExited records app.exit (spool only; never network).
func TUIExited(openFor time.Duration, kind ExitKind) {
	withState(func(s *State, now time.Time) bool {
		s.TUIOpen = false
		s.spool("app.exit", map[string]any{"open_dur": DurBucket(openFor), "exit_kind": string(kind)}, "", now)
		return true
	})
}

// CLICommand counts a human CLI command and its feature, and records
// app.start at most once per local hour for the CLI surface.
func CLICommand(f Feature) {
	withState(func(s *State, now time.Time) bool {
		r := s.day(dayOf(now))
		inc(&r.CLICmds)
		s.markActive(now)
		if f != "" {
			s.countFeature(r, f, false, now)
		}
		if bit := HourBit(now.Local().Hour()); r.CLIStarts&bit == 0 {
			r.CLIStarts |= bit
			s.spool("app.start", map[string]any{"start_kind": string(StartNormal)}, "", now)
		}
		return true
	})
}

// FeatureUsed counts one use of a feature (feature.daily).
func FeatureUsed(f Feature, failed bool) {
	withState(func(s *State, now time.Time) bool {
		s.markActive(now)
		s.countFeature(s.day(dayOf(now)), f, failed, now)
		return true
	})
}

func (s *State) countFeature(r *DailyRollup, f Feature, failed bool, now time.Time) {
	name := string(f)
	if !contains(FeatureValues, name) {
		name = "other"
	}
	if r.Features == nil {
		r.Features = map[string]*FeatureCount{}
	}
	c := r.Features[name]
	if c == nil {
		c = &FeatureCount{}
		r.Features[name] = c
	}
	inc(&c.Count)
	if failed {
		inc(&c.Errors)
	}
	if step, ok := featureMilestones[name]; ok && !failed {
		s.reach(step, "", "", now)
	}
}

// SessionCreateInfo describes a newly created session. Tool is the raw tool
// name (normalised here); SessionID is hashed with the local salt here.
type SessionCreateInfo struct {
	Tool      string
	Via       CreateVia
	Worktree  bool
	MCPs      int
	Skills    int
	InGroup   bool
	Remote    bool
	Parented  bool
	SessionID string
}

// SessionCreated records session.create and the creation milestones.
func SessionCreated(in SessionCreateInfo) {
	record("session.create", map[string]any{
		"tool": NormalizeTool(in.Tool), "via": string(in.Via), "worktree": in.Worktree,
		"mcps": CountBucket(in.MCPs), "skills": CountBucket(in.Skills), "in_group": in.InGroup,
		"remote": in.Remote, "parented": in.Parented,
	}, in.SessionID)
}

// SessionEndInfo describes a session ending.
type SessionEndInfo struct {
	Tool      string
	Kind      EndKind
	Lifetime  time.Duration
	Restarts  int
	SessionID string
}

// SessionEnded records session.end.
func SessionEnded(in SessionEndInfo) {
	record("session.end", map[string]any{
		"tool": NormalizeTool(in.Tool), "end_kind": string(in.Kind),
		"lifetime": DurBucket(in.Lifetime), "restarts": CountBucket(in.Restarts),
	}, in.SessionID)
}

// SessionRunning marks the first_session_running milestone.
func SessionRunning(tool string) {
	withState(func(s *State, now time.Time) bool {
		if s.Milestones&milestoneBit(stepFirstRunning) != 0 {
			return false
		}
		s.reach(stepFirstRunning, NormalizeTool(tool), "", now)
		return true
	})
}

// MessageSent counts one message into a session (send.daily). Only the
// length bucket of the text is kept; the text is never seen here.
func MessageSent(tool string, via SendVia, chars int, queued bool) {
	withState(func(s *State, now time.Time) bool {
		r := s.day(dayOf(now))
		s.markActive(now)
		inc(&r.Sends)
		if r.SendsBy == nil {
			r.SendsBy = map[string]*SendCount{}
		}
		k := pairKey(NormalizeTool(tool), sendViaOrOther(via))
		c := r.SendsBy[k]
		if c == nil {
			c = &SendCount{Len: map[string]int{}}
			r.SendsBy[k] = c
		}
		inc(&c.Count)
		if queued {
			inc(&c.Queued)
		}
		l := c.Len[LenBucket(chars)]
		inc(&l)
		c.Len[LenBucket(chars)] = l
		if actor() == "human" {
			s.reach(stepFirstSend, NormalizeTool(tool), "", now)
		}
		return true
	})
}

func sendViaOrOther(v SendVia) string {
	if contains(sendVias, string(v)) {
		return string(v)
	}
	return string(SendCLI)
}

// Attached counts one attach and the time spent attached (attach.daily).
func Attached(tool string, via AttachVia, d time.Duration) {
	withState(func(s *State, now time.Time) bool {
		r := s.day(dayOf(now))
		s.markActive(now)
		inc(&r.Attaches)
		if r.AttachBy == nil {
			r.AttachBy = map[string]*AttachCount{}
		}
		v := string(via)
		if !contains(attachVias, v) {
			v = string(AttachTUI)
		}
		k := pairKey(NormalizeTool(tool), v)
		c := r.AttachBy[k]
		if c == nil {
			c = &AttachCount{}
			r.AttachBy[k] = c
		}
		inc(&c.Count)
		if d > 0 && c.TotalSec < 30*24*3600 {
			c.TotalSec += int64(d / time.Second)
		}
		s.reach(stepFirstAttach, NormalizeTool(tool), "", now)
		return true
	})
}

// ErrorOccurred records an error class, deduped per (area, kind) per local
// hour and capped per day. The error message is never passed in.
func ErrorOccurred(area ErrArea, kind ErrKind, tool string) {
	withState(func(s *State, now time.Time) bool {
		r := s.day(dayOf(now))
		k := string(area) + "|" + string(kind) + "|" + strconv.Itoa(now.Local().Hour())
		if r.ErrorKeys[k] || r.Errors >= dailyErrorCap {
			return false
		}
		if r.ErrorKeys == nil {
			r.ErrorKeys = map[string]bool{}
		}
		r.ErrorKeys[k] = true
		inc(&r.Errors)
		s.spool("error", map[string]any{
			"area": string(area), "kind": string(kind), "tool": NormalizeTool(tool),
			"before_first_success": s.Milestones&milestoneBit(stepFirstRunning) == 0,
			"onboarding_step":      s.onboardingStep(),
		}, "", now)
		return true
	})
}

// UpdateAttempted records an update attempt between two versions.
func UpdateAttempted(from, to string, kind UpdateKind, outcome UpdateOutcome, restart bool) {
	record("update", map[string]any{
		"from_minor": minorOf(from), "to_minor": minorOf(to),
		"kind": string(kind), "outcome": string(outcome), "restart": restart,
	}, "")
}

// EnvInfo is the TUI environment snapshot. Every field is an enum or a mask.
type EnvInfo struct {
	Terminal       string
	TmuxMinor      string
	Shell          string
	InstallMethod  string
	Color          string
	ToolsInstalled uint32
	ConfigSections uint32
}

// EnvSnapshot records env.snapshot at most once per local day.
func EnvSnapshot(in EnvInfo) {
	withState(func(s *State, now time.Time) bool {
		r := s.day(dayOf(now))
		if r.EnvSnapshot {
			return false
		}
		r.EnvSnapshot = true
		s.spool("env.snapshot", map[string]any{
			"terminal": oneOf(in.Terminal, terminals), "tmux_minor": minorOrOther(in.TmuxMinor),
			"shell": oneOf(in.Shell, shells), "install_method": oneOf(in.InstallMethod, installMethods),
			"color": oneOf(in.Color, colorModes), "tools_installed": int(in.ToolsInstalled),
			"config_sections": int(in.ConfigSections),
		}, "", now)
		return true
	})
}

// oneOf returns v if it is in the allow-list, else the list's catch-all.
func oneOf(v string, allowed []string) string {
	if contains(allowed, v) {
		return v
	}
	if contains(allowed, "other") {
		return "other"
	}
	return allowed[len(allowed)-1]
}

func minorOrOther(v string) string {
	if reMinor.MatchString(v) {
		return v
	}
	return "other"
}
