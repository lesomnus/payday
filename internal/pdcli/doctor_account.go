package pdcli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lesomnus/payday/internal/pdgen"
)

// accountPkg is the account layer's runtime.
const accountPkg = "github.com/lesomnus/payday/account"

// doctorAccounts says what an app whose holders are memberships of accounts
// has left out (payday#184): a method an account calls as itself whose layer
// declares nothing it reads, and a resolver that serves no call as one.
//
// Both fail safe and both are found late. A method whose layer declares
// nothing reads nothing through the walled servers -- no tenant to narrow by,
// so the wall answers every entity with none -- which is an empty answer
// rather than a leak, and is found the day somebody asks why the page is
// blank. A resolver nobody wired serves every credential as a holder's, so an
// account's session names nobody who is here and every call it makes is
// refused.
//
// Read off the app's own Go, as the access record's wiring is: a call of
// `pd.AccountReads` naming the method -- by the constant protoc-gen-go-grpc
// writes for it, or its full name written out -- is its declaration, an empty
// `pd.Reads` included, which is how a method that reads through the servers
// nothing at all says so; and `pd.AccountResolver` or an `account.Resolver` is
// the resolver. A test does not count, and neither does generated Go: what
// they say is not what the app serves.
func doctorAccounts(l Layout, s *pdgen.Schema) []Finding {
	if s.Accounts == nil {
		return nil
	}

	declared := map[string]bool{}
	wired := false
	_ = filepath.WalkDir(l.Work, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "testdata", "vendor", ".git", "ent":
				return fs.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}

		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil || ast.IsGenerated(f) {
			return nil
		}

		account := importedAs(f, accountPkg)
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "AccountReads" || len(v.Args) < 2 {
					break
				}
				switch m := v.Args[1].(type) {
				case *ast.SelectorExpr:
					declared[m.Sel.Name] = true
				case *ast.Ident:
					declared[m.Name] = true
				case *ast.BasicLit:
					if m.Kind == token.STRING {
						if s, err := strconv.Unquote(m.Value); err == nil {
							declared[s] = true
						}
					}
				}

			case *ast.SelectorExpr:
				// `pd.AccountResolver`, whatever the generated package is
				// called where it is used.
				if v.Sel.Name == "AccountResolver" {
					wired = true
				}

			case *ast.CompositeLit:
				sel, ok := v.Type.(*ast.SelectorExpr)
				if !ok || account == "" || sel.Sel.Name != "Resolver" {
					break
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == account {
					wired = true
				}
			}

			return true
		})

		return nil
	})

	var vs []Finding
	var missing []string
	for _, m := range s.Accounts.Methods {
		if !declared[m.Const] && !declared[m.Full] {
			missing = append(missing, m.Full)
		}
	}
	if len(missing) > 0 {
		m := s.Accounts.Methods[0]
		for _, v := range s.Accounts.Methods {
			if v.Full == missing[0] {
				m = v
			}
		}

		vs = append(vs, Finding{
			What: fmt.Sprintf("methods an account calls as itself declare nothing they read, so the walled servers "+
				"answer each with nothing -- there is no tenant to narrow by, and what the wall would have said, the "+
				"layer has to: %s", strings.Join(missing, ", ")),
			Fix: fmt.Sprintf(`// in the layer serving it, before it reads
ctx, err := pd.AccountReads(ctx, app.%s, pd.Reads{
	Invitation: func(ctx context.Context, account pdid.Id) (predicate.Invitation, error) {
		return invitation.Account(account.Uuid()), nil
	},
})

// or, for one that reads nothing through the walled servers, that it does not
ctx, err := pd.AccountReads(ctx, app.%s, pd.Reads{})

docs/guide/accounts.md has the rest.`, m.Const, m.Const),
		})
	}

	if !wired {
		vs = append(vs, Finding{
			What: "the schema says the holders are memberships of accounts, and no resolver serves a call as one: " +
				"a credential that names an account names nobody here, and every call it makes is refused",
			Fix: `r := pd.AccountResolver(client, "acme-workspace", namesAnAccount, Resolver(ungated))

chain := grpcx.Serving(ctx, ...).
	WithUnary(auth.InterceptorUnary(handler, r, auth.PublicDefault)).
	WithStream(auth.InterceptorStream(handler, r, auth.PublicDefault)).
	...

where namesAnAccount says which credentials are an account's -- a session minted for one.`,
		})
	}

	return vs
}
