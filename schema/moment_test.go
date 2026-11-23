package schema_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/schema"
)

// TestAMomentIsNamedForItsEventInThePastTense, by the descriptors of every
// schema of payday's own: the entities an app copies and the wire payday
// serves itself.
//
// A `date_*` field names its event in the past tense, so that the name does
// not turn wrong once the moment has passed: `date_expires` reads as a promise
// about the future on a grant that ended a year ago, where `date_expired`, like
// `date_created`, says when. It was the one of payday's that broke the rule, in
// a grant, a token's answer and an entitlement, and this keeps it from coming
// back by copying.
func TestAMomentIsNamedForItsEventInThePastTense(t *testing.T) {
	x := require.New(t)

	// What carries each name, so that a failure says where.
	names := map[string][]string{}

	owned, err := schema.Owned()
	x.NoError(err)
	for own, fs := range owned {
		for _, f := range fs {
			names[f.Name] = append(names[f.Name], own.String())
		}
	}

	var walk func(ms protoreflect.MessageDescriptors)
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := range ms.Len() {
			m := ms.Get(i)
			for j := range m.Fields().Len() {
				f := m.Fields().Get(j)
				names[string(f.Name())] = append(names[string(f.Name())], string(f.FullName()))
			}
			walk(m.Messages())
		}
	}
	protoregistry.GlobalFiles.RangeFilesByPackage("payday", func(fd protoreflect.FileDescriptor) bool {
		walk(fd.Messages())
		return true
	})

	// A list of nothing would pass.
	x.Contains(names, "date_created")
	x.Contains(names, "date_effective", "a grant's window is read from payday's own wire")
	x.Empty(names["date_expires"])
}
