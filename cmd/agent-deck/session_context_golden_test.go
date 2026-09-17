package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxfixture"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/registry"
)

// The golden suite asserts `agent-deck session context --json` against recorded
// fixtures.
//
// What it is for: harness version drift. Claude Code and Codex CLI both update
// themselves, and when a record type is renamed or reshaped the inspector's
// failure mode is not a crash — it is a category that quietly reports nothing.
// A byte-for-byte assertion turns that into a CI failure with a readable diff.
//
// What is asserted byte-for-byte is the document's `report` member: every
// figure, every provenance, every caveat, every absolute path. The wrapper
// around it (totals, the token-accounting flag, the window percentage) is
// derived from the report by construction, so it is asserted as invariants
// instead — a golden of a value that is computed from another golden would fail
// twice for one cause and could never fail on its own.
//
// Regenerating: run with -update after confirming the change is intended. A
// golden that is regenerated without reading the diff is worse than no golden.

// updateGolden regenerates the recorded documents instead of asserting on them.
var updateGolden = os.Getenv("AGENTDECK_UPDATE_CONTEXT_GOLDEN") != ""

// managedClaudePolicyPaths are machine-wide policy files Claude Code loads
// before anything else. They belong to the host, not to a fixture, so a case
// that reconstructs a memory hierarchy cannot be asserted on a machine that has
// one: the report would correctly contain a file the golden never recorded.
var managedClaudePolicyPaths = []string{
	"/Library/Application Support/ClaudeCode/CLAUDE.md",
	"/etc/claude-code/CLAUDE.md",
}

func hostManagedClaudePolicy() (string, bool) {
	for _, p := range managedClaudePolicyPaths {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// fixtureParent returns a short-lived parent directory for a fixture root.
//
// It is deliberately not t.TempDir(): Go derives that path from the test's
// name, so a long subtest name would leave no room to pad the root to
// [ctxfixture.FixedRootLength] and the case would skip instead of assert.
func fixtureParent(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ctxfix")
	if err != nil {
		t.Fatalf("creating a fixture parent directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// contextGoldenView inspects one fixture and wraps it exactly as the CLI does.
func contextGoldenView(t *testing.T, c *ctxfixture.Case) (contextView, string) {
	t.Helper()
	// Neutralize the environment variables that would change what a fixture
	// loads, so a developer's shell cannot make this suite fail for a reason
	// unrelated to the code.
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "")
	t.Setenv("AGENTDECK_CONTEXT_WINDOW", "")
	if c.Adapter == "claude" {
		if path, ok := hostManagedClaudePolicy(); ok {
			t.Skipf("this machine has a managed Claude policy file at %s, which the fixture's memory hierarchy cannot account for", path)
		}
	}

	req, root, err := c.MaterializeFixed(fixtureParent(t))
	if err != nil {
		if errors.Is(err, ctxfixture.ErrParentTooLong) {
			t.Skipf("this machine's temporary directory leaves no room for a fixed-length fixture root: %v", err)
		}
		t.Fatalf("materializing %s: %v", c.Name, err)
	}
	rep, err := registry.Default().Inspect(context.Background(), req)
	if err != nil {
		t.Fatalf("inspecting %s: %v", c.Name, err)
	}
	return contextView{
		Ref:     c.Title,
		Title:   c.Title,
		Profile: c.Profile,
		Tool:    c.Tool,
		Report:  rep,
	}, root
}

// TestSessionContextJSONGolden asserts the report document byte-for-byte.
func TestSessionContextJSONGolden(t *testing.T) {
	cases, err := ctxfixture.LoadAll()
	if err != nil {
		t.Fatalf("loading the fixture corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("the fixture corpus is empty: this suite would pass without checking anything")
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			view, root := contextGoldenView(t, c)
			doc := buildContextJSON(view)

			raw, err := json.MarshalIndent(doc.Report, "", "  ")
			if err != nil {
				t.Fatalf("marshalling the report: %v", err)
			}
			got := ctxfixture.Redact(string(raw), root) + "\n"

			if updateGolden {
				if err := c.WriteGolden([]byte(got)); err != nil {
					t.Fatalf("recording the golden for %s: %v", c.Name, err)
				}
				t.Logf("recorded the document for %s; re-run without AGENTDECK_UPDATE_CONTEXT_GOLDEN to assert on it", c.Name)
				return
			}
			want, ok := c.Golden()
			if !ok {
				t.Fatalf("case %q has no recorded document; regenerate with AGENTDECK_UPDATE_CONTEXT_GOLDEN=1", c.Name)
			}
			if got != string(want) {
				t.Fatalf("the report document changed for %s.\n"+
					"If a harness format changed, that is what this test is for: read the diff before regenerating.\n%s",
					c.Name, firstDifference(string(want), got))
			}
		})
	}
}

// TestSessionContextJSONWrapperIsDerivedFromTheReport asserts the parts of the
// document that are computed rather than recorded.
//
// They are checked as invariants and not as a golden because they are functions
// of the report: recording them would produce a second file that can only ever
// fail for the same reason as the first, while proving nothing about the rule
// that links them.
func TestSessionContextJSONWrapperIsDerivedFromTheReport(t *testing.T) {
	cases, err := ctxfixture.LoadAll()
	if err != nil {
		t.Fatalf("loading the fixture corpus: %v", err)
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			view, _ := contextGoldenView(t, c)
			doc := buildContextJSON(view)

			if doc.SchemaVersion != contextSchemaVersion {
				t.Errorf("schema version = %d, want %d", doc.SchemaVersion, contextSchemaVersion)
			}
			if doc.Session.Tool != c.Tool || doc.Session.Title != c.Title {
				t.Errorf("session identity = %+v, want tool %q title %q", doc.Session, c.Tool, c.Title)
			}

			fixed, fixedComplete := view.Report.FixedTotal()
			if doc.Totals.Fixed.Tokens != fixed || doc.Totals.Fixed.Complete != fixedComplete {
				t.Errorf("fixed total = %+v, want %d (complete=%v)", doc.Totals.Fixed, fixed, fixedComplete)
			}
			attributed, attributedComplete := view.Report.AttributedTotal()
			if doc.Totals.Attributed.Tokens != attributed || doc.Totals.Attributed.Complete != attributedComplete {
				t.Errorf("attributed total = %+v, want %d (complete=%v)", doc.Totals.Attributed, attributed, attributedComplete)
			}
			if !doc.Totals.Fixed.Complete && !strings.HasPrefix(doc.Totals.Fixed.Note, "LOWER BOUND") {
				t.Error("an incomplete total must be labelled a lower bound")
			}

			// A percentage of an unknown denominator is exactly the
			// plausible-looking lie this feature exists to prevent.
			if !view.Report.Window.Known() && doc.Totals.WindowPercent != nil {
				t.Error("a window percentage was published without a known window size")
			}

			// Potential must never reach the gauge.
			if potential, any := view.Report.PotentialTotal(); any {
				if doc.Totals.Potential == nil {
					t.Fatal("a report with potential cost must publish it")
				}
				if doc.Totals.Potential.Tokens != potential {
					t.Errorf("potential = %d, want %d", doc.Totals.Potential.Tokens, potential)
				}
				if doc.Totals.Fixed.Tokens >= fixed+potential && potential > 0 {
					t.Error("potential cost leaked into the fixed total")
				}
			}
		})
	}
}

// TestSessionContextJSONEncodesUnknownsAsNull is the wire-format half of the
// package's central promise: a consumer must not be able to read a number that
// does not exist. The resumed and unknown-version fixtures both have an unknown
// remainder, and it has to arrive as null rather than as 0.
func TestSessionContextJSONEncodesUnknownsAsNull(t *testing.T) {
	cases, err := ctxfixture.LoadAll()
	if err != nil {
		t.Fatalf("loading the fixture corpus: %v", err)
	}

	checked := 0
	for _, c := range cases {
		if c.Expect.Anchored {
			continue
		}
		checked++
		t.Run(c.Name, func(t *testing.T) {
			view, _ := contextGoldenView(t, c)
			raw, err := json.Marshal(buildContextJSON(view))
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}

			var doc struct {
				Report struct {
					Anchor         *json.RawMessage `json:"anchor"`
					Reconciliation struct {
						Unaccounted struct {
							Tokens *int   `json:"tokens"`
							Prov   string `json:"provenance"`
							Reason string `json:"reason"`
						} `json:"unaccounted"`
					} `json:"reconciliation"`
				} `json:"report"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			if doc.Report.Anchor != nil {
				t.Error("a case that cannot be anchored must encode no anchor at all")
			}
			if doc.Report.Reconciliation.Unaccounted.Tokens != nil {
				t.Errorf("the unknown remainder encoded as %d; it must be null", *doc.Report.Reconciliation.Unaccounted.Tokens)
			}
			if doc.Report.Reconciliation.Unaccounted.Prov != "unknown" {
				t.Errorf("provenance = %q, want unknown", doc.Report.Reconciliation.Unaccounted.Prov)
			}
			if strings.TrimSpace(doc.Report.Reconciliation.Unaccounted.Reason) == "" {
				t.Error("an unknown must carry the reason there is no number")
			}
		})
	}
	if checked == 0 {
		t.Fatal("no negative fixture is present: the unknown-encoding promise is untested")
	}
}

// TestSessionContextJSONIsByteStable guards the golden itself: a document that
// re-marshalled differently from one run to the next would make every assertion
// above flap, and the first symptom would be a maintainer regenerating the
// golden to make CI green.
func TestSessionContextJSONIsByteStable(t *testing.T) {
	cases, err := ctxfixture.LoadAll()
	if err != nil {
		t.Fatalf("loading the fixture corpus: %v", err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			view, _ := contextGoldenView(t, c)
			first, err := json.MarshalIndent(buildContextJSON(view), "", "  ")
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			second, err := json.MarshalIndent(buildContextJSON(view), "", "  ")
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			if string(first) != string(second) {
				t.Fatal("the document is not byte-stable across two marshals of the same report")
			}
		})
	}
}

// TestSessionContextGoldenCoversBothHarnessesAndBothOutcomes checks the corpus
// itself. A golden suite is only as good as its cases, and the two that matter
// most are the ones that must NOT produce a number.
func TestSessionContextGoldenCoversBothHarnessesAndBothOutcomes(t *testing.T) {
	cases, err := ctxfixture.LoadAll()
	if err != nil {
		t.Fatalf("loading the fixture corpus: %v", err)
	}

	adapters := map[string]bool{}
	var anchored, unanchored int
	for _, c := range cases {
		adapters[c.Adapter] = true
		if c.Expect.Anchored {
			anchored++
		} else {
			unanchored++
		}
	}
	for _, want := range []string{"claude", "codex"} {
		if !adapters[want] {
			t.Errorf("the corpus has no %s case", want)
		}
	}
	if anchored == 0 || unanchored == 0 {
		t.Errorf("the corpus has %d anchored and %d unanchored cases; both outcomes must be recorded", anchored, unanchored)
	}
}

// firstDifference renders the first differing line of two documents, so a
// failure names the change instead of printing two thousand-line blobs.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, g := "", ""
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w != g {
			return fmt.Sprintf("first difference at line %d:\n  recorded: %s\n  produced: %s", i+1, w, g)
		}
	}
	return "the documents differ only in length"
}
