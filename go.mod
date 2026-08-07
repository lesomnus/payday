module github.com/lesomnus/payday

go 1.26.2

require (
	github.com/google/uuid v1.6.0
	github.com/protobuf-orm/protobuf-orm v0.0.0-20260807003431-ce1156ba9f29
	github.com/stretchr/testify v1.11.1
	google.golang.org/grpc v1.83.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

tool google.golang.org/protobuf/cmd/protoc-gen-go

// Worked on side by side with payday for now.
replace github.com/protobuf-orm/protobuf-orm => /workspaces/github.com/protobuf-orm/protobuf-orm
