// Package pdpb is what payday's own `.proto` files generate: the options a
// schema is declared with, and the two services payday serves on an app's
// behalf.
//
// It is generated in whole. There is no hand-written Go here and this file is
// the exception -- a package comment cannot live in a `.pb.go`, because that
// file is rewritten on every generation and the comment would go with it.
//
// # What is in it
//
//	(payday.entity)   what an entity declares: its domain, its tenancy, whether
//	                  it is listed and watched, what Erase does to a row. The
//	                  generator reads this and refuses what it does not say;
//	                  see internal/pdgen.
//	(payday.field)    what a field declares -- `secret`, today.
//	BatchService      several writes as one transaction; see payday/batch.
//	TokenService      what a token stands for, asked of whoever issued it; see
//	                  auth.Remote.
//
// # Why an app imports it
//
// Because its schema does. An app's `.proto` files import `payday.proto` for
// the options, so the Go the app generates refers to the types here -- and the
// extension has to be registered in the process for `proto.GetExtension` to
// read an option off a descriptor at run time. Nothing about that is a choice
// an app makes; it follows from declaring an entity.
package pdpb
