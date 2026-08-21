package panedrive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/fixture"
)

// World is the machine a pane run happens on: a home directory, an environment,
// and whatever state was seeded into it.
//
// Driver A injects its sessions straight into the model. Driver B cannot: it
// drives the shipped binary, which reads a profile database off disk. So the
// world here is a real sandboxed HOME with a real state.db — and that is a
// second thing Driver B proves which Driver A cannot, because the path from
// "row in SQLite" to "session on screen" only exists in the binary.
//
// EVERY path is inside Root. This repository has three times had a run resolve
// agent-deck's live profile out of the real home and destroy it (2025-12-11,
// 2026-04-17, 2026-06-04). seedProfile below therefore refuses to write a
// database whose resolved path is not under the sandbox, and does so by
// checking the path the product itself resolved, not the one we asked for.
type World struct {
	// Name identifies the world in artifacts and in the matrix row.
	Name string
	// Description is the one line a reader of the transcript gets.
	Description string
	// Root is the sandbox. Nothing outside it is written.
	Root string
	// Home is $HOME for the pane.
	Home string
	// Profile is AGENTDECK_PROFILE, empty for a genuinely cold first run.
	Profile string
	// Env is the environment handed to the tmux server, and therefore to the
	// binary under test.
	Env map[string]string
	// Notes record anything a reader needs in order to trust the frames.
	Notes []string
	// ExpectFirstRunModal declares that this world has never been configured,
	// so the binary is expected to ask something before the deck appears. The
	// modal is captured as evidence and dismissed with "n"; it is not silently
	// swallowed, because "what a first-time user actually sees" is the whole
	// reason a cold row exists.
	ExpectFirstRunModal bool
	// StopAfter, when set, ends the journey after that step, with Reason
	// recorded. It scopes a row; it never relaxes an assertion.
	StopAfter string
	// StopReason must be non-empty whenever StopAfter is.
	StopReason string
}

// baseEnv builds the environment every world shares.
//
// The three interesting entries:
//
//   - AGENTDECK_SKIP_UPDATE_CHECK=1. Without it the binary reaches the network
//     on startup and can block the pane on a prompt. It is an env var rather
//     than a config file setting on purpose: the cold row's entire claim is
//     that it runs with NO configuration, and a config file written to suppress
//     an update check would quietly falsify that.
//   - AGENT_DECK_ALLOW_OUTER_TMUX=1. The binary refuses to start its TUI inside
//     a non-agentdeck tmux session (issue #560). Our pane is exactly that.
//   - XDG_* pinned inside the sandbox. Leaving them unset would let
//     agentpaths fall back to $HOME/.config, which is inside the sandbox too —
//     but "inside because HOME happens to be" is an accident, and this is the
//     one place where an accident writes to a real profile.
func baseEnv(home string) map[string]string {
	return map[string]string{
		"HOME":                        home,
		"XDG_CONFIG_HOME":             filepath.Join(home, ".config"),
		"XDG_DATA_HOME":               filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":              filepath.Join(home, ".cache"),
		"CLAUDE_CONFIG_DIR":           filepath.Join(home, ".claude"),
		"AGENTDECK_SKIP_UPDATE_CHECK": "1",
		"AGENT_DECK_ALLOW_OUTER_TMUX": "1",
		"TERM":                        "xterm-256color",
		"COLORTERM":                   "truecolor",
		"NO_COLOR":                    "",
	}
}

// ColdFirstRun builds the mandatory matrix row: a machine that has never run
// agent-deck. No config file, no profile, no database, no sessions.
//
// This row exists because first-run modals and empty states are where "feels
// broken on arrival" lives, and neither is reachable from a fixture that starts
// with data already in it. It is also the only row that proves the shipped
// binary boots from nothing at all.
func ColdFirstRun(root string) (*World, error) {
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	env := baseEnv(home)
	return &World{
		Name:        "pane-cold-first-run",
		Description: "a machine that has never run agent-deck: no config, no profile, no database, no sessions",
		Root:        root,
		Home:        home,
		Profile:     "",
		Env:         env,
		Notes: []string{
			"HOME and every XDG_* variable point inside " + root + "; nothing outside it is read or written",
			"no config file is written at all — the update check is suppressed by AGENTDECK_SKIP_UPDATE_CHECK=1, an env var, so the claim 'no configuration' stays literally true",
		},
		ExpectFirstRunModal: true,
		StopAfter:           "01-home",
		StopReason: "a cold first run owns no session, so there is nothing to press C on. " +
			"The journey's later steps belong to a row that has one; recording them here would " +
			"mean recording twenty seconds of timeout per step and calling it evidence. " +
			"What this row is for — that the shipped binary boots from nothing, what it asks " +
			"first, and what the empty deck looks like — is fully covered by the steps it does run.",
	}, nil
}

// Loaded builds a row with a real session in a real database: the recorded
// corpus case materialized into the sandbox, then seeded into a profile the
// shipped binary will load on its own.
func Loaded(caseName, root string) (*World, error) {
	return LoadedWith(caseName, root, fixture.Variant{})
}

// LoadedWithModel is Loaded on a session whose model identifier this build has
// never been taught, so the context window resolves to "unknown".
//
// It is the world the feature's own user landed in first. agent-deck will not
// invent a window size for an unrecognised identifier, so the gauge loses its
// percentage and the header prints "window unknown" — and a screen that answers
// "how full is my context" with silence is exactly the arrival experience no
// amount of code reading detects. Only the model identifier is rewritten, and
// only inside this row's sandbox; every token figure is the recorded case's.
func LoadedWithModel(caseName, root, model string) (*World, error) {
	w, err := LoadedWith(caseName, root, fixture.Variant{})
	if err != nil {
		return nil, err
	}
	files, replaced, err := fixture.OverrideModel(filepath.Join(root, "fixture"), model)
	if err != nil {
		return nil, err
	}
	// A world may also place a transcript under its HOME rather than under the
	// fixture tree — which of the two the binary reads depends on the case's
	// config dir. Both are rewritten, and the row refuses to exist if neither
	// held a transcript: a fixture that silently rewrote nothing would produce a
	// row that looks like it covered the untaught-model screen and did not.
	homeFiles, homeReplaced, err := fixture.OverrideModel(w.Home, model)
	if err != nil {
		return nil, err
	}
	if files+homeFiles == 0 {
		return nil, fmt.Errorf(
			"panedrive: no transcript under %s or %s carries a model identifier, so the untaught-model world was not built",
			filepath.Join(root, "fixture"), w.Home)
	}
	replaced = append(replaced, homeReplaced...)
	w.Name += "-model-" + strings.NewReplacer("[", "", "]", "", ".", "-").Replace(model)
	w.Description += fmt.Sprintf(" — on model %q, which this build's window table does not recognise", model)
	w.Notes = append(w.Notes, fmt.Sprintf(
		"model override: the identifier %s was rewritten to %q in %d transcript file(s) under the fixture tree and %d under HOME. "+
			"Nothing else was changed: no conversation content, no usage record, no token figure. The model identifier is not a "+
			"measurement, it is the key the context-window table is looked up by, and it is the only input that produces the "+
			"unknown-window screen this row exists to capture.",
		strings.Join(replaced, ", "), model, files, homeFiles))
	return w, nil
}

// LoadedWith is Loaded with a derived world: a matrix row may ask for the same
// recorded case with nothing to inspect, with far too much, or with the session
// in a state nobody records. The derivation is the fixture package's business;
// what matters here is that whatever it produced still goes into a REAL profile
// database inside the sandbox and is loaded by the binary itself.
func LoadedWith(caseName, root string, v fixture.Variant) (*World, error) {
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	setup, err := fixture.FromCorpusVariant(caseName, filepath.Join(root, "fixture"), v)
	if err != nil {
		return nil, err
	}

	env := baseEnv(home)
	// The corpus case decides where the transcript lives; the world must agree
	// with it or the binary will resolve a report from somewhere else.
	for k, v := range setup.Env {
		env[k] = v
	}
	const profile = "sixgate"
	env["AGENTDECK_PROFILE"] = profile

	// A real config file, because this row is NOT the cold one: it represents a
	// machine somebody already uses. Update checks are off here as well, by
	// config this time, which is what a real user's file would say.
	if err := writeConfig(home); err != nil {
		return nil, err
	}

	dbPath, err := seedProfile(home, profile, setup.Instances)
	if err != nil {
		return nil, err
	}

	notes := append([]string{
		"HOME and every XDG_* variable point inside " + root + "; nothing outside it is read or written",
		"the session was written into a real profile database at " + dbPath + " and is loaded by the binary itself, not injected into the model",
	}, setup.Notes...)

	return &World{
		Name:        "pane-" + caseName,
		Description: setup.Description + " — seeded into a real profile database and loaded by the shipped binary",
		Root:        root,
		Home:        home,
		Profile:     profile,
		Env:         env,
		Notes:       notes,
	}, nil
}

// writeConfig writes the minimal user config a configured machine would have.
func writeConfig(home string) error {
	dir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	const cfg = `# Written by SIXGATE's Driver B for a sandboxed run. Nothing here is read from
# or written to a real agent-deck installation.
[updates]
check_enabled = false
auto_update = false
`
	return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o644) //nolint:gosec // sandbox fixture
}

// seedProfile writes the fixture's sessions into a real profile database inside
// the sandbox, and refuses to write anywhere else.
//
// The guard is the point. It asks the product where it resolved the database to
// and compares THAT against the sandbox, rather than trusting the environment it
// just set — because every previous incident of this class began with code that
// had set the right variables and resolved the wrong path anyway.
func seedProfile(home, profile string, instances []*session.Instance) (string, error) {
	restore := applyEnv(baseEnv(home), map[string]string{"AGENTDECK_PROFILE": profile})
	defer restore()

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return "", fmt.Errorf("panedrive: opening the sandbox profile: %w", err)
	}
	defer func() { _ = storage.Close() }()

	resolved := storage.Path()
	if !withinDir(home, resolved) {
		return "", fmt.Errorf(
			"panedrive: REFUSING TO SEED — the profile database resolved to %q, which is outside the sandbox %q. "+
				"That is the 2026-06-04 data-loss path: a run that believed it was isolated and was not", resolved, home)
	}
	if err := storage.Save(instances); err != nil {
		return "", fmt.Errorf("panedrive: seeding the sandbox profile: %w", err)
	}
	return resolved, nil
}

// applyEnv sets variables on this process and returns a restore func. The
// driver is a single-purpose binary, so mutating its own environment is safe;
// restoring anyway keeps one world from leaking into the next when several run
// in one process.
func applyEnv(sets ...map[string]string) func() {
	saved := map[string]*string{}
	remember := func(k string) {
		if _, done := saved[k]; done {
			return
		}
		if v, ok := os.LookupEnv(k); ok {
			c := v
			saved[k] = &c
			return
		}
		saved[k] = nil
	}
	for _, m := range sets {
		for k, v := range m {
			remember(k)
			_ = os.Setenv(k, v)
		}
	}
	return func() {
		for k, v := range saved {
			if v == nil {
				_ = os.Unsetenv(k)
				continue
			}
			_ = os.Setenv(k, *v)
		}
	}
}

// envPairs renders a world's environment for the artifact, with anything that
// could carry a secret elided. A run.json is committed to the repository.
func envPairs(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		if strings.Contains(strings.ToUpper(k), "TOKEN") ||
			strings.Contains(strings.ToUpper(k), "KEY") ||
			strings.Contains(strings.ToUpper(k), "SECRET") {
			out[k] = "<elided>"
			continue
		}
		out[k] = v
	}
	return out
}
