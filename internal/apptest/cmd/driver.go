//go:build !js

package cmd

// The one driver this app runs on in a process. It is blank-imported by the app
// rather than by payday so that an app does not carry an engine it never opens.
//
// # Why this is its own file, and why it is tagged
//
// The engine this driver brings is SQLite compiled to Wasm and run on wazero,
// which is 15 MB of the linked binary. Under GOOS=js the sandbox opens
// "sqlite3-wasm" instead -- a Web Worker beside the page, and the whole reason
// `dbsqlite3wasm` exists -- so the wazero engine is never opened there.
//
// It was still linked, because a blank import is a property of the *package*
// and `wasm/main.go` imports this one for `Build`. Nothing said so: the sandbox
// worked, and the only symptom was a 72 MB module. Splitting the import into a
// file the js build does not compile is what makes "an app does not carry an
// engine it never opens" true rather than intended.
import _ "github.com/lesomnus/payday/config/dbsqlite3"
