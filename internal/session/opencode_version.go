package session

import (
	"regexp"
	"strconv"
	"sync"
)

// OpenCode 2.x reshaped the root command's flags. It rejects the 1.x
// -m/--model, --agent and --port flags ("Unrecognized flag: --port in command
// opencode") and exits at once, so a session launched with any of them dies
// before the TUI draws (spawn_died_fast). In 2.x the model and agent are
// picked inside the TUI, and events come from a shared background service
// rather than a per-TUI --port server.
//
// buildOpenCodeCommand therefore asks the installed binary for its version
// (`opencode --version`, well under the probe timeout) and emits those flags
// only for 1.x. A successful answer is memoised per resolved binary path, mtime
// and size, so an upgrade is re-probed. A binary that cannot be found, or whose
// version does not parse, keeps the 1.x flags: that is the existing behaviour.

var openCodeVersionPattern = regexp.MustCompile(`(\d+)\.\d+\.\d+`)

// parseOpenCodeMajorVersion extracts the major version from `opencode
// --version` output: "opencode v2.0.20" on 2.x, a bare "1.14.3" on 1.x.
func parseOpenCodeMajorVersion(out string) (int, bool) {
	m := openCodeVersionPattern.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return major, true
}

type openCodeVersionAnswer struct {
	major int
	ok    bool
}

// openCodeVersionMemo maps modelProbeKey -> openCodeVersionAnswer.
var openCodeVersionMemo sync.Map

// probeOpenCodeMajorVersion reports the major version of the OpenCode binary
// sessions launch. It is a var so tests can pin a version without an installed
// CLI; TestMain pins "unknown" so the package's tests never exec the host's
// opencode.
var probeOpenCodeMajorVersion = probeInstalledOpenCodeMajorVersion

func probeInstalledOpenCodeMajorVersion() (int, bool) {
	key, err := modelProbeBinaryKey(commandProbeBinary(GetToolCommand("opencode"), "opencode"))
	if err != nil {
		return 0, false
	}
	if cached, ok := openCodeVersionMemo.Load(key); ok {
		answer := cached.(openCodeVersionAnswer)
		return answer.major, answer.ok
	}
	out, err := runModelProbeCommand(key.Path, "--version")
	if err != nil {
		// Not memoised: a cold start can time out once, and remembering that
		// would keep the 1.x flags until the binary changes.
		return 0, false
	}
	var answer openCodeVersionAnswer
	answer.major, answer.ok = parseOpenCodeMajorVersion(string(out))
	openCodeVersionMemo.Store(key, answer)
	return answer.major, answer.ok
}

// openCodeRejectsV1LaunchFlags reports whether this session's OpenCode is 2.x
// or newer. Sandboxed and SSH sessions run a binary this host cannot see, so
// they keep the 1.x flags.
func (i *Instance) openCodeRejectsV1LaunchFlags() bool {
	if i.IsSandboxed() || i.IsSSH() {
		return false
	}
	major, ok := probeOpenCodeMajorVersion()
	return ok && major >= 2
}
