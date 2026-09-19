package costs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/google/uuid"
)

// Usage extraction is the recall Claude reader's (internal/recall/reader):
// the transcript is decoded once, with the same decoder that indexes its
// text, and the usage records land here. `costs sync` still works without
// the index (it walks the transcript itself), and a recall backfill or
// sweep hands the same events to ImportUsage for every conversation bound
// to a deck session, so a 4 GB corpus is not read a second time for costs.

// SyncResult holds the result of a historical sync operation.
type SyncResult struct {
	SessionsScanned int
	EventsImported  int
	EventsSkipped   int
	Errors          []string
}

// SyncSession holds the info needed to locate a session's transcript.
type SyncSession struct {
	InstanceID      string
	ClaudeSessionID string
	ProjectPath     string
	Tool            string
}

// SyncFromTranscripts reads historical usage from Claude transcript files
// and backfills cost_events for managed sessions.
func SyncFromTranscripts(store *Store, pricer *Pricer, sessions []SyncSession) SyncResult {
	var result SyncResult

	home, err := os.UserHomeDir()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("get home dir: %v", err))
		return result
	}

	importer := NewUsageImporter(store, pricer)
	for _, sess := range sessions {
		if sess.Tool != "claude" || sess.ClaudeSessionID == "" {
			continue
		}

		result.SessionsScanned++

		// Derive transcript path: ~/.claude/projects/<slugified-path>/<session-id>.jsonl
		sluggedPath := slugifyProjectPath(sess.ProjectPath)
		transcriptPath := filepath.Join(home, ".claude", "projects", sluggedPath, sess.ClaudeSessionID+".jsonl")

		if _, err := os.Stat(transcriptPath); os.IsNotExist(err) {
			continue
		}

		events, err := reader.ScanClaudeUsage(context.Background(), transcriptPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("scan %s: %v", transcriptPath, err))
		}
		imported, skipped, errs := importer.Import(sess.InstanceID, events)
		result.EventsImported += imported
		result.EventsSkipped += skipped
		result.Errors = append(result.Errors, errs...)
	}

	return result
}

// UsageImporter writes reader usage events as cost_events, deduplicated on
// the record uuid so a re-run (or a recall sweep after a `costs sync`)
// never double counts.
type UsageImporter struct {
	store    *Store
	pricer   *Pricer
	existing map[string]bool
}

// NewUsageImporter returns an importer over store.
func NewUsageImporter(store *Store, pricer *Pricer) *UsageImporter {
	return &UsageImporter{store: store, pricer: pricer, existing: map[string]bool{}}
}

// Import writes the events of one deck session. It returns how many were
// imported, how many already existed, and the write errors.
func (u *UsageImporter) Import(instanceID string, events []reader.Usage) (imported, skipped int, errs []string) {
	for _, ev := range events {
		if ev.In == 0 && ev.Out == 0 {
			continue
		}
		key := ev.UUID
		if key == "" {
			key = uuid.NewString()
		}
		dedupKey := fmt.Sprintf("%s_%s", instanceID, key)
		if u.existing[dedupKey] {
			skipped++
			continue
		}
		var count int
		if err := u.store.db.QueryRow("SELECT COUNT(*) FROM cost_events WHERE id = ?", dedupKey).Scan(&count); err != nil {
			continue
		}
		if count > 0 {
			skipped++
			u.existing[dedupKey] = true
			continue
		}
		ts := ev.TS
		if ts.IsZero() {
			ts = time.Now()
		}
		costEvent := CostEvent{
			ID:               dedupKey,
			SessionID:        instanceID,
			Timestamp:        ts,
			Model:            ev.Model,
			InputTokens:      ev.In,
			OutputTokens:     ev.Out,
			CacheReadTokens:  ev.CacheR,
			CacheWriteTokens: ev.CacheW,
			CostMicrodollars: u.pricer.ComputeCost(ev.Model, ev.In, ev.Out, ev.CacheR, ev.CacheW),
		}
		if err := u.store.WriteCostEvent(costEvent); err != nil {
			errs = append(errs, fmt.Sprintf("write event: %v", err))
			continue
		}
		u.existing[dedupKey] = true
		imported++
	}
	return imported, skipped, errs
}

// Usage implements the recall ingest UsageSink: the sweep hands over the
// usage of every conversation bound to a deck session.
func (u *UsageImporter) Usage(instanceID string, events []reader.Usage) error {
	_, _, errs := u.Import(instanceID, events)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// slugifyProjectPath converts a project path to Claude's directory slug format.
// /home/user/Documents/Projects/foo -> -home-user-Documents-Projects-foo
// Claude replaces / with - and also . with -, and trims trailing slashes.
func slugifyProjectPath(projectPath string) string {
	projectPath = strings.TrimRight(projectPath, "/")
	slug := strings.ReplaceAll(projectPath, "/", "-")
	slug = strings.ReplaceAll(slug, ".", "-")
	return slug
}
