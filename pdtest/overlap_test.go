package pdtest

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOverlappingWritersDoNotFail pins what [DB] promises about contention: two
// calls at once, each reading a row and then writing it, must not fail.
//
// It is the shape every app's suite makes by accident -- two RPCs in flight, or
// a stream and a write -- and on SQLite it is also the shape that fails without
// `_txlock=immediate`, immediately and regardless of `busy_timeout`, because
// SQLite will not wait for two connections that both hold a read lock and both
// want to promote. Eight writers and sixty transactions each is far more
// contention than a test makes and takes a few tens of milliseconds; before the
// transaction mode was asked for, ~20% of them came back `database is locked`.
//
// Under [Postgres] it asserts the same thing about a database that never had
// the problem, which is the point of the promise being about [DB] rather than
// about SQLite.
func TestOverlappingWritersDoNotFail(t *testing.T) {
	x := require.New(t)

	drv, dsn := DB(t)
	db, err := sql.Open(drv, dsn)
	x.NoError(err)
	defer db.Close()

	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE overlap (id INTEGER PRIMARY KEY, n INTEGER)`)
	x.NoError(err)
	_, err = db.ExecContext(ctx, `INSERT INTO overlap (id, n) VALUES (1, 0)`)
	x.NoError(err)

	const writers, each = 8, 60

	var locked int64
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if err := readThenWrite(ctx, db); err != nil {
					if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "busy") {
						atomic.AddInt64(&locked, 1)

						continue
					}

					// Anything else is this test failing rather than the
					// promise, so it says so from the goroutine it happened in.
					t.Error(err)

					return
				}
			}
		}()
	}
	wg.Wait()

	x.Zero(atomic.LoadInt64(&locked), "%d of %d transactions were refused for a lock; see DB's comment on _txlock", locked, writers*each)
}

// readThenWrite is one transaction of the kind a generated write makes: it reads
// the row it is about before it changes it.
func readThenWrite(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // the commit is what is under test

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT n FROM overlap WHERE id = 1`).Scan(&n); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE overlap SET n = ? WHERE id = 1`, n+1); err != nil {
		return err
	}

	return tx.Commit()
}
