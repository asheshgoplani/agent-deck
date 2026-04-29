package costs

import (
	"context"
	"fmt"
)

// recomputeBatchSize controls how many cost_events are fetched and updated per
// transaction during Recompute. 1000 is a balance between memory pressure and
// transaction overhead on a personal-scale SQLite database.
const recomputeBatchSize = 1000

// Recompute walks every cost_events row in `store`, recalculates
// cost_microdollars using `pricer`, and writes any differences back. It is
// idempotent: a second run with no pricing data changes returns updated=0.
//
// Rows whose `model` is unknown to the pricer are left untouched and counted
// as skipped (writing 0 over an existing positive cost would lose data).
//
// When dryRun is true no UPDATE is executed; the returned counts describe
// what would change.
func Recompute(ctx context.Context, store *Store, pricer *Pricer, dryRun bool) (updated, skipped int, err error) {
	if store == nil {
		return 0, 0, fmt.Errorf("recompute: store is nil")
	}
	if pricer == nil {
		return 0, 0, fmt.Errorf("recompute: pricer is nil")
	}

	var afterRowID int64
	for {
		if err := ctx.Err(); err != nil {
			return updated, skipped, err
		}

		events, lastRowID, err := store.PageEventsAfter(afterRowID, recomputeBatchSize)
		if err != nil {
			return updated, skipped, fmt.Errorf("page events after rowid %d: %w", afterRowID, err)
		}
		if len(events) == 0 {
			return updated, skipped, nil
		}

		batchUpdates := make(map[string]int64, len(events))
		for _, ev := range events {
			if _, ok := pricer.GetPrice(ev.Model); !ok {
				skipped++
				continue
			}
			recomputed := pricer.ComputeCost(ev.Model, ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheWriteTokens)
			if recomputed == ev.CostMicrodollars {
				skipped++
				continue
			}
			batchUpdates[ev.ID] = recomputed
			updated++
		}

		if !dryRun && len(batchUpdates) > 0 {
			if err := store.ApplyCostUpdates(ctx, batchUpdates); err != nil {
				return updated, skipped, fmt.Errorf("apply updates: %w", err)
			}
		}

		afterRowID = lastRowID
		if len(events) < recomputeBatchSize {
			return updated, skipped, nil
		}
	}
}
