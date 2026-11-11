package valid_test

import (
	"strings"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/valid"
)

// robot is the schema's `robot`: what a migration asks about a table it
// declares, which is its columns by name.
var robot = []*entschema.Table{{
	Name:    "robot",
	Columns: []*entschema.Column{{Name: "id"}, {Name: "alias"}, {Name: "keeper"}},
}}

// TestAColumnNothingWritesThatTakesNoRowWithoutOneIsRefused: a column an
// earlier build declared and this one does not -- an event's `desc` the day
// it stopped being a column -- is left where it is by a migration, and if it
// is NOT NULL with no default, every row written to its table after is
// refused. So it is refused first, named, with the statement that drops it;
// and once it is dropped, nothing is.
func TestAColumnNothingWritesThatTakesNoRowWithoutOneIsRefused(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	db, d := open(t)

	exec(t, db, `CREATE TABLE robot (id text NOT NULL PRIMARY KEY, alias text NOT NULL, keeper text NOT NULL, "desc" text NOT NULL)`)

	err := valid.Columns(ctx, db, d, robot)
	x.ErrorIs(err, valid.ErrUnknownRequired)
	x.ErrorContains(err, "robot.desc")
	t.Log(err)

	// What it says to run, as it says to.
	run := []string{}
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "ALTER TABLE ") {
			run = append(run, line)
		}
	}
	x.Len(run, 1, "one statement that runs, and the default only said")
	x.Contains(run[0], "DROP COLUMN")
	exec(t, db, run...)

	x.NoError(valid.Columns(ctx, db, d, robot))
	exec(t, db, `INSERT INTO robot (id, alias, keeper) VALUES ('a', 'arm-01', 'kim')`)
}

// TestAColumnNothingWritesThatTakesARowWithoutOneIsLeftAlone: nullable, or
// with a default, a column the schema does not declare costs a write nothing,
// and a deployment may have one on purpose -- it is what `entschema.Check`
// leaves alone too. Nor is a column the schema declares asked about, NOT NULL
// with no default though it is: what the app writes gives it its value. Nor a
// table the schema does not declare.
func TestAColumnNothingWritesThatTakesARowWithoutOneIsLeftAlone(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	db, d := open(t)

	exec(t, db,
		`CREATE TABLE robot (id text NOT NULL PRIMARY KEY, alias text NOT NULL, keeper text NOT NULL, note text NULL, size integer NOT NULL DEFAULT 0)`,
		`CREATE TABLE operators_own (id text NOT NULL PRIMARY KEY, kept text NOT NULL)`,
	)
	x.NoError(valid.Columns(ctx, db, d, robot))

	// And a database neither of the two speaks is not asked.
	x.NoError(valid.Columns(ctx, db, dialect.MySql, robot))
}

// TestAColumnThatMakesItsOwnValueIsLeftAlone: PostgreSQL has two kinds of
// column that are NOT NULL with no default and take a row without a value
// all the same, since they make their own -- an identity and a generated
// column. Neither is refused; and a column of a domain that has a default
// takes the domain's.
func TestAColumnThatMakesItsOwnValueIsLeftAlone(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	db, d := open(t)
	if d != dialect.Postgres {
		t.Skipf("an identity, a generated column and a domain's default are PostgreSQL's; set %s", pdtest.Postgres)
	}

	exec(t, db,
		`CREATE DOMAIN rank AS integer NOT NULL DEFAULT 1`,
		`CREATE TABLE robot (id text NOT NULL PRIMARY KEY, alias text NOT NULL, keeper text NOT NULL,
			seq bigint GENERATED ALWAYS AS IDENTITY,
			label text GENERATED ALWAYS AS (alias || '@' || keeper) STORED,
			rank rank)`,
	)
	x.NoError(valid.Columns(ctx, db, dialect.Postgres, robot))
	exec(t, db, `INSERT INTO robot (id, alias, keeper) VALUES ('a', 'arm-01', 'kim')`)

	// And one that does not, beside them, is.
	exec(t, db, `ALTER TABLE robot ADD COLUMN "desc" text NOT NULL DEFAULT ''`, `ALTER TABLE robot ALTER COLUMN "desc" DROP DEFAULT`)
	err := valid.Columns(ctx, db, dialect.Postgres, robot)
	x.ErrorIs(err, valid.ErrUnknownRequired)
	x.ErrorContains(err, "robot.desc, text")
	x.NotContains(err.Error(), "seq")
	x.NotContains(err.Error(), "label")
	x.NotContains(err.Error(), "rank")
	x.ErrorContains(err, "SET DEFAULT", "and the default it could have instead, said")
}
