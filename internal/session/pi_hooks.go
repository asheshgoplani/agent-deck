package session

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
	"github.com/asheshgoplani/agent-deck/internal/send"
)

// piHookExtensionSource is the pi extension agent-deck installs into pi's
// global extension directory. It is the only copy: the file on disk is
// generated from these bytes, never hand-edited (see PiHookExtensionSource).
//
//go:embed assets/pi/agent-deck.ts
var piHookExtensionSource string

const (
	// piHookExtensionFileName is the basename inside pi's extensions dir. pi
	// auto-discovers every *.ts there, so writing the file is the whole
	// install — there is no settings.json entry to merge (unlike Cursor's
	// hooks.json or Codex's config.toml).
	piHookExtensionFileName = "agent-deck.ts"
	// piHookExtensionMarker identifies a file as ours. Uninstall refuses to
	// delete a file without it, so a user's own agent-deck.ts is never lost.
	piHookExtensionMarker = "AGENTDECK PI HOOK EXTENSION"
	// piHookExtensionVersion is bumped whenever the emitted event set or the
	// payload shape changes, so `pi-hooks status` can report drift and
	// `pi-hooks install` can upgrade in place.
	piHookExtensionVersion = 5
)

var piHookExtensionVersionRe = regexp.MustCompile(piHookExtensionMarker + ` v(\d+)`)

// PiHookExtensionSource returns the extension source agent-deck installs.
func PiHookExtensionSource() string { return piHookExtensionSource }

// PiHookExtensionVersion returns the version agent-deck ships.
func PiHookExtensionVersion() int { return piHookExtensionVersion }

// PiExtensionsDir returns pi's global extension directory. pi reads its config
// directory from PI_CODING_AGENT_DIR and defaults to ~/.pi/agent; extensions
// live in the "extensions" subdirectory of it.
func PiExtensionsDir() string {
	return piExtensionsDirOf(os.Getenv("PI_CODING_AGENT_DIR"))
}

// piExtensionsDirOf is PiExtensionsDir for a given PI_CODING_AGENT_DIR value.
func piExtensionsDirOf(agentDir string) string {
	if dir := strings.TrimSpace(agentDir); dir != "" {
		return sandboxedToolDir("Pi extensions dir (PI_CODING_AGENT_DIR)", filepath.Join(dir, "extensions"))
	}
	dir, err := sandboxguard.HomeJoin("Pi extensions dir (~/.pi/agent/extensions)", ".pi", "agent", "extensions")
	if err != nil {
		return filepath.Join(os.TempDir(), ".pi", "agent", "extensions")
	}
	return dir
}

// PiHookExtensionPath returns the full path of the installed extension file.
func PiHookExtensionPath(extensionsDir string) string {
	return filepath.Join(extensionsDir, piHookExtensionFileName)
}

// PiHooksState describes what is currently on disk at PiHookExtensionPath.
type PiHooksState int

const (
	// PiHooksNotInstalled: no file at all.
	PiHooksNotInstalled PiHooksState = iota
	// PiHooksInstalled: our file, current version, byte-identical.
	PiHooksInstalled
	// PiHooksOutdated: our file, but an older version or locally modified.
	PiHooksOutdated
	// PiHooksForeign: a file we did not write. Install and uninstall both
	// refuse to touch it.
	PiHooksForeign
)

// InspectPiHooks reports the on-disk state of the pi extension, plus the
// installed version when the file is one of ours.
func InspectPiHooks(extensionsDir string) (PiHooksState, int) {
	data, err := os.ReadFile(PiHookExtensionPath(extensionsDir))
	if err != nil {
		return PiHooksNotInstalled, 0
	}
	content := string(data)
	match := piHookExtensionVersionRe.FindStringSubmatch(content)
	if match == nil {
		return PiHooksForeign, 0
	}
	version, _ := strconv.Atoi(match[1])
	if version == piHookExtensionVersion && content == piHookExtensionSource {
		return PiHooksInstalled, version
	}
	return PiHooksOutdated, version
}

// CheckPiHooksInstalled reports whether the current extension is installed.
func CheckPiHooksInstalled(extensionsDir string) bool {
	state, _ := InspectPiHooks(extensionsDir)
	return state == PiHooksInstalled
}

// InstallPiHooks writes (or upgrades) the agent-deck pi extension. It returns
// false with no error when the current version is already in place, and an
// error when a foreign agent-deck.ts is in the way — clobbering somebody
// else's extension is never the right call, and the caller tells the user to
// move it aside.
func InstallPiHooks(extensionsDir string) (bool, error) {
	state, _ := InspectPiHooks(extensionsDir)
	switch state {
	case PiHooksInstalled:
		return false, nil
	case PiHooksForeign:
		return false, fmt.Errorf("%s exists and was not written by agent-deck; move it aside and retry",
			PiHookExtensionPath(extensionsDir))
	}

	if err := os.MkdirAll(extensionsDir, 0755); err != nil {
		return false, fmt.Errorf("create pi extensions dir: %w", err)
	}
	if err := atomicfile.WriteFile(PiHookExtensionPath(extensionsDir), []byte(piHookExtensionSource), 0644); err != nil {
		return false, fmt.Errorf("write pi extension: %w", err)
	}
	sessionLog.Info("pi_hooks_installed",
		slog.String("extensions_dir", extensionsDir),
		slog.Int("version", piHookExtensionVersion))
	return true, nil
}

// RemovePiHooks deletes the agent-deck pi extension. It returns false with no
// error when there is nothing of ours to remove, and an error for a foreign
// agent-deck.ts (same reasoning as InstallPiHooks).
func RemovePiHooks(extensionsDir string) (bool, error) {
	state, _ := InspectPiHooks(extensionsDir)
	switch state {
	case PiHooksNotInstalled:
		return false, nil
	case PiHooksForeign:
		return false, fmt.Errorf("%s was not written by agent-deck; refusing to remove it",
			PiHookExtensionPath(extensionsDir))
	}

	if err := os.Remove(PiHookExtensionPath(extensionsDir)); err != nil {
		return false, fmt.Errorf("remove pi extension: %w", err)
	}
	sessionLog.Info("pi_hooks_removed", slog.String("extensions_dir", extensionsDir))
	return true, nil
}

const (
	// piSocketFloor is the first pi that has pi.sendMessage with triggerTurn and
	// the before_agent_start custom event the wake socket rides on.
	piSocketFloor = "1.2.0"
	// piVersionTimeout bounds `pi --version`; a wedged pi must not stall a launch.
	piVersionTimeout = 2 * time.Second
)

// piVersionOutputCap bounds what is read of `pi --version`: a version is a few
// bytes, and a pi that floods stdout must not be buffered whole.
const piVersionOutputCap = 4096

// piVersionRe is x.y.z with an optional semver prerelease suffix.
var piVersionRe = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?`)

// cappedWriter keeps the first max bytes and discards the rest, reporting every
// write as taken so the writer is never blocked or failed.
type cappedWriter struct {
	buf []byte
	max int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// PiSocketInfo is what PiSocketPath learned for one launch.
type PiSocketInfo struct {
	// Path is the AGENTDECK_PI_SOCKET value for this launch; empty when off.
	Path string
	// Version is the first major.minor.patch token of `pi --version`; empty when unread.
	Version string
	// Binary is the resolved pi executable on the launch PATH.
	Binary string
	// Reason is "" when on, else "pi_too_old", "pi_version_unknown",
	// "path_too_long" or "no_runtime_dir". A launch adds "extension_not_installed"
	// and "extension_outdated".
	Reason string
	// Detail is the text `pi-hooks status` prints inside "Socket: off (...)".
	Detail string
}

// PiSocketPath decides whether the wake socket is on for a pi launched with
// env, and where it listens: ProfileRuntimeDir(profile)/pi/<sha256(instance)[:12]>.sock.
// The hashed leaf keeps the path under the 103-byte sun_path limit that the
// instance id itself would exceed. The version comes from running the pi on
// env's PATH, every call: a package.json can disagree with the binary that
// actually launches, and a cache would outlive an upgrade. It is pure and
// never logs; piSocketEnvForLaunch is the caller that warns.
func PiSocketPath(profile, instanceID string, env []string) PiSocketInfo {
	bin, found := lookPathIn("pi", pathOf(env))
	return piSocketProbe(profile, instanceID, env, bin, found)
}

// PiStatusEnv is the env `pi-hooks status` probes pi with: the launch env with
// the spawn-path prelude's directories ahead of PATH, so status resolves the pi
// a launch of a plain `pi` resolves. A command's own PATH= or other leading
// assignments are not seen here; status has no session.
func PiStatusEnv() []string {
	_, _, pathEnv, _ := (&Instance{}).launchBinary("pi", "pi")
	env := childenv.ForLaunch("")
	return append(env[:len(env):len(env)], "PATH="+pathEnv)
}

// piSocketProbe is PiSocketPath for a pi binary the caller already resolved.
func piSocketProbe(profile, instanceID string, env []string, bin string, found bool) PiSocketInfo {
	info := PiSocketInfo{Binary: bin}
	if !found {
		info.Reason, info.Detail = "pi_version_unknown", "pi --version failed: pi not found on PATH"
		return info
	}
	ctx, cancel := context.WithTimeout(context.Background(), piVersionTimeout)
	defer cancel()
	// #nosec G204 -- the binary is the pi the launch resolves and the only arg is a fixed flag.
	cmd := exec.CommandContext(ctx, info.Binary, "--version")
	cmd.Env = env
	// A wrapper script's children outlive a kill of the script alone: the pi
	// leads its own group, and the timeout kills the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// A killed group's pipe holders are gone; do not wait for strays.
	cmd.WaitDelay = 200 * time.Millisecond
	out := &cappedWriter{max: piVersionOutputCap}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		info.Reason, info.Detail = "pi_version_unknown", fmt.Sprintf("pi --version failed: %v", err)
		return info
	}
	info.Version = piVersionRe.FindString(string(out.buf))
	if info.Version == "" {
		info.Reason, info.Detail = "pi_version_unknown", "pi --version failed: no x.y.z in its output"
		return info
	}
	if compareVersions(info.Version, piSocketFloor) < 0 {
		info.Reason, info.Detail = "pi_too_old", fmt.Sprintf("pi %s is below %s", info.Version, piSocketFloor)
		return info
	}
	path, err := piSocketFile(profile, instanceID)
	if err != nil {
		info.Reason, info.Detail = "no_runtime_dir", err.Error()
		return info
	}
	if len(path) > send.MaxSockPath {
		info.Reason, info.Detail = "path_too_long", fmt.Sprintf("path %d bytes, limit %d", len(path), send.MaxSockPath)
		return info
	}
	info.Path = path
	return info
}

// piSocketFile is where a pi instance's extension listens: the hashed instance
// under the profile's runtime directory. The launch exports it and the daemon's
// wake dials it; neither checks the length here.
func piSocketFile(profile, instanceID string) (string, error) {
	runtimeDir, err := agentpaths.ProfileRuntimeDir(profile)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(instanceID))
	return filepath.Join(runtimeDir, "pi", hex.EncodeToString(sum[:])[:12]+".sock"), nil
}

// piSocketEnvForLaunch is the launch-side caller of PiSocketPath: the
// AGENTDECK_PI_SOCKET assignment for the pi launch env, or "" when the socket
// is off, in which case it logs one warning with the version seen and why.
func piSocketEnvForLaunch(profile, instanceID string, env []string) (string, PiSocketInfo) {
	bin, found := lookPathIn("pi", pathOf(env))
	return piSocketLaunch(profile, instanceID, env, bin, found)
}

// piSocketLaunch is piSocketEnvForLaunch for a pi the caller already resolved.
// The extension is what serves the socket, so a missing or outdated one comes
// first, the same order `pi-hooks status` prints. It is looked for where this
// pi loads extensions from: env's PI_CODING_AGENT_DIR, which carries a
// command's own leading assignment, else this process's.
func piSocketLaunch(profile, instanceID string, env []string, bin string, found bool) (string, PiSocketInfo) {
	extDir := PiExtensionsDir()
	if agentDir, ok := envValueOf(env, "PI_CODING_AGENT_DIR"); ok {
		extDir = piExtensionsDirOf(agentDir)
	}
	var info PiSocketInfo
	switch state, version := InspectPiHooks(extDir); state {
	case PiHooksNotInstalled, PiHooksForeign:
		info = PiSocketInfo{Reason: "extension_not_installed", Detail: "extension not installed"}
	case PiHooksOutdated:
		info = PiSocketInfo{Reason: "extension_outdated",
			Detail: fmt.Sprintf("extension is v%d, current v%d", version, PiHookExtensionVersion())}
	default:
		info = piSocketProbe(profile, instanceID, env, bin, found)
	}
	if info.Path == "" {
		sessionLog.Warn("pi_wake_socket off",
			slog.String("version", info.Version),
			slog.String("reason", info.Reason))
		return "", info
	}
	return "AGENTDECK_PI_SOCKET=" + info.Path, info
}

// piSocketLaunchPrefix is the shell prefix a pi launch of command carries for
// the wake socket: the quoted assignment and a space, or an empty assignment
// when a local launch leaves it off (or "" for SSH and sandboxed ones). The pi
// probed is the one the pane will run: the command's own word on the pane's
// PATH and with the command's leading assignments, as openCodeLaunchBinary
// resolves opencode.
func (i *Instance) piSocketLaunchPrefix(command string) string {
	// The socket is for local launches: the extension of an SSH or sandboxed pi
	// runs in another filesystem and process namespace, which the host daemon
	// cannot reach. Such a launch exports nothing and records false, so its
	// doorbell is typed as always.
	if i.IsSSH() || i.IsSandboxed() {
		i.PiSocketExported = false
		return ""
	}
	bin, found, pathEnv, assigns := i.launchBinary(command, "pi")
	env := childenv.ForLaunch("")
	env = append(env[:len(env):len(env)], "PATH="+pathEnv)
	envVar, info := piSocketLaunch(sessionProfileEnvValue(), i.ID, append(env, assigns...), bin, found)
	// What this launch exports is what the daemon must respect.
	i.PiSocketExported = envVar != ""
	if envVar == "" {
		// Explicitly empty, so the pane does not inherit a value from the tmux
		// server's environment (a server first started inside a socket-exported
		// pi pane, as with CLAUDE_JOB_DIR in #2683); the extension reads empty as off.
		return "AGENTDECK_PI_SOCKET= "
	}
	return "AGENTDECK_PI_SOCKET=" + shellescape.Quote(info.Path) + " "
}

// pathOf returns the PATH carried by env.
func pathOf(env []string) string {
	v, _ := envValueOf(env, "PATH")
	return v
}

// envValueOf returns key's value in env: the last assignment wins, as exec's does.
func envValueOf(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// compareVersions compares two x.y.z[-prerelease] strings by semver precedence:
// the triple numerically (1.10.0 > 1.2.0); a prerelease is below its own
// release; prerelease identifiers compare left to right, numeric ones
// numerically and below alphanumeric ones, and a shorter list is below a longer
// one that begins with it.
func compareVersions(a, b string) int {
	na, preA, _ := strings.Cut(a, "-")
	nb, preB, _ := strings.Cut(b, "-")
	pa, pb := strings.Split(na, "."), strings.Split(nb, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	}
	ia, ib := strings.Split(preA, "."), strings.Split(preB, ".")
	for i := 0; i < len(ia) && i < len(ib); i++ {
		if c := comparePrereleaseID(ia[i], ib[i]); c != 0 {
			return c
		}
	}
	return len(ia) - len(ib)
}

func comparePrereleaseID(a, b string) int {
	numA, numB := isDigits(a), isDigits(b)
	switch {
	case numA && numB:
		// Integers of any length: by length, then lexically, so nothing overflows.
		if len(a) != len(b) {
			return len(a) - len(b)
		}
		return strings.Compare(a, b)
	case numA:
		return -1
	case numB:
		return 1
	}
	return strings.Compare(a, b)
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
