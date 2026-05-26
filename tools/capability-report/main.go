package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func main() {
	jsonInput := flag.String("json-input", "-", "path to `go test -json` output, or - for stdin")
	manifestPath := flag.String("manifest", "docs/status/capability-e2e-manifest.json", "where to write the JSON manifest")
	dashboardPath := flag.String("dashboard", "docs/status/capability-dashboard.html", "where to write the HTML dashboard")
	flag.Parse()

	raw, err := readInput(*jsonInput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capability-report: read input: %v\n", err)
		os.Exit(2)
	}

	results := ParseTestResults(raw)
	manifest := BuildManifest(results, time.Now())

	if err := writeManifest(*manifestPath, manifest); err != nil {
		fmt.Fprintf(os.Stderr, "capability-report: write manifest: %v\n", err)
		os.Exit(2)
	}
	html, err := RenderDashboard(manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capability-report: render dashboard: %v\n", err)
		os.Exit(2)
	}
	if err := writeFile(*dashboardPath, []byte(html)); err != nil {
		fmt.Fprintf(os.Stderr, "capability-report: write dashboard: %v\n", err)
		os.Exit(2)
	}

	s := manifest.Summary
	fmt.Printf("capability-report: %d capabilities, %d green, %d failed, %d nightly, %d not covered\n",
		s.Total, s.Green, s.Failed, s.NightlyOnly, s.NotCovered)
	fmt.Printf("capability-report: wrote %s and %s\n", *manifestPath, *dashboardPath)

	// Exit non-zero when a fast-gate capability failed so the gate script can
	// block the release. Tier N failures do not fail the fast gate.
	if manifest.HasFastFailure() {
		fmt.Fprintln(os.Stderr, "capability-report: a Tier F capability FAILED")
		os.Exit(1)
	}
}

func readInput(path string) ([]byte, error) {
	if path == "-" || path == "" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func writeManifest(path string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFile(path, data)
}

func writeFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}
