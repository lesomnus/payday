package pdgen

import "github.com/protobuf-orm/protobuf-orm/graph"

// Asserted is [asserted], for the test that holds it against every shape a
// field can have in an `Add` request.
func Asserted(p graph.Prop) string { return asserted(p) }
