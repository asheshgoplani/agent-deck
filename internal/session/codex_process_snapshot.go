package session

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// psSnapshotTimeout bounds the pass's one whole-table ps. Every worker of the
// pass waits on it, so a hung ps must not hold the whole sweep; on timeout
// the pass falls back per row as on any other ps failure. A var for tests.
var psSnapshotTimeout = 10 * time.Second

// processSnapshot is one read of the whole process table, shared by every
// row of a status pass. Without it, Codex discovery ran `ps -eo pid=,ppid=`
// once per pane-tree walk (up to twice per Codex row) and `ps -p <pid> -o
// args=` for every pid in the tree.
//
// The table is as old as the pass (read on first use, about 1 s at 500 rows),
// so a row probed late sees its pane as it was when the table was read: a
// Codex started, or exec'd into by the pane pid, after that read is not a
// candidate yet, exactly as if the row had been probed that much earlier
// (the next pass sees it). A pane created after the read is missing from the
// table and takes the per-row pgrep fallback.
type processSnapshot struct {
	children map[int][]int
	present  map[int]bool // every pid in children, built once for all walks
	parseErr error
	args     map[int]string
	err      error // ps failed: callers fall back as they do without a pass
}

// processes returns the pass's process table, read on first use.
func (p *StatusUpdatePass) processes() *processSnapshot {
	p.procsOnce.Do(func() {
		// -ww: whole argv whatever the output width, as `ps -p <pid> -o args=` reads it.
		ctx, cancel := context.WithTimeout(context.Background(), psSnapshotTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "ps", "-ww", "-eo", "pid=,ppid=,args=")
		cmd.WaitDelay = time.Second // a stray child holding stdout must not outlive the bound
		out, err := cmd.Output()
		if err != nil {
			p.procs.err = err
			return
		}
		table, args := splitPSArgs(out)
		p.procs.children, p.procs.parseErr = parsePSParentChildMap(table)
		p.procs.present = presentPIDs(p.procs.children)
		p.procs.args = args
	})
	return &p.procs
}

// processTree returns rootPIDs and their descendants from one ps snapshot:
// the pass's shared one, or a fresh one without a pass. A non-nil error means
// the snapshot is missing or incomplete.
func (p *StatusUpdatePass) processTree(rootPIDs []int) ([]int, error) {
	if p == nil {
		procTable, err := exec.Command("ps", "-eo", "pid=,ppid=").Output()
		if err != nil {
			return nil, err
		}
		return collectProcessTreePIDsFromTable(rootPIDs, procTable)
	}
	procs := p.processes()
	if procs.err != nil {
		return nil, procs.err
	}
	return walkProcessTreeIn(rootPIDs, procs.children, procs.present, procs.parseErr)
}

// isLikelyCodexProcess reads pid's argv from the pass's snapshot when it
// holds pid, and asks ps for that pid otherwise.
func (p *StatusUpdatePass) isLikelyCodexProcess(pid int) (bool, error) {
	if p != nil {
		if args, ok := p.processes().args[pid]; ok {
			return isCodexArgs(args), nil
		}
	}
	return isLikelyCodexProcessPID(pid)
}

// splitPSArgs splits `pid=,ppid=,args=` output into the pid/ppid table that
// parsePSParentChildMap reads (row for row, so its checks and errors stay the
// same: a short row is quoted as ps printed it, less padding) and pid -> argv with its own spacing. Lines are split by hand: an
// argv under -ww can outgrow bufio.Scanner's token limit.
func splitPSArgs(procTable []byte) ([]byte, map[int]string) {
	var table bytes.Buffer
	argsByPID := make(map[int]string)
	for line := range bytes.Lines(procTable) {
		pidField, rest, _ := strings.Cut(strings.TrimSpace(string(line)), " ")
		ppidField, args, _ := strings.Cut(strings.TrimSpace(rest), " ")
		table.WriteString(strings.TrimSpace(pidField+" "+ppidField) + "\n")
		if pid, err := strconv.Atoi(pidField); err == nil && pid > 0 {
			argsByPID[pid] = strings.TrimSpace(args)
		}
	}
	return table.Bytes(), argsByPID
}
