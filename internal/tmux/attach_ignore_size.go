package tmux

// attach-session -f (client flags such as ignore-size) is tmux >= 3.2.
const (
	attachFlagsMinTmuxMajor = 3
	attachFlagsMinTmuxMinor = 2
)

// tmuxSupportsAttachIgnoreSize reports whether ver (as parseTmuxVersion
// returns it) accepts `attach-session -f ignore-size`. An empty or
// unparseable version counts as unsupported, so the caller attaches without
// the flag instead of handing tmux one it rejects.
func tmuxSupportsAttachIgnoreSize(ver string) bool {
	major, minor, _, ok := splitTmuxVersion(ver)
	if !ok {
		return false
	}
	return major > attachFlagsMinTmuxMajor ||
		(major == attachFlagsMinTmuxMajor && minor >= attachFlagsMinTmuxMinor)
}

// SupportsAttachIgnoreSize reports whether the tmux that serves socketName
// accepts `attach-session -f ignore-size`. It reads that socket's own binary
// ([tmux.binaries]) rather than the host's.
func SupportsAttachIgnoreSize(socketName string) bool {
	return tmuxSupportsAttachIgnoreSize(socketTmuxVersionString(socketName))
}
