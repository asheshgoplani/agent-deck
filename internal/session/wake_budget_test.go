package session

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

// PR-B0 (move the send budget gate into internal/session). WakeBudget is
// the one send budget gate: `session send` reaches it through
// cmd/agent-deck's budgetForSend (pinned by TestBudgetForSend_IsSessionWakeBudget),
// and the daemon's wake path runs that CLI, so this table covers both. The
// cases are the contract checkSendBudget had on main, branch for branch.

const (
	wakeBudgetProfile = "wakebudget"
	enforcedDaily1    = "[costs.budgets]\nenforce = true\ndaily_limit = 1.0\n"
)

// warnLineRE is the whole of stderr for a gate that could not check: one line.
var warnLineRE = regexp.MustCompile(`^Warning: cost budget not checked: [^\n]+\n$`)

type wakeBudgetCase struct {
	name string
	// config is the config.toml body.
	config string
	// spentMicro is the cost the target has today; siblingMicro is what a
	// session in the target's group (siblingGroup) has.
	spentMicro, siblingMicro int64
	siblingGroup             string
	targetGroup              string
	nilTarget                bool
	passStorage              bool // the caller's own storage instead of nil
	dropCostTable            bool // makes the budget query fail (needs passStorage: a reopened store recreates the table)
	nilDB                    bool // the caller's storage has no database
	breakStore               bool // state.db becomes a directory: the profile store cannot be opened
	// want
	action     costs.BudgetAction
	reason     string
	usedMicro  int64
	limitMicro int64
	resets     bool // ResetsAt is set
	// warns: stderr is exactly one "Warning: cost budget not checked: ..." line;
	// otherwise nothing is printed.
	warns bool
}

func wakeBudgetCases() []wakeBudgetCase {
	return []wakeBudgetCase{
		{name: "nil target", config: enforcedDaily1, spentMicro: 5e6, nilTarget: true,
			action: costs.BudgetActionNone},
		{name: "tracking off", config: "[costs]\nenabled = false\n" + enforcedDaily1, spentMicro: 5e6,
			action: costs.BudgetActionNone},
		{name: "not enforced (display-only)", config: "[costs.budgets]\ndaily_limit = 1.0\n", spentMicro: 5e6,
			action: costs.BudgetActionNone},
		{name: "enforced but no limit set", config: "[costs.budgets]\nenforce = true\n", spentMicro: 5e6,
			action: costs.BudgetActionNone},
		{name: "under the limit", config: "[costs.budgets]\nenforce = true\ndaily_limit = 100.0\n", spentMicro: 5e6,
			action: costs.BudgetActionNone},
		{name: "nil storage opens the profile store: stop", config: enforcedDaily1, spentMicro: 5e6,
			action: costs.BudgetActionStop, reason: "daily global limit exceeded", usedMicro: 5e6, limitMicro: 1e6, resets: true},
		{name: "caller's storage is used and left open: stop", config: enforcedDaily1, spentMicro: 5e6, passStorage: true,
			action: costs.BudgetActionStop, reason: "daily global limit exceeded", usedMicro: 5e6, limitMicro: 1e6, resets: true},
		{name: "warn at 85 percent", config: enforcedDaily1, spentMicro: 850_000,
			action: costs.BudgetActionWarn, reason: "daily global limit exceeded", usedMicro: 850_000, limitMicro: 1e6, resets: true},
		{name: "profile store cannot be opened warns and allows", config: enforcedDaily1, spentMicro: 5e6, breakStore: true,
			action: costs.BudgetActionNone, warns: true},
		{name: "failed query warns and allows (caller's storage)", config: enforcedDaily1, spentMicro: 5e6, dropCostTable: true, passStorage: true,
			action: costs.BudgetActionNone, warns: true},
		{name: "caller's storage without a database allows silently", config: enforcedDaily1, spentMicro: 5e6, nilDB: true,
			action: costs.BudgetActionNone},
		{name: "group daily limit sums the group's sessions", config: "[costs.budgets.groups.team]\ndaily_limit = 1.0\n[costs.budgets]\nenforce = true\n",
			spentMicro: 600_000, siblingMicro: 600_000, targetGroup: "team", siblingGroup: "team",
			action: costs.BudgetActionStop, reason: "group daily limit exceeded", usedMicro: 1_200_000, limitMicro: 1e6, resets: true},
		{name: "a subgroup is its own group", config: "[costs.budgets.groups.team]\ndaily_limit = 1.0\n[costs.budgets]\nenforce = true\n",
			spentMicro: 600_000, siblingMicro: 600_000, targetGroup: "team", siblingGroup: "team/sub",
			action: costs.BudgetActionNone},
	}
}

func TestWakeBudget_EqualsCheckSendBudgetOnEveryBranch(t *testing.T) {
	for _, tc := range wakeBudgetCases() {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
			cfgDir := filepath.Join(home, "config", "agent-deck")
			if err := os.MkdirAll(cfgDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			ClearUserConfigCache()
			t.Cleanup(ClearUserConfigCache)

			target := &Instance{ID: "wb-target", Title: "wb-target", GroupPath: tc.targetGroup}
			sibling := &Instance{ID: "wb-sibling", Title: "wb-sibling", GroupPath: tc.siblingGroup}
			instances := []*Instance{target, sibling}

			seed, err := NewStorageWithProfile(wakeBudgetProfile)
			if err != nil {
				t.Fatal(err)
			}
			store := costs.NewStore(seed.GetDB().DB())
			for id, micro := range map[string]int64{target.ID: tc.spentMicro, sibling.ID: tc.siblingMicro} {
				if micro == 0 {
					continue
				}
				if err := store.WriteCostEvent(costs.CostEvent{
					ID: "ev-" + id, SessionID: id, Timestamp: time.Now(), Model: "claude-test",
					InputTokens: 1000, CostMicrodollars: micro,
				}); err != nil {
					t.Fatal(err)
				}
			}
			dbPath := ""
			_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() && d.Name() == "state.db" {
					dbPath = p
				}
				return nil
			})
			if dbPath == "" {
				t.Fatal("profile state.db not found")
			}
			if tc.dropCostTable {
				if _, err := seed.GetDB().DB().Exec(`DROP TABLE cost_events`); err != nil {
					t.Fatal(err)
				}
			}
			if tc.breakStore {
				if err := seed.Close(); err != nil {
					t.Fatal(err)
				}
				for _, suffix := range []string{"", "-wal", "-shm"} {
					_ = os.Remove(dbPath + suffix)
				}
				if err := os.Mkdir(dbPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var storage *Storage
			if tc.nilDB {
				storage = &Storage{}
			}
			if tc.passStorage {
				storage = seed
				t.Cleanup(func() { _ = seed.Close() })
			} else if !tc.breakStore {
				if err := seed.Close(); err != nil {
					t.Fatal(err)
				}
			}

			in := target
			if tc.nilTarget {
				in = nil
			}
			var got costs.BudgetResult
			stderr := captureStderr(t, func() {
				got = WakeBudget(wakeBudgetProfile, in, instances, storage)
			})

			if got.Action != tc.action || got.Reason != tc.reason || got.UsedMicro != tc.usedMicro || got.LimitMicro != tc.limitMicro {
				t.Fatalf("WakeBudget = {%v %q used %d limit %d}, want {%v %q used %d limit %d}",
					got.Action, got.Reason, got.UsedMicro, got.LimitMicro, tc.action, tc.reason, tc.usedMicro, tc.limitMicro)
			}
			if got.ResetsAt.IsZero() == tc.resets {
				t.Fatalf("ResetsAt = %v, want set = %v", got.ResetsAt, tc.resets)
			}
			if tc.warns != warnLineRE.MatchString(stderr) || (!tc.warns && stderr != "") {
				t.Fatalf("stderr = %q, want warning line = %v", stderr, tc.warns)
			}
			if tc.passStorage && !tc.dropCostTable {
				// The gate borrows the caller's storage; it must not close it.
				if err := storage.GetDB().DB().Ping(); err != nil {
					t.Fatalf("caller's storage closed by WakeBudget: %v", err)
				}
			}
		})
	}
}

// The gate sums a group limit over the target's exact group only.
// (Moved from cmd/agent-deck/send_budget_paths_test.go with its function.)
func TestBudgetGroupMembersExactMatch(t *testing.T) {
	target := &Instance{ID: "a", GroupPath: "work"}
	got := budgetGroupMembers(target, []*Instance{
		target, {ID: "b", GroupPath: "work"}, {ID: "c", GroupPath: "work/sub"}, {ID: "d", GroupPath: "other"},
	})
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("members = %v, want a,b (subgroups are separate)", got)
	}
}
