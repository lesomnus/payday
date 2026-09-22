package dbsqlite3wasm_test

import (
	"testing"

	"github.com/lesomnus/sqlite3-wasm/driver"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"

	_ "github.com/lesomnus/payday/config/dbsqlite3wasm"
)

// TestTheDsnIsOneThisDriverTakes, because this driver refuses a parameter it
// does not know and the refusal lands in a browser.
//
// `config.DbConfig.Open` adds two things to a SQLite DSN that does not say
// them, and one of them is spelled differently here than in the other SQLite
// driver: `_timefmt=unixepoch_nano` is what `sqlite3` calls the integer time
// format and `_time_integer_format=unix_nano` is what this one calls it. A
// default written for the dialect rather than the driver is therefore a DSN
// this rejects -- `unknown driver parameter "_timefmt"` -- and what that looks
// like is the wasm instance exiting before it serves anything, with the page
// showing no app and no reason.
//
// So the parameters payday sets are put through this driver's own parser. The
// whole path is the browser's and is tested there; this is the part that can
// be checked in a process, and it is the part that broke.
func TestTheDsnIsOneThisDriverTakes(t *testing.T) {
	x := require.New(t)

	// The page's own DSN; see internal/apptest/wasm/main.go.
	cfg, err := driver.ParseDSN("file:/sandbox?vfs=memdb&_txlock=immediate&_time_integer_format=unix_nano")
	x.NoError(err)

	// And read back the way they are written, which is the half of the format
	// that is not the driver refusing anything: written as nanoseconds and read
	// with no format named, this driver takes anything above 1e12 for
	// milliseconds, so every timestamp would come back in the year 56000-odd.
	x.Equal(driver.IntegerTimeUnixNano, cfg.IntegerTime)
	x.Equal("immediate", cfg.TxLock)

	// The dialect is the other driver's, so the dialect is not what the time
	// format can be chosen by.
	v, err := config.DbConfig{Driver: "sqlite3-wasm"}.Speaks()
	x.NoError(err)
	x.Equal(config.DialectSQLite, v)
}
