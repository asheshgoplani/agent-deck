// Package crossfixture supplies the shared scaffolding for tests that
// must verify TUI ↔ web ↔ CLI parity (TEST-PLAN.md §6.4 / TUI-TEST-PLAN.md
// §6.4 crossProcessFixture).
//
// The harness is intentionally driver-agnostic — it owns the *isolation*
// (HOME, ~/.agent-deck, env vars, optional binary path, optional web
// base URL) and the *parity check* (deep-equal of three snapshots).
// Concrete tests bring their own web Server / CLI binary / TUI driver.
//
// Why this shape:
//
//   - Booting `internal/web.Server` requires non-trivial wiring
//     (MenuData, push, cost store) that varies by test.
//   - Wiring teatest into a concrete *Home is intrusive and lives in
//     internal/ui where this package can't reach without an import cycle.
//
// So the fixture exposes hooks rather than auto-wired clients.
//
// Usage:
//
//	cf := crossfixture.New(t, crossfixture.Options{Profile: "test"})
//	srv := bootMyWebServer(cf.AgentDeckDir)
//	cf.AttachWeb(srv.URL)
//	cf.AttachCLI(buildBinary(t))
//
//	web := cf.MustGetBytes(t, "/api/sessions")
//	cli, _ := cf.RunCLI("list", "--json")
//	cf.AssertParity(t, crossfixture.Snapshots{CLI: cli, Web: web, TUI: web})
package crossfixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Options controls how the fixture seeds the environment.
type Options struct {
	// Profile sets AGENTDECK_PROFILE for both the in-process server and
	// the CLI subprocess. Empty leaves it unset.
	Profile string

	// AgentDeckSubdir is the relative path under HOME where ~/.agent-deck
	// lives. Defaults to ".agent-deck" — override only when emulating
	// non-standard installs.
	AgentDeckSubdir string
}

// TB is the subset of testing.TB used by Assert helpers. We avoid
// pulling in *testing.T so the package can be exercised under stub Ts in
// its own unit tests.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// Fixture is the live test scaffold.
type Fixture struct {
	Home         string // HOME for this test
	AgentDeckDir string // <Home>/<AgentDeckSubdir>
	WebURL       string // empty until AttachWeb
	CLIBinary    string // empty until AttachCLI

	t *testing.T
}

// New seeds env (HOME, AGENTDECK_PROFILE) under t.Setenv (auto-restored)
// and creates the agent-deck data dir.
func New(t *testing.T, opts Options) *Fixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	if opts.Profile != "" {
		t.Setenv("AGENTDECK_PROFILE", opts.Profile)
	}

	subdir := opts.AgentDeckSubdir
	if subdir == "" {
		subdir = ".agent-deck"
	}
	dir := filepath.Join(home, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("crossfixture: mkdir %s: %v", dir, err)
	}

	return &Fixture{
		Home:         home,
		AgentDeckDir: dir,
		t:            t,
	}
}

// AttachWeb records the base URL of an already-started web server. Tests
// stand the server up themselves (so they control the wiring of MenuData
// / pushService / SessionMutator); the fixture just remembers the URL
// for GetJSON / GetBytes calls.
func (f *Fixture) AttachWeb(baseURL string) {
	f.WebURL = baseURL
}

// AttachCLI records the path of an already-built agent-deck binary.
// Tests build it once via testing.Main / TestMain (or share a binary
// across tests) and pass the path here so RunCLI can exec it under the
// fixture's isolated env.
func (f *Fixture) AttachCLI(binaryPath string) {
	f.CLIBinary = binaryPath
}

// GetJSON GETs the path under WebURL and decodes into out. Fails the
// test on transport / decode errors.
func (f *Fixture) GetJSON(t TB, path string, out any) {
	t.Helper()
	body := f.MustGetBytes(t, path)
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("crossfixture: decode %s: %v\nbody: %s", path, err, body)
	}
}

// MustGetBytes returns the response body for GET WebURL+path or fails.
func (f *Fixture) MustGetBytes(t TB, path string) []byte {
	t.Helper()
	if f.WebURL == "" {
		t.Fatalf("crossfixture: no web server attached (call AttachWeb first)")
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(f.WebURL + path)
	if err != nil {
		t.Fatalf("crossfixture: GET %s: %v", path, err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("crossfixture: GET %s status=%d body=%s", path, resp.StatusCode, body)
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("crossfixture: read %s: %v", path, err)
		return nil
	}
	return body
}

// RunCLI runs the attached CLI binary with the given args under the
// fixture's isolated env. Returns the combined stdout+stderr output.
func (f *Fixture) RunCLI(args ...string) ([]byte, error) {
	if f.CLIBinary == "" {
		return nil, fmt.Errorf("crossfixture: no CLI binary attached (call AttachCLI first)")
	}
	cmd := exec.Command(f.CLIBinary, args...)
	// Inherit the test's env — t.Setenv has already injected HOME / profile.
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

// Snapshots is the three-way parity input. Tests fill in whichever
// channels they exercised; nil entries are skipped during AssertParity.
//
// The expected shape is "the canonical JSON representation of the same
// observable" — e.g. all three should be the deep-equal session-list
// JSON after sort. Tests are responsible for normalizing (sorting,
// stripping volatile fields like timestamps).
type Snapshots struct {
	CLI []byte
	Web []byte
	TUI []byte
}

// AssertParity fails the test if any two non-nil snapshots disagree
// byte-for-byte. The error message lists every snapshot so the
// divergence is one glance.
func (f *Fixture) AssertParity(t TB, s Snapshots) {
	t.Helper()
	type entry struct {
		name string
		body []byte
	}
	var entries []entry
	if s.CLI != nil {
		entries = append(entries, entry{"CLI", s.CLI})
	}
	if s.Web != nil {
		entries = append(entries, entry{"Web", s.Web})
	}
	if s.TUI != nil {
		entries = append(entries, entry{"TUI", s.TUI})
	}
	if len(entries) < 2 {
		return
	}
	ref := entries[0]
	for _, e := range entries[1:] {
		if !bytes.Equal(ref.body, e.body) {
			t.Errorf("crossfixture: parity violation: %s != %s\n%s=%s\n%s=%s",
				ref.name, e.name, ref.name, ref.body, e.name, e.body)
			return
		}
	}
}
