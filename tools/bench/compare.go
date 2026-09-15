package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
)

type comparisonRun struct {
	SchemaVersion int                `json:"schema_version"`
	Platform      string             `json:"platform"`
	Machine       string             `json:"machine"`
	Seed          int64              `json:"seed"`
	Runs          int                `json:"runs"`
	Metrics       []comparisonMetric `json:"metrics"`
}

type comparisonMetric struct {
	Size    int       `json:"size"`
	Name    string    `json:"name"`
	Unit    string    `json:"unit"`
	Samples []float64 `json:"samples"`
	P50     float64   `json:"p50"`
	P95     float64   `json:"p95"`
}

func readComparison(path string) (comparisonRun, error) {
	var run comparisonRun
	data, err := os.ReadFile(path)
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal(data, &run); err != nil {
		return run, err
	}
	if run.SchemaVersion != 1 || run.Platform == "" || (run.Machine == "" || run.Machine == "unspecified") || run.Runs < 1 || len(run.Metrics) == 0 {
		return run, fmt.Errorf("%s: invalid or incomplete run metadata", path)
	}
	seen := map[string]bool{}
	for _, metric := range run.Metrics {
		key := fmt.Sprintf("%d/%s", metric.Size, metric.Name)
		if seen[key] || metric.Name == "" || metric.Unit == "" || len(metric.Samples) == 0 {
			return run, fmt.Errorf("%s: duplicate or incomplete metric %s", path, key)
		}
		seen[key] = true
		values := append([]float64{metric.P50, metric.P95}, metric.Samples...)
		for _, v := range values {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return run, fmt.Errorf("%s: invalid value for %s", path, key)
			}
		}
		samples := append([]float64(nil), metric.Samples...)
		sort.Float64s(samples)
		for _, quantile := range []struct{ q, value float64 }{{0.50, metric.P50}, {0.95, metric.P95}} {
			expected := samples[int(math.Ceil(quantile.q*float64(len(samples))))-1]
			if math.Abs(expected-quantile.value) > 1e-6*math.Max(1, expected) {
				return run, fmt.Errorf("%s: inconsistent quantile for %s", path, key)
			}
		}
	}
	return run, nil
}

func compare(args []string) error {
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	baselinePath := flags.String("baseline", "", "baseline JSON")
	runPath := flags.String("run", "", "candidate JSON")
	threshold := flags.Float64("threshold", 0.25, "maximum fractional p95 increase")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *baselinePath == "" || *runPath == "" || flags.NArg() != 0 || *threshold < 0 || math.IsNaN(*threshold) || math.IsInf(*threshold, 0) {
		return fmt.Errorf("compare requires -baseline FILE -run FILE and a finite nonnegative -threshold")
	}
	baseline, err := readComparison(*baselinePath)
	if err != nil {
		return err
	}
	candidate, err := readComparison(*runPath)
	if err != nil {
		return err
	}
	return compareRuns(baseline, candidate, *threshold)
}

func compareRuns(baseline, candidate comparisonRun, threshold float64) error {
	if baseline.SchemaVersion != candidate.SchemaVersion || baseline.Platform != candidate.Platform || baseline.Machine != candidate.Machine || baseline.Seed != candidate.Seed || baseline.Runs != candidate.Runs {
		return fmt.Errorf("incompatible run metadata: schema, platform, machine class, seed and run count must match")
	}
	if len(baseline.Metrics) != len(candidate.Metrics) {
		return fmt.Errorf("metric sets differ")
	}
	current := map[string]comparisonMetric{}
	for _, metric := range candidate.Metrics {
		current[fmt.Sprintf("%d/%s", metric.Size, metric.Name)] = metric
	}
	failed := false
	fmt.Println("| Sessions | Metric | Baseline p95 | Run p95 | Limit | Result |")
	fmt.Println("|---:|---|---:|---:|---:|---|")
	for _, before := range baseline.Metrics {
		key := fmt.Sprintf("%d/%s", before.Size, before.Name)
		after, ok := current[key]
		if !ok || before.Unit != after.Unit || len(before.Samples) != len(after.Samples) {
			return fmt.Errorf("missing or incompatible metric %s", key)
		}
		limit := before.P95 * (1 + threshold)
		status := "PASS"
		if after.P95 > limit {
			status = "REGRESSION"
			failed = true
		}
		fmt.Printf("| %d | %s (%s) | %.3f | %.3f | %.3f | %s |\n", before.Size, before.Name, before.Unit, before.P95, after.P95, limit, status)
	}
	if failed {
		return fmt.Errorf("p95 regression exceeds %.1f%% threshold", threshold*100)
	}
	return nil
}
