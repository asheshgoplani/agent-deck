package session

import (
	"errors"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

// ErrNoNativeTranscript reports a session whose harness the rows reader
// streams directly, but whose transcript does not exist on this machine yet.
var ErrNoNativeTranscript = errors.New("no native transcript on this machine yet")

// RowsHarness maps a session tool to the native format the recall rows
// reader parses.
func RowsHarness(tool string) string {
	switch {
	case IsClaudeCompatible(tool):
		return "claude"
	case IsCodexCompatible(tool):
		return "codex"
	}
	return tool
}

// LiveRowsSource resolves a deck session to the native transcript it writes
// now, for query.ReadRows. peers are the profile's other sessions. A harness
// the rows reader does not stream directly returns query.ErrRowsUnsupported;
// a missing transcript returns ErrNoNativeTranscript. The CLI's
// `recall timeline` and the web timeline route share it.
func LiveRowsSource(inst *Instance, peers []*Instance) (query.RowsSource, query.RowsSession, error) {
	h := RowsHarness(inst.Tool)
	if !query.SupportsDirectRows(h) {
		return query.RowsSource{}, query.RowsSession{}, fmt.Errorf("%w: %s", query.ErrRowsUnsupported, inst.Tool)
	}
	resolve := func() (string, error) {
		if p := LiveTranscriptPath(inst, peers); p != "" {
			return p, nil
		}
		return "", fmt.Errorf("session %s has %w", inst.Title, ErrNoNativeTranscript)
	}
	path, err := resolve()
	if err != nil {
		return query.RowsSource{}, query.RowsSession{}, err
	}
	native := inst.ClaudeSessionID
	if native == "" {
		native = inst.CodexSessionID
	}
	if native == "" {
		native = inst.PiSessionID
	}
	src := query.RowsSource{Harness: h, Path: path, Resolve: resolve}
	sess := query.RowsSession{ID: inst.ID, Harness: h, NativeID: native, Path: path, Title: inst.Title, Cwd: inst.EffectiveWorkingDir()}
	return src, sess, nil
}
