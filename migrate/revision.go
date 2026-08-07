package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"ariga.io/atlas/sql/migrate"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

// RevisionTable is where a database records which migrations were applied to
// it. It is not part of the ent schema; it belongs to the database, not to the
// app.
const RevisionTable = "schema_revisions"

// revisionColumns is the order the columns are read and written in.
var revisionColumns = []string{
	"version",
	"description",
	"type",
	"applied",
	"total",
	"executed_at",
	"execution_time",
	"error",
	"error_stmt",
	"hash",
	"partial_hashes",
	"operator_version",
}

// revisionSchema holds no type that is spelled differently by the databases
// this package migrates, so it is the same statement everywhere. Times are text
// in RFC 3339 rather than a timestamp, which every database stores its own way.
const revisionSchema = `CREATE TABLE IF NOT EXISTS ` + RevisionTable + ` (
	version          TEXT NOT NULL PRIMARY KEY,
	description      TEXT NOT NULL,
	type             BIGINT NOT NULL,
	applied          BIGINT NOT NULL,
	total            BIGINT NOT NULL,
	executed_at      TEXT NOT NULL,
	execution_time   BIGINT NOT NULL,
	error            TEXT NOT NULL,
	error_stmt       TEXT NOT NULL,
	hash             TEXT NOT NULL,
	partial_hashes   TEXT NOT NULL,
	operator_version TEXT NOT NULL
)`

var _ migrate.RevisionReadWriter = (*Revisions)(nil)

// Revisions is the history of the migrations that were applied to a database,
// kept in the database itself so that a deployment knows where it stands.
type Revisions struct {
	db      *sql.DB
	dialect string

	// schema is where the table ended up, for the databases that have more
	// than one place to put it.
	schema string
}

// NewRevisions reads the history of `db`, creating it if this is the first
// time the database is migrated.
func NewRevisions(ctx context.Context, db *sql.DB, d string) (*Revisions, error) {
	v := &Revisions{db: db, dialect: d}

	if d == dialect.Postgres {
		// The table is created wherever the search path points. Atlas has to
		// be told which schema that is, or the one it finds the table in is a
		// schema it knows nothing about, and it refuses to migrate a database
		// that holds something it did not put there.
		var s sql.NullString
		if err := db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&s); err != nil {
			return nil, fmt.Errorf("read the current schema: %w", err)
		}

		v.schema = s.String
	}

	if _, err := db.ExecContext(ctx, revisionSchema); err != nil {
		return nil, fmt.Errorf("create %s: %w", RevisionTable, err)
	}

	return v, nil
}

func (r *Revisions) Ident() *migrate.TableIdent {
	return &migrate.TableIdent{Name: RevisionTable, Schema: r.schema}
}

func (r *Revisions) ReadRevisions(ctx context.Context) ([]*migrate.Revision, error) {
	q, args := entsql.Dialect(r.dialect).
		Select(revisionColumns...).
		From(entsql.Table(RevisionTable)).
		OrderBy(entsql.Asc("version")).
		Query()

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query revisions: %w", err)
	}
	defer rows.Close()

	vs := []*migrate.Revision{}
	for rows.Next() {
		v, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}

		vs = append(vs, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read revisions: %w", err)
	}

	return vs, nil
}

func (r *Revisions) ReadRevision(ctx context.Context, version string) (*migrate.Revision, error) {
	q, args := entsql.Dialect(r.dialect).
		Select(revisionColumns...).
		From(entsql.Table(RevisionTable)).
		Where(entsql.EQ("version", version)).
		Query()

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query revision %q: %w", version, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("query revision %q: %w", version, err)
		}

		return nil, migrate.ErrRevisionNotExist
	}

	return scanRevision(rows)
}

func (r *Revisions) WriteRevision(ctx context.Context, v *migrate.Revision) error {
	hashes, err := json.Marshal(v.PartialHashes)
	if err != nil {
		return fmt.Errorf("marshal partial hashes: %w", err)
	}

	q, args := entsql.Dialect(r.dialect).
		Insert(RevisionTable).
		Columns(revisionColumns...).
		Values(
			v.Version,
			v.Description,
			int64(v.Type),
			int64(v.Applied),
			int64(v.Total),
			v.ExecutedAt.UTC().Format(time.RFC3339Nano),
			int64(v.ExecutionTime),
			v.Error,
			v.ErrorStmt,
			v.Hash,
			string(hashes),
			v.OperatorVersion,
		).
		// A revision is written again as a migration makes progress, and once
		// more if it has to be resolved by hand.
		OnConflict(
			entsql.ConflictColumns("version"),
			entsql.ResolveWithNewValues(),
		).
		Query()

	if _, err := r.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("write revision %q: %w", v.Version, err)
	}

	return nil
}

func (r *Revisions) DeleteRevision(ctx context.Context, version string) error {
	q, args := entsql.Dialect(r.dialect).
		Delete(RevisionTable).
		Where(entsql.EQ("version", version)).
		Query()

	if _, err := r.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("delete revision %q: %w", version, err)
	}

	return nil
}

func scanRevision(rows *sql.Rows) (*migrate.Revision, error) {
	var (
		v          migrate.Revision
		kind       int64
		applied    int64
		total      int64
		executedAt string
		took       int64
		hashes     string
	)
	err := rows.Scan(
		&v.Version,
		&v.Description,
		&kind,
		&applied,
		&total,
		&executedAt,
		&took,
		&v.Error,
		&v.ErrorStmt,
		&v.Hash,
		&hashes,
		&v.OperatorVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("scan revision: %w", err)
	}

	t, err := time.Parse(time.RFC3339Nano, executedAt)
	if err != nil {
		return nil, fmt.Errorf("revision %q: executed_at: %w", v.Version, err)
	}
	if err := json.Unmarshal([]byte(hashes), &v.PartialHashes); err != nil {
		return nil, fmt.Errorf("revision %q: partial hashes: %w", v.Version, err)
	}

	v.Type = migrate.RevisionType(kind)
	v.Applied = int(applied)
	v.Total = int(total)
	v.ExecutedAt = t
	v.ExecutionTime = time.Duration(took)

	return &v, nil
}
