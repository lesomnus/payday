// The app payday is tried against. It is a module of its own because it owns
// the ent dependency, which payday itself does not have: the wall is generated
// into an app and payday only says what it should say.
module github.com/lesomnus/payday/internal/apptest

go 1.26.4

require (
	entgo.io/ent v0.14.6
	github.com/google/uuid v1.6.0
	github.com/lesomnus/payday v0.0.0-20260808062503-7d8351263d45
	github.com/lesomnus/protobuf-patch v0.0.0-20260803175157-e1b7a0c2804f
	github.com/ncruces/go-sqlite3 v0.35.3
	github.com/protobuf-orm/protobuf-orm v0.0.0-20260807003431-ce1156ba9f29
	github.com/protobuf-orm/protoc-gen-orm-ent/runtime v0.0.0-20260807205916-3e9c932f5f85
	github.com/stretchr/testify v1.11.1
	google.golang.org/grpc v1.83.0
	google.golang.org/protobuf v1.36.11
)

require (
	ariga.io/atlas v0.36.2-0.20250730182955-2c6300d0a3e1 // indirect
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/bmatcuk/doublestar v1.3.4 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/displaywidth v0.6.2 // indirect
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/inflect v0.21.3 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/hashicorp/hcl/v2 v2.18.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lesomnus/grpc-dgram v0.0.0-20260726142955-d48ce49dbd65 // indirect
	github.com/lesomnus/mkot v0.0.0-20260801183340-9c83100aa7c2 // indirect
	github.com/lesomnus/mkot/mkotx v0.0.0-20260801183340-9c83100aa7c2 // indirect
	github.com/lesomnus/mkot/pretty v0.0.0-20260801183340-9c83100aa7c2 // indirect
	github.com/lesomnus/otx v0.0.0-20260807173743-977a5687d6ba // indirect
	github.com/lesomnus/otx/otxgrpc v0.0.0-20260807173743-977a5687d6ba // indirect
	github.com/lesomnus/z v0.0.0-20260531102454-3f1853bb4278 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.23 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/ncruces/go-sqlite3-wasm/v3 v3.2.35304 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/olekukonko/cat v0.0.0-20250911104152-50322a0618f6 // indirect
	github.com/olekukonko/errors v1.1.0 // indirect
	github.com/olekukonko/ll v0.1.4-0.20260115111900-9e59c2286df0 // indirect
	github.com/olekukonko/tablewriter v1.1.3 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/protobuf-orm/protoc-gen-orm-go v0.0.0-20260808062124-7336db3ccda7 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/spf13/cobra v1.7.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/zclconf/go-cty v1.14.4 // indirect
	github.com/zclconf/go-cty-yaml v1.1.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/bridges/otelslog v0.18.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.68.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/log v0.20.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/sdk v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.20.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.47.0 // indirect
	golang.org/x/tools/go/packages/packagestest v0.1.1-deprecated // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260724162435-b2f20204f0df // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

tool (
	entgo.io/ent/cmd/ent
	github.com/protobuf-orm/protoc-gen-orm-go
)

// transport/jsport is in grpc-dgram's tree and not in any version the proxy
// has yet. It is somebody else's unpushed work, so this points at the checkout
// rather than pushing it; drop the line once grpc-dgram publishes it.
replace github.com/lesomnus/grpc-dgram => /workspaces/github.com/lesomnus/grpc-dgram
