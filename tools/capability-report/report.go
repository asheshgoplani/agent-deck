// Command capability-report turns `go test -json` output from the capability
// E2E suite into a JSON manifest and a self-contained HTML dashboard.
//
// It is the typed, testable rendering step referenced by
// scripts/capability-e2e.sh. The manifest is the ground truth: every card on
// the dashboard traces back to one capability entry, and a re-run is one
// command. See docs/testing/2026-05-26-capability-e2e-strategy.md section 4.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Tier classifies how a capability is verified.
const (
	// TierF is the fast gate: deterministic, offline, isolated socket, runs on
	// every release.
	TierF = "F"
	// TierN is nightly / on-demand: real external agents, real SSH, real keys.
	// Not run in the fast pass; shown greyed on the dashboard.
	TierN = "N"
)

// Result statuses a capability can hold in the manifest.
const (
	StatusPass    = "pass"
	StatusFail    = "fail"
	StatusNightly = "nightly" // Tier N, not run in this fast pass.
	StatusNotRun  = "not-run" // Tier F capability with no matching test result.
)

// Capability is one row of the inventory: the static metadata plus the result
// filled in from a test run. Static fields come from the registry below; the
// dynamic fields (Status, RuntimeSeconds) are populated by BuildManifest.
type Capability struct {
	ID             string   `json:"id"`
	Group          string   `json:"group"`
	Title          string   `json:"title"`
	HowWeTest      string   `json:"how_we_test"`
	Assertion      string   `json:"assertion"`
	Tier           string   `json:"tier"`
	Chips          []string `json:"chips"`
	TestName       string   `json:"test_name,omitempty"`
	Status         string   `json:"status"`
	RuntimeSeconds float64  `json:"runtime_seconds"`
	// Snapshot is the real terminal pane (or CLI/registry output) captured at
	// the test's verification point. It is display-only proof, not asserted on;
	// empty when no snapshot was captured for this capability.
	Snapshot string `json:"snapshot,omitempty"`
}

// Summary is the headline strip on the dashboard.
type Summary struct {
	Total       int `json:"total"`
	Green       int `json:"green"`
	Failed      int `json:"failed"`
	NightlyOnly int `json:"nightly_only"`
	NotCovered  int `json:"not_covered"`
}

// Manifest is the full report: a snapshot timestamp, the summary, and every
// capability in inventory order.
type Manifest struct {
	GeneratedAt  string       `json:"generated_at"`
	Summary      Summary      `json:"summary"`
	Capabilities []Capability `json:"capabilities"`
}

// testResult is the pass/fail + runtime parsed for a single test function.
type testResult struct {
	status  string
	elapsed float64
}

// registry is the authoritative capability inventory for Wave 1. Test names
// map 1:1 to the capability tests in tests/capability/. Tier N rows have no
// test yet and are surfaced as honest, documented gaps (see
// docs/testing/capability-gaps.md), not faked green.
func registry() []Capability {
	const (
		chipLocal = "Local-isolated"
		chipStub  = "Deterministic-stub"
		chipReal  = "Real-agent"
		chipKey   = "Needs-creds"
		chipNet   = "Needs-network"
	)
	return []Capability{
		{
			ID: "add", Group: "Session lifecycle", TestName: "TestCapability_Lifecycle_Add",
			Title:     "Register a session",
			HowWeTest: "We run the add command to register a new session, then read the saved registry back. We confirm the row exists with the title, tool, group, and folder we asked for.",
			Assertion: "new registry row carries the given title, tool, group, and working directory",
			Tier:      TierF, Chips: []string{chipLocal},
		},
		{
			ID: "start", Group: "Session lifecycle", TestName: "TestCapability_Lifecycle_Start",
			Title:     "Start a session",
			HowWeTest: "We register a session and start it. We then ask the throwaway tmux server whether a live pane actually appeared, and confirm the registry flips the session to an active state.",
			Assertion: "a real tmux pane appears on the isolated socket and status becomes active",
			Tier:      TierF, Chips: []string{chipLocal},
		},
		{
			ID: "stop", Group: "Session lifecycle", TestName: "TestCapability_Lifecycle_Stop",
			Title:     "Stop a session",
			HowWeTest: "We start a session, then stop it. We confirm the tmux pane is gone and the registry returns the session to the stopped state.",
			Assertion: "tmux pane disappears and registry status returns to stopped",
			Tier:      TierF, Chips: []string{chipLocal},
		},
		{
			ID: "restart", Group: "Session lifecycle", TestName: "TestCapability_Lifecycle_Restart",
			Title:     "Restart a session",
			HowWeTest: "We start a session and restart it. We confirm exactly one pane exists afterward (no accidental duplicate) and the session is active again.",
			Assertion: "exactly one pane remains after restart and status is active (guards the #30 double-spawn)",
			Tier:      TierF, Chips: []string{chipLocal},
		},
		{
			ID: "rm", Group: "Session lifecycle", TestName: "TestCapability_Lifecycle_Rm",
			Title:     "Remove a session",
			HowWeTest: "We confirm a stopped session can be removed and disappears from the registry, and that removing a still-running session is refused unless forced, so it is not destroyed by accident.",
			Assertion: "stopped session leaves the registry; a running session is refused without force",
			Tier:      TierF, Chips: []string{chipLocal},
		},
		{
			ID: "launch", Group: "Session lifecycle", TestName: "TestCapability_Lifecycle_Launch",
			Title:     "Launch in one step",
			HowWeTest: "We use the single launch command, which creates, starts, and messages a session at once, pointed at the stand-in echo agent. We confirm the registry row exists and the echoed message shows up on screen.",
			Assertion: "one launch command creates the row, starts the pane, and the echoed message appears",
			Tier:      TierF, Chips: []string{chipLocal, chipStub},
		},
		{
			ID: "fork", Group: "Session lifecycle", TestName: "TestCapability_Lifecycle_Fork",
			Title:     "Fork guard",
			HowWeTest: "Forking is only valid for a Claude session with live context. We confirm forking a non-Claude session is cleanly refused and creates no orphan child row. The full context-inheriting fork is a documented nightly gap.",
			Assertion: "forking a non-Claude session is refused and no child row is created",
			Tier:      TierF, Chips: []string{chipLocal},
		},
		{
			ID: "send-output-echo", Group: "Agent interaction", TestName: "TestCapability_Agent_EchoRoundTrip",
			Title:     "Send a message to an agent and read its reply",
			HowWeTest: "We launch a tiny stand-in agent that simply repeats whatever you say. We send it a unique message through the normal send command, then read the screen back. If the screen shows the echoed message we know it reached the agent and a reply came out the other side.",
			Assertion: "the pane shows ECHO:<token> after a real send, proving readiness, send-keys, and capture read-back",
			Tier:      TierF, Chips: []string{chipLocal, chipStub},
		},
		// Tier N: documented honest gaps. No test in Wave 1; shown greyed.
		{
			ID: "send-output-claude", Group: "Agent interaction",
			Title:     "Real agent round trip (Claude)",
			HowWeTest: "We launch a real Claude session, wait for its prompt, send a fixed instruction, and check the reply. This needs a real API key and network, so it runs nightly, not on every release.",
			Assertion: "a real Claude reply contains the expected token",
			Tier:      TierN, Chips: []string{chipReal, chipKey, chipNet},
		},
		{
			ID: "fork-context", Group: "Agent interaction",
			Title:     "Fork inherits Claude context",
			HowWeTest: "We fork a live Claude session and confirm the child inherits the conversation with a distinct id and a parent link. This needs a real Claude session id from a live transcript, so it runs nightly.",
			Assertion: "child session links the parent and inherits conversation context",
			Tier:      TierN, Chips: []string{chipReal, chipKey, chipNet},
		},
		{
			ID: "mcp-attach", Group: "MCP", TestName: "TestCapability_MCP_AttachDetach",
			Title:     "Attach and detach an MCP",
			HowWeTest: "We register a stub tool server, attach it to a session, and read the session's .mcp.json file back to confirm the entry was written. We then detach it and confirm the entry is gone. We also confirm attaching an unknown server is refused and never writes a broken config.",
			Assertion: ".mcp.json gains the server entry on attach and loses it on detach; an unknown server is refused",
			Tier:      TierF, Chips: []string{chipLocal, chipStub},
		},
		{
			ID: "mcp-loads", Group: "MCP",
			Title:     "MCP actually loads in the agent",
			HowWeTest: "We attach a tool server and ask a real agent to list its tools, confirming the agent honors the attachment. This needs a real agent to introspect, so it runs nightly.",
			Assertion: "the agent lists the attached MCP server",
			Tier:      TierN, Chips: []string{chipReal, chipKey, chipNet},
		},
		{
			ID: "worktree", Group: "Worktrees", TestName: "TestCapability_Worktree_CreateFinish",
			Title:     "Create and finish a git worktree",
			HowWeTest: "Against a throwaway git repo, we create a session on a new branch in its own worktree and confirm the worktree directory really exists on disk. We then run finish, and confirm the worktree and branch are removed, the session is gone, and the ORIGINAL repository is untouched.",
			Assertion: "the worktree dir is created on its own branch, then finish removes it and the session while leaving the source repo intact (the #1200 data-loss guard)",
			Tier:      TierF, Chips: []string{chipLocal},
		},
		{
			ID: "groups", Group: "Groups and profiles", TestName: "TestCapability_Groups_Filtering",
			Title:     "Organize sessions into groups",
			HowWeTest: "We create sessions in two different groups and confirm that filtering the registry by group returns exactly that group's members, with no session bleeding across groups.",
			Assertion: "each group lists exactly its own sessions; no cross-group leakage",
			Tier:      TierF, Chips: []string{chipLocal},
		},
		{
			ID: "profiles", Group: "Groups and profiles", TestName: "TestCapability_Profiles_Isolation",
			Title:     "Keep profiles isolated",
			HowWeTest: "We add one session under the default profile and another under a separate profile, then list each profile. We confirm neither profile can see the other's session.",
			Assertion: "a session in one profile is invisible to the other (no cross-profile data bleed)",
			Tier:      TierF, Chips: []string{chipLocal},
		},
		{
			ID: "multitool", Group: "Multi-tool", TestName: "TestCapability_MultiTool_Readiness",
			Title:     "Bring multiple tool kinds to readiness",
			HowWeTest: "We launch two distinct stand-in tools and confirm each one reaches an active state AND echoes back the unique message we sent it, proving the launch and readiness machinery is not tied to a single tool.",
			Assertion: "two different tools each reach active and echo their token back",
			Tier:      TierF, Chips: []string{chipLocal, chipStub},
		},
		{
			ID: "conductor-finished", Group: "Conductor comms backbone", TestName: "TestCapability_Conductor_FinishedSignal",
			Title:     "Worker reports it finished",
			HowWeTest: "A worker prints a completion sentinel on its last turn. We run the real Stop-hook handler and confirm it records a done outcome (status and summary), then run the notifier daemon and confirm it emits a distinct finished signal to the parent instead of an ambiguous waiting. An ordinary turn with no sentinel records no completion.",
			Assertion: "the Stop-hook persists done status=ok, and the daemon emits a finished event carrying that outcome (issue #1186)",
			Tier:      TierF, Chips: []string{chipLocal, chipStub},
		},
		{
			ID: "conductor-dedup", Group: "Conductor comms backbone", TestName: "TestCapability_Conductor_Dedup",
			Title:     "An idle worker does not re-notify",
			HowWeTest: "We make a worker transition once, then run the notifier daemon twice over the same idle worker. We confirm exactly one notification is produced across both passes, proving the de-duplication ledger persists between polls.",
			Assertion: "two daemon passes over the same idle child emit exactly one event (issue #1187 dedup)",
			Tier:      TierF, Chips: []string{chipLocal, chipStub},
		},
	}
}

// ParseTestResults reads `go test -json` event lines and returns a map from
// test function name to its pass/fail status and elapsed seconds. Lines that
// are not JSON objects (e.g. build output) are ignored.
func ParseTestResults(jsonl []byte) map[string]testResult {
	results := make(map[string]testResult)
	for _, line := range bytes.Split(jsonl, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev struct {
			Action  string  `json:"Action"`
			Test    string  `json:"Test"`
			Elapsed float64 `json:"Elapsed"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Test == "" {
			continue
		}
		switch ev.Action {
		case "pass":
			results[ev.Test] = testResult{status: StatusPass, elapsed: ev.Elapsed}
		case "fail":
			results[ev.Test] = testResult{status: StatusFail, elapsed: ev.Elapsed}
		}
	}
	return results
}

// BuildManifest joins the static registry with parsed test results, computing
// each capability's status and the headline summary.
func BuildManifest(results map[string]testResult, generatedAt time.Time) Manifest {
	caps := registry()
	var sum Summary
	for i := range caps {
		c := &caps[i]
		switch {
		case c.Tier == TierN:
			c.Status = StatusNightly
		case c.TestName != "":
			if r, ok := results[c.TestName]; ok {
				c.Status = r.status
				c.RuntimeSeconds = r.elapsed
			} else {
				c.Status = StatusNotRun
			}
		default:
			c.Status = StatusNotRun
		}

		sum.Total++
		switch c.Status {
		case StatusPass:
			sum.Green++
		case StatusFail:
			sum.Failed++
		case StatusNightly:
			sum.NightlyOnly++
		case StatusNotRun:
			sum.NotCovered++
		}
	}
	return Manifest{
		GeneratedAt:  generatedAt.UTC().Format(time.RFC3339),
		Summary:      sum,
		Capabilities: caps,
	}
}

// HasFastFailure reports whether any Tier F capability failed. The gate script
// exits non-zero when this is true.
func (m Manifest) HasFastFailure() bool {
	for _, c := range m.Capabilities {
		if c.Tier == TierF && c.Status == StatusFail {
			return true
		}
	}
	return false
}

// AttachSnapshots fills each capability's Snapshot from a per-capability
// artifact file named "<id>.txt" inside dir, as written by the capability
// tests at their verification point. A missing dir or missing file is not an
// error: snapshots are optional display data, and a re-run without the tests
// (or a Tier N capability that never ran) simply leaves Snapshot empty. Files
// with no matching capability id are ignored.
func (m *Manifest) AttachSnapshots(dir string) {
	for i := range m.Capabilities {
		c := &m.Capabilities[i]
		raw, err := os.ReadFile(filepath.Join(dir, c.ID+".txt"))
		if err != nil {
			continue
		}
		if s := cleanSnapshot(string(raw)); s != "" {
			c.Snapshot = s
		}
	}
}

// noiseLine patterns are shell-login boilerplate that the sandbox's interactive
// login bash prints when a pane's shell starts (the Ubuntu MOTD), plus the bare
// shell-prompt lines that carry no agent-deck output. None of it proves a
// capability, so a snapshot must never show it. This is the render-time safety
// net; capture-time extraction (in tests/capability) is the primary defense.
var noiseLine = []*regexp.Regexp{
	regexp.MustCompile(`To run a command as administrator`),
	regexp.MustCompile(`man sudo_root`),
	regexp.MustCompile(`sudo_root`),
	regexp.MustCompile(`man sudo`),
	regexp.MustCompile(`Welcome to Ubuntu`),
	regexp.MustCompile(`^\s*\* (Documentation|Support|Management|Strictly|Get|Just)`),
	regexp.MustCompile(`^\s*Last login:`),
	regexp.MustCompile(`System (information|load|restart)`),
	// A bare interactive prompt line ("user@host:~/path$" possibly followed by
	// the shell-spawn command), which is pane chrome, not agent output.
	regexp.MustCompile(`^[^\s@]+@[^\s:]+:\S*[#$]\s*(bash -c .*)?$`),
}

// braille matches the spinner glyphs (U+2800..U+28FF) agent-deck's status line
// animates while an agent is "working"; those redraw frames leave garbled
// "working" / "...EADY >" fragments in a raw capture that mean nothing to a reader.
var braille = regexp.MustCompile(`[\x{2800}-\x{28FF}]`)

// isNoise reports whether a captured line is shell/MOTD/spinner chrome rather
// than meaningful agent-deck content.
func isNoise(line string) bool {
	if braille.MatchString(line) {
		return true
	}
	for _, re := range noiseLine {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// cleanSnapshot normalizes a captured pane for display: it strips carriage
// returns, drops shell-login MOTD / bare-prompt / spinner-noise lines, trims
// trailing whitespace on each line, collapses runs of blank lines to one, and
// drops leading and trailing blank lines. It keeps all meaningful content. The
// result is the text shown verbatim (HTML-escaped) in the dashboard's terminal
// block.
func cleanSnapshot(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "")
	var lines []string
	prevBlank := false
	for _, ln := range strings.Split(raw, "\n") {
		ln = strings.TrimRight(ln, " \t")
		if isNoise(ln) {
			continue
		}
		if ln == "" {
			if prevBlank {
				continue
			}
			prevBlank = true
		} else {
			prevBlank = false
		}
		lines = append(lines, ln)
	}
	start, end := 0, len(lines)
	for start < end && lines[start] == "" {
		start++
	}
	for end > start && lines[end-1] == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// groupedView is the per-group slice handed to the template.
type groupedView struct {
	Name string
	Caps []Capability
}

// grouped returns capabilities bucketed by Group, preserving first-seen group
// order so the dashboard sections match the inventory order.
func (m Manifest) grouped() []groupedView {
	var order []string
	buckets := map[string][]Capability{}
	for _, c := range m.Capabilities {
		if _, ok := buckets[c.Group]; !ok {
			order = append(order, c.Group)
		}
		buckets[c.Group] = append(buckets[c.Group], c)
	}
	out := make([]groupedView, 0, len(order))
	for _, g := range order {
		out = append(out, groupedView{Name: g, Caps: buckets[g]})
	}
	return out
}

// statusClass maps a status to the dashboard's color class.
func statusClass(status string) string {
	switch status {
	case StatusPass:
		return "green"
	case StatusFail:
		return "red"
	case StatusNightly:
		return "grey"
	default:
		return "amber"
	}
}

// statusLabel is the human-readable status word on a card.
func statusLabel(status string) string {
	switch status {
	case StatusPass:
		return "PASS"
	case StatusFail:
		return "FAIL"
	case StatusNightly:
		return "NIGHTLY"
	default:
		return "NOT RUN"
	}
}

// runtimeLabel formats the measured runtime, or a dash placeholder when the
// capability did not run in this pass.
func runtimeLabel(c Capability) string {
	if c.Status == StatusPass || c.Status == StatusFail {
		return fmt.Sprintf("%.1fs", c.RuntimeSeconds)
	}
	return "not measured"
}

var dashboardTmpl = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"statusClass":  statusClass,
	"statusLabel":  statusLabel,
	"runtimeLabel": runtimeLabel,
}).Parse(dashboardHTML))

// RenderDashboard renders the full self-contained HTML dashboard from a
// manifest. The output is deterministic for a given manifest, which is what
// makes it unit-testable.
func RenderDashboard(m Manifest) (string, error) {
	var buf bytes.Buffer
	data := struct {
		Manifest
		Groups []groupedView
	}{Manifest: m, Groups: m.grouped()}
	if err := dashboardTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// dashboardHTML is the self-contained template. Style rules: inline CSS, system
// font stack, the green/amber/red CSS variables from the testing-overview page,
// no em-dash separators, no emoji. Status is shown with a colored dot plus a
// word so it reads without color alone.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>agent-deck Capability E2E Dashboard</title>
<style>
  :root {
    --green: #16a34a;
    --amber: #d97706;
    --red: #dc2626;
    --grey: #8b949e;
    --ink: #1f2328;
    --muted: #57606a;
    --line: #d8dee4;
    --bg: #f6f8fa;
  }
  * { box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    color: var(--ink); line-height: 1.55; margin: 0; background: #fff;
  }
  .wrap { max-width: 1100px; margin: 0 auto; padding: 40px 24px 80px; }
  header.page { border-bottom: 2px solid var(--line); padding-bottom: 20px; margin-bottom: 28px; }
  header.page h1 { font-size: 28px; margin: 0 0 6px; letter-spacing: -0.02em; }
  header.page .date { color: var(--muted); font-size: 14px; }
  header.page .pitch { font-size: 15px; color: var(--ink); margin-top: 12px; max-width: 780px; }
  h2 { font-size: 20px; margin: 36px 0 12px; letter-spacing: -0.01em; }
  .muted { color: var(--muted); }
  .summary { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px; margin: 16px 0 8px; }
  .stat { border: 1px solid var(--line); border-radius: 10px; padding: 14px 16px; background: #fff; }
  .stat .label { font-size: 12px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted); }
  .stat .value { font-size: 26px; font-weight: 650; margin-top: 4px; }
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 14px; margin: 12px 0; }
  .card { border: 1px solid var(--line); border-radius: 10px; padding: 16px 18px; box-shadow: 0 1px 2px rgba(0,0,0,0.04); background: #fff; }
  .card.green { border-top: 3px solid var(--green); }
  .card.amber { border-top: 3px solid var(--amber); }
  .card.red { border-top: 3px solid var(--red); }
  .card.grey { border-top: 3px solid var(--grey); }
  .card h3 { font-size: 15px; margin: 0 0 8px; }
  .status { font-size: 12px; font-weight: 650; letter-spacing: 0.04em; }
  .dot { display: inline-block; width: 9px; height: 9px; border-radius: 50%; margin-right: 6px; vertical-align: middle; }
  .dot.green { background: var(--green); }
  .dot.amber { background: var(--amber); }
  .dot.red { background: var(--red); }
  .dot.grey { background: var(--grey); }
  .how { font-size: 13.5px; color: var(--ink); margin: 8px 0; }
  .assert { font-size: 12.5px; color: var(--muted); margin: 6px 0; }
  .meta { font-size: 12px; color: var(--muted); margin-top: 8px; }
  .chips { margin-top: 8px; }
  .chip { display: inline-block; font-size: 11px; background: var(--bg); border: 1px solid var(--line); border-radius: 999px; padding: 2px 9px; margin: 2px 4px 2px 0; color: var(--muted); }
  .term { margin-top: 12px; }
  .term .term-label { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted); margin-bottom: 5px; }
  .terminal {
    margin: 0; background: #0d1117; color: #e6edf3; border: 1px solid #30363d; border-radius: 8px;
    padding: 12px 14px; font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
    font-size: 12px; line-height: 1.45; tab-size: 4;
    /* Wrap long lines so the meaningful content is fully visible without a
       horizontal scrollbar clipping it (Wave 1.5 overflow fix). */
    white-space: pre-wrap; word-break: break-word; overflow-wrap: anywhere;
  }
  footer { margin-top: 40px; padding-top: 16px; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--muted); }
</style>
</head>
<body>
<div class="wrap">
<header class="page">
  <h1>agent-deck Capability E2E Dashboard</h1>
  <div class="date">Generated {{.GeneratedAt}}</div>
  <p class="pitch">One card per capability agent-deck promises a user can do. Each fast-gate capability is verified by a test that performs the real action through the compiled binary on an isolated tmux socket and asserts on the real effect: registry rows or live pane content. Nightly capabilities need real agents, keys, or network and run out of band.</p>
</header>

<section class="summary">
  <div class="stat"><div class="label">Capabilities</div><div class="value">{{.Summary.Total}}</div></div>
  <div class="stat"><div class="label">Green</div><div class="value">{{.Summary.Green}}</div></div>
  <div class="stat"><div class="label">Failed</div><div class="value">{{.Summary.Failed}}</div></div>
  <div class="stat"><div class="label">Nightly only</div><div class="value">{{.Summary.NightlyOnly}}</div></div>
  <div class="stat"><div class="label">Not yet covered</div><div class="value">{{.Summary.NotCovered}}</div></div>
</section>

{{range .Groups}}
<h2>{{.Name}}</h2>
<div class="cards">
  {{range .Caps}}
  <div class="card {{statusClass .Status}}">
    <div class="status"><span class="dot {{statusClass .Status}}"></span>{{statusLabel .Status}}</div>
    <h3>{{.Title}}</h3>
    <p class="how">{{.HowWeTest}}</p>
    <p class="assert">Pass when: {{.Assertion}}</p>
    <div class="meta">Tier {{.Tier}} . Runtime: {{runtimeLabel .}}{{if .TestName}} . {{.TestName}}{{end}}</div>
    <div class="chips">{{range .Chips}}<span class="chip">{{.}}</span>{{end}}</div>
    {{if .Snapshot}}<div class="term"><div class="term-label">What the terminal showed at verification</div><pre class="terminal">{{.Snapshot}}</pre></div>{{end}}
  </div>
  {{end}}
</div>
{{end}}

<footer>
  Rendered from docs/status/capability-e2e-manifest.json by tools/capability-report. Regenerate with scripts/capability-e2e.sh. Honest gaps are tracked in docs/testing/capability-gaps.md.
</footer>
</div>
</body>
</html>
`
