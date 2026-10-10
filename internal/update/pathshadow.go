package update

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Stale PATH copies.
//
// An install replaces the binary agent-deck runs from (for example
// ~/.local/bin/agent-deck). An older copy earlier on PATH, such as a
// root-owned /usr/local/bin/agent-deck symlink to an old build, keeps every
// shell, hook and script that resolves `agent-deck` through PATH on the old
// release while the updated binary reports itself current. The scan here
// finds those copies so update, update --check, doctor and the restarted
// TUI can say so with the exact fix. It only reads and probes: a foreign
// copy is never removed or replaced.

// ShadowCopy is one agent-deck executable that comes before the updated
// binary on PATH and runs an older (or unreadable) version.
type ShadowCopy struct {
	// Path is the PATH entry as a shell finds it (dir/agent-deck).
	Path string `json:"path"`
	// Target is where Path resolves to when it is a symlink; empty
	// otherwise.
	Target string `json:"target,omitempty"`
	// Version is what `<path> version` reports; empty when it could not
	// be read.
	Version string `json:"version"`
	// Installed and InstalledPath describe the updated binary it shadows.
	Installed     string `json:"installed"`
	InstalledPath string `json:"installed_path"`
	// InstalledOnPath is false when the updated binary is not on PATH at
	// all, so shells only ever find copies like this one.
	InstalledOnPath bool `json:"installed_on_path"`
	// NeedsSudo is set when the current user cannot remove Path (its
	// directory is not writable, typically root-owned /usr/local/bin).
	NeedsSudo bool `json:"needs_sudo"`
	// Remove and Reorder are the two fixes, as commands to copy.
	Remove  string `json:"remove"`
	Reorder string `json:"reorder"`
}

// shadowBinaryName is the executable name looked up on PATH.
var shadowBinaryName = func() string {
	if runtime.GOOS == "windows" {
		return "agent-deck.exe"
	}
	return "agent-deck"
}()

// ShadowScan describes one PATH scan. Zero-value seams use the real
// implementations.
type ShadowScan struct {
	// Self is the updated binary (os.Executable).
	Self string
	// SelfVersion is the version of Self on disk; copies are compared
	// against it.
	SelfVersion string
	// PathEnv is the PATH to walk, in order.
	PathEnv string
	// Probe reads a binary's version (default ProbeBinaryVersion).
	Probe func(path string) (string, error)
	// Writable reports whether the current user may remove entries in
	// dir (default access(2) W_OK).
	Writable func(dir string) bool
}

// FindShadowingCopies scans this process's PATH for agent-deck copies that
// shadow the executable it runs from, compared against selfVersion (the
// version of that executable on disk). Any failure to locate the
// executable yields nil: the scan only ever adds a warning.
func FindShadowingCopies(selfVersion string) []ShadowCopy {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return nil
	}
	return ShadowScan{Self: exe, SelfVersion: selfVersion, PathEnv: os.Getenv("PATH")}.Find()
}

// Find walks PathEnv in order up to the entry that resolves to Self and
// returns every earlier agent-deck executable whose version is older than
// SelfVersion or cannot be read. Copies at or after Self, the same file
// reached through a symlink, and copies that are the same or a newer
// version are not reported. When Self is not on PATH at all, every older
// copy on PATH is reported, since shells run one of those instead.
func (s ShadowScan) Find() []ShadowCopy {
	self := strings.TrimSpace(s.Self)
	selfVersion := strings.TrimPrefix(strings.TrimSpace(s.SelfVersion), "v")
	if self == "" || selfVersion == "" {
		return nil
	}
	selfInfo, err := os.Stat(self)
	if err != nil {
		return nil
	}
	probe := s.Probe
	if probe == nil {
		probe = ProbeBinaryVersion
	}
	writable := s.Writable
	if writable == nil {
		writable = dirWritable
	}
	// Walk PATH once for every agent-deck executable, in shell lookup
	// order, and note where the updated binary itself sits.
	var found []string
	installedPath := self
	onPath := false
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(s.PathEnv) {
		// Empty and relative entries depend on the caller's working
		// directory; a shell lookup there says nothing about installs.
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(filepath.Clean(dir), shadowBinaryName)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		info, err := os.Stat(candidate) // follows symlinks
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		if os.SameFile(info, selfInfo) {
			// The PATH entry that reaches the updated binary is what the
			// user knows it by (os.Executable may have resolved a symlink).
			installedPath = candidate
			onPath = true
			break // everything after the updated binary is shadowed by it
		}
		found = append(found, candidate)
	}
	reorder := fmt.Sprintf("export PATH=%s:\"$PATH\"", ShellQuote([]string{filepath.Dir(installedPath)}))

	var out []ShadowCopy
	for _, path := range found {
		version, err := probe(path)
		version = strings.TrimPrefix(strings.TrimSpace(version), "v")
		if err == nil && version != "" && CompareVersions(version, selfVersion) >= 0 {
			continue
		}
		c := ShadowCopy{
			Path:            path,
			Version:         version,
			Installed:       selfVersion,
			InstalledPath:   installedPath,
			InstalledOnPath: onPath,
			NeedsSudo:       !writable(filepath.Dir(path)),
			Reorder:         reorder,
		}
		if l, err := os.Lstat(path); err == nil && l.Mode()&os.ModeSymlink != 0 {
			if target, err := filepath.EvalSymlinks(path); err == nil {
				c.Target = target
			}
		}
		c.Remove = ShellQuote([]string{"rm", path})
		if c.NeedsSudo {
			c.Remove = "sudo " + c.Remove
		}
		out = append(out, c)
	}
	return out
}

// Warning is the human text for one shadowing copy: what shadows what,
// both versions, and the two fixes.
func (c ShadowCopy) Warning() string {
	version := "unknown version"
	if c.Version != "" {
		version = "v" + c.Version
	}
	what := version
	if c.Target != "" {
		what += ", symlink to " + c.Target
	}
	if c.NeedsSudo {
		what += ", not removable without sudo"
	}
	var b strings.Builder
	if c.InstalledOnPath {
		fmt.Fprintf(&b, "Warning: %s (%s) comes before %s (v%s) on PATH, so shells, hooks and scripts run the older copy.\n", c.Path, what, c.InstalledPath, c.Installed)
	} else {
		fmt.Fprintf(&b, "Warning: %s (%s) is the agent-deck on PATH; the updated %s (v%s) is not on PATH, so shells, hooks and scripts run the older copy.\n", c.Path, what, c.InstalledPath, c.Installed)
	}
	removeNote := ""
	if c.Target != "" {
		removeNote = "  (removes the symlink only)"
	}
	fmt.Fprintf(&b, "  Fix: %s%s\n", c.Remove, removeNote)
	fmt.Fprintf(&b, "   or: put %s first on PATH in your shell profile: %s\n", filepath.Dir(c.InstalledPath), c.Reorder)
	b.WriteString("  agent-deck never removes it for you.\n")
	return b.String()
}

// LogAttrs are the structured fields of a path_shadowed log line.
func (c ShadowCopy) LogAttrs() []any {
	return []any{
		slog.String("path", c.Path),
		slog.String("target", c.Target),
		slog.String("version", c.Version),
		slog.String("installed", c.Installed),
		slog.String("installed_path", c.InstalledPath),
		slog.Bool("needs_sudo", c.NeedsSudo),
	}
}

// ShadowWarnings joins the warnings of copies; empty when there are none.
func ShadowWarnings(copies []ShadowCopy) string {
	var b strings.Builder
	for _, c := range copies {
		b.WriteString(c.Warning())
	}
	return b.String()
}
