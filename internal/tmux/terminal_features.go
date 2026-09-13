package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

// agentDeckTerminalFeature is the single `terminal-features` entry agent-deck
// wants present on the tmux server: OSC 8 hyperlink tracking plus extended key
// reporting, for every terminal type. One entry is sufficient regardless of how
// many sessions, clients or agent-deck processes come and go — `terminal-features`
// is a SERVER option, shared by every session on the socket.
const agentDeckTerminalFeature = "*:hyperlinks:extkeys"

// agentDeckTerminalFeatureIndex is a shared insertion protocol, not a free slot
// selected from a snapshot. tmux accepts signed 32-bit array indices and stores
// them sparsely; the highest valid index sorts after ordinary configuration
// entries without allocating the intervening slots. Keep it stable across
// processes and releases. An existing foreign value, including empty, wins.
const agentDeckTerminalFeatureIndex = 2147483647

// terminalFeatureState is what the target tmux server currently reports for the
// server-wide `terminal-features` array.
//
// known is true only when values is the WHOLE array as tmux printed it on a
// clean exit — possibly empty. It is false whenever the read did not complete:
// no server behind the socket, a server wedged past tmuxPollTimeout, a tmux too
// old to know the option, or a client abandoned at cmd.WaitDelay with its
// stdout still held open. Nothing is ever written to a server in that state
// (see terminalFeatureArgsFor).
type terminalFeatureState struct {
	values []string
	known  bool
}

// readTerminalFeatures reads the server-wide `terminal-features` array from the
// tmux server selected by socketName ("" = the user's default server).
//
// `show-options -sv <array>` prints one value per line, and it keeps real
// newlines even with no client attached (the no-client sanitiser documented at
// tmuxFieldSep rewrites control bytes in FORMAT output, not in show-options).
func readTerminalFeatures(socketName string) terminalFeatureState {
	out, err := runBoundedOutput(socketName, "show-options", "-sv", "terminal-features")
	return classifyTerminalFeatures(out, err)
}

// classifyTerminalFeatures turns a `show-options -sv terminal-features` result
// into a state. It is separate from the exec so the cases that matter can be
// tested directly, the way parsePanePID's contract is.
//
// A clean exit with NO output is a real answer: the array is empty, which is
// what `set -s terminal-features ""` leaves behind. It must not be confused
// with "we could not read", because the two demand opposite actions — an empty
// array may receive a no-overwrite insertion, while an unreadable array is never written.
//
// Any error is "could not read", and that includes exec.ErrWaitDelay with
// bytes already in the buffer. Under the WaitDelay contract (see
// tmuxSubprocessWaitDelay) cmd.Wait gives up on the stdio goroutine while
// something still holds the pipe open, so whatever arrived is a PREFIX of the
// array, not the array: the copy was cut off, and this side cannot tell whether
// the last line it saw was the last value. Planning a rewrite from a prefix
// would `set` the array down to that prefix and drop every entry past it — a
// user's entries, not only ours. Partial output is therefore treated exactly
// like no output.
func classifyTerminalFeatures(out []byte, err error) terminalFeatureState {
	if err != nil {
		return terminalFeatureState{known: false}
	}
	body := strings.TrimRight(string(out), "\n")
	if body == "" {
		return terminalFeatureState{known: true} // empty array, no values
	}
	return terminalFeatureState{values: strings.Split(body, "\n"), known: true}
}

// terminalFeatureArgsFor only plans installation after a complete absence read.
// All initializers target the same sparse slot. tmux's indexed -o check and
// insertion are one server operation, including on tmux 3.4, whose unindexed
// array format cannot be used to test membership.
func terminalFeatureArgsFor(st terminalFeatureState) []string {
	if !st.known || countTerminalFeature(st.values) != 0 {
		return nil
	}
	return []string{"set-option", "-soq",
		fmt.Sprintf("terminal-features[%d]", agentDeckTerminalFeatureIndex), agentDeckTerminalFeature}
}

// ensureTerminalFeature applies a previously read state without replacing any
// occupant. A successful -o -q command can be a no-op, so only the subsequent
// complete read can establish observed presence. No collision fallback appends
// elsewhere, and foreign values are never included in diagnostics.
func ensureTerminalFeature(socketName string, st terminalFeatureState) {
	args := terminalFeatureArgsFor(st)
	if len(args) == 0 {
		return
	}
	if _, err := runBoundedOutput(socketName, args...); err != nil {
		statusLog.Warn("terminal_features_installation_failed", slog.Any("error", err))
		return
	}
	after, err := runBoundedOutput(socketName, "show-options", "-s", "terminal-features")
	if err != nil {
		statusLog.Warn("terminal_features_installation_unverified")
		return
	}
	if len(terminalFeatureIndices(after)) == 0 {
		statusLog.Warn("terminal_features_installation_deferred",
			slog.Int("index", agentDeckTerminalFeatureIndex),
			slog.String("reason", "owned_entry_absent_after_attempt"))
	}
}

// terminalFeatureIndices recognizes only exact owned entries at their actual
// indices in the complete indexed show-options output.
func terminalFeatureIndices(indexed []byte) []int {
	indices := make([]int, 0)
	seen := make(map[int]bool)
	for _, line := range strings.Split(string(indexed), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || value != agentDeckTerminalFeature ||
			!strings.HasPrefix(name, "terminal-features[") || !strings.HasSuffix(name, "]") {
			continue
		}
		digits := strings.TrimSuffix(strings.TrimPrefix(name, "terminal-features["), "]")
		index, err := strconv.ParseUint(digits, 10, 31)
		if err != nil || seen[int(index)] {
			continue
		}
		seen[int(index)] = true
		indices = append(indices, int(index))
	}
	sort.Ints(indices)
	return indices
}

// terminalFeatureCleanupScript rechecks both the keeper and each candidate
// immediately before deleting only that candidate index. A concurrent replacement
// survives, and removing the keeper prevents deletion of the last owned copy.
// Only validated decimal indices and fixed literals enter tmux command syntax.
// stdin keeps argv bounded even for thousands of duplicates; no user values are
// copied into the script and no whole-array assignment is generated.
func terminalFeatureCleanupScript(indexed []byte) string {
	indices := terminalFeatureIndices(indexed)
	if len(indices) < 2 {
		return ""
	}
	var script strings.Builder
	for _, index := range indices[1:] {
		fmt.Fprintf(&script,
			"if-shell -F '#{&&:#{==:#{terminal-features[%d]},%s},#{==:#{terminal-features[%d]},%s}}' 'set-option -suq terminal-features[%d]'\n",
			indices[0], agentDeckTerminalFeature, index, agentDeckTerminalFeature, index)
	}
	return script.String()
}

func runTerminalFeatureCleanup(socketName, script string) error {
	if script == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), tmuxPollTimeout)
	defer cancel()
	cmd := tmuxExecContext(ctx, socketName, "source-file", "-")
	cmd.Stdin = strings.NewReader(script)
	return cmd.Run()
}

// canCleanTerminalFeatures retains the original conservative cleanup gate:
// comma-bearing or blank values leave existing duplicates untouched. Cleanup no
// longer rewrites any array; even safe arrays use guarded indexed deletions.
func canCleanTerminalFeatures(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" || strings.Contains(v, ",") {
			return false
		}
	}
	return true
}

// configureTerminalFeatures deliberately reads the current server on every pass. A
// socket name may refer to a replacement server, so process-lifetime memoization
// cannot establish that its terminal features are configured.
func (s *Session) configureTerminalFeatures() {
	indexed, err := runBoundedOutput(s.SocketName, "show-options", "-s", "terminal-features")
	if err != nil {
		return // Partial indexed reads must not authorize any mutation.
	}
	indices := terminalFeatureIndices(indexed)
	if len(indices) > 1 {
		st := readTerminalFeatures(s.SocketName)
		if !st.known || !canCleanTerminalFeatures(st.values) {
			return
		}
		if err := runTerminalFeatureCleanup(s.SocketName, terminalFeatureCleanupScript(indexed)); err != nil {
			// Runtime guards can skip candidates; a plan is not a removal count.
			statusLog.Debug("terminal_features_cleanup_failed", slog.Any("error", err))
		}
	}
	if len(indices) == 0 {
		ensureTerminalFeature(s.SocketName, terminalFeatureState{known: true})
	}
}

// countTerminalFeature counts occurrences of agent-deck's entry in values.
func countTerminalFeature(values []string) int {
	n := 0
	for _, v := range values {
		if v == agentDeckTerminalFeature {
			n++
		}
	}
	return n
}
