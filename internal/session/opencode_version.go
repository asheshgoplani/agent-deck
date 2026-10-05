package session

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// OpenCode 2.x reshaped the root command's flags. It rejects the 1.x
// -m/--model, --agent and --port flags ("Unrecognized flag: --port in command
// opencode") and exits at once, so a session launched with any of them dies
// before the TUI draws (spawn_died_fast). In 2.x model and agent overrides
// are applied through the session API, and events come from a shared service
// rather than a per-TUI --port server.
//
// buildOpenCodeCommand therefore asks the binary a launch will run for its
// version (`opencode --version`, well under the probe timeout) and emits those
// flags only for 1.x. A successful answer is memoised per resolved binary path,
// mtime and size, so an upgrade is re-probed. A binary that cannot be found, or
// whose version does not parse, keeps the 1.x flags: that is the existing
// behaviour.

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

// probeOpenCodeMajorVersion reports the major version of the OpenCode binary a
// local launch of i runs. It is a var so tests can pin a version without an
// installed CLI; TestMain pins "unknown" so the package's tests never exec the
// host's opencode.
var probeOpenCodeMajorVersion = probeInstalledOpenCodeMajorVersion

func probeInstalledOpenCodeMajorVersion(i *Instance) (int, bool) {
	binary, ok := i.openCodeLaunchBinary()
	if !ok {
		return 0, false
	}
	return probeOpenCodeBinaryMajorVersion(binary)
}

func probeOpenCodeBinaryMajorVersion(binary string) (int, bool) {
	key, err := modelProbeBinaryKey(binary)
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

// openCodeLaunchBinary resolves the executable of the configured [opencode]
// command the way the pane will: the spawn-path prelude's dirs ahead of this
// process's PATH, then any leading PATH= assignment in the command itself
// ($PATH in it expands to the PATH before it). Words are split quote-aware,
// so a quoted path with spaces stays whole. An `env` wrapper is looked
// through: `env [VAR=value...] opencode` runs opencode with those assignments
// applied, PATH included.
func (i *Instance) openCodeLaunchBinary() (string, bool) {
	words, ok := shellwords.Split(i.openCodeForkBinary())
	if !ok {
		return "", false
	}
	pathEnv := os.Getenv("PATH")
	if dirs := i.spawnPathDirs(); len(dirs) > 0 {
		pathEnv = strings.Join(append(dirs, pathEnv), string(os.PathListSeparator))
	}
	name := "opencode"
	wrapped := false
	for _, word := range words {
		if isShellEnvAssignment(word) {
			if value, found := strings.CutPrefix(word, "PATH="); found {
				before := pathEnv
				pathEnv = os.Expand(value, func(v string) string {
					if v == "PATH" {
						return before
					}
					return os.Getenv(v)
				})
			}
			continue
		}
		if !wrapped && filepath.Base(word) == "env" {
			wrapped = true
			continue
		}
		if wrapped && strings.HasPrefix(word, "-") {
			// env options (-i, -u NAME, -S ...) change what follows; don't guess.
			return "", false
		}
		name = word
		break
	}
	return lookPathIn(name, pathEnv)
}

// lookPathIn is exec.LookPath against an explicit PATH value. A name with a
// slash is used as given (a leading ~/ expands to $HOME, as the shell would).
func lookPathIn(name, pathEnv string) (string, bool) {
	if rest, found := strings.CutPrefix(name, "~/"); found {
		name = filepath.Join(os.Getenv("HOME"), rest)
	}
	if strings.Contains(name, "/") {
		return name, isExecutableFile(name)
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		if candidate := filepath.Join(dir, name); isExecutableFile(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// openCodeRejectsV1LaunchFlags reports whether this session's OpenCode is 2.x
// or newer. Sandboxed and SSH sessions run a binary this host cannot see, so
// they keep the 1.x flags.
func (i *Instance) openCodeRejectsV1LaunchFlags() bool {
	if i.IsSandboxed() || i.IsSSH() {
		return false
	}
	major, ok := probeOpenCodeMajorVersion(i)
	return ok && major >= 2
}
