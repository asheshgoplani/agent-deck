package session

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

// A SQLite view evaluates our probe during the authoritative SELECT. The probe
// attempts a real competing write using a second connection with no busy wait.
// This pins the exact old read/write gap, without production instrumentation or
// scheduling sleeps: a reserved transaction must exclude that writer already.
func TestStorageSnapshotTransactionReservesBeforeRead(t *testing.T) {
	for _, mutation := range []string{"UPDATE stored_instances SET account = 'seminno' WHERE id = 'shared'", "DELETE FROM stored_instances WHERE id = 'shared'"} {
		t.Run(mutation, func(t *testing.T) {
			var armed atomic.Bool
			var other *statedb.StateDB
			attempt := make(chan error, 1)
			function := fmt.Sprintf("storage_probe_%d", time.Now().UnixNano())
			require.NoError(t, sqlite.RegisterScalarFunction(function, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				if armed.CompareAndSwap(true, false) {
					_, err := other.DB().Exec(mutation)
					attempt <- err
				}
				return args[0], nil
			}))
			s, a, _ := snapshotFixture(t)
			var err error
			other, err = statedb.Open(s.dbPath)
			require.NoError(t, err)
			t.Cleanup(func() { other.Close() })
			other.DB().SetMaxOpenConns(1)
			_, err = other.DB().Exec("PRAGMA busy_timeout=0")
			require.NoError(t, err)
			// Include every column so the view also supports the old writer. Names
			// are schema-controlled, quoted, and never supplied by a CLI caller.
			columns, err := s.db.DB().Query("PRAGMA table_info(instances)")
			require.NoError(t, err)
			var names, selected, values []string
			for columns.Next() {
				var cid, notnull, pk int
				var name, kind string
				var defaultValue any
				require.NoError(t, columns.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk))
				if name == "acknowledged" {
					continue
				}
				quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
				names = append(names, quoted)
				values = append(values, "NEW."+quoted)
				if name == "title" {
					selected = append(selected, function+"(title) AS title")
				} else {
					selected = append(selected, quoted)
				}
			}
			require.NoError(t, columns.Err())
			require.NoError(t, columns.Close())
			_, err = s.db.DB().Exec("ALTER TABLE instances RENAME TO stored_instances")
			require.NoError(t, err)
			_, err = s.db.DB().Exec("CREATE VIEW instances AS SELECT " + strings.Join(selected, ",") + " FROM stored_instances")
			require.NoError(t, err)
			_, err = s.db.DB().Exec("CREATE TRIGGER store_instance INSTEAD OF INSERT ON instances BEGIN INSERT OR REPLACE INTO stored_instances (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(values, ",") + "); END")
			require.NoError(t, err)
			assignments := make([]string, len(names))
			for i, name := range names {
				assignments[i] = name + " = " + values[i]
			}
			_, err = s.db.DB().Exec("CREATE TRIGGER update_instance INSTEAD OF UPDATE ON instances BEGIN UPDATE stored_instances SET " + strings.Join(assignments, ",") + " WHERE id = OLD.id; END")
			require.NoError(t, err)
			a[0].Title = "alice"
			armed.Store(true)
			saveErr := s.Save(a)
			select {
			case err := <-attempt:
				require.ErrorContains(t, err, "locked", "a competing write must be excluded while the authoritative row is being read")
			default:
				t.Fatal("authoritative-read probe was not evaluated")
			}
			require.NoError(t, saveErr)
			// The same competing mutation is allowed after commit, proving the
			// reservation was released rather than leaking a lock or connection.
			_, err = other.DB().Exec(mutation)
			require.NoError(t, err)
		})
	}
}

func TestStorageSnapshotKeepsEditsMadeDuringSavePending(t *testing.T) {
	var armed atomic.Bool
	var instance *Instance
	function := fmt.Sprintf("storage_pending_edit_%d", time.Now().UnixNano())
	require.NoError(t, sqlite.RegisterScalarFunction(function, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		if armed.CompareAndSwap(true, false) {
			instance.Title = "edited while saving"
		}
		return int64(1), nil
	}))
	s, a, _ := snapshotFixture(t)
	instance = a[0]
	_, err := s.db.DB().Exec("CREATE TRIGGER pending_edit AFTER UPDATE ON instances BEGIN SELECT " + function + "(); END")
	require.NoError(t, err)
	instance.Title = "submitted"
	armed.Store(true)
	require.NoError(t, s.Save(a))
	row, err := s.db.LoadInstanceByID(instance.ID)
	require.NoError(t, err)
	require.Equal(t, "submitted", row.Title)
	require.Equal(t, "edited while saving", instance.Title)
	require.NoError(t, s.Save(a))
	row, err = s.db.LoadInstanceByID(instance.ID)
	require.NoError(t, err)
	require.Equal(t, "edited while saving", row.Title, "the successful save baseline must contain submitted values, not a later live edit")
}
