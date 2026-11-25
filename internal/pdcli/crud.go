package pdcli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
)

// checkCrud refuses an app whose own schema still says `crud` in an
// `(orm.message)`'s `rpc`, naming each place, before buf is run on it.
//
// `orm` had `crud`, which stood for Add, Get, Patch and Erase, and an Apply
// came with every Patch whether anything called it or not. It has neither now
// (protobuf-orm/protobuf-orm#4): each verb is named on its own, `apply` among
// them, and no verb brings another. Against that `orm` a schema that says
// `crud` does not compile, and what buf says of it --
//
//	cannot resolve message field name for `orm.RpcOptions`
//
// -- names the option and not the change, so whoever reads it goes looking for
// a typo. This says what happened and what to write.
//
// Read from the files rather than from a compile because the compile is what
// fails: there is no descriptor to ask. The app's own files and its overlays,
// which it wrote; not payday's copies, which `pd gen` writes again from what
// payday ships, nor a contract, which is generated. A file that does not parse
// is buf's to say, with a line and a column.
func checkCrud(l Layout) error {
	at, err := crudsIn(l)
	if err != nil || len(at) == 0 {
		return err
	}

	return errors.New(crudSaid(at))
}

// doctorCrud is [checkCrud] as `pd doctor` says it: a finding nothing after it
// can get past, since the schema does not compile. The fix is in what it says,
// beside where, so it is not said a second time as the finding's fix.
func doctorCrud(l Layout) []Finding {
	at, err := crudsIn(l)
	if err != nil {
		return []Finding{{What: fmt.Sprintf("cannot read this app's schema: %s", err), Fatal: true}}
	}
	if len(at) == 0 {
		return nil
	}

	return []Finding{{What: crudSaid(at), Fatal: true}}
}

const crudFix = "option (orm.message) = {rpc: {add: {}, get: {}, patch: {}, erase: {}}};"

func crudSaid(at []string) string {
	b := &strings.Builder{}
	for _, v := range at {
		fmt.Fprintf(b, "%s: says `crud` in an (orm.message)'s rpc\n", v)
	}
	b.WriteString("\n`crud` is gone from orm: each verb is named on its own -- `add`, `get`, `patch`, `apply` and `erase` -- " +
		"and `apply` no longer comes with `patch`. Name the verbs the entity serves, as `crud` did:\n\n" +
		"    " + crudFix + "\n\n" +
		"and `apply: {}` beside them where something calls an Apply.")

	return b.String()
}

// crudsIn is where the app's own schema says `crud`, as `file:line`, in the
// order the files are walked.
func crudsIn(l Layout) ([]string, error) {
	at := []string{}
	err := filepath.WalkDir(l.Path(DirProto), func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if p == l.Path(l.DirPd()) {
				return fs.SkipDir
			}

			return nil
		case !strings.HasSuffix(p, ".proto") || strings.HasSuffix(p, ".g.proto"):
			return nil
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Contains(b, []byte("crud")) {
			return nil
		}

		f, err := parser.Parse(p, bytes.NewReader(b), reporter.NewHandler(nil))
		if err != nil {
			return nil
		}

		return ast.Walk(f, &ast.SimpleVisitor{DoVisitOptionNode: func(n *ast.OptionNode) error {
			for _, v := range crudsOf(n) {
				at = append(at, fmt.Sprintf("%s:%d", l.rel(p), f.NodeInfo(v).Start().Line))
			}

			return nil
		}})
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	return at, nil
}

// crudsOf is each `crud` an option says under `(orm.message)`'s `rpc`, however
// it is spelled: in a literal, `(orm.message) = {rpc: {crud: true}}`; by path,
// `(orm.message).rpc.crud = true`; or between the two.
func crudsOf(n *ast.OptionNode) []ast.Node {
	parts := n.Name.Parts
	if len(parts) == 0 || !parts[0].IsExtension() || !strings.HasSuffix(string(parts[0].Name.AsIdentifier()), "orm.message") {
		return nil
	}

	path := []string{}
	for _, v := range parts[1:] {
		path = append(path, string(v.Name.AsIdentifier()))
	}
	switch {
	case len(path) >= 2 && path[0] == "rpc" && path[1] == "crud":
		return []ast.Node{n}
	case len(path) == 1 && path[0] == "rpc":
		return fieldsNamed(n.Val, "crud")
	case len(path) == 0:
		out := []ast.Node{}
		for _, v := range fieldsOfLiteral(n.Val, "rpc") {
			out = append(out, fieldsNamed(v.Val, "crud")...)
		}
		return out
	}

	return nil
}

// fieldsOfLiteral is each field of a message literal called `name`.
func fieldsOfLiteral(v ast.ValueNode, name string) []*ast.MessageFieldNode {
	m, ok := v.(*ast.MessageLiteralNode)
	if !ok {
		return nil
	}

	out := []*ast.MessageFieldNode{}
	for _, f := range m.Elements {
		if f.Name != nil && !f.Name.IsExtension() && string(f.Name.Name.AsIdentifier()) == name {
			out = append(out, f)
		}
	}

	return out
}

func fieldsNamed(v ast.ValueNode, name string) []ast.Node {
	out := []ast.Node{}
	for _, f := range fieldsOfLiteral(v, name) {
		out = append(out, f)
	}

	return out
}
