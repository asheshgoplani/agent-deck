package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContextExportOptions bounds a read-only context export. SessionID may be
// supplied for Pi; when empty, ExportContext reads the exact ID from Pi's
// instance-scoped session header. A zero MaxBytes uses DefaultHandoffMaxChars
// (a byte budget, despite the historical field name).
type ContextExportOptions struct {
	SessionID string
	MaxBytes  int
}

// ContextSourceIdentity is the identity that an eventual switch journal must
// retain. It is deliberately independent of the artifact path: paths can move,
// but the tool/session/account identity must not silently change.
type ContextSourceIdentity struct {
	InstanceID  string `json:"instance_id,omitempty"`
	Tool        string `json:"tool"`
	SessionID   string `json:"session_id"`
	Account     string `json:"account,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
	WorkingDir  string `json:"working_dir,omitempty"`
	Title       string `json:"title,omitempty"`
	GroupPath   string `json:"group_path,omitempty"`
}

// ContextArtifact describes the exact source bytes and the payload handed to a
// target harness. SourceSHA256 is always over the complete source artifact;
// PayloadSHA256 is over Payload and therefore makes truncation observable.
type ContextArtifact struct {
	Path          string `json:"path"`
	Format        string `json:"format"`
	SourceBytes   int64  `json:"source_bytes"`
	SourceSHA256  string `json:"source_sha256"`
	PayloadBytes  int    `json:"payload_bytes"`
	PayloadSHA256 string `json:"payload_sha256"`
	Truncated     bool   `json:"truncated"`
}

// ContextManifest is the common, serializable contract for Claude, Codex and
// Pi read-only exports. A native export retains the source artifact format;
// callers performing a cross-harness handoff must label it transferred rather
// than presenting it as native resume.
type ContextManifest struct {
	Version        int                   `json:"version"`
	Source         ContextSourceIdentity `json:"source"`
	TargetTool     string                `json:"target_tool,omitempty"`
	Artifact       ContextArtifact       `json:"artifact"`
	Continuity     string                `json:"continuity"` // native or transferred
	LossDisclosure []string              `json:"loss_disclosure,omitempty"`
}

// ContextExport contains no destination and performs no writes. Payload is a
// bounded copy of the identified source artifact; the source remains untouched.
type ContextExport struct {
	Manifest ContextManifest `json:"manifest"`
	Payload  []byte          `json:"-"`
	// portable marks an already-projected payload produced by the dedicated
	// cross-harness adapter. It is intentionally private so native callers
	// cannot relabel a raw export without projection/preflight.
	portable bool
}

// MarkContextTransferred returns a copy labelled for cross-harness delivery.
// It never changes the source export or writes a target artifact. The explicit
// label prevents a prompt/context handoff from being represented as native
// resume continuity.
func MarkContextTransferred(export *ContextExport, targetTool string) (*ContextExport, error) {
	if export == nil {
		return nil, fmt.Errorf("context export is nil")
	}
	targetTool = strings.TrimSpace(targetTool)
	if targetTool == "" {
		return nil, fmt.Errorf("target tool is empty")
	}
	copyExport := *export
	copyExport.Manifest = export.Manifest
	copyExport.Manifest.TargetTool = targetTool
	copyExport.Manifest.Continuity = "transferred"
	copyExport.Manifest.LossDisclosure = append([]string(nil), export.Manifest.LossDisclosure...)
	copyExport.Manifest.LossDisclosure = append(copyExport.Manifest.LossDisclosure,
		fmt.Sprintf("cross-harness transfer to %s is context-only; native session state is not resumed", targetTool))
	copyExport.Payload = append([]byte(nil), export.Payload...)
	return &copyExport, nil
}

// ExportContext dispatches to an exact-ID, read-only adapter. It never scans
// for the newest file: an absent or colliding ID is an error.
func ExportContext(inst *Instance, opts ContextExportOptions) (*ContextExport, error) {
	return exportContext(inst, opts, false)
}

// ExportPortableContext is the cross-harness-only export adapter. Unlike the
// native ExportContext contract, it projects readable conversation turns before
// applying its budget and refuses an empty projection. It must be used before
// target creation or lifecycle work, never to replace native exports.
func ExportPortableContext(inst *Instance, opts ContextExportOptions) (*ContextExport, error) {
	return exportContext(inst, opts, true)
}

func exportContext(inst *Instance, opts ContextExportOptions, portable bool) (*ContextExport, error) {
	if inst == nil {
		return nil, fmt.Errorf("session is nil")
	}
	switch {
	case IsClaudeCompatible(inst.Tool):
		return exportClaudeContext(inst, opts.MaxBytes, portable)
	case IsCodexCompatible(inst.Tool):
		return exportCodexContext(inst, opts.MaxBytes, portable)
	case inst.Tool == "pi":
		sessionID := strings.TrimSpace(opts.SessionID)
		if sessionID == "" {
			var err error
			sessionID, err = exactPiSessionID(inst)
			if err != nil {
				return nil, err
			}
		}
		return exportPiContext(inst, sessionID, opts.MaxBytes, portable)
	default:
		return nil, fmt.Errorf("context export is unsupported for tool %q", inst.Tool)
	}
}

// ExportClaudeContext exports the transcript named by inst.ClaudeSessionID.
// Configured account directories are searched only for that exact ID; no mtime
// or newest-file fallback is permitted.
func ExportClaudeContext(inst *Instance, maxBytes int) (*ContextExport, error) {
	return exportClaudeContext(inst, maxBytes, false)
}

func exportClaudeContext(inst *Instance, maxBytes int, portable bool) (*ContextExport, error) {
	if inst == nil || !IsClaudeCompatible(inst.Tool) {
		return nil, fmt.Errorf("Claude context export requires a Claude session")
	}
	if err := validateExactSessionID(inst.ClaudeSessionID); err != nil {
		return nil, fmt.Errorf("invalid Claude session ID: %w", err)
	}
	if !inst.TranscriptIsResolvableLocally() {
		return nil, fmt.Errorf("session %q is remote; transcript is not locally readable", inst.Title)
	}
	path, err := canonicalClaudeExactTranscriptPath(inst)
	if err != nil {
		return nil, err
	}
	path, err = uniqueRegularArtifact([]string{path}, inst.ClaudeSessionID+".jsonl")
	if err != nil {
		return nil, err
	}
	return exportContextArtifact(path, ContextSourceIdentity{
		InstanceID: inst.ID, Tool: inst.Tool, SessionID: inst.ClaudeSessionID, Account: inst.Account, ProjectPath: inst.ProjectPath, WorkingDir: inst.EffectiveWorkingDir(), Title: inst.Title, GroupPath: inst.GroupPath,
	}, "claude-jsonl", "native", maxBytes, inst.ClaudeSessionID, portable)
}

// ExportCodexContext exports the rollout named by inst.CodexSessionID. More
// than one exact-ID match is a collision and is refused rather than guessed.
func ExportCodexContext(inst *Instance, maxBytes int) (*ContextExport, error) {
	return exportCodexContext(inst, maxBytes, false)
}

func exportCodexContext(inst *Instance, maxBytes int, portable bool) (*ContextExport, error) {
	if inst == nil || !IsCodexCompatible(inst.Tool) {
		return nil, fmt.Errorf("Codex context export requires a Codex session")
	}
	if err := validateExactSessionID(inst.CodexSessionID); err != nil {
		return nil, fmt.Errorf("invalid Codex session ID: %w", err)
	}
	if !inst.TranscriptIsResolvableLocally() {
		return nil, fmt.Errorf("session %q is remote; rollout is not locally readable", inst.Title)
	}
	paths, err := exactCodexRolloutMatches(inst.CodexSessionID, inst.getCodexHomeDir())
	if err != nil {
		return nil, err
	}
	path, err := uniqueRegularArtifact(paths, "rollout for "+inst.CodexSessionID)
	if err != nil {
		return nil, err
	}
	return exportContextArtifact(path, ContextSourceIdentity{
		InstanceID: inst.ID, Tool: inst.Tool, SessionID: inst.CodexSessionID, Account: inst.Account, ProjectPath: inst.ProjectPath, WorkingDir: inst.EffectiveWorkingDir(), Title: inst.Title, GroupPath: inst.GroupPath,
	}, "codex-rollout-jsonl", "native", maxBytes, inst.CodexSessionID, portable)
}

// ExportPiContext exports the persisted exact Pi document, or one uniquely
// validated document in inst.ID's private directory, and requires its header
// ID to equal sessionID. Pi's branch-aware JSONL format is retained as source
// bytes; the adapter does not select a newest file or infer identity from mtime.
func ExportPiContext(inst *Instance, sessionID string, maxBytes int) (*ContextExport, error) {
	return exportPiContext(inst, sessionID, maxBytes, false)
}

func exportPiContext(inst *Instance, sessionID string, maxBytes int, portable bool) (*ContextExport, error) {
	if inst == nil || inst.Tool != "pi" {
		return nil, fmt.Errorf("Pi context export requires a Pi session")
	}
	sessionID = strings.TrimSpace(sessionID)
	if err := validateExactSessionID(sessionID); err != nil {
		return nil, fmt.Errorf("invalid Pi session ID: %w", err)
	}
	if !inst.TranscriptIsResolvableLocally() {
		return nil, fmt.Errorf("session %q is remote; transcript is not locally readable", inst.Title)
	}
	artifact, err := resolveExactPiSessionArtifact(inst)
	if err != nil {
		return nil, err
	}
	if artifact.SessionID != sessionID {
		return nil, fmt.Errorf("Pi session identity mismatch: requested %q, file contains %q", sessionID, artifact.SessionID)
	}
	path := artifact.Path
	return exportContextArtifact(path, ContextSourceIdentity{
		InstanceID: inst.ID, Tool: inst.Tool, SessionID: sessionID, Account: inst.Account, ProjectPath: inst.ProjectPath, WorkingDir: inst.EffectiveWorkingDir(), Title: inst.Title, GroupPath: inst.GroupPath,
	}, "pi-session-jsonl", "native", maxBytes, sessionID, portable)
}

// canonicalClaudeExactTranscriptPath returns the one account-bound location
// which may prove a Claude session's source bytes. In particular, an exact
// native ID is not globally unique across accounts: a prior migration can
// legitimately leave the same ID in both homes. Looking in every configured
// home would therefore make retries ambiguous, or worse, let a missing source
// account borrow another account's transcript.
//
// Claude keys its project directory from the actual launch cwd. For multi-repo
// sessions that is EffectiveWorkingDir, not the display ProjectPath. Do not
// fall back to ProjectPath when they differ: that would turn a neighbouring
// project's exact-ID artifact into source evidence. All existing path
// components are also required to be real directories/files; a symlinked
// account/project/artifact path is refused rather than followed.
func canonicalClaudeExactTranscriptPath(inst *Instance) (string, error) {
	if inst == nil {
		return "", fmt.Errorf("Claude source instance is nil")
	}
	if err := validateExactSessionID(inst.ClaudeSessionID); err != nil {
		return "", fmt.Errorf("invalid Claude source identity: %w", err)
	}
	dir := strings.TrimSpace(GetClaudeConfigDirForInstance(inst))
	if inst.Account != "" {
		cfg, err := LoadUserConfig()
		if err != nil {
			return "", fmt.Errorf("load source Claude account %q: %w", inst.Account, err)
		}
		accountDir := ""
		if cfg != nil {
			accountDir = strings.TrimSpace(cfg.GetProfileClaudeConfigDir(inst.Account))
		}
		if accountDir == "" {
			return "", fmt.Errorf("source Claude account %q has no configured config_dir", inst.Account)
		}
		dir = accountDir
	}
	dir = ExpandPath(dir)
	if dir == "" {
		return "", fmt.Errorf("source Claude config dir is empty")
	}
	workingDir := strings.TrimSpace(inst.EffectiveWorkingDir())
	if workingDir == "" {
		return "", fmt.Errorf("source Claude effective working directory is empty")
	}
	encoded := ConvertToClaudeDirName(workingDir)
	if encoded == "" {
		encoded = "-"
	}
	path := filepath.Join(dir, "projects", encoded, inst.ClaudeSessionID+".jsonl")
	if err := ensureNoSymlinkPath(path); err != nil {
		return "", fmt.Errorf("unsafe exact Claude source path: %w", err)
	}
	return path, nil
}

// claudeExactTranscriptCandidates is retained for preview callers. It exposes
// at most the canonical account-bound path; it never searches another account
// or the raw ProjectPath as a fallback.
func claudeExactTranscriptCandidates(inst *Instance) []string {
	path, err := canonicalClaudeExactTranscriptPath(inst)
	if err != nil {
		return nil
	}
	return []string{path}
}

func validateExactSessionID(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session ID is empty")
	}
	if filepath.Base(sessionID) != sessionID || strings.ContainsRune(sessionID, '\x00') || strings.ContainsAny(sessionID, "*?[]\\") {
		return fmt.Errorf("session ID must be a single literal path component")
	}
	return nil
}

func exactCodexRolloutMatches(sessionID, home string) ([]string, error) {
	pattern := filepath.Join(ExpandPath(home), "sessions", "*", "*", "*", "rollout-*-"+sessionID+".jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("locate Codex rollout: %w", err)
	}
	return matches, nil
}

func uniqueRegularArtifact(paths []string, label string) (string, error) {
	var found []string
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("inspect %s: %w", label, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink artifact for %s: %s", label, path)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("artifact for %s is not a regular file: %s", label, path)
		}
		found = append(found, path)
	}
	if len(found) == 0 {
		return "", fmt.Errorf("no exact context artifact found for %s", label)
	}
	if len(found) > 1 {
		return "", fmt.Errorf("ambiguous exact context artifact for %s (%d matches)", label, len(found))
	}
	return found[0], nil
}

type piSessionArtifact struct {
	Path      string
	SessionID string
}

// resolveExactPiSessionArtifact resolves only a persisted Pi identity/path or
// one uniquely validated JSONL in this instance's private Pi directory. Pi
// v0.85+ generates timestamped filenames, so session.jsonl is legacy-only and
// must never be treated as the current filename by default.
func resolveExactPiSessionArtifact(inst *Instance) (piSessionArtifact, error) {
	if inst == nil || inst.Tool != "pi" {
		return piSessionArtifact{}, fmt.Errorf("Pi session identity requires a Pi instance")
	}
	if !validSwitchIdentity(inst.ID) {
		return piSessionArtifact{}, fmt.Errorf("Pi instance has unsafe identity %q", inst.ID)
	}
	dir, err := piInstanceSessionDir(inst.ID)
	if err != nil {
		return piSessionArtifact{}, err
	}
	persistedID, persistedPath := strings.TrimSpace(inst.PiSessionID), strings.TrimSpace(inst.PiSessionPath)
	if persistedID != "" || persistedPath != "" {
		if persistedID == "" || persistedPath == "" {
			return piSessionArtifact{}, fmt.Errorf("Pi persisted native identity/path is incomplete")
		}
		if err := validateExactSessionID(persistedID); err != nil {
			return piSessionArtifact{}, fmt.Errorf("invalid persisted Pi session identity: %w", err)
		}
		if !piPathInInstanceDir(persistedPath, dir) {
			return piSessionArtifact{}, fmt.Errorf("persisted Pi session path is outside its instance directory")
		}
		id, err := readValidatedPiSessionHeader(persistedPath, inst.EffectiveWorkingDir(), false)
		if err != nil {
			return piSessionArtifact{}, err
		}
		if id != persistedID {
			return piSessionArtifact{}, fmt.Errorf("persisted Pi session identity mismatch: expected %q, file contains %q", persistedID, id)
		}
		return piSessionArtifact{Path: persistedPath, SessionID: id}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return piSessionArtifact{}, fmt.Errorf("read Pi instance session directory: %w", err)
	}
	var candidates []piSessionArtifact
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return piSessionArtifact{}, fmt.Errorf("inspect Pi session artifact: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return piSessionArtifact{}, fmt.Errorf("Pi session artifact is not a regular non-symlink file: %s", path)
		}
		// A header without cwd predates Pi's timestamped session documents. Keep
		// the historical session.jsonl fixture/installation shape compatible,
		// but never apply that exception to generated filenames.
		id, headerErr := readValidatedPiSessionHeader(path, inst.EffectiveWorkingDir(), entry.Name() == "session.jsonl")
		if headerErr != nil {
			return piSessionArtifact{}, headerErr
		}
		candidates = append(candidates, piSessionArtifact{Path: path, SessionID: id})
	}
	if len(candidates) == 0 {
		return piSessionArtifact{}, fmt.Errorf("no exact Pi context artifact found for instance %q", inst.ID)
	}
	if len(candidates) > 1 {
		return piSessionArtifact{}, fmt.Errorf("ambiguous Pi context artifacts for instance %q (%d matches)", inst.ID, len(candidates))
	}
	return candidates[0], nil
}

func piInstanceSessionDir(instanceID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve Pi home: %w", err)
	}
	dir := filepath.Join(home, ".pi", "agent-deck", instanceID)
	if err := ensureNoSymlinkPath(dir); err != nil {
		return "", fmt.Errorf("unsafe Pi instance session directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("Pi instance session directory is not a real directory")
	}
	return dir, nil
}

func piPathInInstanceDir(path, dir string) bool {
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".jsonl" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func readValidatedPiSessionHeader(path, expectedCWD string, allowLegacyNoCWD bool) (string, error) {
	if err := ensureNoSymlinkPath(path); err != nil {
		return "", fmt.Errorf("unsafe Pi session path: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect Pi session: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("Pi session is not a regular non-symlink file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read Pi session: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	// Pi headers are small; this limit avoids treating an arbitrary giant line
	// as a valid header while retaining normal JSONL compatibility.
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var header struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Cwd  string `json:"cwd"`
		}
		if err := json.Unmarshal([]byte(line), &header); err != nil || header.Type != "session" || strings.TrimSpace(header.ID) == "" {
			return "", fmt.Errorf("Pi session %s has no valid session header", path)
		}
		if err := validateExactSessionID(header.ID); err != nil {
			return "", fmt.Errorf("Pi session %s has invalid native session identity: %w", path, err)
		}
		if header.Cwd == "" {
			if !allowLegacyNoCWD {
				return "", fmt.Errorf("Pi session %s has foreign or missing working directory", path)
			}
		} else if expectedCWD == "" || filepath.Clean(header.Cwd) != filepath.Clean(expectedCWD) {
			return "", fmt.Errorf("Pi session %s has foreign or missing working directory", path)
		}
		return header.ID, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Pi session: %w", err)
	}
	return "", fmt.Errorf("Pi session %s is empty", path)
}

func exactPiSessionID(inst *Instance) (string, error) {
	artifact, err := resolveExactPiSessionArtifact(inst)
	if err != nil {
		return "", err
	}
	return artifact.SessionID, nil
}

const maxReadableContextArtifactBytes int64 = 64 << 20

// exportContextArtifact keeps the native export contract raw and exact. The
// portable path is deliberately opt-in and used only by a cross-harness launch
// plan, so metadata-only but valid native artifacts remain exportable for
// native switching and diagnostics.
func exportContextArtifact(path string, source ContextSourceIdentity, format, continuity string, maxBytes int, expectedID string, portable bool) (*ContextExport, error) {
	if portable {
		return exportPortableArtifact(path, source, format, continuity, maxBytes, expectedID)
	}
	return exportArtifact(path, source, format, continuity, maxBytes, expectedID)
}

// exportArtifact is the native raw-artifact contract. Do not project or reject
// empty readable turns here: native account switching legitimately carries
// exact metadata-only artifacts without creating a different harness target.
func exportArtifact(path string, source ContextSourceIdentity, format, continuity string, maxBytes int, expectedID string) (*ContextExport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read context artifact: %w", err)
	}
	if err := validateJSONLIdentity(data, expectedID); err != nil {
		return nil, fmt.Errorf("validate context artifact %s: %w", path, err)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultHandoffMaxChars
	}
	payload, truncated := boundedContextPayload(data, maxBytes)
	sourceHash := sha256.Sum256(data)
	payloadHash := sha256.Sum256(payload)
	losses := []string{}
	if truncated {
		losses = append(losses, fmt.Sprintf("payload truncated to %d bytes; source remains identified by its full hash", maxBytes))
	}
	return &ContextExport{
		Manifest: ContextManifest{
			Version: 1, Source: source,
			Artifact:   ContextArtifact{Path: path, Format: format, SourceBytes: int64(len(data)), SourceSHA256: hex.EncodeToString(sourceHash[:]), PayloadBytes: len(payload), PayloadSHA256: hex.EncodeToString(payloadHash[:]), Truncated: truncated},
			Continuity: continuity, LossDisclosure: losses,
		},
		Payload: payload,
	}, nil
}

func exportPortableArtifact(path string, source ContextSourceIdentity, format, continuity string, maxBytes int, expectedID string) (*ContextExport, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect context artifact: %w", err)
	}
	if info.Size() > maxReadableContextArtifactBytes {
		return nil, fmt.Errorf("context artifact is %d bytes, exceeding the %d-byte readable projection safety limit", info.Size(), maxReadableContextArtifactBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read context artifact: %w", err)
	}
	if err := validateJSONLIdentity(data, expectedID); err != nil {
		return nil, fmt.Errorf("validate context artifact %s: %w", path, err)
	}
	projection, readable := projectPortableContext(source.Tool, data)
	if !readable {
		return nil, fmt.Errorf("exact context artifact has no readable user or assistant text; refusing to create a target from non-conversation or unknown records")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultHandoffMaxChars
	}
	payload, truncated := boundedReadableContextPayload([]byte(projection), maxBytes)
	sourceHash := sha256.Sum256(data)
	payloadHash := sha256.Sum256(payload)
	losses := []string{"portable payload contains readable user and assistant text only; raw native metadata is retained only by source hash/manifest"}
	if truncated {
		losses = append(losses, fmt.Sprintf("readable context truncated to %d bytes after projection; source remains identified by its full hash", maxBytes))
	}
	return &ContextExport{
		Manifest: ContextManifest{
			Version: 1, Source: source,
			Artifact:   ContextArtifact{Path: path, Format: format, SourceBytes: int64(len(data)), SourceSHA256: hex.EncodeToString(sourceHash[:]), PayloadBytes: len(payload), PayloadSHA256: hex.EncodeToString(payloadHash[:]), Truncated: truncated},
			Continuity: continuity, LossDisclosure: losses,
		},
		Payload:  payload,
		portable: true,
	}, nil
}

func boundedReadableContextPayload(data []byte, maxBytes int) ([]byte, bool) {
	if len(data) <= maxBytes {
		return append([]byte(nil), data...), false
	}
	const notice = "[…earlier readable context truncated…]\n"
	if maxBytes <= len(notice) {
		return append([]byte(nil), data[len(data)-maxBytes:]...), true
	}
	keep := maxBytes - len(notice)
	start := len(data) - keep
	// Prefer a complete readable turn over a byte fragment when possible.
	if newline := bytes.IndexByte(data[start:], '\n'); newline >= 0 && start+newline+1 < len(data) {
		start += newline + 1
		if len(notice)+len(data[start:]) <= maxBytes {
			return append([]byte(notice), data[start:]...), true
		}
	}
	return append([]byte(notice), data[len(data)-keep:]...), true
}

func validateJSONLIdentity(data []byte, expectedID string) error {
	if expectedID == "" {
		return nil
	}
	lines := bytes.Split(data, []byte("\n"))
	hasFinalNewline := len(data) > 0 && data[len(data)-1] == '\n'
	for index, raw := range lines {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var record struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Payload   struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			// A writer may be interrupted while appending its final record. That
			// one unterminated line is explicitly tolerated; a malformed prefix
			// would otherwise hide later records (including a foreign identity).
			if index == len(lines)-1 && !hasFinalNewline {
				return nil
			}
			return fmt.Errorf("malformed JSONL record at line %d: %w", index+1, err)
		}
		identities := []string{record.SessionID, record.Payload.SessionID}
		// Codex response_item payload.id identifies a message, not a session.
		// Only session_meta (or the legacy untyped metadata shape) binds ID.
		if record.Type == "session_meta" || record.Type == "" {
			identities = append(identities, record.Payload.ID)
		}
		for _, got := range identities {
			if got != "" && got != expectedID {
				return fmt.Errorf("session identity mismatch: requested %q, file contains %q", expectedID, got)
			}
		}
	}
	return nil
}

func boundedContextPayload(data []byte, maxBytes int) ([]byte, bool) {
	if len(data) <= maxBytes {
		return append([]byte(nil), data...), false
	}
	start := len(data) - maxBytes
	if newline := strings.IndexByte(string(data[start:]), '\n'); newline >= 0 {
		start += newline + 1
	}
	if start >= len(data) {
		start = len(data) - maxBytes
	}
	return append([]byte(nil), data[start:]...), true
}
