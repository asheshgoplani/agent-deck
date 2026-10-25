package ui

import (
	"log/slog"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
)

// Updates while attached (defect 310). The binary watch and auto_restart
// run on the tick, which stops for the whole attach (the loop is parked in
// tea.Exec), so a deck left attached never took an update and nothing said
// one was waiting. The attach worker (heartbeat.go) now also looks for a
// newer build and tells the person in the attach to detach; the detach
// back to the home screen then runs the existing restart path at once
// instead of a tick later, which a quick re-attach used to beat. Nothing
// here interrupts an attach. [updates].auto_restart=false turns both off.

// attachUpdateNoticeEvery is how often the notice is repeated; the tmux
// status line (or a remote's) paints over it after a while.
const attachUpdateNoticeEvery = 10 * time.Minute

// attachUpdate is what the attach worker knows about a newer build. The
// loop publishes its binary watch here (shareUpdateState), the worker adds
// what it probed itself while attached, and the detach hands that back.
type attachUpdate struct {
	mu        sync.Mutex
	exe       string
	running   string
	probed    binaryFingerprint // file the loop last probed
	installed string            // newer build the loop knows of
	held      bool              // the loop holds that build back (failed dry run)
	updating  bool              // the unattended updater is still running
	gen       uint64            // bumped by each detach; a probe from before it is dropped
	found     binaryFingerprint // file the worker probed while attached
	foundVer  string            // its version when newer, else ""
	failed    binaryFingerprint // file whose probe failed
	failures  int               // failed probes of it, up to binaryProbeMaxFailures
	noticeAt  time.Time
	noticeVer string
}

// shareUpdateState publishes the binary watch for the attach worker. Event
// loop only.
func (h *Home) shareUpdateState() {
	a := &h.attachUpdate
	a.mu.Lock()
	defer a.mu.Unlock()
	w := h.binaryWatch
	if w == nil {
		a.exe = ""
		return
	}
	a.exe, a.running, a.probed, a.installed = w.execPath, w.runningVersion, w.probed, w.installedVersion
	// A detach would not restart into a held target or past a running
	// updater (maybeAutoRestart), so the notice must not say it would.
	a.held = time.Now().Before(h.autoRestartHoldUntil)
	a.updating = h.autoInstallInFlight != ""
}

// attachUpdateNotice is the line shown in the attach.
func attachUpdateNotice(version string) string {
	return "agent-deck v" + version + " is installed: detach to restart"
}

// attachedUpdateTick is the attach worker's update look: while attached it
// shows the notice when a newer build waits and auto_restart is on, at most
// every attachUpdateNoticeEvery or when the version changes. It never
// restarts anything.
func (h *Home) attachedUpdateTick(now time.Time) {
	a := &h.attachUpdate
	if !h.isAttaching.Load() {
		a.mu.Lock()
		a.noticeAt, a.noticeVer = time.Time{}, ""
		a.mu.Unlock()
		return
	}
	version := h.attachedUpdateVersion()
	if version == "" || !h.autoRestartEnabled() {
		return
	}
	a.mu.Lock()
	due := a.noticeAt.IsZero() || now.Sub(a.noticeAt) >= attachUpdateNoticeEvery || version != a.noticeVer
	a.mu.Unlock()
	if due && tmux.ShowAttachNotice(attachUpdateNotice(version)) {
		a.mu.Lock()
		a.noticeAt, a.noticeVer = now, version
		a.mu.Unlock()
		uiLog.Info("tui_update_notice_attached", slog.String("running", Version), slog.String("installed", version))
	}
}

// attachedUpdateVersion is the newer build a detach would restart into:
// the loop's, or one that landed during the attach (one stat per tick, one
// probe per distinct file, retried like the loop's, off the loop). "" while
// the updater runs or the loop holds the build it knows back.
func (h *Home) attachedUpdateVersion() string {
	a := &h.attachUpdate
	a.mu.Lock()
	exe, running, probed, installed, held, updating := a.exe, a.running, a.probed, a.installed, a.held, a.updating
	found, foundVer, failed, failures, gen := a.found, a.foundVer, a.failed, a.failures, a.gen
	a.mu.Unlock()
	if updating {
		return ""
	}
	if !held && (installed != "" || exe == "") {
		return installed
	}
	if exe == "" {
		return ""
	}
	fp, err := statBinary(exe)
	if err != nil || fp.Equal(probed) {
		return ""
	}
	if fp.Equal(found) {
		return foundVer
	}
	if fp.Equal(failed) && failures >= binaryProbeMaxFailures {
		return ""
	}
	version, err := probeBinaryVersion(exe)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen {
		return "" // a detach came in between; this probe is stale
	}
	if err != nil || version == "" {
		if fp.Equal(a.failed) {
			a.failures++
		} else {
			a.failed, a.failures = fp, 1
		}
		return ""
	}
	if !update.InstalledVersionNeedsRestart(version, running) {
		version = ""
	}
	a.found, a.foundVer = fp, version
	return version
}

// restartOnDetach runs when an attach returns to the home screen: it hands
// the worker's probe to the binary watch and runs the existing auto
// restart right away. Event loop only.
func (h *Home) restartOnDetach() tea.Cmd {
	a := &h.attachUpdate
	a.mu.Lock()
	found, version := a.found, a.foundVer
	a.found, a.foundVer, a.noticeAt, a.noticeVer = binaryFingerprint{}, "", time.Time{}, ""
	a.failed, a.failures = binaryFingerprint{}, 0
	a.gen++
	a.mu.Unlock()
	if version != "" && h.binaryWatch != nil {
		h.binaryWatch.recordProbe(found, version, nil)
	}
	if h.installedUpdateVersion() == "" {
		return nil
	}
	return h.maybeAutoRestart()
}
