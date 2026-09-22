package store

import (
	"context"
	"database/sql"
)

// ReadSnapshot starts one read-only transaction. Callers must read the
// session, its sources, and the cursor anchor before closing it.
func (s *Store) ReadSnapshot(ctx context.Context) (*sql.Tx, error) {
	return s.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}
