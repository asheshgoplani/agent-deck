package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ReviveSummary tallies outcomes from a reviver sweep. Format() produces the
// single-line human-readable summary emitted by the CLI.
type ReviveSummary struct {
	Revived int
	Errored int
	Alive   int
	Dead    int
}

// Format returns the single-line summary. Stable keys — tests assert on
// substring presence, so "revived=N errored=N alive=N dead=N" is a contract.
func (s ReviveSummary) Format() string {
	return fmt.Sprintf("revived=%d errored=%d alive=%d dead=%d",
		s.Revived, s.Errored, s.Alive, s.Dead)
}

// runReviveAll is the testable core: classify all instances, trigger revives,
// aggregate the summary. Separate from handleSessionRevive so tests can stub
// the reviver and storage without exec'ing the binary.
//
// It also returns the subset of instances that were actually revived (status
// mutated). The caller persists ONLY these via a targeted, sweep-free write so
// revive can never clobber a session a concurrent process added after the
// snapshot was loaded (lost-update race fix).
func runReviveAll(instances []*session.Instance, rev *session.Reviver) (ReviveSummary, []*session.Instance) {
	outcomes := rev.ReviveAll(instances)
	byID := make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		if inst != nil {
			byID[inst.ID] = inst
		}
	}
	summary := ReviveSummary{}
	var revived []*session.Instance
	for _, o := range outcomes {
		switch o.Class {
		case session.ClassAlive:
			summary.Alive++
		case session.ClassDead:
			summary.Dead++
		case session.ClassErrored:
			summary.Errored++
			if o.Revived {
				summary.Revived++
				if inst := byID[o.InstanceID]; inst != nil {
					revived = append(revived, inst)
				}
			}
		}
	}
	return summary, revived
}

// reviveAndPersist is the testable persist seam: run the reviver over the given
// instances, then durably persist ONLY the rows it actually revived, via the
// targeted, status-only, sweep-free path (Storage.PersistRevivedInstances →
// statedb.PersistInstanceStatusesTx). It deliberately does NOT call
// saveSessionData / SaveWithGroups / SaveInstances: that full load-modify-write
// path runs a `DELETE FROM instances WHERE id NOT IN (<stale snapshot>)` sweep
// that silently drops any session a concurrent `add` inserted after we loaded
// our snapshot, and a full-row rewrite that would clobber concurrent edits to
// the revived rows. Keeping persistence behind this single function means a
// future regression back to the full-rewrite path is a one-line change here that
// the CLI-level regression test (revive_persist_cli_test.go) will catch.
func reviveAndPersist(
	storage *session.Storage,
	instances []*session.Instance,
	rev *session.Reviver,
) (ReviveSummary, error) {
	summary, revived := runReviveAll(instances, rev)
	if len(revived) > 0 {
		if err := storage.PersistRevivedInstances(revived); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

// handleSessionRevive dispatches `agent-deck session revive [--all|--name <title>]`.
// Rebuilds dead control pipes for sessions whose tmux server is still alive
// (see REPORT-D). Exits 0 on success, 1 on usage/load errors, 2 if --name not found.
func handleSessionRevive(profile string, args []string) {
	fs := flag.NewFlagSet("session revive", flag.ExitOnError)
	all := fs.Bool("all", false, "Revive all errored sessions with alive tmux servers")
	name := fs.String("name", "", "Revive a single session by title or id")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session revive [--all | --name <title>]")
		fmt.Println()
		fmt.Println("Re-establish control pipes for sessions whose tmux server survived")
		fmt.Println("but whose pipe was killed (e.g., SSH logout on Linux+systemd hosts).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if !*all && *name == "" {
		fs.Usage()
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	rev := session.NewReviver()

	var target []*session.Instance
	if *all {
		target = instances
	} else {
		inst, errMsg, errCode := ResolveSession(*name, instances)
		if inst == nil {
			out.Error(errMsg, errCode)
			if errCode == ErrCodeNotFound {
				os.Exit(2)
			}
			os.Exit(1)
			return
		}
		target = []*session.Instance{inst}
	}

	summary, err := reviveAndPersist(storage, target, rev)
	if err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	jsonData := map[string]interface{}{
		"success": true,
		"revived": summary.Revived,
		"errored": summary.Errored,
		"alive":   summary.Alive,
		"dead":    summary.Dead,
	}
	out.Success(summary.Format(), jsonData)
}

// reviveOnStartup is the non-blocking startup hook. Called once from main()
// before TUI boot. Silently logs failures; never surfaces errors to the user
// — this is a best-effort recovery, not a gate.
func reviveOnStartup(profile string) {
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return
	}
	rev := session.NewReviver()
	_ = rev.ReviveAll(instances)
	// Note: we intentionally do NOT save storage here — the reviver is fire-
	// and-forget on startup. The next TUI tick or CLI command will persist
	// status mutations through the normal save path.
}
