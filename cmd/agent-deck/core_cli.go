package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// envCoreRegistry switches the slice-1 commands (session start/stop/restart,
// list, group list) back to their legacy handlers when set to "0". The
// registry path is the default; the legacy handlers stay until the registry
// path has soaked (docs/core-registry.md).
const envCoreRegistry = "AGENT_DECK_CORE_REGISTRY"

// coreRegistryEnabled reports whether the five registry-backed commands run
// through internal/core.
func coreRegistryEnabled() bool {
	return os.Getenv(envCoreRegistry) != "0"
}

var (
	cliRegistryOnce sync.Once
	cliRegistry     *core.Registry
)

// coreRegistry returns the process-wide command registry, wired to the CLI's
// existing session resolver and yolo override.
func coreRegistry() *core.Registry {
	cliRegistryOnce.Do(func() {
		cliRegistry = core.NewRegistry()
		deps := core.Deps{
			Resolve: ResolveSession,
			ApplyYolo: func(inst *session.Instance, enabled bool) error {
				return applyCLIYoloOverride(inst, enabled)
			},
		}
		if err := core.RegisterBuiltins(cliRegistry, deps); err != nil {
			panic(err)
		}
	})
	return cliRegistry
}

// jsonMode is the value of --json on registry-backed commands: off, the
// legacy JSON shape (--json, --json=true), or the response envelope
// (--json=envelope).
type jsonMode int

const (
	jsonOff jsonMode = iota
	jsonLegacy
	jsonEnvelope
)

// errFlagParse mirrors the flag package's boolValue error so an invalid
// --json value is reported byte-for-byte as before.
var errFlagParse = errors.New("parse error")

// jsonModeFlag is a boolean flag that also accepts "envelope". It reports
// itself as a bool flag with a "false" zero value, so help output and
// normalizeArgs treat it exactly like the fs.Bool it replaces.
type jsonModeFlag struct{ mode jsonMode }

func (f *jsonModeFlag) String() string {
	if f == nil {
		return "false"
	}
	switch f.mode {
	case jsonLegacy:
		return "true"
	case jsonEnvelope:
		return "envelope"
	}
	return "false"
}

func (f *jsonModeFlag) Set(s string) error {
	if s == "envelope" {
		f.mode = jsonEnvelope
		return nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return errFlagParse
	}
	if v {
		f.mode = jsonLegacy
	} else {
		f.mode = jsonOff
	}
	return nil
}

func (f *jsonModeFlag) IsBoolFlag() bool { return true }

// enabled reports whether any JSON output was requested.
func (f *jsonModeFlag) enabled() bool { return f.mode != jsonOff }

func (f *jsonModeFlag) envelope() bool { return f.mode == jsonEnvelope }

// runCore executes a registry command with obs receiving its progress events.
func runCore(id string, in any, obs core.Observer) *core.Result {
	ctx := context.Background()
	if obs != nil {
		ctx = core.WithObserver(ctx, obs)
	}
	return coreRegistry().Run(ctx, id, in)
}

// printEnvelope writes the response envelope as indented JSON.
func printEnvelope(res *core.Result) {
	output, err := json.MarshalIndent(res.Envelope(""), "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to format JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(output))
}

// legacyErrorCode maps a core error code onto the CLI's historical JSON error
// code and exit status. It is the single place the registry path decides exit
// statuses for the slice-1 commands.
func legacyErrorCode(code string) (string, int) {
	switch code {
	case core.CodeNotFound:
		return ErrCodeNotFound, 2
	case core.CodeAmbiguous:
		return ErrCodeAmbiguous, 1
	case core.CodeStorage, core.CodeNoActive:
		return ErrCodeNotFound, 1
	default:
		return ErrCodeInvalidOperation, 1
	}
}

// exitCoreError renders a failed registry result the way the legacy handler
// did (or as an envelope) and exits. usage, when non-nil, is printed after
// the error for CodeMissingArg outside envelope mode.
func exitCoreError(out *CLIOutput, mode *jsonModeFlag, res *core.Result, usage func()) {
	ce := core.AsError(res.Err)
	legacy, exit := legacyErrorCode(ce.Code)
	if mode.envelope() {
		printEnvelope(res)
		os.Exit(exit)
	}
	if sf, ok := ce.Data.(*core.SpawnFailure); ok && ce.Code == core.CodeSpawnFailed {
		if sf.SaveErr != nil && !out.jsonMode {
			fmt.Fprintf(os.Stderr, "Warning: failed to save session state: %v\n", sf.SaveErr)
		}
		// spawnFailureOutput reads only the instance's ID and Title, so a stub
		// carrying those two renders exactly what the legacy handler printed.
		msg, data := spawnFailureOutput(sf.Verb, &session.Instance{ID: sf.ID, Title: sf.Title}, ce.Cause)
		out.ErrorWithData(msg, ErrCodeInvalidOperation, data)
		os.Exit(exit)
	}
	out.Error(ce.Message, legacy)
	if ce.Code == core.CodeMissingArg && usage != nil {
		usage()
	}
	os.Exit(exit)
}

// exitCLIError reports an adapter-side failure (before or after the command
// ran) as a legacy error or an envelope, then exits with status exit.
func exitCLIError(out *CLIOutput, mode *jsonModeFlag, id, code, msg string, exit int) {
	if mode.envelope() {
		printEnvelope(&core.Result{ID: id, Err: &core.Error{Code: code, Message: msg}})
		os.Exit(exit)
	}
	legacy, _ := legacyErrorCode(code)
	out.Error(msg, legacy)
	os.Exit(exit)
}
