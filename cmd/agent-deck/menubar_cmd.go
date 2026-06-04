//go:build darwin

package main

// menubar is a macOS menu-bar front-end for agent-deck. It is a pure wrapper:
// every status read and control action shells out to commands that already
// exist (`agent-deck status/conductor/session ...`) or to OS tools
// (`launchctl`, `open`, `osascript`). The one piece of state it owns itself is
// the headless web server's child process — there is no `web stop` verb, so the
// menu-bar process spawns the server and SIGTERMs it (firing the server's
// existing graceful Shutdown). A pid file lets that ownership survive a restart.
//
// See docs/superpowers/specs/2026-06-01-menubar-app-design.md for the design.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
)

const (
	mbPollInterval   = 5 * time.Second
	mbDefaultListen  = "127.0.0.1:8420"
	mbBridgeLabel    = "com.agentdeck.conductor-bridge"
	mbNotifierLabel  = "com.agentdeck.transition-notifier"
	mbConductorSlots = 8
)

// Configuration resolved once in runMenubar.
var (
	mbProfile string
	mbListen  string
	mbSelfExe string
)

// Web child-process tracking.
var (
	mbWebMu  sync.Mutex
	mbWebCmd *exec.Cmd
)

// Menu items, held globally so the poller can update them.
var (
	miSessions    *systray.MenuItem
	miWeb         *systray.MenuItem
	miWebStart    *systray.MenuItem
	miWebStop     *systray.MenuItem
	miWebOpen     *systray.MenuItem
	miBridge      *systray.MenuItem
	miBridgeStart *systray.MenuItem
	miBridgeStop  *systray.MenuItem
	miNotifier    *systray.MenuItem
	miNotifyStart *systray.MenuItem
	miNotifyStop  *systray.MenuItem
	miConductors  *systray.MenuItem
	mbCondSlots   []*systray.MenuItem
	mbIconActive  []byte
	mbIconIdle    []byte
	mbRefreshCh   = make(chan struct{}, 1)
)

// Conductor state captured each poll, indexed to match the slot menu items.
type mbCondInfo struct {
	Name      string
	SessionID string
	Running   bool
}

var (
	mbStateMu sync.Mutex
	mbConds   []mbCondInfo
)

func runMenubar(profile string, args []string) {
	fs := flag.NewFlagSet("menubar", flag.ExitOnError)
	listen := fs.String("listen", mbDefaultListen, "web server listen address used for start/stop and health checks")
	_ = fs.Parse(args)

	mbProfile = profile
	mbListen = *listen
	if exe, err := os.Executable(); err == nil {
		mbSelfExe = exe
	} else {
		mbSelfExe = "agent-deck"
	}
	mbIconActive = mbCircleIcon(true)
	mbIconIdle = mbCircleIcon(false)

	systray.Run(mbOnReady, func() {})
}

func mbOnReady() {
	systray.SetTemplateIcon(mbIconIdle, mbIconIdle)
	systray.SetTitle("")
	systray.SetTooltip("Agent Deck")

	// Keep this status line ENABLED. macOS scrolls an NSStatusItem menu to its
	// first *enabled* item when it opens; a disabled leading item makes the menu
	// open scrolled past "Sessions" (and its separator) into a scroll-arrow state
	// that only corrects after a hover repaint. Leaving it enabled keeps the menu
	// pinned to the top on open. Clicking it is harmless — it just refreshes.
	miSessions = systray.AddMenuItem("Sessions: …", "Click to refresh")
	systray.AddSeparator()

	miWeb = systray.AddMenuItem("Web server: …", "")
	miWebStart = miWeb.AddSubMenuItem("Start", "Start the headless web server")
	miWebStop = miWeb.AddSubMenuItem("Stop", "Stop the web server")
	miWebOpen = miWeb.AddSubMenuItem("Open in browser", "Open the web UI")

	miBridge = systray.AddMenuItem("Bridge daemon: …", "")
	miBridgeStart = miBridge.AddSubMenuItem("Start", "launchctl load the bridge daemon")
	miBridgeStop = miBridge.AddSubMenuItem("Stop", "launchctl unload the bridge daemon")

	miNotifier = systray.AddMenuItem("Notifier: …", "")
	miNotifyStart = miNotifier.AddSubMenuItem("Start", "launchctl load the transition-notifier daemon")
	miNotifyStop = miNotifier.AddSubMenuItem("Stop", "launchctl unload the transition-notifier daemon")

	miConductors = systray.AddMenuItem("Conductors: …", "")
	for i := 0; i < mbConductorSlots; i++ {
		slot := miConductors.AddSubMenuItem("", "")
		slot.Hide()
		mbCondSlots = append(mbCondSlots, slot)
		idx := i
		go func() {
			for range slot.ClickedCh {
				mbToggleConductor(idx)
				mbTriggerRefresh()
			}
		}()
	}

	systray.AddSeparator()
	miOpenTUI := systray.AddMenuItem("Open TUI (Terminal)", "Launch the agent-deck TUI in Terminal")
	miRefresh := systray.AddMenuItem("Refresh now", "Re-check all surfaces")
	systray.AddSeparator()
	miQuit := systray.AddMenuItem("Quit", "Quit the menu-bar app (does not stop sessions)")

	mbOnClick(miSessions, func() {})
	mbOnClick(miWebStart, mbSpawnWeb)
	mbOnClick(miWebStop, mbStopWeb)
	mbOnClick(miWebOpen, func() { _ = exec.Command("open", "http://"+mbListen).Start() })
	mbOnClick(miBridgeStart, func() { mbDaemon(mbBridgeLabel, true) })
	mbOnClick(miBridgeStop, func() { mbDaemon(mbBridgeLabel, false) })
	mbOnClick(miNotifyStart, func() { mbDaemon(mbNotifierLabel, true) })
	mbOnClick(miNotifyStop, func() { mbDaemon(mbNotifierLabel, false) })
	mbOnClick(miOpenTUI, mbOpenTUI)
	mbOnClick(miRefresh, func() {})

	go func() {
		for range miQuit.ClickedCh {
			systray.Quit()
			return
		}
	}()

	// Poller: initial refresh, then on ticker or on-demand.
	go func() {
		mbRefresh()
		t := time.NewTicker(mbPollInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				mbRefresh()
			case <-mbRefreshCh:
				mbRefresh()
			}
		}
	}()
}

// mbOnClick wires a menu item's click channel to fn and triggers a refresh.
func mbOnClick(mi *systray.MenuItem, fn func()) {
	go func() {
		for range mi.ClickedCh {
			fn()
			mbTriggerRefresh()
		}
	}()
}

func mbTriggerRefresh() {
	select {
	case mbRefreshCh <- struct{}{}:
	default:
	}
}

// mbRefresh re-reads every surface's state and updates the menu.
func mbRefresh() {
	anyRunning := false

	var counts struct {
		Waiting int `json:"waiting"`
		Running int `json:"running"`
		Idle    int `json:"idle"`
		Error   int `json:"error"`
		Stopped int `json:"stopped"`
		Total   int `json:"total"`
	}
	if err := mbRunJSON(&counts, "status", "--json"); err == nil {
		miSessions.SetTitle(fmt.Sprintf("Sessions: %d running · %d waiting · %d idle",
			counts.Running, counts.Waiting, counts.Idle))
		if counts.Running > 0 || counts.Waiting > 0 {
			anyRunning = true
		}
	} else {
		miSessions.SetTitle("Sessions: status error")
	}

	var cs struct {
		Enabled         bool `json:"enabled"`
		DaemonRunning   bool `json:"daemon_running"`
		NotifierRunning bool `json:"notifier_daemon_running"`
		Conductors      []struct {
			Name      string `json:"name"`
			SessionID string `json:"session_id"`
			Running   bool   `json:"running"`
		} `json:"conductors"`
	}
	if err := mbRunJSON(&cs, "conductor", "status", "--json"); err == nil {
		mbSetDaemonUI(miBridge, miBridgeStart, miBridgeStop, cs.DaemonRunning, "Bridge daemon")
		mbSetDaemonUI(miNotifier, miNotifyStart, miNotifyStop, cs.NotifierRunning, "Notifier")
		if cs.DaemonRunning || cs.NotifierRunning {
			anyRunning = true
		}
		mbStateMu.Lock()
		mbConds = mbConds[:0]
		for _, c := range cs.Conductors {
			mbConds = append(mbConds, mbCondInfo{Name: c.Name, SessionID: c.SessionID, Running: c.Running})
		}
		mbStateMu.Unlock()
		if mbUpdateCondSlots() {
			anyRunning = true
		}
	}

	running, owned := mbWebStatus()
	mbSetWebUI(running, owned)
	if running {
		anyRunning = true
	}

	if anyRunning {
		systray.SetTemplateIcon(mbIconActive, mbIconActive)
	} else {
		systray.SetTemplateIcon(mbIconIdle, mbIconIdle)
	}
}

func mbSetDaemonUI(parent, start, stop *systray.MenuItem, running bool, name string) {
	if running {
		parent.SetTitle("● " + name + ": running")
		start.Disable()
		stop.Enable()
	} else {
		parent.SetTitle("○ " + name + ": stopped")
		start.Enable()
		stop.Disable()
	}
}

func mbSetWebUI(running, owned bool) {
	switch {
	case running && owned:
		miWeb.SetTitle("● Web server: running " + mbListenPort(mbListen))
		miWebStart.Disable()
		miWebStop.Enable()
		miWebOpen.Enable()
	case running && !owned:
		miWeb.SetTitle("● Web server: running (external)")
		miWebStart.Disable()
		miWebStop.Disable() // not ours to stop
		miWebOpen.Enable()
	default:
		miWeb.SetTitle("○ Web server: stopped")
		miWebStart.Enable()
		miWebStop.Disable()
		miWebOpen.Disable()
	}
}

// mbUpdateCondSlots fills the conductor submenu slots and reports whether any
// conductor is running.
func mbUpdateCondSlots() bool {
	mbStateMu.Lock()
	defer mbStateMu.Unlock()
	running := 0
	for i, slot := range mbCondSlots {
		if i < len(mbConds) {
			c := mbConds[i]
			dot, action := "○", "start"
			if c.Running {
				dot, action, running = "●", "stop", running+1
			}
			slot.SetTitle(fmt.Sprintf("%s %s — %s", dot, c.Name, action))
			if c.SessionID == "" {
				slot.Disable()
			} else {
				slot.Enable()
			}
			slot.Show()
		} else {
			slot.Hide()
		}
	}
	miConductors.SetTitle(fmt.Sprintf("Conductors: %d running", running))
	return running > 0
}

func mbToggleConductor(i int) {
	mbStateMu.Lock()
	var c mbCondInfo
	if i < len(mbConds) {
		c = mbConds[i]
	}
	mbStateMu.Unlock()
	if c.SessionID == "" {
		return
	}
	verb := "start"
	if c.Running {
		verb = "stop"
	}
	mbRunCtl("session", verb, c.SessionID)
}

// --- web server child-process control ------------------------------------

func mbSpawnWeb() {
	mbWebMu.Lock()
	defer mbWebMu.Unlock()
	if mbWebCmd != nil && mbWebCmd.Process != nil {
		return
	}
	args := []string{}
	if mbProfile != "" {
		args = append(args, "-p", mbProfile)
	}
	args = append(args, "web", "--no-tui", "--listen", mbListen)
	cmd := exec.Command(mbSelfExe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if f, err := os.OpenFile(mbWebLogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		mbNotify("web start failed: " + err.Error())
		return
	}
	mbWebCmd = cmd
	mbWriteWebPid(cmd.Process.Pid, mbListen)
	go func() {
		_ = cmd.Wait()
		mbWebMu.Lock()
		if mbWebCmd == cmd {
			mbWebCmd = nil
			mbRemoveWebPid()
		}
		mbWebMu.Unlock()
		mbTriggerRefresh()
	}()
}

func mbStopWeb() {
	mbWebMu.Lock()
	defer mbWebMu.Unlock()
	pid := 0
	if mbWebCmd != nil && mbWebCmd.Process != nil {
		pid = mbWebCmd.Process.Pid
	} else if p, _, ok := mbReadWebPid(); ok {
		pid = p
	}
	if pid <= 0 {
		return
	}
	// Started with Setpgid, so the negative pid signals the whole group;
	// fall back to the bare pid for adopted processes that aren't leaders.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	mbWebCmd = nil
	mbRemoveWebPid()
}

// mbWebStatus reports whether the web server is up (via /healthz) and whether
// this menu-bar process owns the pid (and can therefore stop it).
func mbWebStatus() (running, owned bool) {
	mbWebMu.Lock()
	if mbWebCmd != nil && mbWebCmd.Process != nil {
		owned = true
	}
	mbWebMu.Unlock()
	if pid, _, ok := mbReadWebPid(); ok && mbProcessAlive(pid) {
		owned = true
	}
	running = mbHealthzOK(mbListen)
	if !running && owned {
		// Process is alive but not yet (or no longer) answering — surface it
		// as running so Stop stays available rather than orphaning the pid.
		if pid, _, ok := mbReadWebPid(); ok && mbProcessAlive(pid) {
			running = true
		}
	}
	return running, owned
}

// --- daemon + misc control -------------------------------------------------

func mbDaemon(label string, load bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		mbNotify("cannot resolve home dir")
		return
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	if _, err := os.Stat(plist); err != nil {
		mbNotify(label + ": not installed (run: agent-deck conductor setup)")
		return
	}
	verb := "load"
	if !load {
		verb = "unload"
	}
	if out, err := exec.Command("launchctl", verb, plist).CombinedOutput(); err != nil {
		mbNotify(fmt.Sprintf("launchctl %s failed: %s", verb, strings.TrimSpace(string(out))))
	}
}

func mbOpenTUI() {
	cmd := "agent-deck"
	if mbProfile != "" {
		cmd = "agent-deck -p " + mbProfile
	}
	script := fmt.Sprintf("tell application \"Terminal\"\nactivate\ndo script %q\nend tell", cmd)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		mbNotify("could not open Terminal: " + err.Error())
	}
}

// mbRunCtl runs an agent-deck control command, surfacing failures as a
// notification.
func mbRunCtl(args ...string) {
	full := []string{}
	if mbProfile != "" {
		full = append(full, "-p", mbProfile)
	}
	full = append(full, args...)
	if out, err := exec.Command(mbSelfExe, full...).CombinedOutput(); err != nil {
		mbNotify(fmt.Sprintf("%s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out))))
	}
}

// mbRunJSON runs an agent-deck command and unmarshals its stdout into target.
func mbRunJSON(target any, args ...string) error {
	full := []string{}
	if mbProfile != "" {
		full = append(full, "-p", mbProfile)
	}
	full = append(full, args...)
	out, err := exec.Command(mbSelfExe, full...).Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(out, target)
}

func mbNotify(msg string) {
	script := fmt.Sprintf("display notification %q with title \"Agent Deck\"", msg)
	_ = exec.Command("osascript", "-e", script).Run()
}

// --- pid file + health helpers --------------------------------------------

func mbAgentDeckHome() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".agent-deck")
	}
	return ".agent-deck"
}

func mbWebPidPath() string { return filepath.Join(mbAgentDeckHome(), "menubar-web.pid") }
func mbWebLogPath() string { return filepath.Join(mbAgentDeckHome(), "menubar-web.log") }

type mbWebPidFile struct {
	PID  int    `json:"pid"`
	Addr string `json:"addr"`
}

func mbWriteWebPid(pid int, addr string) {
	b, _ := json.Marshal(mbWebPidFile{PID: pid, Addr: addr})
	_ = os.MkdirAll(mbAgentDeckHome(), 0o755)
	_ = os.WriteFile(mbWebPidPath(), b, 0o644)
}

func mbReadWebPid() (int, string, bool) {
	b, err := os.ReadFile(mbWebPidPath())
	if err != nil {
		return 0, "", false
	}
	var f mbWebPidFile
	if json.Unmarshal(b, &f) != nil || f.PID == 0 {
		return 0, "", false
	}
	return f.PID, f.Addr, true
}

func mbRemoveWebPid() { _ = os.Remove(mbWebPidPath()) }

func mbProcessAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func mbHealthzOK(addr string) bool {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func mbListenPort(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ""
}

// --- icon ------------------------------------------------------------------

// mbCircleIcon renders a 22x22 template icon: a filled disc when active, a
// ring when idle. Template icons are black-with-alpha; macOS tints them to
// match the menu bar.
func mbCircleIcon(filled bool) []byte {
	const s = 22
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	cx, cy := float64(s)/2, float64(s)/2
	const rOuter, rInner = 8.5, 5.0
	black := color.RGBA{R: 0, G: 0, B: 0, A: 255}
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			d := math.Hypot(dx, dy)
			on := d <= rOuter
			if !filled {
				on = d <= rOuter && d >= rInner
			}
			if on {
				img.Set(x, y, black)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
