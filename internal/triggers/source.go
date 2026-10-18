package triggers

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Trigger kinds.
const (
	KindLaunchd   = "launchd"
	KindSystemd   = "systemd"
	KindCron      = "cron"
	KindWatcher   = "watcher"
	KindHeartbeat = "heartbeat"
	KindChannel   = "channel"
	KindInterval  = "interval"
	KindSession   = "session"
	KindSSH       = "ssh"
	KindCLI       = "cli"
)

// CommandCenter is the --source of the web command center's ask.
const CommandCenter = "command-center"

// Source names one sender. ID is the stable trigger key: "<kind>:<name>"
// for a named kind (heartbeat:ops, channel:telegram), and the unit's own key
// "<launchd|systemd>:<unit>" for a bare job name, so a tagged send and the
// scanned unit land on the same row. UnitKind/Unit name the launchd job or
// systemd unit this process runs under, when inference found one.
type Source struct {
	Kind     string
	Name     string
	UnitKind string
	Unit     string
	Inferred bool
}

func (s Source) ID() string {
	if s.Unit != "" && s.Kind == s.UnitKind {
		return s.Kind + ":" + s.Unit
	}
	return s.Kind + ":" + s.Name
}

// UnitID is the key of the unit this send runs under ("" when none). A mute
// on that unit applies whatever --source the job passes.
func (s Source) UnitID() string {
	if s.Unit == "" {
		return ""
	}
	return s.UnitKind + ":" + s.Unit
}

// IsTrigger reports whether fires from this source belong in the default
// triggers view. Session-to-session sends and a person typing at a shell are
// messages, not triggers; they are kept but only shown with --all.
func (s Source) IsTrigger() bool { return s.Kind != KindSession && s.Kind != KindCLI }

// Env is what source inference reads; the real process uses OSEnv.
type Env struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	PPID     int
}

// OSEnv is the environment of this process.
func OSEnv() Env { return Env{Getenv: os.Getenv, ReadFile: os.ReadFile, PPID: os.Getppid()} }

// ParseExplicit turns a --source / AGENTDECK_SOURCE value into a Source.
// "kind:name" with a known kind keeps the kind (heartbeat:ashesh,
// channel:telegram); anything else is a launchd/systemd-agnostic job name.
func ParseExplicit(v string) Source {
	v = strings.TrimSpace(v)
	if v == CommandCenter {
		// The web command center is a channel of its own: it never joins
		// the unit the web server happens to run under.
		return Source{Kind: KindChannel, Name: CommandCenter}
	}
	if kind, name, ok := strings.Cut(v, ":"); ok && name != "" && knownKind(kind) {
		return Source{Kind: kind, Name: cleanName(name)}
	}
	return Source{Kind: "job", Name: cleanName(v)}
}

func knownKind(k string) bool {
	switch k {
	case KindLaunchd, KindSystemd, KindCron, KindWatcher, KindHeartbeat, KindChannel, KindInterval, KindSession, KindSSH, KindCLI, "job":
		return true
	}
	return false
}

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// Resolve picks the source of a send: an explicit --source wins, then
// AGENTDECK_SOURCE, then the sending session, then inference from how this
// process was started (launchd label, systemd unit, cron, ssh), else "cli".
func Resolve(explicit, senderSessionID string, env Env) Source {
	if explicit == "" {
		explicit = env.Getenv("AGENTDECK_SOURCE")
	}
	if explicit != "" {
		src := ParseExplicit(explicit)
		if inf := Infer(env); inf.Kind == KindLaunchd || inf.Kind == KindSystemd || inf.Kind == KindCron {
			src.UnitKind, src.Unit = inf.Kind, inf.Name
			if src.Kind == "job" {
				// A bare name from a launchd job or systemd unit joins the
				// scanned row of that unit; the explicit name stays the
				// display name.
				src.Kind = inf.Kind
			}
		}
		return src
	}
	if senderSessionID != "" && senderSessionID != "cli" {
		return Source{Kind: KindSession, Name: senderSessionID}
	}
	return Infer(env)
}

// Infer works out the sender from the process environment. Every result is
// marked Inferred so the view can say so.
func Infer(env Env) Source {
	if label := env.Getenv("XPC_SERVICE_NAME"); label != "" && label != "0" && !strings.HasPrefix(label, "application.") {
		return Source{Kind: KindLaunchd, Name: cleanName(label), Inferred: true}
	}
	if env.Getenv("INVOCATION_ID") != "" && env.ReadFile != nil {
		if data, err := env.ReadFile("/proc/self/cgroup"); err == nil {
			if unit := systemdUnitFromCgroup(string(data)); unit != "" {
				return Source{Kind: KindSystemd, Name: unit, Inferred: true}
			}
		}
	}
	if name, ok := cronAncestor(env); ok {
		return Source{Kind: KindCron, Name: name, Inferred: true}
	}
	if env.Getenv("SSH_CONNECTION") != "" {
		return Source{Kind: KindSSH, Name: "ssh", Inferred: true}
	}
	return Source{Kind: KindCLI, Name: "cli", Inferred: true}
}

// systemdUnitFromCgroup returns the leaf service unit of a cgroup v2 path such
// as "0::/user.slice/user-1000.slice/user@1000.service/app.slice/x.service",
// without the ".service" suffix. The user manager itself is not a sender.
func systemdUnitFromCgroup(cgroup string) string {
	for _, line := range strings.Split(strings.TrimSpace(cgroup), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		// Only a unit of the user manager (user@<uid>.service/...) is a
		// trigger: that is what the scan reads and `triggers run` can start.
		// A system service (a CI runner, a host agent) running agent-deck is
		// not a sender of its own.
		if !strings.Contains(parts[2], "/user@") {
			continue
		}
		leaf := filepath.Base(parts[2])
		if !strings.HasSuffix(leaf, ".service") || strings.HasPrefix(leaf, "user@") {
			continue
		}
		return strings.TrimSuffix(leaf, ".service")
	}
	return ""
}

// cronAncestor walks up to four parent processes. When one of them is cron,
// the sender is named after the script the job ran (the child of cron's
// shell), falling back to "cron".
func cronAncestor(env Env) (string, bool) {
	if env.ReadFile == nil || env.PPID <= 1 {
		return "", false
	}
	script := ""
	pid := env.PPID
	for i := 0; i < 4 && pid > 1; i++ {
		comm, ppid, args := procInfo(env, pid)
		if comm == "cron" || comm == "crond" {
			if script == "" {
				script = "cron"
			}
			return cleanName(script), true
		}
		if s := scriptArg(args); s != "" {
			script = s
		}
		pid = ppid
	}
	return "", false
}

func procInfo(env Env, pid int) (comm string, ppid int, args []string) {
	p := "/proc/" + strconv.Itoa(pid)
	if b, err := env.ReadFile(p + "/stat"); err == nil {
		s := string(b)
		if l, r := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')'); l >= 0 && r > l {
			comm = s[l+1 : r]
			if f := strings.Fields(s[r+1:]); len(f) >= 2 {
				ppid, _ = strconv.Atoi(f[1])
			}
		}
	}
	if b, err := env.ReadFile(p + "/cmdline"); err == nil {
		args = strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	}
	return comm, ppid, args
}

// scriptArg returns the basename of the script a shell or interpreter runs.
func scriptArg(args []string) string {
	if len(args) < 2 {
		return ""
	}
	switch filepath.Base(args[0]) {
	case "sh", "bash", "zsh", "dash", "python", "python3", "perl", "ruby", "node":
	default:
		return ""
	}
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		return filepath.Base(a)
	}
	return ""
}
