package procowner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file holds the pure parsing half of the BSD `ps` provider used on
// platforms without /proc. It carries no build tag on purpose: the parser is
// where the bugs live, and it must be testable on the machine that runs CI.

// psFieldOrder is the -o format the darwin prober asks for. lstart is last
// because it is the only field containing spaces.
const psFieldOrder = "pid=,ppid=,pgid=,uid=,state=,lstart="

// parsePSLine reads one `ps -o pid=,ppid=,pgid=,uid=,state=,lstart=` row.
//
// lstart is a human date with embedded spaces ("Sat Aug 16 17:21:38 2026"), so
// the four leading integers are consumed positionally and everything after them
// is the start identity verbatim. Normalising internal whitespace matters: ps
// pads day-of-month to two columns, so the same process can print with one or
// two spaces depending on the day, and an identity that changes shape over time
// would read as a stranger.
func parsePSLine(line string) (ProcInfo, error) {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return ProcInfo{}, fmt.Errorf("%w: ps row %q has %d fields, need at least 6",
			ErrUnreadable, line, len(fields))
	}
	nums := make([]int, 4)
	for i := 0; i < 4; i++ {
		v, err := strconv.Atoi(fields[i])
		if err != nil {
			return ProcInfo{}, fmt.Errorf("%w: unparseable ps field %q", ErrUnreadable, fields[i])
		}
		nums[i] = v
	}
	state := fields[4]
	start := strings.Join(fields[5:], " ")
	if strings.TrimSpace(start) == "" {
		return ProcInfo{}, fmt.Errorf("%w: ps row %q has no start time", ErrUnreadable, line)
	}
	return ProcInfo{
		PID:     nums[0],
		PPID:    nums[1],
		PGID:    nums[2],
		UID:     nums[3],
		State:   state,
		StartID: start,
	}, nil
}

// parsePSTable reads a whole `ps -A` listing, skipping rows it cannot parse
// rather than failing the scan: one unreadable row must not cost us the
// attribution of every other process.
func parsePSTable(out string) []ProcInfo {
	var infos []ProcInfo
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		info, err := parsePSLine(line)
		if err != nil {
			continue
		}
		infos = append(infos, info)
	}
	return infos
}

// descendantsFromTable follows parent links down from root over a single
// snapshot of the process table.
func descendantsFromTable(root ProcInfo, table []ProcInfo) []ProcInfo {
	byParent := map[int][]ProcInfo{}
	for _, info := range table {
		if info.PID <= 1 || info.IsZombie() {
			continue
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
	return out
}

// bootTimePattern extracts the seconds field from `sysctl -n kern.boottime`,
// whose output looks like `{ sec = 1755300000, usec = 123456 } Sat Aug 16 ...`.
var bootTimePattern = regexp.MustCompile(`sec\s*=\s*(\d+)`)

// parseKernBootTime turns kern.boottime into a boot identifier.
func parseKernBootTime(out string) (string, error) {
	match := bootTimePattern.FindStringSubmatch(out)
	if len(match) != 2 {
		return "", fmt.Errorf("%w: unparseable kern.boottime %q", ErrUnreadable, strings.TrimSpace(out))
	}
	return match[1], nil
}

// lstartLayout is the BSD `ps -o lstart=` format ("Sat Aug 16 17:21:38 2026").
const lstartLayout = "Mon Jan 2 15:04:05 2006"

// parseLstart parses a `ps` lstart value. Fields are re-joined with single
// spaces by parsePSLine, so the layout carries no column padding.
func parseLstart(value string) (time.Time, error) {
	t, err := time.Parse(lstartLayout, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: unparseable start identity %q", ErrUnreadable, value)
	}
	return t, nil
}
