package pdcli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
	"google.golang.org/protobuf/types/descriptorpb"
)

// buf's pinned compiler emits these positions after merging the overlays.
// Keep its original diagnostic even when the failed schema cannot be indexed.
// Explicit imports leave dependency direction in the author's source; adding
// them silently could introduce an import cycle between service contracts.
var missingContractType = regexp.MustCompile("(?m)^(.+_svc\\.g\\.proto):[0-9]+:[0-9]+:cannot find `([^`]+)` in this scope")

func (g Gen) contractImportHints(cause error) error {
	missing := missingContractType.FindAllStringSubmatch(cause.Error(), -1)
	if len(missing) == 0 {
		return cause
	}
	files := map[string]*descriptorpb.FileDescriptorProto{}
	types := map[string][]string{}
	// Read the final contracts: a type can come from another app overlay,
	// not just from the entity generator's first pass.
	err := contractsIn(g.Out, func(p string) error {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h := reporter.NewHandler(nil)
		ast, err := parser.Parse(p, bytes.NewReader(b), h)
		if err != nil {
			return err
		}
		parsed, err := parser.ResultFromAST(ast, false, h)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Join(g.Out, DirProto), p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fd := parsed.FileDescriptorProto()
		files[rel] = fd
		var messages func(string, []*descriptorpb.DescriptorProto, []*descriptorpb.EnumDescriptorProto)
		messages = func(prefix string, ms []*descriptorpb.DescriptorProto, es []*descriptorpb.EnumDescriptorProto) {
			for _, e := range es {
				name := strings.TrimPrefix(prefix+"."+e.GetName(), ".")
				types[name] = append(types[name], rel)
			}
			for _, m := range ms {
				name := strings.TrimPrefix(prefix+"."+m.GetName(), ".")
				types[name] = append(types[name], rel)
				messages(name, m.GetNestedType(), m.GetEnumType())
			}
		}
		messages(fd.GetPackage(), fd.GetMessageType(), fd.GetEnumType())
		return nil
	})
	if err != nil {
		return cause
	}
	var hints []string
	for _, miss := range missing {
		for rel, fd := range files {
			if miss[1] != rel && !strings.HasSuffix(filepath.ToSlash(miss[1]), "/"+rel) {
				continue
			}
			overlay := strings.TrimSuffix(rel, "_svc.g.proto") + "_svc.ext.proto"
			if _, err := os.Stat(g.Layout.Path(DirExt, overlay)); err != nil {
				continue
			}
			name := strings.TrimPrefix(miss[2], ".")
			if !strings.HasPrefix(miss[2], ".") {
				local := strings.TrimPrefix(fd.GetPackage()+"."+name, ".")
				if len(types[local]) > 0 {
					name = local
				}
			}
			// Duplicated declarations are a schema error, not a choice of imports.
			if candidates := types[name]; len(candidates) == 1 {
				target := candidates[0]
				if target != rel && !slices.Contains(fd.GetDependency(), target) {
					where := fmt.Sprintf("%s: %s is declared in %s; ", g.Layout.Rel(DirExt, overlay), name, target)
					if contractReaches(files, target, rel, map[string]bool{}) {
						hints = append(hints, where+fmt.Sprintf("import %q; would create an import cycle; move shared payload declarations to a separate .proto file or restructure the overlay dependency", target))
					} else {
						hints = append(hints, where+fmt.Sprintf("add import %q; to this overlay and run pd gen again", target))
					}
				}
			}
		}
	}
	if len(hints) == 0 {
		return cause
	}
	slices.Sort(hints)
	return fmt.Errorf("%w\n%s", cause, strings.Join(slices.Compact(hints), "\n"))
}

func contractReaches(files map[string]*descriptorpb.FileDescriptorProto, from, to string, seen map[string]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	for _, dep := range files[from].GetDependency() {
		if contractReaches(files, dep, to, seen) {
			return true
		}
	}
	return false
}
