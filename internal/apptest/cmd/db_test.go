package cmd_test

import (
	"testing"

	"github.com/lesomnus/payday/config"
	_ "github.com/lesomnus/payday/config/dbpgx"
	"github.com/lesomnus/payday/pdtest"
)

// dbOf is the database one test runs on.
//
// SQLite unless `PDTEST_POSTGRES` names another; see [pdtest.DB]. It is here
// rather than written out at each site because the point of the variable is
// that the **whole** suite moves, and a site that kept its own SQLite would be
// the one that never ran against the real thing.
func dbOf(t *testing.T) config.DbConfig {
	t.Helper()

	drv, dsn := pdtest.DB(t)

	return config.DbConfig{Driver: drv, Dsn: dsn}
}
