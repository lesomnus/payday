package dbsqlite3_test

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ncruces/go-sqlite3/vfs/memdb"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	_ "github.com/lesomnus/payday/config/dbsqlite3"
)

// TestOverlappingWritesWaitThroughTheDsnAnAppWrites opens the database the way
// a deployment does, through [config.DbConfig.Open] with the DSN `pd new`
// writes, and makes the contention two RPCs at once make.
//
// That DSN asks for `foreign_keys(1)`, and asking for any PRAGMA turns off the
// minute this driver otherwise waits for a lock. With no wait, the
// `_txlock=immediate` that makes writers queue makes the second of any two
// refused instead: kamino's console signing in for the first time sent three
// lists at once and two of them answered `database is locked`.
func TestOverlappingWritesWaitThroughTheDsnAnAppWrites(t *testing.T) {
	x := require.New(t)

	// In memory, shared between connections and locked like a file, so the
	// contention is SQLite's and not the disk's.
	db, _, err := config.DbConfig{
		Driver: "sqlite3",
		Dsn:    memdb.TestDB(t, url.Values{"_pragma": {"foreign_keys(1)"}}),
	}.Open(t.Context())
	x.NoError(err)
	defer db.Close()

	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE overlap (id INTEGER PRIMARY KEY, n INTEGER)`)
	x.NoError(err)
	_, err = db.ExecContext(ctx, `INSERT INTO overlap (id, n) VALUES (1, 0)`)
	x.NoError(err)

	const writers, each = 8, 30

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

					t.Error(err)

					return
				}
			}
		}()
	}
	wg.Wait()

	x.Zero(atomic.LoadInt64(&locked), "%d of %d transactions were refused for a lock", locked, writers*each)

	var n int
	x.NoError(db.QueryRowContext(ctx, `SELECT n FROM overlap WHERE id = 1`).Scan(&n))
	x.Equal(writers*each, n)
}

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
