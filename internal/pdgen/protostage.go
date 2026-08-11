package pdgen

import (
	"fmt"
	"path"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/ormpb"
)

// Proto writes the overlay that adds a `List` to the service contract, or the
// empty string for a schema that declared none.
//
// It has to be a proto and it has to be written before anything else runs: a
// `List` is an RPC, and an RPC has to exist in the contract before the messages,
// the stubs and the servers are generated from it. So this is a second pass of
// the same plugin, run beside `protoc-gen-orm-service`, and what it writes is
// merged the same way an app's own hand-written overlay is.
//
// What it does *not* do is invent a filter. Only what the schema declared can
// be filtered by, because the alternative -- a filter per field, generated --
// is a query surface nobody chose: every column becomes something a caller can
// make the database sort and scan by, and which of those are indexed is not
// something the declaration said.
func Proto(vs []*Entity, pkg string) string {
	var b strings.Builder
	n := 0

	for _, v := range vs {
		if v.List == nil {
			continue
		}

		n++
		e := v.GoName()

		b.WriteString(fmt.Sprintf("\nservice %sService {\n", e))
		b.WriteString(fmt.Sprintf("  // List reads %ss a page at a time.\n", e))
		b.WriteString(fmt.Sprintf("  rpc List(%sListRequest) returns (%sListResponse);\n", e, e))
		if v.Watch {
			b.WriteString(fmt.Sprintf(`
  // Watch the %ss this caller may see, as they are now and as they change.
  //
  // What arrives is **state and never a delta**, so a client converges rather
  // than replays: it keeps what it was last told about a row and replaces it.
  // An event it did not receive is one it does not need, since the next one
  // about that row carries the whole of it.
  //
  // The first message is everything that matches right now, so a client does
  // not have to List first and race the subscription. A row may arrive twice --
  // once in that first message and once as a change that happened while it was
  // being read -- and that is harmless for the same reason.
  rpc Watch(%sWatchRequest) returns (stream %sWatchResponse);
`, e, e, e))
		}
		b.WriteString("}\n")

		b.WriteString(fmt.Sprintf(`
message %sListRequest {
  // Bounded because the work is: each filter is a predicate in the same query,
  // so this is what says how much of the database one request may read.
  repeated %sFilter filters = 1;

  // How many to answer with. Nothing said is what the schema declared, and more
  // than the cap is the cap -- a caller asking for more than there is meant no
  // harm, so it is not an error and it is not the whole table either.
  int32 size = 2 [features.field_presence = IMPLICIT];

  // Where to carry on from: the "next" of the answer before. It names the last
  // row of that page rather than counting rows from the start, so a row added
  // ahead of the page does not shift it and a caller reading through never sees
  // one twice or misses one.
  string after = 3 [features.field_presence = IMPLICIT];
}

message %sListResponse {
  repeated %s items = 1;

  // What to ask for next, and empty when this was the last of them.
  //
  // Empty means there is no more *for now*: a list is read as it is, and one
  // that has grown since answers a fresh call. It is not empty merely because
  // the page came back short -- a page is short when the last row of it was the
  // last row there was, which is a thing the server can only know by having
  // looked.
  string next = 2 [features.field_presence = IMPLICIT];
}
`, e, e, e, e))

		if v.Watch {
			b.WriteString(fmt.Sprintf(`
message %sWatchRequest {
  // Which rows to watch, in the same words List takes them: what is watched is
  // what that list would answer with, now and afterwards.
  //
  // Required, and bounded the way a List is -- for a heavier reason. A list
  // runs its filters once; a watch runs them again for every write that touches
  // the entity, for as long as the stream is open. A watch with no filters is
  // the whole table, forever, which is the one shape that has no cap at all.
  repeated %sFilter filters = 1;

  // Skip what is there now and send only what changes from here.
  //
  // Off by default, and the default is the safe one: a client that does not
  // know the current state and did not ask for it holds something wrong until
  // the next write happens to correct it.
  bool skip_snapshot = 2 [features.field_presence = IMPLICIT];
}

message %sWatchResponse {
  // What changed, or -- in the first message -- everything that matches.
  repeated %sWatchItem items = 1;
}

message %sWatchItem {
  // Which row. Always said, since one that is gone still has to be named.
  bytes id = 1;

  // The row as it is now, and **absent when it is no longer one this caller may
  // see**: erased, or moved out of the filters this stream named.
  //
  // Absence is the whole of how a removal is said. There is no flag for it, and
  // there is deliberately no way to tell those two apart -- a stream that
  // distinguished "erased" from "no longer yours" would be telling a caller
  // about rows that stopped being theirs, which is the thing the wall is for.
  //
  // It is only ever said about a row this stream has already carried. One that
  // never matched is not news.
  %s value = 2;

  // The RPC that changed it, by the name gRPC knows it by. It is what the
  // caller of *that* RPC asked for and not the write it became, so an RPC
  // written by hand is here under its own name.
  //
  // Empty in the first message, which is not something anybody asked for -- it
  // is what is already there.
  string action = 3 [features.field_presence = IMPLICIT];
}
`, e, e, e, e, e, e))
		}

		b.WriteString(fmt.Sprintf("message %sFilter {\n", e))
		for i, by := range v.List.By {
			if by.Ref {
				b.WriteString(fmt.Sprintf("  %sRef ref = %d;\n", e, i+1))
				continue
			}
			if by.Edge != "" {
				// The target's ref rather than its identifier, so that a caller
				// names it the way they name it everywhere else.
				b.WriteString(fmt.Sprintf("  %sRef %s = %d;\n", by.Target, by.Edge, i+1))
				continue
			}
			b.WriteString(fmt.Sprintf("  %s %s = %d;\n", protoTypeOf(by.Type), by.Field, i+1))
		}
		b.WriteString("}\n")
	}
	if n == 0 {
		return ""
	}

	return fmt.Sprintf(`// Code generated by protoc-gen-pd. DO NOT EDIT.
//
// The List of each entity that declared one. It is an overlay: it is merged
// onto the contract protoc-gen-orm-service generates, the same way an app's own
// hand-written additions are.

edition = "2023";

package %s;
%s`, pkg, b.String())
}

// protoTypeOf is how a field is written in a filter.
//
// A filter compares for equality and nothing else, so what matters is only that
// the wire type matches the column. Anything the schema can declare that is not
// here is refused when the declaration is read, not here.
func protoTypeOf(t ormpb.Type) string {
	switch t {
	case ormpb.Type_TYPE_UUID, ormpb.Type_TYPE_BYTES:
		return "bytes"
	case ormpb.Type_TYPE_STRING:
		return "string"
	case ormpb.Type_TYPE_BOOL:
		return "bool"
	case ormpb.Type_TYPE_INT32:
		return "int32"
	case ormpb.Type_TYPE_INT64:
		return "int64"
	case ormpb.Type_TYPE_UINT32:
		return "uint32"
	case ormpb.Type_TYPE_UINT64:
		return "uint64"
	}

	return ""
}

// Ts is the domain table, as TypeScript.
//
// It is generated for the same reason the Go one is, and the reason is sharper
// here: a domain byte is what says an identifier names a Robot rather than a
// Tenant, and the two halves of an app that disagree about it produce an
// identifier that reads as the wrong kind of thing on one side and the right
// kind on the other -- with nothing failing. Writing the table twice is exactly
// the drift `pdid` was made to prevent, so it is written once and emitted
// twice.
//
// Nothing else about an entity comes across. The messages are protobuf-es's and
// the client is a runtime function of a descriptor; what payday adds here is
// the one fact protobuf does not carry.
func Ts(s *Schema) string {
	b := &strings.Builder{}

	b.WriteString(`// Code generated by protoc-gen-pd. DO NOT EDIT.

/**
 * The domains this app's identifiers carry, and their names.
 *
 * It is the same table the Go half has, from the same declaration. Importing
 * this registers them, which is what makes ` + "`pdid.domainName`" + ` and a slug's
 * ` + "`#word`" + ` mean anything on this side.
 *
 * @module
 */

import { pdid } from '@lesomnus/payday'

`)

	for _, v := range s.Sorted() {
		fmt.Fprintf(b, "/** The domain identifiers of %s carry. */\n", v.FullName())
		fmt.Fprintf(b, "export const %sDomain = %d\n\n", v.GoName(), v.Domain)
	}

	b.WriteString("// Registered as this module is loaded, which is why importing it is the\n")
	b.WriteString("// whole of what an app does with it.\n")
	for _, v := range s.Sorted() {
		fmt.Fprintf(b, "pdid.register(%q, %sDomain, %q)\n", v.FullName(), v.GoName(), v.Name)
	}

	return b.String()
}

// TsEntities is the declaration the local store is implemented against: one
// object per entity, and no behaviour at all.
//
// This is §10.5's whole prescription in one function. The generator this
// replaces emitted the **bodies** of `get`, `_hydrate`, `_dehydrate` and
// `_compare` per entity, which is why it was 350 lines of string-building for
// an app with four tables -- and why what came out was unreadable. Everything
// those bodies did can be done once, at run time, by reading the protobuf-es
// descriptor and the object below.
//
// So the rule is the same one the Go layers are held to: **generate the
// declaration, implement the behaviour once, in a source somebody can read.**
// What is here is only what protobuf does not already carry.
func TsEntities(s *Schema, srcOf func(*Entity) string) string {
	b := &strings.Builder{}

	b.WriteString(`// Code generated by protoc-gen-pd. DO NOT EDIT.

/**
 * What the local store is built from: one declaration per entity.
 *
 * Nothing here is behaviour. The store reads these together with the
 * protobuf-es descriptors -- which carry every field's name, number and type at
 * run time -- and implements reading, writing, normalizing and reconciling
 * once, for every entity there is.
 *
 * @module
 */

import type { EntityDesc } from '@lesomnus/payday/store'

`)

	// The imports, one per file that holds an entity, and deduplicated: several
	// entities share a file often enough for it to be the normal case.
	seen := map[string][]string{}
	order := []string{}
	for _, v := range s.Sorted() {
		p := srcOf(v)
		if _, ok := seen[p]; !ok {
			order = append(order, p)
		}

		seen[p] = append(seen[p], v.GoName())
	}
	for _, p := range order {
		fmt.Fprintf(b, "import { %sSchema } from '%s'\n", strings.Join(seen[p], "Schema, "), p)
	}

	b.WriteString("\n")
	for _, v := range s.Sorted() {
		fmt.Fprintf(b, "/** %s, as the store holds it. */\n", v.FullName())
		fmt.Fprintf(b, "export const %s = {\n", v.GoName())
		fmt.Fprintf(b, "\ttypeName: %q,\n", v.FullName())
		fmt.Fprintf(b, "\tschema: %sSchema,\n", v.GoName())
		fmt.Fprintf(b, "\tdomain: %d,\n", v.Domain)

		// The version field, by the name protobuf-es gives it. It is what makes
		// a stale answer lose to a fresh one, and generation refuses a `watch:`
		// without one -- so this is empty only for an entity nothing watches.
		if v.HasVersionField() {
			fmt.Fprintf(b, "\tversion: %q,\n", lowerCamel(string(v.GetVersionField().Name())))
		}

		// The edges, which are what makes this a normalized store rather than a
		// pile of documents: a row arrives with its neighbours nested, and the
		// store takes them apart and keeps one copy of each.
		refs := []string{}
		for e := range v.Edges() {
			if e.IsList() {
				continue
			}

			refs = append(refs, fmt.Sprintf("{ field: %q, to: %q }",
				lowerCamel(string(e.Name())), e.Target().FullName()))
		}
		if len(refs) > 0 {
			fmt.Fprintf(b, "\trefs: [%s],\n", strings.Join(refs, ", "))
		}

		b.WriteString("} as const satisfies EntityDesc\n\n")
	}

	b.WriteString("/** Every entity of this app, which is what a store is opened over. */\n")
	b.WriteString("export const entities = [")
	for i, v := range s.Sorted() {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(v.GoName())
	}
	b.WriteString("] as const\n")

	return b.String()
}

// lowerCamel is a proto field name as protobuf-es writes it in TypeScript.
func lowerCamel(v string) string {
	out := &strings.Builder{}
	up := false
	for i, c := range v {
		switch {
		case c == '_':
			up = true
		case up:
			out.WriteRune(upper(c))
			up = false
		default:
			_ = i
			out.WriteRune(c)
		}
	}

	return out.String()
}

func upper(c rune) rune {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}

	return c
}

// TsImport is the module a generated entity's messages are in, relative to the
// root of the generated tree -- protoc-gen-es keeps the directory a .proto was
// in and renames the file.
func TsImport(p string) string {
	return "./" + path.Join(path.Dir(p), strings.TrimSuffix(path.Base(p), ".proto")+"_pb.js")
}
