package pdcli

import (
	"fmt"
	"path/filepath"
	"strings"
)

// The buf templates are written here rather than kept in the app, and that is
// the enforcement `pd gen` is for.
//
// Everything in them is a decision that fails quietly when it is got wrong:
//
//   - `strategy: all` on every plugin. The default runs a plugin once per
//     directory, and an entity whose tenant is declared in another one is then
//     not a generation target when its own file is read -- so the wall is
//     generated with a hole in it and nothing fails.
//   - one `module=` for every plugin, so that everything lands in one Go
//     package. Two packages means two sets of ent schemas and no edge between
//     them, which is the wall again.
//   - `../` in front of every namer for an app whose messages are not at the
//     module root. A generator names its output relative to `go_package`, and
//     the ent runtime and the servers belong beside `go.mod` rather than beside
//     the messages; without it they follow the messages and `internal/ent`
//     becomes `api/internal/ent`.
//   - `API_OPAQUE`. The generated servers build requests with the `_builder`
//     form and read them with `Has`/`Get`; the open API has neither.
//
// A page of instructions gets each of these right the first time and wrong the
// third.

// Templates is every buf template one generation runs, in order.
//
// It is exported so that what is claimed about them above can be tested by
// reading them, which is the only way to test most of it: a `strategy` left at
// the default does not fail, it generates less.
func Templates(l Layout, out string) []string {
	return []string{
		tmplContracts(l, filepath.Join(out, "svc"), filepath.Join(out, "pd")),
		tmplCode(l, out),
	}
}

// tmplContracts is the pass that writes the service contracts.
//
// It runs `protoc-gen-pd` as well, because a `List` is an RPC and has to be in
// the contract before anything is generated from the contract.
//
// `svc` and `pd` are the two halves, and they are scratch directories rather
// than places in the app: only the merge reads them.
func tmplContracts(l Layout, svc string, pd string) string {
	return fmt.Sprintf(`version: v2
inputs:
  - directory: %s
plugins:
  - local: [go, tool, github.com/protobuf-orm/protoc-gen-orm-service]
    out: %s
    strategy: all
  - local: [go, tool, github.com/lesomnus/payday/cmd/protoc-gen-pd]
    out: %s
    opt:
      - stage=proto
    strategy: all
`,
		yamlPath(l.Rel(DirProto)),
		yamlPath(rel(l.Work, svc)),
		yamlPath(rel(l.Work, pd)),
	)
}

// tmplCode is the pass that writes Go.
func tmplCode(l Layout, out string) string {
	return fmt.Sprintf(`version: v2
inputs:
  - directory: %s
plugins:
  - local: [go, tool, google.golang.org/protobuf/cmd/protoc-gen-go]
    out: %[2]s
    opt:
      - module=%[3]s
      - default_api_level=API_OPAQUE
    strategy: all
  - local: [go, tool, google.golang.org/grpc/cmd/protoc-gen-go-grpc]
    out: %[2]s
    opt:
      - module=%[3]s
    strategy: all
  - local: [go, tool, github.com/protobuf-orm/protoc-gen-orm-go]
    out: %[2]s
    opt:
      - module=%[3]s
      - query.namer={{ .Name }}.g.go
      - store.name=store.g.go
    strategy: all
  - local: [go, tool, github.com/protobuf-orm/protoc-gen-orm-ent]
    out: %[2]s
    opt:
      - module=%[3]s
      - schema.namer=%[4]s/schema/{{ .Name }}.go
      - ent.namer=%[4]s/{{ .Name }}.g.go
      - server.namer=%[5]s/{{ .Name }}.g.go
      - store.name=%[5]s/store.g.go
    strategy: all
  - local: [go, tool, github.com/lesomnus/payday/cmd/protoc-gen-pd]
    out: %[2]s
    opt:
      - module=%[3]s
      - ent=%[4]s
      - bare=%[5]s
      - out=%[6]s
    strategy: all
`,
		yamlPath(l.Rel(DirProto)),
		yamlPath(out),
		l.Module,
		l.Up()+DirEnt, l.Up()+DirBare, l.Up()+DirPd_,
	)
}

// rel is [filepath.Rel] answering with the absolute path when there is no
// relative one -- a staging directory in the system's temp is not under the
// workspace, and buf takes either.
func rel(base string, p string) string {
	v, err := filepath.Rel(base, p)
	if err != nil || strings.HasPrefix(v, "..") {
		return p
	}

	return v
}

// yamlPath quotes a path so that one holding a character YAML reads as syntax
// is still a path.
func yamlPath(p string) string { return fmt.Sprintf("%q", filepath.ToSlash(p)) }

// tmplTs is the pass that writes TypeScript.
//
// One plugin, which is worth saying because it looks like it should be two:
// protobuf-es v2 emits the service descriptors as well as the messages, and
// Connect's `createClient` takes those descriptors directly. There is no
// separate client generator to keep in step.
//
// It is a pass of its own rather than another plugin on the Go one because the
// two have different audiences: a backend-only app runs `pd gen` and never
// installs npm, and a plugin it has no binary for would fail the whole
// generation rather than the half it is about.
func tmplTs(l Layout, out string, plugin string) string {
	return fmt.Sprintf(`version: v2
inputs:
  - directory: %[1]s
plugins:
  - local: [%[2]s]
    out: %[3]s
    opt:
      - target=ts
      - import_extension=js
    strategy: all
  - local: [go, tool, github.com/lesomnus/payday/cmd/protoc-gen-pd]
    out: %[3]s
    opt:
      - stage=ts
    strategy: all
`,
		yamlPath(l.Rel(DirProto)),
		yamlPath(plugin),
		yamlPath(rel(l.Work, filepath.Join(out, DirTsGen))),
	)
}
