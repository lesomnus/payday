package cli

// The one driver this app runs on in a process. It is blank-imported by the app
// rather than by payday so that an app does not carry an engine it never opens.
//
// It is in this package and not in `cmd` because a blank import is a property of
// the package that writes it, and `cmd` is imported by the sandbox for `Build`.
// A driver named there is linked into the page as well -- and this one brings
// SQLite compiled to Wasm and run on wazero, which is wasm inside wasm and
// 15 MB of what a browser has to download. The page opens "sqlite3-wasm"
// instead, a Worker beside it.
//
// A build tag would also have done it, and did for one commit. This is better:
// a tag says "do not compile what would compile", and what is actually true is
// that a process needs a database engine and a page needs a different one. The
// package boundary says that, and nothing has to be excluded to make it true.
import _ "github.com/lesomnus/payday/config/dbsqlite3"
