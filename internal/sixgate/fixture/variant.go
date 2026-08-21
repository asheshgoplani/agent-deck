package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Variants are how G3 turns one recorded case into many worlds.
//
// The corpus records what a real session looked like. A matrix has to ask what
// happens when there is nothing to inspect, when there is far too much, and when
// the session is not running at all — and none of those are recordable: nobody
// has a transcript of a session that was never started. So they are DERIVED from
// a recorded case by changing the world around it, and every derivation says out
// loud what it changed, in a note that lands in the row's transcript.
//
// The line this file will not cross is inventing transcript content. A variant
// may delete a memory file, add one, or change a session's status; it never
// edits the recorded conversation, because a fabricated transcript would produce
// numbers that are evidence of the fixture and of nothing else.

// Data-size axis values.
const (
	// DataTypical is the case exactly as recorded.
	DataTypical = "typical"
	// DataEmpty strips the world of everything the inspector can enumerate:
	// no memory files anywhere in the hierarchy, no skills. It is where empty
	// states live, and an empty state that renders a blank instead of a
	// sentence is the failure this framework exists to catch.
	DataEmpty = "empty"
	// DataEnormous is the pathological end: a deep ancestor chain each level of
	// which contributes memory files, plus one file larger than the reader's
	// own byte cap so the truncation path is exercised rather than assumed.
	DataEnormous = "enormous"
)

// Session-state axis values.
const (
	StateRunning = "running"
	StateWaiting = "waiting"
	StateIdle    = "idle"
	StateStopped = "stopped"
	StateError   = "error"
	// StateCompacted is a property of the recorded case, not of the instance:
	// a compacted session is one whose transcript was replayed after a
	// compaction, which only the `claude-resumed` case actually contains. The
	// row names the fixture; this value exists so the matrix can say which axis
	// coordinate that row is covering.
	StateCompacted = "compacted"
	// StateNeverStarted is a session agent-deck knows about that has no harness
	// session behind it. It is the state where the inspector has nothing to
	// read, and therefore the state most likely to render a blank.
	StateNeverStarted = "never-started"
)

// Variant selects one derived world.
type Variant struct {
	// SessionState is one of the State* constants. Empty means StateRunning.
	SessionState string
	// DataSize is one of the Data* constants. Empty means DataTypical.
	DataSize string
}

// enormousDepth is how many ancestor directories the enormous variant builds.
//
// Claude Code's memory hierarchy is the ancestor chain of the project directory,
// so the only honest way to produce a hundred memory files is to produce fifty
// directories that each hold two. Fifty levels lands well under the discovery
// walk's own 256-file ceiling, which is where the interesting behaviour is:
// close enough to the cap to matter, not so far past it that the row only ever
// tests the cap.
const enormousDepth = 50

// levelNames are the ancestor directory names, one character each.
//
// The length is load-bearing and was found by running the row. Claude Code
// addresses a project's transcripts by encoding the project's ABSOLUTE PATH
// into a single directory name, so a deep chain with readable level names
// ("l01/l02/…") produced a 300-character file name and the fixture died with
// "file name too long" before any frame was captured. One character per level
// keeps the encoded name inside the 255-byte limit every filesystem here
// enforces, at the cost of a chain that reads as gibberish — which is the right
// trade for a row whose entire subject is a pathological hierarchy.
const levelNames = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOP"

// enormousFileBytes is the size of the oversized project memory file. It is
// deliberately larger than ctxinspect's own 1 MiB per-file read cap, so the row
// exercises the truncation path rather than a merely large file.
const enormousFileBytes = 5 << 20

// FromCorpusVariant materializes a recorded case into root and then derives the
// world the variant describes.
//
// root must be a caller-owned temporary directory. Nothing outside it is read or
// written — including by the enormous variant, whose ancestor chain is built
// INSIDE root rather than by walking up from it.
func FromCorpusVariant(name, root string, v Variant) (*Setup, error) {
	if v.DataSize == "" {
		v.DataSize = DataTypical
	}
	if v.SessionState == "" {
		v.SessionState = StateRunning
	}

	dir := root
	if v.DataSize == DataEnormous {
		// The case is materialized at the bottom of a deep chain so that every
		// level above it is a directory this fixture owns and may put a memory
		// file in.
		if enormousDepth > len(levelNames) {
			return nil, fmt.Errorf("fixture: enormousDepth %d exceeds the %d single-character level names available", enormousDepth, len(levelNames))
		}
		var parts []string
		for i := 0; i < enormousDepth; i++ {
			parts = append(parts, string(levelNames[i]))
		}
		dir = filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	setup, err := FromCorpus(name, dir)
	if err != nil {
		return nil, err
	}
	setup.Name = name
	if err := applyDataSize(setup, root, v.DataSize); err != nil {
		return nil, err
	}
	if err := applySessionState(setup, v.SessionState); err != nil {
		return nil, err
	}
	return setup, nil
}

// applyDataSize reshapes what there is to inspect.
func applyDataSize(s *Setup, sandbox, size string) error {
	switch size {
	case DataTypical:
		return nil

	case DataEmpty:
		removed, err := stripInstructionFiles(s.Root)
		if err != nil {
			return err
		}
		skills, err := removeSkillDirs(s.Root)
		if err != nil {
			return err
		}
		s.Description += " — stripped to nothing: no memory file anywhere in the hierarchy, no skills"
		s.Notes = append(s.Notes, fmt.Sprintf(
			"data_size=empty: %d instruction file(s) and %d skill directory(ies) were deleted from the materialized world. "+
				"The transcript is untouched — what the session already said is still there; what the harness would load from disk is not.",
			removed, skills))
		return nil

	case DataEnormous:
		files, bytes, err := growInstructionHierarchy(s, sandbox)
		if err != nil {
			return err
		}
		s.Description += fmt.Sprintf(" — grown to %d memory files across %d ancestor levels", files, enormousDepth)
		s.Notes = append(s.Notes, fmt.Sprintf(
			"data_size=enormous: %d instruction files totalling %d bytes were written into the %d ancestor directories "+
				"BELOW the sandbox root, never above it. The last of them REPLACES the case's own project memory file and is "+
				"%d bytes, larger than the inspector's per-file read cap, so this row exercises truncation rather than assuming it.",
			files, bytes, enormousDepth, enormousFileBytes))
		return nil

	default:
		return fmt.Errorf("fixture: unknown data size %q", size)
	}
}

// instructionNames are the memory-file names any supported harness reads. The
// empty variant removes all of them regardless of adapter, because "empty" has
// to mean empty for whichever adapter the row runs.
var instructionNames = map[string]bool{
	"CLAUDE.md": true, "CLAUDE.local.md": true, "AGENTS.md": true,
}

// stripInstructionFiles deletes every memory file under root.
func stripInstructionFiles(root string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !instructionNames[d.Name()] {
			return nil
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// removeSkillDirs deletes every skills directory under root.
func removeSkillDirs(root string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || d.Name() != "skills" {
			return nil
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		n++
		return filepath.SkipDir
	})
	return n, err
}

// growInstructionHierarchy fills the ancestor chain with memory files.
//
// It refuses to write anywhere that is not under the sandbox. That check is not
// paranoia about this function: the ancestor chain of a project directory ends
// at "/", and a loop that walked it without a floor would put a CLAUDE.md in the
// developer's home directory and in the filesystem root.
func growInstructionHierarchy(s *Setup, sandbox string) (files, bytes int, err error) {
	project := s.Instances[0].ProjectPath
	if project == "" {
		return 0, 0, fmt.Errorf("fixture: the materialized case has no project path to grow a hierarchy under")
	}
	sandboxAbs, err := filepath.Abs(sandbox)
	if err != nil {
		return 0, 0, err
	}

	body := strings.Repeat("- a project rule that costs tokens just by existing\n", 160)
	dir := filepath.Dir(project)
	for {
		if !withinDir(sandboxAbs, dir) {
			break
		}
		for _, name := range []string{"CLAUDE.md", "CLAUDE.local.md"} {
			path := filepath.Join(dir, name)
			content := "# " + filepath.Base(dir) + " / " + name + "\n\n" + body
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // sandbox fixture
				return files, bytes, err
			}
			files++
			bytes += len(content)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// One file larger than the reader's cap, in the project itself, so the row
	// is not merely "many small files".
	big := filepath.Join(project, "CLAUDE.md")
	huge := []byte("# an unreasonably large project memory file\n\n" +
		strings.Repeat("x", enormousFileBytes))
	if err := os.WriteFile(big, huge, 0o644); err != nil { //nolint:gosec // sandbox fixture
		return files, bytes, err
	}
	files++
	bytes += len(huge)
	return files, bytes, nil
}

// withinDir reports whether path is dir or lives beneath it.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// applySessionState puts the fixture's session into the lifecycle state the row
// declares.
func applySessionState(s *Setup, state string) error {
	if len(s.Instances) == 0 {
		return fmt.Errorf("fixture: no instance to put into state %q", state)
	}
	inst := s.Instances[0]
	switch state {
	case StateRunning, StateCompacted:
		inst.Status = session.StatusRunning
	case StateWaiting:
		inst.Status = session.StatusWaiting
	case StateIdle:
		inst.Status = session.StatusIdle
	case StateStopped:
		inst.Status = session.StatusStopped
	case StateError:
		inst.Status = session.StatusError
	case StateNeverStarted:
		// A session agent-deck knows about that has never had a harness behind
		// it: no session id, so nothing resolves a transcript. The instance
		// stays in the list, which is the point — the row asks what the
		// inspector shows for a row it cannot read anything about.
		inst.Status = session.StatusIdle
		inst.ClaudeSessionID = ""
		inst.CodexSessionID = ""
		s.Notes = append(s.Notes,
			"session_state=never-started: the instance's harness session id was cleared, so no transcript resolves. "+
				"Nothing was faked to fill the gap; what the inspector shows here IS the answer this row asks for.")
	default:
		return fmt.Errorf("fixture: unknown session state %q", state)
	}
	if state != StateNeverStarted {
		s.Notes = append(s.Notes, "session_state="+state+": the instance's status was set to "+string(inst.Status))
	}
	return nil
}
