//go:build linux

package procowner

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ProviderLinuxProc is the provider name recorded in receipts written here.
const ProviderLinuxProc = "linux_proc"

// procRoot is a variable so tests can point the reader at a fixture tree
// instead of the live /proc. Production never changes it.
var procRoot = "/proc"

// LinuxProber reads process start identity from /proc.
//
// The start identity is field 22 of /proc/<pid>/stat: the process start time in
// clock ticks since boot. Together with the PID and the boot id it is a
// non-reusable identity — the kernel cannot hand out the same (boot, pid,
// start-tick) triple twice.
type LinuxProber struct{}

// NewProber returns the platform's start-identity provider.
func NewProber() Prober { return LinuxProber{} }

// Name implements Prober.
func (LinuxProber) Name() string { return ProviderLinuxProc }

// BootID implements Prober.
func (LinuxProber) BootID() (string, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return "", fmt.Errorf("%w: read boot id: %v", ErrUnreadable, err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", fmt.Errorf("%w: boot id is empty", ErrUnreadable)
	}
	return id, nil
}

// Inspect implements Prober.
func (p LinuxProber) Inspect(pid int) (ProcInfo, error) {
	if pid <= 0 {
		return ProcInfo{}, fmt.Errorf("%w: pid %d", ErrNoProcess, pid)
	}
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	// Read stat first: it is the single read that makes pid, ppid, pgid and
	// start time one coherent observation of one process. Reading them from
	// separate files would let a recycle slip between them.
	data, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		if os.IsNotExist(err) {
			return ProcInfo{}, fmt.Errorf("%w: pid %d", ErrNoProcess, pid)
		}
		return ProcInfo{}, fmt.Errorf("%w: read stat for pid %d: %v", ErrUnreadable, pid, err)
	}
	info, err := parseProcStat(data)
	if err != nil {
		return ProcInfo{}, err
	}
	if info.IsZombie() {
		// Exited, awaiting wait() by its parent. It cannot run code and it
		// cannot be killed again; reporting it alive would make a successful
		// reap look like a failed one.
		return ProcInfo{}, fmt.Errorf("%w: pid %d is a zombie", ErrNoProcess, pid)
	}
	if info.PID != pid {
		// /proc/<pid>/stat naming its own pid as something else means the entry
		// was replaced under us. Refuse to interpret it.
		return ProcInfo{}, fmt.Errorf("%w: pid %d reported itself as %d", ErrUnreadable, pid, info.PID)
	}
	uid, err := ownerUID(dir)
	if err != nil {
		return ProcInfo{}, err
	}
	info.UID = uid
	return info, nil
}

// ownerUID returns the uid owning a /proc/<pid> directory, which is the
// process's real uid.
func ownerUID(dir string) (int, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("%w: %s", ErrNoProcess, dir)
		}
		return 0, fmt.Errorf("%w: stat %s: %v", ErrUnreadable, dir, err)
	}
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("%w: no uid for %s", ErrUnreadable, dir)
	}
	return int(sys.Uid), nil
}

// parseProcStat extracts pid, ppid, pgid and start ticks from a
// /proc/<pid>/stat line.
//
// The comm field is parenthesised and may itself contain spaces AND closing
// parens (a process can name itself "(evil) (x"), so the split point is the
// LAST ')' in the line, not the first. Getting this wrong shifts every
// subsequent field — which would silently compare the wrong number as a start
// time, the one failure mode this whole package exists to prevent.
func parseProcStat(data []byte) (ProcInfo, error) {
	line := string(data)
	open := strings.IndexByte(line, '(')
	closeIdx := strings.LastIndexByte(line, ')')
	if open < 0 || closeIdx < open {
		return ProcInfo{}, fmt.Errorf("%w: malformed stat line", ErrUnreadable)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line[:open]))
	if err != nil {
		return ProcInfo{}, fmt.Errorf("%w: unparseable pid in stat line", ErrUnreadable)
	}
	comm := line[open+1 : closeIdx]
	// After the comm the remaining fields are space-separated, starting at
	// field 3 (state). Field N of the stat(5) layout is rest[N-3].
	rest := strings.Fields(line[closeIdx+1:])
	const (
		stateIdx     = 0  // field 3
		ppidIdx      = 1  // field 4
		pgidIdx      = 2  // field 5
		starttimeIdx = 19 // field 22
	)
	if len(rest) <= starttimeIdx {
		return ProcInfo{}, fmt.Errorf("%w: stat line has %d fields after comm, need %d",
			ErrUnreadable, len(rest), starttimeIdx+1)
	}
	ppid, err := strconv.Atoi(rest[ppidIdx])
	if err != nil {
		return ProcInfo{}, fmt.Errorf("%w: unparseable ppid", ErrUnreadable)
	}
	pgid, err := strconv.Atoi(rest[pgidIdx])
	if err != nil {
		return ProcInfo{}, fmt.Errorf("%w: unparseable pgid", ErrUnreadable)
	}
	start := rest[starttimeIdx]
	if _, err := strconv.ParseUint(start, 10, 64); err != nil {
		return ProcInfo{}, fmt.Errorf("%w: unparseable start time %q", ErrUnreadable, start)
	}
	return ProcInfo{
		PID:     pid,
		PPID:    ppid,
		PGID:    pgid,
		Comm:    comm,
		State:   rest[stateIdx],
		StartID: start,
	}, nil
}

// Descendants implements Prober by walking /proc once and following parent
// links down from root.
//
// One pass over /proc is deliberate: every (pid, ppid, start) triple comes from
// a single read of that process's stat file, so a pid cannot be recorded with
// another process's start identity. A process that exits mid-scan simply drops
// out — a missed descendant is a process we never claim to own, which is the
// safe direction to fail in.
func (p LinuxProber) Descendants(root ProcInfo) ([]ProcInfo, error) {
	if root.PID <= 0 {
		return nil, fmt.Errorf("%w: pid %d", ErrNoProcess, root.PID)
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrUnreadable, procRoot, err)
	}
	byParent := map[int][]ProcInfo{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 1 {
			continue
		}
		info, inspectErr := p.Inspect(pid)
		if inspectErr != nil {
			continue // vanished or unreadable: never guessed at
		}
		byParent[info.PPID] = append(byParent[info.PPID], info)
	}

	var out []ProcInfo
	seen := map[int]bool{root.PID: true}
	queue := []int{root.PID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range byParent[parent] {
			if seen[child.PID] {
				continue
			}
			seen[child.PID] = true
			out = append(out, child)
			queue = append(queue, child.PID)
		}
	}
	return out, nil
}

// CompareStart implements StartComparer. Linux start identities are clock ticks
// since boot, so they order numerically within a boot — and a receipt only ever
// compares identities from one boot, because BootID scopes it.
func (LinuxProber) CompareStart(a, b string) (int, error) {
	av, err := strconv.ParseUint(a, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: unparseable start identity %q", ErrUnreadable, a)
	}
	bv, err := strconv.ParseUint(b, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: unparseable start identity %q", ErrUnreadable, b)
	}
	switch {
	case av < bv:
		return -1, nil
	case av > bv:
		return 1, nil
	default:
		return 0, nil
	}
}
