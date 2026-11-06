package pdgen

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// ReadResource is a computed read model, deliberately outside the ORM graph.
// Keeping it out of Schema.Entities is what prevents adding a service from
// creating an Ent model, SQL wall, trail or persistence implementation.
type ReadResource struct {
	Message  *protogen.Message
	Options  *pdpb.ReadResource
	Filter   protoreflect.FullName
	Metadata protoreflect.FullName
}

func (r *ReadResource) GoName() string { return r.Message.GoIdent.GoName }

func (r *ReadResource) Name() string {
	return pdid.Name(r.Options.GetName(), string(r.Message.Desc.Name()))
}

func readResources(files []*protogen.File) ([]*ReadResource, error) {
	var resources []*ReadResource
	for _, file := range files {
		if !file.Generate {
			continue
		}
		var nested func([]*protogen.Message) error
		nested = func(messages []*protogen.Message) error {
			for _, message := range messages {
				if proto.HasExtension(message.Desc.Options(), pdpb.E_Resource) {
					return fmt.Errorf("%s: a read resource must be a top-level message", message.Desc.FullName())
				}
				if err := nested(message.Messages); err != nil {
					return err
				}
			}
			return nil
		}
		for _, message := range file.Messages {
			if err := nested(message.Messages); err != nil {
				return nil, err
			}
			options, _ := proto.GetExtension(message.Desc.Options(), pdpb.E_Resource).(*pdpb.ReadResource)
			if options == nil {
				continue
			}
			if proto.HasExtension(message.Desc.Options(), ormpb.E_Message) || proto.HasExtension(message.Desc.Options(), pdpb.E_Entity) {
				return nil, fmt.Errorf("%s: (payday.resource) cannot also declare an ORM or payday entity", message.Desc.FullName())
			}
			id := message.Desc.Fields().ByName("id")
			if id == nil || id.Number() != 1 || id.Kind() != protoreflect.BytesKind || id.IsList() || id.IsMap() {
				return nil, fmt.Errorf("%s: a read resource requires opaque bytes id = 1", message.Desc.FullName())
			}
			filter := protoreflect.FullName(strings.TrimPrefix(options.GetFilter(), "."))
			metadata := protoreflect.FullName(strings.TrimPrefix(options.GetMetadata(), "."))
			if !filter.IsValid() || metadata != "" && !metadata.IsValid() {
				return nil, fmt.Errorf("%s: resource filter and metadata must name fully-qualified message types", message.Desc.FullName())
			}
			resources = append(resources, &ReadResource{Message: message, Options: options, Filter: filter, Metadata: metadata})
		}
	}
	return resources, nil
}

func resourceMessages(files []*protogen.File) map[protoreflect.FullName]protoreflect.MessageDescriptor {
	all := map[protoreflect.FullName]protoreflect.MessageDescriptor{}
	var walk func([]*protogen.Message)
	walk = func(messages []*protogen.Message) {
		for _, message := range messages {
			all[message.Desc.FullName()] = message.Desc
			walk(message.Messages)
		}
	}
	for _, file := range files {
		walk(file.Messages)
	}
	return all
}

// ValidateReadResources runs after contract merging. A filter may be declared
// in a message-only service overlay so that it can use an entity's generated
// Ref; those messages do not exist during the first, contract-generating pass.
func ValidateReadResources(s *Schema, files []*protogen.File) error {
	all := resourceMessages(files)
	for _, resource := range s.Resources {
		for _, name := range []protoreflect.FullName{resource.Filter, resource.Metadata} {
			if name != "" && all[name] == nil {
				return fmt.Errorf("%s: resource type %s is not a message after contract merging", resource.Message.Desc.FullName(), name)
			}
		}
	}
	return nil
}

func checkResourceNames(s *Schema) error {
	names := map[string]bool{}
	cli := map[string]bool{}
	for _, entity := range s.Entities {
		names[entity.GoName()], cli[entity.Name] = true, true
	}
	for _, resource := range s.Resources {
		if len(s.Entities) > 0 && resource.GoName() == "Next" {
			return fmt.Errorf("%s: resource name Next is reserved for middleware traversal", resource.Message.Desc.FullName())
		}
		if names[resource.GoName()] || cli[resource.Name()] {
			return fmt.Errorf("%s: resource name collides with another resource or entity", resource.Message.Desc.FullName())
		}
		names[resource.GoName()], cli[resource.Name()] = true, true
	}
	return nil
}

// These identifiers land beside protobuf types, not in the ORM layer. Refuse
// a collision while reading the schema rather than writing two declarations
// that only the application's next Go build would discover.
func checkResourceSymbols(s *Schema, files []*protogen.File) error {
	packages := map[protogen.GoImportPath]bool{}
	for _, resource := range s.Resources {
		packages[resource.Message.GoIdent.GoImportPath] = true
	}
	reserved := map[string]bool{
		"ResourceServer": true, "StaticResourceServer": true,
		"ServerWithResources": true, "serverWithResources": true,
		"ComposeServer": true, "RegisterServerWithResources": true,
		"ReadResources": true,
	}
	check := func(name string, origin protoreflect.FullName) error {
		if reserved[name] {
			return fmt.Errorf("%s: Go symbol %s is reserved for generated read resource helpers", origin, name)
		}
		return nil
	}
	var enums func([]*protogen.Enum) error
	enums = func(values []*protogen.Enum) error {
		for _, value := range values {
			if err := check(value.GoIdent.GoName, value.Desc.FullName()); err != nil {
				return err
			}
			for _, item := range value.Values {
				if err := check(item.GoIdent.GoName, item.Desc.FullName()); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var messages func([]*protogen.Message) error
	messages = func(values []*protogen.Message) error {
		for _, value := range values {
			if err := check(value.GoIdent.GoName, value.Desc.FullName()); err != nil {
				return err
			}
			if err := enums(value.Enums); err != nil {
				return err
			}
			if err := messages(value.Messages); err != nil {
				return err
			}
		}
		return nil
	}
	for _, file := range files {
		if !packages[file.GoImportPath] {
			continue
		}
		if err := messages(file.Messages); err != nil {
			return err
		}
		if err := enums(file.Enums); err != nil {
			return err
		}
		for _, service := range file.Services {
			name := service.GoName
			for _, symbol := range []string{name + "Client", name + "Server", "New" + name + "Client", "Register" + name + "Server", "Unimplemented" + name + "Server", "Unsafe" + name + "Server", name + "_ServiceDesc"} {
				if err := check(symbol, service.Desc.FullName()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ReadResourceProto emits only the contract. It promises neither SQL queries
// nor automatic authorization: an absent application implementation remains
// gRPC Unimplemented, instead of exposing a computed value through a fake wall.
func ReadResourceProto(resources []*ReadResource, file *protogen.File, files []*protogen.File) string {
	if len(resources) == 0 {
		return ""
	}
	all := resourceMessages(files)
	imports := map[string]bool{file.Desc.Path(): true}
	contract := strings.TrimSuffix(file.Desc.Path(), ".proto") + "_svc.g.proto"
	for _, resource := range resources {
		for _, name := range []protoreflect.FullName{resource.Filter, resource.Metadata} {
			if message := all[name]; message != nil && message.ParentFile().Path() != contract {
				imports[message.ParentFile().Path()] = true
			}
		}
	}
	var out strings.Builder
	out.WriteString("// Code generated by protoc-gen-pd. DO NOT EDIT.\n\nedition = \"2023\";\n")
	fmt.Fprintf(&out, "package %s;\n", file.Desc.Package())
	paths := make([]string, 0, len(imports))
	for name := range imports {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	for _, name := range paths {
		fmt.Fprintf(&out, "import %q;\n", name)
	}
	goPackage := file.Desc.Options().(*descriptorpb.FileOptions).GetGoPackage()
	fmt.Fprintf(&out, "option go_package = %q;\n", goPackage)
	for _, resource := range resources {
		name := string(resource.Message.Desc.Name())
		value := "." + string(resource.Message.Desc.FullName())
		fmt.Fprintf(&out, "\nservice %sService {\n  rpc Get(%sGetRequest) returns (%s);\n  rpc List(%sListRequest) returns (%sListResponse);\n", name, name, value, name, name)
		if resource.Options.GetWatch() {
			fmt.Fprintf(&out, "  // Complete collection snapshots, including empty collections.\n  rpc Watch(%sWatchRequest) returns (stream %sWatchResponse);\n", name, name)
		}
		out.WriteString("}\n")
		fmt.Fprintf(&out, "message %sRef { bytes id = 1 [features.field_presence = IMPLICIT]; }\nmessage %sGetRequest { %sRef ref = 1; }\n", name, name, name)
		fmt.Fprintf(&out, "message %sListRequest {\n  repeated .%s filters = 1;\n  int32 size = 2 [features.field_presence = IMPLICIT];\n  string after = 3 [features.field_presence = IMPLICIT];\n  bool reverse = 4 [features.field_presence = IMPLICIT];\n}\n", name, resource.Filter)
		fmt.Fprintf(&out, "message %sListResponse {\n  repeated %s items = 1;\n  string next = 2 [features.field_presence = IMPLICIT];\n", name, value)
		if resource.Metadata != "" {
			fmt.Fprintf(&out, "  .%s metadata = 3;\n", resource.Metadata)
		}
		out.WriteString("}\n")
		if resource.Options.GetWatch() {
			fmt.Fprintf(&out, "message %sWatchRequest {\n  repeated .%s filters = 1;\n  // Collection revision last observed; never an authorization grant.\n  string revision = 2 [features.field_presence = IMPLICIT];\n}\nmessage %sWatchResponse {\n  // Replace the complete previous collection, including with an empty one.\n  repeated %s items = 1;\n", name, resource.Filter, name, value)
			if resource.Metadata != "" {
				fmt.Fprintf(&out, "  .%s metadata = 2;\n", resource.Metadata)
			}
			out.WriteString("}\n")
		}
	}
	return out.String()
}

// EmitReadResourceServers composes the generated non-ORM services with the
// existing application stack. No generated ORM store file is replaced, and the
// application supplies the read model bound to its final authorized server.
func EmitReadResourceServers(g *protogen.GeneratedFile, s *Schema, pkg protogen.GoImportPath) {
	g.P("type ResourceServer interface {")
	for _, resource := range s.Resources {
		g.P(resource.GoName(), "() ", pkg.Ident(resource.GoName()+"ServiceServer"))
	}
	g.P("}")
	g.P("type StaticResourceServer struct {")
	for _, resource := range s.Resources {
		g.P(resource.GoName(), "Server ", pkg.Ident(resource.GoName()+"ServiceServer"))
	}
	g.P("}")
	for _, resource := range s.Resources {
		name := resource.GoName()
		g.P("func(s StaticResourceServer) ", name, "() ", pkg.Ident(name+"ServiceServer"), " { if s.", name, "Server == nil { return Unimplemented", name, "ServiceServer{} }; return s.", name, "Server }")
	}
	g.P("type ServerWithResources interface {")
	if len(s.Entities) > 0 {
		g.P("Server")
	}
	g.P("ResourceServer\n}")
	g.P("type serverWithResources struct {")
	if len(s.Entities) > 0 {
		g.P("Server")
	}
	g.P("ResourceServer\n}")
	if len(s.Entities) > 0 {
		g.P("func ComposeServer(base Server, resources ResourceServer) ServerWithResources { return serverWithResources{base,resources} }")
		g.P("// Next preserves traversal of the original middleware stack.")
		g.P("func(s serverWithResources) Next() Server { return s.Server }")
	} else {
		g.P("func ComposeServer(resources ResourceServer) ServerWithResources { return serverWithResources{resources} }")
	}
	g.P("// RegisterServerWithResources registers the existing stack and its computed read services.")
	g.P("func RegisterServerWithResources(registrar ", pkgGrpc.Ident("ServiceRegistrar"), ", server ServerWithResources) {")
	if len(s.Entities) > 0 {
		g.P("RegisterServer(registrar,server)")
	}
	for _, resource := range s.Resources {
		name := resource.GoName()
		g.P("Register", name, "ServiceServer(registrar,server.", name, "())")
	}
	g.P("}")
	g.P("// ReadResources describes computed values; it does not register identifier domains.")
	g.P("var ReadResources = map[string]string{")
	for _, resource := range s.Resources {
		g.P(strconv.Quote(string(resource.Message.Desc.FullName())), ":", strconv.Quote(resource.Name()), ",")
	}
	g.P("}")
}

func ResourceContractFilename(file *protogen.File) string {
	return path.Join(path.Dir(file.Desc.Path()), strings.TrimSuffix(path.Base(file.Desc.Path()), ".proto")+"_svc.resource.proto")
}
