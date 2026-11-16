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
	native := inst.ClaudeSessionID
	if native == "" {
		native = inst.CodexSessionID
	}
	resolve := func() (string, error) {
		if h == "pi" {
			// Pi: the exact session document, or why there is none (no
			// file yet, several files and no saved identity, a remote).
			doc, err := piSessionDocument(inst)
			if err != nil {
				return "", fmt.Errorf("session %s has %w: %v", inst.Title, ErrNoNativeTranscript, err)
			}
			native = doc.SessionID
			return doc.Path, nil
		}
		if p := LiveTranscriptPath(inst, peers); p != "" {
			return p, nil
		}
		return "", fmt.Errorf("session %s has %w", inst.Title, ErrNoNativeTranscript)
	}
	path, err := resolve()
	if err != nil {
		return query.RowsSource{}, query.RowsSession{}, err
	}
	src := query.RowsSource{Harness: h, Path: path, Resolve: resolve}
	sess := query.RowsSession{ID: inst.ID, Harness: h, NativeID: native, Path: path, Title: inst.Title, Cwd: inst.EffectiveWorkingDir()}
	return src, sess, nil
}

// piSessionDocument is a local Pi session's exact session document: the
// saved PiSessionID/PiSessionPath pair, or the single validated file in the
// instance's own Pi directory. A remote session, no file, or several files
// without a saved identity is an error that says which.
func piSessionDocument(inst *Instance) (piSessionArtifact, error) {
	if !inst.TranscriptIsResolvableLocally() {
		return piSessionArtifact{}, fmt.Errorf("the Pi session is not on this machine")
	}
	return resolveExactPiSessionArtifact(inst)
}
