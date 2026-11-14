package issuereport

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// logTailBytes bounds how much of the end of debug.log is read.
const logTailBytes = 256 * 1024

// logLineCells bounds one log line in the excerpt.
const logLineCells = 400

// debugLogPath is the agent-deck debug log (<XDG cache>/agent-deck/debug.log,
// the file logging.Init writes). Overridable for tests.
var debugLogPath = func() (string, error) { return agentpaths.CachePath("debug.log") }

// commandOutput runs a short probe (tmux -V, sw_vers). Overridable for tests.
var commandOutput = func(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output() // #nosec G204 -- fixed probe commands
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CollectDiagnostics gathers the machine context for a bug report: the
// agent-deck version, OS (with release) and arch, the terminal (from
// TERM_PROGRAM, else TERM, plus tmux) and the last logLines lines of the
// agent-deck debug log. Every field is already passed through Scrub.
func CollectDiagnostics(version string, logLines int) Diagnostics {
	home, _ := os.UserHomeDir()
	d := Diagnostics{
		Version:  strings.TrimSpace(version),
		OS:       osDescription(),
		Arch:     runtime.GOARCH,
		Terminal: terminalDescription(),
		Tool:     strings.TrimSpace(os.Getenv("AGENTDECK_TOOL")),
	}
	if logLines > 0 {
		if p, err := debugLogPath(); err == nil {
			d.LogExcerpt = tailLines(p, logLines)
		}
	}
	d.Version = Scrub(d.Version, home)
	d.OS = Scrub(d.OS, home)
	d.Terminal = Scrub(d.Terminal, home)
	d.Tool = Scrub(d.Tool, home)
	// Scrub the whole excerpt before cutting long lines: a cut through a
	// secret would leave a prefix too short for the patterns to see.
	d.LogExcerpt = truncateLines(Scrub(d.LogExcerpt, home), logLineCells)
	return d
}

func osDescription() string {
	switch runtime.GOOS {
	case "darwin":
		if v := commandOutput("sw_vers", "-productVersion"); v != "" {
			return "macOS " + v
		}
		return "macOS"
	case "linux":
		if name := osReleasePrettyName("/etc/os-release"); name != "" {
			return "Linux (" + name + ")"
		}
		return "Linux"
	default:
		return runtime.GOOS
	}
}

func osReleasePrettyName(path string) string {
	f, err := os.Open(path) // #nosec G304 -- fixed system path
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func terminalDescription() string {
	term := strings.TrimSpace(os.Getenv("TERM_PROGRAM"))
	if term != "" {
		if v := strings.TrimSpace(os.Getenv("TERM_PROGRAM_VERSION")); v != "" {
			term += " " + v
		}
	} else {
		term = strings.TrimSpace(os.Getenv("TERM"))
	}
	if v := commandOutput("tmux", "-V"); v != "" {
		term = joinNonEmpty(", ", term, v)
	}
	return term
}

// truncateLines cuts every line of s to cells display cells.
func truncateLines(s string, cells int) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = runewidth.Truncate(l, cells, "…")
	}
	return strings.Join(lines, "\n")
}

// tailLines returns the last n lines of the file at path, uncut (the
// caller scrubs, then cuts them). Missing or unreadable files give "".
func tailLines(path string, n int) string {
	f, err := os.Open(path) // #nosec G304 -- agent-deck's own log path
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := st.Size() - logTailBytes
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, logTailBytes))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // first line is likely partial
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		lines[i] = strings.ToValidUTF8(l, "?")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
