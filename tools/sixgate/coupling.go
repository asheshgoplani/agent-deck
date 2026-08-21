package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/artifact"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/oracle"
)

// mustLabelCoupling is the check that turns G4's rule from a sentence into a
// mechanism.
//
// G4's rule is: a figure with no source of truth may ship only if the screen
// says it is an estimate. G4 writes the list of such figures; G2 asserts the
// markers are rendered on the recorded frames. Neither gate alone closes the
// loop — G4 could emit a list nobody read, and G2 could pass against a list
// written before the oracle got stricter. So the verdict compares the digest of
// the contract G2 recorded obeying against the file G4 actually wrote.
//
// The failure this prevents is specific and cheap to fall into: run the assert,
// then regenerate the oracle, then ship. Both artifacts are green and the
// second one describes obligations the first never checked.
func mustLabelCoupling(t artifact.Tree) []string {
	g2, _ := artifact.GateByID(artifact.G2)
	g4, _ := artifact.GateByID(artifact.G4)
	contract := filepath.Join(t.GateDir(g4), oracle.MustLabelJSONFile)
	results := filepath.Join(t.GateDir(g2), "results.json")

	onDisk, err := oracle.Digest(contract)
	if err != nil {
		return []string{"cannot read " + t.Rel(contract) + ": " + err.Error()}
	}

	raw, err := os.ReadFile(results) //nolint:gosec // path derived from the gate tree
	if err != nil {
		if os.IsNotExist(err) {
			// G2's own presence check already reports this; saying it twice
			// would bury the real problem in a duplicate.
			return nil
		}
		return []string{"cannot read " + t.Rel(results) + ": " + err.Error()}
	}
	var probe struct {
		MustLabel struct {
			Present bool   `json:"present"`
			SHA256  string `json:"sha256"`
			Entries int    `json:"entries"`
			Error   string `json:"error"`
		} `json:"must_label"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return []string{t.Rel(results) + " is not readable JSON: " + err.Error()}
	}

	switch {
	case onDisk == "" && probe.MustLabel.Present:
		return []string{fmt.Sprintf(
			"G2 recorded obeying a must-label contract that is no longer on disk (%s) — the on-screen estimate labels it asserted are no longer required by any oracle, so one of the two gates is describing software the other is not",
			t.Rel(contract))}
	case onDisk == "":
		return []string{fmt.Sprintf(
			"G4 wrote no %s. Every unoracled figure must be declared, and G2 must assert its on-screen label; without the contract, \"G2 passed\" does not mean the estimates admit they are estimates",
			t.Rel(contract))}
	case probe.MustLabel.Error != "":
		return []string{"G2 could not read the must-label contract: " + probe.MustLabel.Error}
	case !probe.MustLabel.Present:
		return []string{fmt.Sprintf(
			"%s exists but G2 ran without it — re-run: sixgate assert %s",
			t.Rel(contract), t.Slug)}
	case probe.MustLabel.SHA256 != onDisk:
		return []string{fmt.Sprintf(
			"G2 obeyed must-label contract %s but %s now hashes to %s — the oracle was regenerated after the assert run, so results.json satisfies a contract that no longer exists. Re-run: sixgate assert %s",
			short(probe.MustLabel.SHA256), t.Rel(contract), short(onDisk), t.Slug)}
	}
	return nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
