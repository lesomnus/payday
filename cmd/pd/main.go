// Command pd generates a payday app from its schema, and says when the two
// have come apart.
//
// It is meant to be run as `go tool pd`, which is why the name is two letters:
// it never goes on the PATH, so there is nothing for it to collide with.
package main

import "github.com/lesomnus/payday/internal/pdcli"

func main() { pdcli.Main() }
