package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/lesomnus/payday/pdpb"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

const readResourceSchema = `edition = "2023";
package test;
import "payday.proto";
option go_package = "example.com/app";
message Reading {
 bytes id = 1;
 string logical_id = 2;
 option (payday.resource) = {name:"reading", filter:".test.ReadingFilter", metadata:".test.Snapshot", watch:true};
}
message ReadingFilter { bytes job_id = 1; bool history = 2; }
message Snapshot { string revision = 1; }
`

func readResourcePlugin(t *testing.T, source string) *protogen.Plugin {
	t.Helper()
	_ = pdpb.E_Resource
	compiler := protocompile.Compiler{Resolver: protocompile.CompositeResolver{
		&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(map[string]string{"test/reading.proto": source})},
		protocompile.ResolverFunc(func(name string) (protocompile.SearchResult, error) {
			file, err := protoregistry.GlobalFiles.FindFileByPath(name)
			return protocompile.SearchResult{Desc: file}, err
		}),
	}}
	files, err := compiler.Compile(t.Context(), "test/reading.proto")
	if err != nil {
		t.Fatal(err)
	}
	request := &pluginpb.CodeGeneratorRequest{FileToGenerate: []string{"test/reading.proto"}}
	seen := map[string]bool{}
	var add func(protoreflect.FileDescriptor)
	add = func(file protoreflect.FileDescriptor) {
		if seen[file.Path()] {
			return
		}
		seen[file.Path()] = true
		for i := 0; i < file.Imports().Len(); i++ {
			add(file.Imports().Get(i).FileDescriptor)
		}
		data, err := proto.Marshal(protodesc.ToFileDescriptorProto(file))
		if err != nil {
			t.Fatal(err)
		}
		copy := &descriptorpb.FileDescriptorProto{}
		if err := proto.Unmarshal(data, copy); err != nil {
			t.Fatal(err)
		}
		request.ProtoFile = append(request.ProtoFile, copy)
	}
	for _, file := range files {
		add(file)
	}
	plugin, err := protogen.Options{}.New(request)
	if err != nil {
		t.Fatal(err)
	}
	return plugin
}

func TestReadResourceOnlySchemaGeneratesReadContractsWithoutORM(t *testing.T) {
	plugin := readResourcePlugin(t, readResourceSchema)
	if err := run(context.Background(), plugin, opts{Stage: "proto"}); err != nil {
		t.Fatal(err)
	}
	files := plugin.Response().GetFile()
	if len(files) != 1 {
		t.Fatalf("non-ORM resource produced %d contracts, want one", len(files))
	}
	for _, text := range []string{"service ReadingService", "rpc Get(ReadingGetRequest)", "rpc List(ReadingListRequest)", "rpc Watch(ReadingWatchRequest)", "repeated .test.Reading items", ".test.Snapshot metadata", "string revision = 2"} {
		if !strings.Contains(files[0].GetContent(), text) {
			t.Errorf("generated contract does not contain %q", text)
		}
	}
	for _, text := range []string{"rpc Add", "rpc Patch", "rpc Erase", "orm.message", "ReadingWatchItem"} {
		if strings.Contains(files[0].GetContent(), text) {
			t.Errorf("nonpersistent full-snapshot contract unexpectedly contains %q", text)
		}
	}
}

func TestReadResourceTypesAreValidatedAfterContractMerging(t *testing.T) {
	source := strings.Replace(readResourceSchema, ".test.ReadingFilter", ".test.DeferredFilter", 1)
	plugin := readResourcePlugin(t, source)
	if err := run(t.Context(), plugin, opts{Stage: "proto"}); err != nil {
		t.Fatalf("first pass must allow a filter supplied by a message-only overlay: %v", err)
	}
	plugin = readResourcePlugin(t, source)
	if err := run(t.Context(), plugin, opts{Stage: "go"}); err == nil || !strings.Contains(err.Error(), "after contract merging") {
		t.Fatalf("missing deferred message was not refused after merging: %v", err)
	}
}

func TestReadResourceOnlyGoHelpersHaveNoPersistenceImports(t *testing.T) {
	plugin := readResourcePlugin(t, readResourceSchema)
	if err := run(t.Context(), plugin, opts{Stage: "go"}); err != nil {
		t.Fatal(err)
	}
	files := plugin.Response().GetFile()
	if len(files) != 1 {
		t.Fatalf("resource-only generation produced %d files, want only read helpers", len(files))
	}
	content := files[0].GetContent()
	for _, text := range []string{"type ResourceServer interface", "type ServerWithResources interface", "func ComposeServer", "func RegisterServerWithResources", "UnimplementedReadingServiceServer", "ReadResources"} {
		if !strings.Contains(content, text) {
			t.Errorf("missing generated helper %q", text)
		}
	}
	for _, text := range []string{"internal/ent", "database/sql", "server/bare", "Scope", "Minter"} {
		if strings.Contains(content, text) {
			t.Errorf("computed read service unexpectedly uses persistence %q", text)
		}
	}
}
