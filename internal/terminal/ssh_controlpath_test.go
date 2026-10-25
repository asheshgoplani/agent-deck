package terminal

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/sshcontrol"
)

// Issue #2837: OpenSSH binds a ControlMaster at "<ControlPath>.<16 random
// chars>" (17 extra bytes). The old "%r@%h:%p" pattern grows with user@host and
// passed sun_path (104 bytes on macOS, 108 on Linux) at ~62 chars, so ssh
// exited 255. These tests expand the template the way OpenSSH does.

const (
	sshBindSuffixLen = 17
	sunPathMax       = 104 // macOS; Linux allows 108
)

// expandControlPath mimics OpenSSH percent-expansion for ControlPath.
func expandControlPath(tmpl, user, host, port string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '%' || i+1 == len(tmpl) {
			b.WriteByte(tmpl[i])
			continue
		}
		i++
		switch tmpl[i] {
		case '%':
			b.WriteByte('%')
		case 'C':
			b.WriteString(strings.Repeat("a", 40)) // hex SHA1
		case 'r':
			b.WriteString(user)
		case 'h':
			b.WriteString(host)
		case 'p':
			b.WriteString(port)
		default:
			return "", fmt.Errorf("unsupported ControlPath token %%%c in %q", tmpl[i], tmpl)
		}
	}
	return b.String(), nil
}

var controlPathRe = regexp.MustCompile(`ControlPath=(?:'(?:\\'')?)?(/[^'\s]+)`)

func controlPathsIn(s string) []string {
	var out []string
	for _, m := range controlPathRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func assertControlPathsFit(t *testing.T, label, emitted string, wantCount int) {
	t.Helper()
	paths := controlPathsIn(emitted)
	if len(paths) != wantCount {
		t.Fatalf("%s: found %d ControlPath values %v, want %d in:\n%s", label, len(paths), paths, wantCount, emitted)
	}
	cases := []struct{ name, user, host string }{
		{"62-char host", "alice", strings.Repeat("h", 62)},
		{"253-char host, long user", strings.Repeat("u", 32), strings.Repeat("h", 253)},
	}
	for _, p := range paths {
		for _, c := range cases {
			got, err := expandControlPath(p, c.user, c.host, "22")
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			if n := len(got) + sshBindSuffixLen; n >= sunPathMax {
				t.Errorf("%s (%s): ControlPath %q expands to %d bytes with bind suffix, want < %d", label, c.name, p, n, sunPathMax)
			}
		}
	}
}

func TestIssue2837_ExpanderDetectsOldPattern(t *testing.T) {
	old := sshcontrol.LegacyDir + "/%r@%h:%p"
	got, err := expandControlPath(old, "alice", strings.Repeat("h", 62), "22")
	if err != nil {
		t.Fatal(err)
	}
	if len(got)+sshBindSuffixLen < sunPathMax {
		t.Fatalf("old pattern expanded to %d bytes; expected the test to be able to fail", len(got)+sshBindSuffixLen)
	}
}

func TestIssue2837_MoshArgsControlPathFits(t *testing.T) {
	args := MoshArgs("me@box", "", []string{"agent-deck"})
	assertControlPathsFit(t, "MoshArgs", strings.Join(args, " "), 1)
}

func TestIssue2837_RemoteAttachControlPathsFit(t *testing.T) {
	ssh := BuildAttachCommand(AttachRequest{Name: "abc", Remote: &RemoteAttach{Host: "me@box"}})
	assertControlPathsFit(t, "ssh attach", ssh, 1)
	// mosh: --ssh= option, the mosh-server probe, and the ssh fallback.
	mosh := BuildAttachCommand(AttachRequest{Name: "abc", Remote: &RemoteAttach{Host: "me@box", Transport: "mosh"}})
	assertControlPathsFit(t, "mosh attach", mosh, 3)
}

var (
	// A ControlPath assembled by hand: the old directory, or a %-token template.
	handBuiltControlPathRe = regexp.MustCompile(`/tmp/agent-deck-ssh|[sS][sS][hH]ControlDir|%%?[CrhpuLlnikTj]`)
)

// TestIssue2837_NoHandBuiltControlPath flags any non-test Go source line that
// builds a ControlPath from the socket directory or a %-token template instead
// of sshcontrol.ControlPath(). ControlPath=none and sshcontrol.ControlPath()
// are allowed. The value
// may sit on the line after "ControlPath=", so the next code line is checked too.
func TestIssue2837_NoHandBuiltControlPath(t *testing.T) {
	for _, root := range []string{"../../internal", "../../cmd"} {
		scanned := 0
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			var code []string
			for _, l := range strings.Split(string(data), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(l), "//") {
					code = append(code, l)
				}
			}
			for i, l := range code {
				idx := strings.Index(l, "ControlPath=")
				if idx < 0 {
					continue
				}
				val := l[idx+len("ControlPath="):]
				if i+1 < len(code) {
					val += " " + code[i+1]
				}
				if handBuiltControlPathRe.MatchString(val) {
					t.Errorf("%s:%d builds a ControlPath by hand: %q; use sshcontrol.ControlPath() (#2837)", path, i+1, strings.TrimSpace(l))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
		if scanned == 0 {
			t.Fatalf("scanned no Go files under %s", root)
		}
	}
}
