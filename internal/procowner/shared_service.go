package procowner

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Host-wide shared services.
//
// OpenCode 2.x runs one background service per host, `opencode serve
// --service`, started by whichever `opencode` process runs first and attached
// to by every other OpenCode TUI on the host. When that first process ran in an
// agent-deck pane, attribution correctly saw the service (and the MCP servers
// it spawns) as descendants of the pane. They are not the session's: other
// sessions depend on them, and they are meant to outlive the TUI that started
// them. A recorded member that is such a service, or runs under one, is
// therefore reported as shared: never counted as owned, never signalled.
//
// The command line is read only to EXCLUDE a member. It can turn "owned" into
// "shared" and nothing else, so it never widens what may be signalled, and a
// snapshot that cannot be read leaves every verdict exactly as before. The argv
// is matched in memory and never stored or printed: command lines can carry
// secrets.

// ProcCommand is one row of a command-line snapshot.
type ProcCommand struct {
	PPID int
	Args []string
}

// CommandReader is implemented by providers that can read live command lines.
// It is optional: a provider without it never classifies anything as shared.
type CommandReader interface {
	// Commands returns the parent pid and argv of every live process the
	// provider can read, keyed by pid.
	Commands() (map[int]ProcCommand, error)
}

// maxSharedServiceHops bounds the ancestry walk; a real tree is a few levels
// deep, and a cycle in a non-atomic snapshot must not spin.
const maxSharedServiceHops = 64

// isOpenCodeSharedService reports whether argv is the OpenCode 2.x host-wide
// service: an `opencode` executable (any path, or as a script argument to a
// shim) run with `serve` and `--service`. The 1.x per-TUI `opencode serve
// --port <n>` does not match.
func isOpenCodeSharedService(args []string) bool {
	for idx := 0; idx+1 < len(args); idx++ {
		if filepath.Base(args[idx]) != "opencode" || args[idx+1] != "serve" {
			continue
		}
		for _, arg := range args[idx+2:] {
			if arg == "--service" {
				return true
			}
		}
	}
	return false
}

// sharedServiceMembers returns, keyed by Member.Key, a detail line for every
// non-leader member whose live process is a host-wide shared service or runs
// under one. The walk goes up from the member and stops at the receipt's
// leader: what runs above the session never exempts the session's own tree.
func sharedServiceMembers(p Prober, r *Receipt, members []Member) map[string]string {
	reader, ok := p.(CommandReader)
	if !ok {
		return nil
	}
	var candidates []Member
	for _, m := range members {
		if m.Role != RoleLeader && m.Key() != r.Leader.Key() {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	table, err := reader.Commands()
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, m := range candidates {
		pid := m.PID
		for hop := 0; hop < maxSharedServiceHops; hop++ {
			if pid <= 1 || pid == r.Leader.PID {
				break
			}
			row, found := table[pid]
			if !found {
				break
			}
			if isOpenCodeSharedService(row.Args) {
				out[m.Key()] = sharedServiceDetail(m.PID, pid)
				break
			}
			pid = row.PPID
		}
	}
	return out
}

func sharedServiceDetail(memberPID, servicePID int) string {
	if memberPID == servicePID {
		return "host-wide OpenCode service (opencode serve --service), shared with other sessions; not owned, never signalled"
	}
	return fmt.Sprintf("runs under the host-wide OpenCode service (pid %d), shared with other sessions; not owned, never signalled", servicePID)
}

// parsePSCommandTable reads `ps -ww -Ao pid=,ppid=,command=`. The command
// column is split on whitespace, which is enough for the match above; rows it
// cannot parse are skipped.
func parsePSCommandTable(out string) map[int]ProcCommand {
	table := map[int]ProcCommand{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		table[pid] = ProcCommand{PPID: ppid, Args: fields[2:]}
	}
	return table
}

// parseProcCmdline splits /proc/<pid>/cmdline, whose arguments are
// NUL-terminated.
func parseProcCmdline(data []byte) []string {
	trimmed := strings.TrimRight(string(data), "\x00")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\x00")
}
