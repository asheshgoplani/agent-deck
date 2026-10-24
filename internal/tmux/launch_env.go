package tmux

import "strings"

// new-session -e (environment for the new session and its first pane) is
// tmux >= 3.2.
const (
	launchEnvMinTmuxMajor = 3
	launchEnvMinTmuxMinor = 2
)

// tmuxSupportsLaunchEnv reports whether ver (as parseTmuxVersion returns it)
// accepts new-session -e. An empty (unknown) version is unsupported, so the
// caller keeps its fallback; an unparseable one ("master") counts as new.
func tmuxSupportsLaunchEnv(ver string) bool {
	if ver == "" {
		return false
	}
	major, minor, _, ok := splitTmuxVersion(ver)
	if !ok {
		return true
	}
	return major > launchEnvMinTmuxMajor ||
		(major == launchEnvMinTmuxMajor && minor >= launchEnvMinTmuxMinor)
}

// SupportsLaunchEnv reports whether Session.LaunchEnv reaches the pane on
// this host's tmux. When it does not, Start ignores LaunchEnv and the caller
// has to deliver the variables some other way.
func SupportsLaunchEnv() bool {
	return tmuxSupportsLaunchEnv(hostTmuxVersionString())
}

// launchEnvArgs returns the new-session `-e NAME=value` pairs for env, or
// nil when env is empty or the host tmux has no -e. Each pair is its own
// argv token, so a value needs no shell quoting; only a trailing ';' needs
// escaping (launchEnvToken).
func launchEnvArgs(env []string) []string {
	if len(env) == 0 || !SupportsLaunchEnv() {
		return nil
	}
	args := make([]string, 0, 2*len(env))
	for _, kv := range env {
		args = append(args, "-e", launchEnvToken(kv))
	}
	return args
}

// launchEnvToken escapes kv for tmux's argv parser, which treats a token
// ending in ';' as a command separator: "T=a;" would end new-session at
// "T=a" and parse the next "-e" as a command. tmux drops a trailing ';' and,
// if the token then ends in '\', turns that '\' into ';'. So one '\' put
// before a trailing ';' gives back the value exactly, also for a value that
// already ends in "\;".
func launchEnvToken(kv string) string {
	if strings.HasSuffix(kv, ";") {
		return kv[:len(kv)-1] + `\;`
	}
	return kv
}
