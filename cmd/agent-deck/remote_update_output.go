package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func remoteOutcomeName(outcome session.RemoteUpdateOutcome) string {
	switch outcome {
	case session.RemoteUpdateOutcomeUpdated:
		return "updated"
	case session.RemoteUpdateOutcomeCurrent:
		return "current"
	case session.RemoteUpdateOutcomeSkipped:
		return "skipped"
	default:
		return "failed"
	}
}

func writeRemoteUpdateJSON(w io.Writer, results []session.RemoteUpdateResult) error {
	type row struct {
		Name    string `json:"name"`
		Host    string `json:"host"`
		From    string `json:"from,omitempty"`
		To      string `json:"to"`
		Outcome string `json:"outcome"`
		Note    string `json:"note,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	rows := make([]row, 0, len(results))
	for _, r := range results {
		entry := row{Name: r.Name, Host: r.Host, From: r.From, To: r.To, Outcome: remoteOutcomeName(r.Outcome), Note: r.Note}
		if r.Err != nil {
			entry.Error = r.Err.Error()
		}
		rows = append(rows, entry)
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(rows)
}

func printRemoteUpdateTable(w io.Writer, results []session.RemoteUpdateResult) {
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "REMOTE\tFROM\tTO\tRESULT")
	for _, r := range results {
		from := r.From
		if from == "" {
			from = "unknown"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", r.Name, from, r.To, remoteOutcomeName(r.Outcome))
	}
	_ = table.Flush()
	fmt.Fprintln(w, remoteUpdateSummary(results))
}
