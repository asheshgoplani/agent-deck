package sshcontrol

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"testing"
)

// sunPathMax is the macOS sockaddr_un.sun_path size; one byte is the NUL.
const sunPathMax = 104

// sshTempSuffixLen is what OpenSSH adds while it binds a new master: it
// listens on "<ControlPath>.<16 random chars>" and renames it afterwards.
const sshTempSuffixLen = 1 + 16

// expandLikeSSH expands the ControlPath tokens agent-deck could use, the way
// OpenSSH does. %C is the hex SHA-1 of %l%h%p%r (newer releases also hash
// %j); either way it is 40 characters.
func expandLikeSSH(path, localHost, user, host, port string) string {
	sum := sha1.Sum([]byte(localHost + host + port + user))
	return strings.NewReplacer(
		"%C", hex.EncodeToString(sum[:]),
		"%r", user,
		"%h", host,
		"%p", port,
		"%l", localHost,
	).Replace(path)
}

// TestControlPath_LongHostFitsUnixSocketLimit is #2837: with a long user and
// host name the bound socket path must still fit macOS sun_path, for the
// per-user directory and for the MkdirTemp fallback, with the largest uid.
func TestControlPath_LongHostFitsUnixSocketLimit(t *testing.T) {
	user := "deploy-automation"
	host := "build-runner-07.eu-central-1.compute.internal.example-corp.net"
	if n := len(user + "@" + host); n < 70 {
		t.Fatalf("user@host is only %d bytes; the case needs a long one", n)
	}
	const maxUID = 4294967294 // largest 32-bit uid, 10 digits
	preferred := (&resolver{parent: "/tmp", uid: maxUID}).preferred()
	// os.MkdirTemp appends a random uint32 in decimal (up to 10 digits).
	fallback := preferred + "-" + "4294967295"
	for name, dir := range map[string]string{"preferred": preferred, "fallback": fallback} {
		bound := expandLikeSSH(dir+socketSuffix, "my-laptop.local", user, host, "2222") +
			strings.Repeat("x", sshTempSuffixLen)
		if len(bound) >= sunPathMax {
			t.Errorf("%s: ssh would bind %q (%d bytes), over the %d-byte macOS sun_path limit",
				name, bound, len(bound), sunPathMax-1)
		}
	}
}
