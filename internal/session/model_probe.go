package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// Model catalog probe (#2388).
//
// The static lists in model_catalog.go and reasoning_effort.go go stale every
// time a vendor ships a model. Where the installed CLI can list its own models
// without a billed request, Agent Deck asks it and merges the answer in front
// of the static list. Today only Codex has such a command (`codex debug
// models`); Claude Code and Gemini have no local listing and stay static.
//
// The probe runs on the host that launches the session, with a short timeout.
// Its result is cached on disk per tool, keyed by the resolved binary path,
// its mtime and size, so a CLI upgrade re-probes. A missing CLI, a timeout, a
// non-zero exit or output the strict parser rejects all fall back to the
// static catalog, so offline behaviour is unchanged. `[models] probe = false`
// turns the probe off entirely.

// ModelProbeResult is what a probe learned from an installed CLI.
type ModelProbeResult struct {
	// Models lists the selectable model IDs in the CLI's own display order.
	Models []string `json:"models"`
	// Efforts maps a model ID to the reasoning efforts that model accepts.
	// Missing entries mean "no per-model information".
	Efforts map[string][]string `json:"efforts,omitempty"`
}

// modelProber describes how to ask one CLI for its model list.
type modelProber struct {
	// name keys the memo, the cache file and log lines. It is a constant, so
	// a tool name from a request never reaches a path or a log entry.
	name   string
	binary func() string
	args   []string
	parse  func([]byte) (*ModelProbeResult, error)
}

var codexModelProber = modelProber{name: "codex", binary: codexProbeBinary, args: []string{"debug", "models"}, parse: parseCodexDebugModels}

// modelProbers lists the tools that can answer.
var modelProbers = []modelProber{codexModelProber}

// modelProberFor returns the prober for a tool kind. A switch, not a map
// lookup, so the returned prober never carries the caller's string.
func modelProberFor(kind string) (modelProber, bool) {
	switch kind {
	case "codex":
		return codexModelProber, true
	}
	return modelProber{}, false
}

// codexProbeBinary is the executable of the configured [codex] command, so
// the catalog comes from the Codex that sessions actually launch. Leading
// VAR=value assignments are skipped; an empty command means "codex".
func codexProbeBinary() string {
	for _, field := range strings.Fields(GetCodexCommand()) {
		if isShellEnvAssignment(field) {
			continue
		}
		if token := strings.Trim(field, `"'`); token != "" {
			return token
		}
		break
	}
	return "codex"
}

const (
	modelProbeCacheSchema = 1
	// modelProbeRecheck bounds how often a lookup re-stats the binary. The
	// TUI filters suggestions on every keystroke, so the hot path must stay
	// in memory.
	modelProbeRecheck = time.Minute
	// modelProbeTTL re-probes an unchanged binary once a day: `codex debug
	// models` can refresh its catalog from the network without an upgrade.
	modelProbeTTL = 24 * time.Hour
	// modelProbeFailureBackoff is how long a failed probe is remembered. It is
	// short because the TUI and web server are long-lived and warm the probe
	// at startup, when a cold CLI is slowest: one timeout there must not hide
	// the probed list for a day. The next lookup after it retries.
	modelProbeFailureBackoff = time.Minute
)

// modelProbeTimeout bounds one probe run. A slow CLI must never block the
// new-session dialog for long; a timeout just means the static list.
var modelProbeTimeout = time.Second

// modelProbeTestEnv opts a test binary into real probing. Without it, test
// binaries never execute an installed CLI, so tests stay hermetic on machines
// that happen to have codex installed.
const modelProbeTestEnv = "AGENTDECK_TEST_MODEL_PROBE"

type modelProbeKey struct {
	Path    string `json:"path"`
	ModTime int64  `json:"mod_time"`
	Size    int64  `json:"size"`
}

type modelProbeCacheFile struct {
	Schema   int               `json:"schema"`
	Key      modelProbeKey     `json:"key"`
	Version  string            `json:"version,omitempty"`
	ProbedAt time.Time         `json:"probed_at"`
	Result   *ModelProbeResult `json:"result"`
}

type modelProbeMemo struct {
	key       modelProbeKey
	result    *ModelProbeResult // nil when the probe failed
	probedAt  time.Time
	checkedAt time.Time
}

// ttl is how long the memo answers for an unchanged binary: a success is kept
// for modelProbeTTL, a failure only for modelProbeFailureBackoff.
func (m *modelProbeMemo) ttl() time.Duration {
	if m.result == nil {
		return modelProbeFailureBackoff
	}
	return modelProbeTTL
}

var (
	modelProbeMu       sync.Mutex
	modelProbeMemos    = map[string]*modelProbeMemo{}
	modelProbeInFlight = map[string]bool{}
)

// ResetModelProbeMemo drops the in-memory probe results so the next lookup
// re-validates against the binary and the disk cache. Tests use it between
// cases that install different fake CLIs.
func ResetModelProbeMemo() {
	modelProbeMu.Lock()
	defer modelProbeMu.Unlock()
	modelProbeMemos = map[string]*modelProbeMemo{}
	modelProbeInFlight = map[string]bool{}
}

// modelProbeEnabled reports whether probing is allowed in this process.
func modelProbeEnabled() bool {
	if testing.Testing() && os.Getenv(modelProbeTestEnv) != "1" {
		return false
	}
	cfg, _ := LoadUserConfig()
	return cfg == nil || cfg.Models.ProbeEnabled()
}

// probedModelCatalog returns the probe result for a tool kind, or nil when the
// tool has no prober, probing is off, or the probe failed. The CLI runs
// outside the lock: while one caller probes, others get the previous result
// (or nil, meaning the static catalog) instead of waiting on it.
func probedModelCatalog(kind string) *ModelProbeResult {
	prober, ok := modelProberFor(kind)
	if !ok || !modelProbeEnabled() {
		return nil
	}
	name := prober.name
	now := time.Now()

	modelProbeMu.Lock()
	memo := modelProbeMemos[name]
	if memo != nil && now.Sub(memo.checkedAt) < modelProbeRecheck {
		modelProbeMu.Unlock()
		return memo.result
	}
	if modelProbeInFlight[name] {
		modelProbeMu.Unlock()
		if memo != nil {
			return memo.result
		}
		return nil
	}

	key, err := modelProbeBinaryKey(prober.binary())
	if err != nil {
		// Not installed: remember the miss so we do not LookPath per keystroke.
		modelProbeMemos[name] = &modelProbeMemo{checkedAt: now}
		modelProbeMu.Unlock()
		return nil
	}
	if memo != nil && memo.key == key && now.Sub(memo.probedAt) < memo.ttl() {
		memo.checkedAt = now
		modelProbeMu.Unlock()
		return memo.result
	}
	if cached := readModelProbeCache(name, key, now); cached != nil {
		modelProbeMemos[name] = &modelProbeMemo{key: key, result: cached.Result, probedAt: cached.ProbedAt, checkedAt: now}
		modelProbeMu.Unlock()
		return cached.Result
	}
	modelProbeInFlight[name] = true
	modelProbeMu.Unlock()

	result, version, err := runModelProbe(prober, key.Path)

	modelProbeMu.Lock()
	delete(modelProbeInFlight, name)
	// Failures are memoized in memory only and for modelProbeFailureBackoff,
	// so a later lookup in this process (or the next process) retries.
	modelProbeMemos[name] = &modelProbeMemo{key: key, result: result, probedAt: now, checkedAt: now}
	modelProbeMu.Unlock()

	if err != nil {
		logging.ForComponent(logging.CompSession).Debug("model_probe_failed", "tool", name, "error", err.Error())
		return nil
	}
	writeModelProbeCache(name, &modelProbeCacheFile{
		Schema: modelProbeCacheSchema, Key: key, Version: version, ProbedAt: now, Result: result,
	})
	return result
}

func modelProbeBinaryKey(binary string) (modelProbeKey, error) {
	path, err := exec.LookPath(binary)
	if err != nil {
		return modelProbeKey{}, err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	info, err := os.Stat(path)
	if err != nil {
		return modelProbeKey{}, err
	}
	return modelProbeKey{Path: path, ModTime: info.ModTime().UnixNano(), Size: info.Size()}, nil
}

// runModelProbe executes the listing command and the version command. Neither
// sends a prompt, so neither is billed.
func runModelProbe(prober modelProber, path string) (*ModelProbeResult, string, error) {
	out, err := runModelProbeCommand(path, prober.args...)
	if err != nil {
		return nil, "", err
	}
	result, err := prober.parse(out)
	if err != nil {
		return nil, "", err
	}
	version, _ := runModelProbeCommand(path, "--version")
	return result, strings.TrimSpace(string(version)), nil
}

func runModelProbeCommand(path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// Do not wait for grandchildren holding the pipe open past the timeout.
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Run(); err != nil {
		command := filepath.Base(path) + " " + strings.Join(args, " ")
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: timed out after %s", command, modelProbeTimeout)
		}
		return nil, fmt.Errorf("%s: %w", command, err)
	}
	return stdout.Bytes(), nil
}

func modelProbeCachePath(kind string) (string, error) {
	dir, err := agentpaths.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "model-probe", kind+".json"), nil
}

func readModelProbeCache(kind string, key modelProbeKey, now time.Time) *modelProbeCacheFile {
	path, err := modelProbeCachePath(kind)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cached modelProbeCacheFile
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil
	}
	if cached.Schema != modelProbeCacheSchema || cached.Key != key || cached.Result == nil ||
		len(cached.Result.Models) == 0 || now.Sub(cached.ProbedAt) >= modelProbeTTL {
		return nil
	}
	return &cached
}

func writeModelProbeCache(kind string, entry *modelProbeCacheFile) {
	path, err := modelProbeCachePath(kind)
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// probeIDPattern is the strict shape accepted for a model slug or an effort
// name from probe output. Anything else rejects the whole probe.
var probeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// parseCodexDebugModels parses `codex debug models`. Only models with
// visibility "list" are offered, ordered by priority; hidden ones still
// contribute efforts so an explicitly chosen hidden model validates.
func parseCodexDebugModels(data []byte) (*ModelProbeResult, error) {
	var payload struct {
		Models *[]struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
			Priority   *int   `json:"priority"`
			Levels     []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse codex debug models: %w", err)
	}
	if payload.Models == nil {
		return nil, errors.New("parse codex debug models: missing models array")
	}
	type entry struct {
		slug     string
		priority int
	}
	var listed []entry
	result := &ModelProbeResult{Efforts: map[string][]string{}}
	for _, m := range *payload.Models {
		if !probeIDPattern.MatchString(m.Slug) {
			return nil, fmt.Errorf("parse codex debug models: invalid slug %q", m.Slug)
		}
		if m.Priority == nil {
			return nil, fmt.Errorf("parse codex debug models: %s has no priority", m.Slug)
		}
		var efforts []string
		for _, level := range m.Levels {
			if !probeIDPattern.MatchString(level.Effort) {
				return nil, fmt.Errorf("parse codex debug models: invalid effort %q for %s", level.Effort, m.Slug)
			}
			if !slices.Contains(efforts, level.Effort) {
				efforts = append(efforts, level.Effort)
			}
		}
		if len(efforts) > 0 {
			result.Efforts[m.Slug] = efforts
		}
		switch m.Visibility {
		case "list":
			listed = append(listed, entry{slug: m.Slug, priority: *m.Priority})
		case "hide":
		default:
			return nil, fmt.Errorf("parse codex debug models: unknown visibility %q for %s", m.Visibility, m.Slug)
		}
	}
	if len(listed) == 0 {
		return nil, errors.New("parse codex debug models: no listed models")
	}
	sort.SliceStable(listed, func(a, b int) bool { return listed[a].priority < listed[b].priority })
	for _, e := range listed {
		if !slices.Contains(result.Models, e.slug) {
			result.Models = append(result.Models, e.slug)
		}
	}
	return result, nil
}

// mergeOrdered returns first's entries followed by the entries of rest that
// first lacks, without duplicates. Merging a probe over the static catalog
// this way keeps the static list as the floor: a probe can add suggestions but
// never remove one a user may rely on.
func mergeOrdered(first, rest []string) []string {
	merged := make([]string, 0, len(first)+len(rest))
	for _, id := range slices.Concat(first, rest) {
		if !slices.Contains(merged, id) {
			merged = append(merged, id)
		}
	}
	return merged
}

// WarmModelCatalog runs the probes in the background so the first dialog
// open does not wait on them.
func WarmModelCatalog() {
	go func() {
		for _, prober := range modelProbers {
			probedModelCatalog(prober.name)
		}
	}()
}
