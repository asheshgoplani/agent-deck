package statedb

import (
	"context"
	"database/sql"
	"net/url"
	"sync"
	"time"
)

// RegistryObserver owns one live read-only connection. data_version values are
// connection-local, so this connection must never be borrowed from a pool.
type RegistryObserver struct {
	mu     sync.Mutex
	db     *sql.DB
	conn   *sql.Conn
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	epoch  uint64
}

func (s *StateDB) NewRegistryObserver() (*RegistryObserver, error) {
	u := url.URL{Scheme: "file", Path: s.path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	probe, done := context.WithTimeout(ctx, 2*time.Second)
	defer done()
	conn, err := db.Conn(probe)
	if err != nil {
		cancel()
		_ = db.Close()
		return nil, err
	}
	o := &RegistryObserver{db: db, conn: conn, ctx: ctx, cancel: cancel, epoch: 1}
	if _, err := o.Version(); err != nil {
		_ = o.Close()
		return nil, err
	}
	return o, nil
}

func (o *RegistryObserver) Version() (int64, error) {
	version, _, err := o.Probe()
	return version, err
}

// Probe binds its value to a connection epoch. Reconnection is conservative:
// the failed observation remains an error, and the next probe uses a new epoch.
func (o *RegistryObserver) Probe() (int64, uint64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ctx, cancel := context.WithTimeout(o.ctx, 2*time.Second)
	defer cancel()
	var version int64
	var err error
	if o.conn == nil {
		err = o.reconnectLocked()
	}
	if err == nil {
		err = o.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version)
	}
	epoch := o.epoch
	if err != nil && o.ctx.Err() == nil {
		_ = o.reconnectLocked()
	}
	return version, epoch, err
}
func (o *RegistryObserver) reconnectLocked() error {
	if o.conn != nil {
		_ = o.conn.Close()
		o.conn = nil
	}
	ctx, cancel := context.WithTimeout(o.ctx, 2*time.Second)
	defer cancel()
	conn, err := o.db.Conn(ctx)
	if err != nil {
		return err
	}
	o.conn = conn
	o.epoch++
	return nil
}

// Snapshot returns persisted rows, never converted process-local runtime state.
func (o *RegistryObserver) Snapshot() (*RegistrySnapshotResult, int64, int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return nil, 0, 0, sql.ErrConnDone
	}
	ctx, cancel := context.WithTimeout(o.ctx, 2*time.Second)
	defer cancel()
	var before, after int64
	if err := o.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&before); err != nil {
		return nil, 0, 0, err
	}
	tx, err := o.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := loadInstances(tx.Query)
	if err != nil {
		return nil, 0, 0, err
	}
	groups, err := loadGroups(tx.Query)
	if err != nil {
		return nil, 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, 0, err
	}
	if err = o.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&after); err != nil {
		return nil, 0, 0, err
	}
	return &RegistrySnapshotResult{Instances: rows, Groups: groups}, before, after, nil
}

func (o *RegistryObserver) Close() error {
	var err error
	o.once.Do(func() {
		o.cancel()
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.conn != nil {
			err = o.conn.Close()
		}
		if e := o.db.Close(); err == nil {
			err = e
		}
	})
	return err
}
