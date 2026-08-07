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
	github.com/bufbuild/protocompile v0.14.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/ettle/strcase v0.2.0 // indirect
	github.com/go-openapi/inflect v0.21.3 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/protobuf-orm/protoc-gen-orm-ent v0.0.0-20260807205916-3e9c932f5f85 // indirect
	github.com/protobuf-orm/protoc-gen-orm-go v0.0.0-20260804121030-6619a23a2859 // indirect
	github.com/protobuf-orm/protoc-gen-orm-service v0.0.0-20260807033829-df58c6f1abb6 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.6.2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

tool (
	github.com/lesomnus/payday/cmd/protoc-gen-pd
	github.com/protobuf-orm/protoc-gen-orm-ent
	github.com/protobuf-orm/protoc-gen-orm-go
	github.com/protobuf-orm/protoc-gen-orm-service
	google.golang.org/grpc/cmd/protoc-gen-go-grpc
	google.golang.org/protobuf/cmd/protoc-gen-go
)

// Worked on side by side with payday for now.
replace github.com/protobuf-orm/protobuf-orm => /workspaces/github.com/protobuf-orm/protobuf-orm

replace github.com/protobuf-orm/protoc-gen-orm-service => /workspaces/github.com/protobuf-orm/protoc-gen-orm-service

replace github.com/protobuf-orm/protoc-gen-orm-ent => /workspaces/github.com/protobuf-orm/protoc-gen-orm-ent
