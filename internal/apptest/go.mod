// The app payday is tried against. It is a module of its own because it owns
// the ent dependency, which payday itself does not have: the wall is generated
// into an app and payday only says what it should say.
module github.com/lesomnus/payday/internal/apptest

go 1.27.0

require (
	github.com/google/uuid v1.6.0
	github.com/lesomnus/grpc-dgram v0.0.0-20260727054342-be775dcd6ec2
	github.com/lesomnus/otx v0.0.0-20260807173743-977a5687d6ba
	github.com/lesomnus/payday v0.0.0-20260901023457-72d006c78d10
	github.com/lesomnus/protobuf-patch v0.0.0-20260803175157-e1b7a0c2804f
	github.com/lesomnus/xli v0.0.0-20260717171524-bf8cac633057
	github.com/lesomnus/z v0.0.0-20260531102454-3f1853bb4278
	github.com/ncruces/go-sqlite3 v0.35.3
	github.com/protobuf-orm/ent v0.0.0-20260901214045-4965c66b728e
	github.com/protobuf-orm/protobuf-orm v0.0.0-20260807003431-ce1156ba9f29
	github.com/protobuf-orm/protoc-gen-orm-ent/runtime v0.0.0-20260901215757-529b9e6aa9b5
	github.com/stretchr/testify v1.11.1
	golang.org/x/sync v0.22.0
	google.golang.org/grpc v1.83.0
	google.golang.org/protobuf v1.36.11
)

require (
	ariga.io/atlas v1.3.0 // indirect
	connectrpc.com/connect v1.19.1 // indirect
	connectrpc.com/cors v0.1.0 // indirect
	connectrpc.com/vanguard v0.4.0 // indirect
	entgo.io/ent v0.14.6 // indirect
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/apparentlymart/go-textseg/v17 v17.0.1 // indirect
	github.com/bmatcuk/doublestar v1.3.4 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/inflect v1.0.0 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/hashicorp/hcl/v2 v2.24.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/lesomnus/mkot v0.0.0-20260801183340-9c83100aa7c2 // indirect
	github.com/lesomnus/mkot/mkotx v0.0.0-20260801183340-9c83100aa7c2 // indirect
	github.com/lesomnus/mkot/pretty v0.0.0-20260801183340-9c83100aa7c2 // indirect
	github.com/lesomnus/otx/otxgrpc v0.0.0-20260807173743-977a5687d6ba // indirect
	github.com/lesomnus/sqlite3-wasm v0.0.0-20260726134538-bebcaebf933e // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.23 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/ncruces/go-sqlite3-wasm/v3 v3.2.35304 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/protobuf-orm/protoc-gen-orm-go v0.0.0-20260814204207-b3aac775fe3e // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/zclconf/go-cty v1.19.0 // indirect
	github.com/zclconf/go-cty-yaml v1.2.0 // indirect
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
	golang.org/x/exp v0.0.0-20250911091902-df9299821621 // indirect
	golang.org/x/mod v0.40.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260724162435-b2f20204f0df // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

tool (
	github.com/protobuf-orm/ent/cmd/ent
	github.com/protobuf-orm/protoc-gen-orm-go
)
