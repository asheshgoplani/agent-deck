package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CrossHarnessTargetEvidenceReader is the small, read-only seam used by the
// native observer. Production reads the per-instance hook status and native
// transcript metadata and checks that the target process is still alive. Tests
// can inject filesystem and process evidence without starting a harness.
type CrossHarnessTargetEvidenceReader struct {
	ReadHookStatus      func(instanceID string) (*HookStatus, time.Time, error)
	ReadNativeArtifact  func(target *Instance, expected FreshTargetIdentity) (CrossHarnessNativeArtifact, error)
	SnapshotPiDirectory func(target *Instance) map[string]string
	ProcessAlive        func(target *Instance) (bool, error)
}

// CrossHarnessNativeArtifact is evidence from a target-owned native session
// document. ModifiedAt is deliberately separate from the document identity:
// a matching old file is not evidence that this target launch created it.
type CrossHarnessNativeArtifact struct {
	Path       string    `json:"path"`
	SessionID  string    `json:"session_id"`
	ModifiedAt time.Time `json:"modified_at"`
	Ready      bool      `json:"ready"`
}

// NativeCrossHarnessTargetObserver correlates target-native lifecycle evidence
// to the exact fresh target. It never compares a mutable target field to
// itself, and never treats a successful tmux launch as readiness.
type NativeCrossHarnessTargetObserver struct {
	Reader       CrossHarnessTargetEvidenceReader
	PollInterval time.Duration
	Now          func() time.Time

	mu       sync.Mutex
	baseline map[string]crossHarnessObservationBaseline
}

type crossHarnessObservationBaseline struct {
	StartedAt time.Time                  `json:"started_at,omitempty"`
	Artifact  CrossHarnessNativeArtifact `json:"artifact,omitempty"`
	// PiDirectorySnapshot records every validated native file present before
	// launch. It is a durable launch-correlation token: a later header or mtime
	// alone cannot attribute an old Pi artifact to this operation.
	PiDirectorySnapshot map[string]string `json:"pi_directory_snapshot,omitempty"`
}

// NewNativeCrossHarnessTargetObserver returns the production observer. Claude
// and Codex readiness comes from their native hook/notify event scoped to the
// target instance. Pi has no equivalent hook contract here, so a durable
// pre-launch directory snapshot plus one new validated native document is used.
func NewNativeCrossHarnessTargetObserver() *NativeCrossHarnessTargetObserver {
	return &NativeCrossHarnessTargetObserver{
		Reader: CrossHarnessTargetEvidenceReader{
			ReadHookStatus:      readCrossHarnessHookStatus,
			ReadNativeArtifact:  readCrossHarnessNativeArtifact,
			SnapshotPiDirectory: snapshotPiSessionDirectory,
			ProcessAlive:        crossHarnessProcessAlive,
		},
		PollInterval: 100 * time.Millisecond,
		Now:          time.Now,
		baseline:     make(map[string]crossHarnessObservationBaseline),
	}
}

// BeginTargetObservation records a pre-launch lower bound. It is called by the
// switch service before lifecycle start, so a native document written very
// early in startup is not rejected merely because Instance.Start stamps
// LastStartedAt slightly later. The marker is per target instance and never
// discovers an artifact by recency.
func (o *NativeCrossHarnessTargetObserver) BeginTargetObservation(target *Instance, expected FreshTargetIdentity) {
	o.PrepareTargetObservation(target, expected)
}

// PrepareTargetObservation returns the pre-launch baseline so the switch
// journal can restore it after a crash. Pi has no hook-provided launch ID; its
// pre-launch artifact identity must therefore survive observer recreation and
// cannot be inferred from a post-launch timestamp alone.
func (o *NativeCrossHarnessTargetObserver) PrepareTargetObservation(target *Instance, expected FreshTargetIdentity) crossHarnessObservationBaseline {
	if target == nil || target.ID == "" {
		return crossHarnessObservationBaseline{}
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	reader := o.Reader
	if reader.ReadNativeArtifact == nil {
		reader.ReadNativeArtifact = readCrossHarnessNativeArtifact
	}
	artifact, _ := reader.ReadNativeArtifact(target, expected)
	baseline := crossHarnessObservationBaseline{StartedAt: now(), Artifact: artifact}
	if canonicalSwitchHarness(expected.Tool) == "pi" && reader.SnapshotPiDirectory != nil {
		// A nil snapshot means the filesystem could not be read; it must not be
		// confused with an empty, successfully captured directory snapshot.
		baseline.PiDirectorySnapshot = reader.SnapshotPiDirectory(target)
	}
	o.restoreTargetObservation(target, baseline)
	return baseline
}

// RestoreTargetObservation installs the durable journal baseline before
// observing an uncertain prior launch. It deliberately does not discover a
// newest artifact: only the pre-launch identity is eligible for comparison.
func (o *NativeCrossHarnessTargetObserver) RestoreTargetObservation(target *Instance, baseline crossHarnessObservationBaseline) {
	o.restoreTargetObservation(target, baseline)
}

func (o *NativeCrossHarnessTargetObserver) restoreTargetObservation(target *Instance, baseline crossHarnessObservationBaseline) {
	if target == nil || target.ID == "" || baseline.StartedAt.IsZero() {
		return
	}
	o.mu.Lock()
	if o.baseline == nil {
		o.baseline = make(map[string]crossHarnessObservationBaseline)
	}
	o.baseline[target.ID] = baseline
	o.mu.Unlock()
}

func (o *NativeCrossHarnessTargetObserver) observationBaseline(target *Instance) crossHarnessObservationBaseline {
	if target == nil {
		return crossHarnessObservationBaseline{}
	}
	o.mu.Lock()
	baseline := o.baseline[target.ID]
	o.mu.Unlock()
	if !baseline.StartedAt.IsZero() {
		return baseline
	}
	return crossHarnessObservationBaseline{StartedAt: target.LastStartedAt}
}

func (o *NativeCrossHarnessTargetObserver) ObserveTarget(ctx context.Context, target *Instance, expected FreshTargetIdentity) (CrossHarnessTargetEvidence, error) {
	if target == nil {
		return CrossHarnessTargetEvidence{}, fmt.Errorf("target is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reader := o.Reader
	if reader.ReadHookStatus == nil {
		reader.ReadHookStatus = readCrossHarnessHookStatus
	}
	if reader.ReadNativeArtifact == nil {
		reader.ReadNativeArtifact = readCrossHarnessNativeArtifact
	}
	if reader.ProcessAlive == nil {
		reader.ProcessAlive = crossHarnessProcessAlive
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	interval := o.PollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}

	baseline := o.observationBaseline(target)
	for {
		var evidence CrossHarnessTargetEvidence
		var ready bool
		if canonicalSwitchHarness(expected.Tool) == "pi" && baseline.PiDirectorySnapshot != nil {
			// Pi has no hook or caller-selected native ID. The durable pre-launch
			// directory snapshot is the launch token: only one new, validated
			// artifact in this exact directory may prove the target launch.
			artifact, err := readPiArtifactCreatedAfterSnapshot(target, expected, baseline.PiDirectorySnapshot)
			if err == nil && freshCrossHarnessArtifact(artifact, baseline.StartedAt, now()) {
				alive, aliveErr := reader.ProcessAlive(target)
				if aliveErr == nil && alive {
					evidence = CrossHarnessTargetEvidence{InstanceID: target.ID, Tool: "pi", SessionID: artifact.SessionID, ArtifactPath: artifact.Path, Ready: true, Evidence: "pi-native-session"}
					ready = true
				}
			}
		} else {
			evidence, ready = observeCrossHarnessEvidence(now(), target, expected, baseline.StartedAt, reader)
		}
		if ready && canonicalSwitchHarness(expected.Tool) == "pi" && baseline.PiDirectorySnapshot == nil {
			// Compatibility for injected legacy observers. Production always
			// installs SnapshotPiDirectory and never uses this header/mtime path.
			if baseline.Artifact.Path == "" || evidence.SessionID == baseline.Artifact.SessionID {
				ready = false
			}
		}
		if ready {
			return evidence, nil
		}
		select {
		case <-ctx.Done():
			return CrossHarnessTargetEvidence{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func observeCrossHarnessEvidence(now time.Time, target *Instance, expected FreshTargetIdentity, startedAt time.Time, reader CrossHarnessTargetEvidenceReader) (CrossHarnessTargetEvidence, bool) {
	if target.ID == "" || expected.InstanceID == "" || target.ID != expected.InstanceID {
		return CrossHarnessTargetEvidence{}, false
	}
	alive, err := reader.ProcessAlive(target)
	if err != nil || !alive {
		return CrossHarnessTargetEvidence{}, false
	}

	switch canonicalSwitchHarness(expected.Tool) {
	case "pi":
		artifact, err := reader.ReadNativeArtifact(target, expected)
		if err != nil || !freshCrossHarnessArtifact(artifact, startedAt, now) || (expected.NativeSessionPath != "" && filepath.Clean(artifact.Path) != filepath.Clean(expected.NativeSessionPath)) || (expected.SessionID != "" && artifact.SessionID != expected.SessionID) || !artifact.Ready {
			return CrossHarnessTargetEvidence{}, false
		}
		return CrossHarnessTargetEvidence{InstanceID: target.ID, Tool: "pi", SessionID: artifact.SessionID, ArtifactPath: artifact.Path, Ready: true, Evidence: "pi-native-session"}, true
	case "claude", "codex":
		status, modifiedAt, err := reader.ReadHookStatus(target.ID)
		if err != nil || status == nil || !freshCrossHarnessHook(status, modifiedAt, startedAt, now) {
			return CrossHarnessTargetEvidence{}, false
		}
		if status.Cwd != "" && filepath.Clean(status.Cwd) != filepath.Clean(target.EffectiveWorkingDir()) {
			return CrossHarnessTargetEvidence{}, false
		}
		sessionID := strings.TrimSpace(status.SessionID)
		if sessionID == "" && canonicalSwitchHarness(expected.Tool) == "codex" {
			if status.CodexStartedSessionID != "" {
				sessionID = strings.TrimSpace(status.CodexStartedSessionID)
			} else {
				sessionID = strings.TrimSpace(status.CodexCompletedSessionID)
			}
		}
		if sessionID == "" {
			return CrossHarnessTargetEvidence{}, false
		}
		if canonicalSwitchHarness(expected.Tool) == "claude" {
			if sessionID != expected.SessionID || !claudeReadinessEvent(status.Event) {
				return CrossHarnessTargetEvidence{}, false
			}
			artifact, artifactErr := reader.ReadNativeArtifact(target, expected)
			if artifactErr != nil || !freshCrossHarnessArtifact(artifact, startedAt, now) || artifact.SessionID != expected.SessionID {
				return CrossHarnessTargetEvidence{}, false
			}
		}
		if canonicalSwitchHarness(expected.Tool) == "codex" && !codexReadinessEvent(status.Event) {
			return CrossHarnessTargetEvidence{}, false
		}
		return CrossHarnessTargetEvidence{InstanceID: target.ID, Tool: canonicalSwitchHarness(expected.Tool), SessionID: sessionID, Ready: true, Evidence: map[bool]string{true: "claude-native-hook", false: "codex-native-notify"}[canonicalSwitchHarness(expected.Tool) == "claude"]}, true
	default:
		return CrossHarnessTargetEvidence{}, false
	}
}

func freshCrossHarnessHook(status *HookStatus, modifiedAt, startedAt, now time.Time) bool {
	if status == nil || status.Status == "" || status.Status == "dead" || status.UpdatedAt.IsZero() || modifiedAt.IsZero() || startedAt.IsZero() {
		return false
	}
	if !modifiedAt.After(startedAt) {
		return false
	}
	if !status.UpdatedAt.IsZero() && status.UpdatedAt.Before(startedAt.Truncate(time.Second)) {
		return false
	}
	if !now.IsZero() && modifiedAt.After(now.Add(2*time.Second)) {
		return false
	}
	return true
}

func freshCrossHarnessArtifact(artifact CrossHarnessNativeArtifact, startedAt, now time.Time) bool {
	if !artifact.Ready || artifact.Path == "" || artifact.ModifiedAt.IsZero() || startedAt.IsZero() {
		return false
	}
	if !artifact.ModifiedAt.After(startedAt) {
		return false
	}
	return now.IsZero() || !artifact.ModifiedAt.After(now.Add(2*time.Second))
}

func claudeReadinessEvent(event string) bool {
	switch normalizeCrossHarnessEventKey(event) {
	case "sessionstart", "userpromptsubmit", "stop", "permissionrequest":
		return true
	default:
		return false
	}
}

func normalizeCrossHarnessEventKey(event string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(event)))
}

func codexReadinessEvent(event string) bool {
	e := strings.ToLower(strings.TrimSpace(event))
	return strings.Contains(e, "thread.started") || strings.Contains(e, "thread/started") || strings.Contains(e, "thread-started") || strings.Contains(e, "session.configured") || strings.Contains(e, "session/configured") || strings.Contains(e, "session-configured") || strings.Contains(e, "turn")
}

func readCrossHarnessHookStatus(instanceID string) (*HookStatus, time.Time, error) {
	path := hookStatusFilePath(instanceID)
	// hookStatusFilePath intentionally falls back from a scoped path to the
	// flat path. For readiness, a present scoped symlink is not a fallback
	// condition: accepting the flat file could bind stale evidence from an old
	// launch to a sandbox target.
	scoped := filepath.Join(GetHooksDir(), "sandbox", instanceID, instanceID+".json")
	if scopedInfo, scopedErr := os.Lstat(scoped); scopedErr == nil {
		if scopedInfo.Mode()&os.ModeSymlink != 0 {
			return nil, time.Time{}, fmt.Errorf("refusing symlinked scoped target hook status")
		}
		path = scoped
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, time.Time{}, fmt.Errorf("refusing symlinked target hook status")
	}
	status := readHookStatusFile(instanceID)
	if status == nil {
		return nil, time.Time{}, fmt.Errorf("target hook status is unavailable")
	}
	return status, info.ModTime(), nil
}

// snapshotPiSessionDirectory is deliberately an all-files snapshot, not a
// newest-file probe. It is captured before launch and persisted in the switch
// journal so recovery can require a genuinely new Pi artifact.
func snapshotPiSessionDirectory(target *Instance) map[string]string {
	if target == nil || target.Tool != "pi" {
		return nil
	}
	dir, err := piInstanceSessionDir(target.ID)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}
		}
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	snapshot := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		id, err := readValidatedPiSessionHeader(path, target.EffectiveWorkingDir(), entry.Name() == "session.jsonl")
		if err != nil {
			return nil
		}
		snapshot[path] = id
	}
	return snapshot
}

// readPiArtifactCreatedAfterSnapshot finds the sole validated Pi document that
// did not exist before this operation. It is scoped to one instance directory;
// malformed headers, symlinks, foreign cwd values, and multiple candidates are
// all refusals rather than reasons to select a newest file.
func readPiArtifactCreatedAfterSnapshot(target *Instance, expected FreshTargetIdentity, snapshot map[string]string) (CrossHarnessNativeArtifact, error) {
	if target == nil || target.Tool != "pi" || snapshot == nil {
		return CrossHarnessNativeArtifact{}, fmt.Errorf("Pi launch snapshot is unavailable")
	}
	if expected.NativeSessionPath != "" {
		return CrossHarnessNativeArtifact{}, fmt.Errorf("Pi launch has a legacy fixed path, not a generated-file snapshot contract")
	}
	dir, err := piInstanceSessionDir(target.ID)
	if err != nil {
		return CrossHarnessNativeArtifact{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return CrossHarnessNativeArtifact{}, err
	}
	var candidates []CrossHarnessNativeArtifact
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if _, existed := snapshot[path]; existed {
			continue
		}
		id, headerErr := readValidatedPiSessionHeader(path, target.EffectiveWorkingDir(), entry.Name() == "session.jsonl")
		if headerErr != nil {
			return CrossHarnessNativeArtifact{}, headerErr
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return CrossHarnessNativeArtifact{}, fmt.Errorf("Pi target session artifact is unavailable")
		}
		if expected.SessionID != "" && id != expected.SessionID {
			return CrossHarnessNativeArtifact{}, fmt.Errorf("Pi target session identity mismatch: requested %q, file contains %q", expected.SessionID, id)
		}
		candidates = append(candidates, CrossHarnessNativeArtifact{Path: path, SessionID: id, ModifiedAt: info.ModTime(), Ready: true})
	}
	if len(candidates) != 1 {
		return CrossHarnessNativeArtifact{}, fmt.Errorf("Pi launch has %d new validated session artifacts", len(candidates))
	}
	return candidates[0], nil
}

func readCrossHarnessNativeArtifact(target *Instance, expected FreshTargetIdentity) (CrossHarnessNativeArtifact, error) {
	if target == nil {
		return CrossHarnessNativeArtifact{}, fmt.Errorf("target is nil")
	}
	switch canonicalSwitchHarness(expected.Tool) {
	case "pi":
		var artifact piSessionArtifact
		var err error
		if expected.NativeSessionPath != "" {
			// Compatibility for persisted legacy plans only. New Pi plans leave
			// this empty because Pi, not Agent Deck, names the timestamped file.
			dir, dirErr := piInstanceSessionDir(target.ID)
			if dirErr != nil || !piPathInInstanceDir(expected.NativeSessionPath, dir) {
				return CrossHarnessNativeArtifact{}, fmt.Errorf("Pi target session artifact is unavailable")
			}
			id, headerErr := readValidatedPiSessionHeader(expected.NativeSessionPath, target.EffectiveWorkingDir(), filepath.Base(expected.NativeSessionPath) == "session.jsonl")
			if headerErr != nil {
				return CrossHarnessNativeArtifact{}, headerErr
			}
			artifact = piSessionArtifact{Path: expected.NativeSessionPath, SessionID: id}
		} else {
			artifact, err = resolveExactPiSessionArtifact(target)
			if err != nil {
				return CrossHarnessNativeArtifact{}, err
			}
		}
		info, err := os.Lstat(artifact.Path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return CrossHarnessNativeArtifact{}, fmt.Errorf("Pi target session artifact is unavailable")
		}
		if expected.SessionID != "" && artifact.SessionID != expected.SessionID {
			return CrossHarnessNativeArtifact{}, fmt.Errorf("Pi target session identity mismatch: requested %q, file contains %q", expected.SessionID, artifact.SessionID)
		}
		return CrossHarnessNativeArtifact{Path: artifact.Path, SessionID: artifact.SessionID, ModifiedAt: info.ModTime(), Ready: true}, nil
	case "claude":
		dir := GetClaudeConfigDirForInstance(target)
		path := filepath.Join(ExpandPath(dir), "projects", ConvertToClaudeDirName(target.EffectiveWorkingDir()), expected.SessionID+".jsonl")
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return CrossHarnessNativeArtifact{}, fmt.Errorf("Claude target session artifact is unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return CrossHarnessNativeArtifact{}, err
		}
		if err := validateJSONLIdentity(data, expected.SessionID); err != nil {
			return CrossHarnessNativeArtifact{}, err
		}
		return CrossHarnessNativeArtifact{Path: path, SessionID: expected.SessionID, ModifiedAt: info.ModTime(), Ready: true}, nil
	default:
		// Codex's native fresh-thread ID is not available until notify emits it;
		// accepting a guessed rollout path would reintroduce newest-artifact
		// discovery. The notify event is therefore the supported Codex subset.
		return CrossHarnessNativeArtifact{}, fmt.Errorf("Codex has no exact fresh artifact path before native notify")
	}
}

func crossHarnessProcessAlive(target *Instance) (bool, error) {
	if target == nil {
		return false, fmt.Errorf("target is nil")
	}
	return target.Exists(), nil
}
