package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompareRejectsRegressionAndMissingCoverage(t *testing.T) {
	baseline := comparisonRun{SchemaVersion: 1, Platform: "linux/arm64", Machine: "test", Seed: 42, Runs: 1, Metrics: []comparisonMetric{{Size: 10, Name: "list", Unit: "ms", Samples: []float64{10}, P50: 10, P95: 10}}}
	candidate := baseline
	candidate.Metrics = append([]comparisonMetric(nil), baseline.Metrics...)
	if err := compareRuns(baseline, candidate, 0.25); err != nil {
		t.Fatal(err)
	}
	candidate.Metrics[0].P95 = 13
	if err := compareRuns(baseline, candidate, 0.25); err == nil {
		t.Fatal("accepted 30% regression")
	}
	candidate.Metrics = nil
	if err := compareRuns(baseline, candidate, 0.25); err == nil {
		t.Fatal("accepted missing metric")
	}
	candidate = baseline
	candidate.Machine = "different"
	if err := compareRuns(baseline, candidate, 0.25); err == nil {
		t.Fatal("accepted incomparable machine")
	}
}

func TestReadComparisonRejectsCorruptEvidence(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"schema_version":1,"platform":"linux","machine":"test","runs":1,"metrics":[{"size":10,"name":"list","unit":"ms","samples":[10],"p50":10,"p95":9}]}`,
		`{"schema_version":1,"platform":"linux","machine":"test","runs":1,"metrics":[{"size":10,"name":"list","unit":"ms","samples":[-1],"p50":-1,"p95":-1}]}`,
	} {
		path := filepath.Join(t.TempDir(), "run.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readComparison(path); err == nil {
			t.Fatalf("accepted corrupt run: %s", body)
		}
	}
}
