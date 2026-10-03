package session

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Scheduled remote talkback. For every remote that sets
// [remotes.<name>] talkback_interval_secs, the notify-daemon runs the same
// incremental drain as `agent-deck remote drain <name> --into <conductor>`
// for each local conductor enrolled with that remote, so a remote child's
// completion no longer waits for the conductor to remember to poll.
//
// The drain runs in a goroutine bounded by remoteTalkbackDrainBound; a remote
// with a drain in flight is skipped, so a hung ssh never stalls the poll
// loop. Failures back off exponentially (1 min to 10 min) and, after
// remoteTalkbackAlertAfter consecutive failures, put ONE urgent record in each
// enrolled conductor's inbox so a dead link is news, not silence. A success
// resets the streak.

const (
	remoteTalkbackDrainBound = 60 * time.Second
	remoteTalkbackBackoffMin = 60 * time.Second
	remoteTalkbackBackoffMax = 10 * time.Minute
	remoteTalkbackAlertAfter = 3
)

// remoteTalkbackTarget is one local conductor enrolled with a remote.
type remoteTalkbackTarget struct {
	Parent  *Instance
	Profile string
}

type remoteTalkbackState struct {
	nextAt      time.Time
	inFlight    bool
	failures    int
	firstFailAt time.Time
	alerted     bool
	targets     []remoteTalkbackTarget // last discovered, for the alert
}

type remoteTalkbackScheduler struct {
	mu    sync.Mutex
	state map[string]*remoteTalkbackState
	wg    sync.WaitGroup

	now     func() time.Time
	remotes func() map[string]RemoteConfig
	targets func(remote string) ([]remoteTalkbackTarget, error)
	drain   func(ctx context.Context, remote string, rc RemoteConfig, t remoteTalkbackTarget) error
	alert   func(remote string, t remoteTalkbackTarget, text string, streakStart time.Time)
}

func (d *TransitionDaemon) newRemoteTalkbackScheduler() *remoteTalkbackScheduler {
	wake := func(parent *Instance, profile string, ev TransitionNotificationEvent) {
		ev.Profile = profile
		d.notifier.fireWakeNudge(parent, ev)
	}
	return &remoteTalkbackScheduler{
		state:   map[string]*remoteTalkbackState{},
		now:     time.Now,
		remotes: configuredTalkbackRemotes,
		targets: discoverTalkbackTargets,
		drain: func(ctx context.Context, remote string, rc RemoteConfig, t remoteTalkbackTarget) error {
			CleanStaleSSHSockets() // #1421: a dead master's socket hangs reuse
			deps := SSHTalkbackDeps(remote, rc)
			deps.Parent = func() (*Instance, string) { return t.Parent, t.Profile }
			deps.Wake = wake
			_, err := RunRemoteTalkback(ctx, remote, t.Parent.ID, deps)
			return err
		},
		alert: func(remote string, t remoteTalkbackTarget, text string, streakStart time.Time) {
			writeTalkbackAlert(remote, t, text, streakStart, wake)
		},
	}
}

// tickRemoteTalkback starts any due remote drains. Called from the Run loop
// only (never from SyncOnce), so `notify-daemon --once` never leaves a
// goroutine behind.
func (d *TransitionDaemon) tickRemoteTalkback(ctx context.Context) {
	if d.remoteTalkback == nil {
		d.remoteTalkback = d.newRemoteTalkbackScheduler()
	}
	d.remoteTalkback.tick(ctx)
}

func configuredTalkbackRemotes() map[string]RemoteConfig {
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return nil
	}
	out := map[string]RemoteConfig{}
	for name, rc := range cfg.Remotes {
		if rc.TalkbackIntervalSecs > 0 {
			out[name] = rc
		}
	}
	return out
}

func (s *remoteTalkbackScheduler) tick(ctx context.Context) {
	remotes := s.remotes()
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		st := s.state[name]
		if st == nil {
			st = &remoteTalkbackState{}
			s.state[name] = st
		}
		if st.inFlight || now.Before(st.nextAt) {
			continue
		}
		st.inFlight = true
		s.wg.Add(1)
		go s.run(ctx, name, remotes[name])
	}
}

// run drains one remote into every enrolled conductor and records the result.
func (s *remoteTalkbackScheduler) run(ctx context.Context, name string, rc RemoteConfig) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(ctx, remoteTalkbackDrainBound)
	defer cancel()
	targets, err := s.targets(name)
	for _, t := range targets {
		if derr := s.drain(ctx, name, rc, t); derr != nil && err == nil {
			err = derr
		}
	}
	s.finish(name, rc, targets, err)
}

func (s *remoteTalkbackScheduler) finish(name string, rc RemoteConfig, targets []remoteTalkbackTarget, err error) {
	now := s.now()
	s.mu.Lock()
	st := s.state[name]
	st.inFlight = false
	if targets != nil {
		st.targets = targets
	}
	if err == nil {
		st.failures, st.alerted, st.firstFailAt = 0, false, time.Time{}
		st.nextAt = now.Add(time.Duration(rc.TalkbackIntervalSecs) * time.Second)
		s.mu.Unlock()
		return
	}
	st.failures++
	if st.failures == 1 {
		st.firstFailAt = now
	}
	backoff := remoteTalkbackBackoffMin << min(st.failures-1, 4)
	st.nextAt = now.Add(min(backoff, remoteTalkbackBackoffMax))
	var alertTargets []remoteTalkbackTarget
	if st.failures >= remoteTalkbackAlertAfter && !st.alerted {
		st.alerted = true
		alertTargets = st.targets
	}
	streakStart, failures := st.firstFailAt, st.failures
	s.mu.Unlock()
	commsLog.Warn("remote_talkback_failed", "remote", name, "failures", failures, "error", err.Error())
	if len(alertTargets) == 0 {
		return
	}
	mins := int(now.Sub(streakStart).Round(time.Minute) / time.Minute)
	text := fmt.Sprintf("remote %s: talkback failing for %d min: %s", name, mins, firstLineOf([]byte(err.Error())))
	for _, t := range alertTargets {
		s.alert(name, t, text, streakStart)
	}
}

// discoverTalkbackTargets lists the local conductors enrolled with remote: a
// conductor-titled session that has a cursor for it, or a pending inbox record
// from it.
func discoverTalkbackTargets(remote string) ([]remoteTalkbackTarget, error) {
	var out []remoteTalkbackTarget
	for _, profile := range profilesForTransitionDaemon() {
		storage, err := NewStorageWithProfile(profile)
		if err != nil {
			return nil, err
		}
		instances, _, err := storage.LoadWithGroups()
		storage.Close()
		if err != nil {
			return nil, err
		}
		for _, inst := range instances {
			if isConductorSessionTitle(inst.Title) && talkbackEnrolled(remote, inst.ID) {
				out = append(out, remoteTalkbackTarget{Parent: inst, Profile: profile})
			}
		}
	}
	return out, nil
}

func talkbackEnrolled(remote, parentID string) bool {
	if _, found, _ := LoadRemoteCursor(remote, parentID); found {
		return true
	}
	events, _ := ReadInboxEvents(parentID)
	for _, ev := range events {
		if ev.SourceRemote == remote {
			return true
		}
	}
	return false
}

// writeTalkbackAlert commits the one urgent "link is down" record of a
// failure streak into the conductor's inbox and wakes it. The streak start
// keys the record, so a retry of the same streak collapses and a later
// streak is a new record.
func writeTalkbackAlert(remote string, t remoteTalkbackTarget, text string, streakStart time.Time,
	wake func(*Instance, string, TransitionNotificationEvent)) {
	ev := TransitionNotificationEvent{
		ChildSessionID:  RemoteScopedChildID(remote, "talkback"),
		ChildTitle:      "remote " + remote + " talkback",
		Profile:         t.Profile,
		FromStatus:      string(StatusRunning),
		ToStatus:        string(StatusError),
		Timestamp:       time.Now(),
		SourceRemote:    remote,
		TargetSessionID: t.Parent.ID,
		TargetKind:      "parent",
		Tier:            TurnTierUrgent,
		Trigger:         TurnTriggerSystem,
		Text:            text,
		LastOutputHash:  fmt.Sprintf("talkback-failing:%d", streakStart.UnixNano()),
	}
	if err := CommitToInbox(t.Parent.ID, ev); err != nil {
		commsLog.Warn("remote_talkback_alert_failed", "remote", remote, "parent", t.Parent.ID, "error", err.Error())
		return
	}
	if wake != nil {
		wake(t.Parent, t.Profile, ev)
	}
}
