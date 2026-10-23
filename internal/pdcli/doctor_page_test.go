package pdcli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// A page of an app that keeps a past, whose transport cannot read an event
// (payday#151): one that speaks JSON and is handed no registry of the app's
// event messages fails a List of events on the first that holds a value.
//
// Against what `pd new` writes, with a past declared -- the page an app that
// keeps one starts from -- and scripts written beside it: a page is read for
// what it says, and need not build for that.

// keeping is what `pd new` writes, declaring a past.
func keeping(t *testing.T, x *require.Assertions) (string, pdcli.Layout) {
	t.Helper()

	dir, _ := scaffold(t, x)

	p := filepath.Join(dir, pdcli.DirProto, "app", "thing.proto")
	b, err := os.ReadFile(p)
	x.NoError(err)
	at := "option go_package = \"github.com/acme/thing\";\n"
	x.Contains(string(b), at)
	x.NoError(os.WriteFile(p, []byte(strings.Replace(string(b), at,
		at+"option (payday.app) = {past: {event: 17, fact: 16}};\n", 1)), 0o644))

	l, err := pdcli.Discover(dir)
	x.NoError(err)
	x.NotNil(l.Past)

	return dir, l
}

// script writes one file of the page, under `ts/src`.
func script(t *testing.T, dir string, name string, src string) {
	t.Helper()

	p := filepath.Join(dir, "ts", "src", filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))
}

// edit replaces `old` in a file of the app, which has to be there.
func edit(t *testing.T, dir string, name string, old string, new string) {
	t.Helper()

	p := filepath.Join(dir, filepath.FromSlash(name))
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Contains(t, string(b), old, "%s has no %q to change", name, old)
	require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644))
}

const told = "makes a transport that speaks JSON and hands it no registry of this app's events"

// TestAPageMadeBeforeTheRegistryIsTold: the transports `pd new` writes hand
// the registry, so a fresh app that keeps a past is told nothing -- and with
// the line gone, which is a page made before it, the file is told, with the
// import from where it is and the line to add. An app with no past is told
// nothing either way: it has no event to read.
func TestAPageMadeBeforeTheRegistryIsTold(t *testing.T) {
	x := require.New(t)

	dir, l := keeping(t, x)
	x.NotContains(found(t, l), told, "what pd new writes was told")

	edit(t, dir, "ts/src/main.tsx", "jsonOptions: { registry: events },", "")

	vs := pdcli.Doctor(t.Context(), l)
	var v *pdcli.Finding
	for i := range vs {
		if strings.Contains(vs[i].What, told) {
			x.Nil(v, "told twice")
			v = &vs[i]
		}
	}
	x.NotNil(v, "a page made before the registry was not told")
	x.True(strings.HasPrefix(v.What, "ts/src/main.tsx "+told), v.What)
	x.False(v.Fatal, "the page is not the generation")
	x.Contains(v.Fix, "import { events } from '../gen/events.js'")
	x.Contains(v.Fix, "createConnectTransport({\n\t...,\n\tjsonOptions: { registry: events },\n})")
	x.Contains(v.Fix, "ts/gen/events.ts is what `pd gen --ts` writes", "this page was never generated")

	// The other transport still hands it.
	x.NotContains(found(t, l), "ts/src/session.tsx "+told)

	t.Run("and an app with no past is not", func(t *testing.T) {
		x := require.New(t)

		dir, l := scaffold(t, x)
		edit(t, dir, "ts/src/main.tsx", "jsonOptions: { registry: events },", "")
		x.Nil(l.Past)
		x.NotContains(found(t, l), told)
	})
}

// TestAPageThatHandsTheRegistryItsOwnWayIsNotTold: what is asked is whether a
// line is missing, so a page that hands its transport a registry by a way of
// its own -- built beside it, under names of its own, in a module of the page
// it imports -- is left alone. So is one that speaks binary, where an Any is
// bytes, a transport a comment or a string mentions, and a test.
func TestAPageThatHandsTheRegistryItsOwnWayIsNotTold(t *testing.T) {
	x := require.New(t)

	dir, l := keeping(t, x)
	for name, src := range map[string]string{
		"shorthand.ts": `import { createConnectTransport } from '@connectrpc/connect-web'
import { events } from '../gen/events.js'

const jsonOptions = { registry: events }
export const transport = createConnectTransport({ baseUrl: '/', jsonOptions })
`,
		"renamed.ts": `import { createConnectTransport as connect } from '@connectrpc/connect-web'
import { events as registry } from '../gen/events.js'

export const transport = connect({ baseUrl: '/', jsonOptions: { registry } })
`,
		// The options are another module's, found by a bundler's rules: a
		// directory's index, named without an extension.
		"wired.ts": `import { createConnectTransport } from '@connectrpc/connect-web'
import { options } from './wire'

export const transport = createConnectTransport({ ...options, baseUrl: '/' })
`,
		"wire/index.ts": `import { createRegistry } from '@bufbuild/protobuf'
import { events } from '../../gen/events.js'

export const options = { jsonOptions: { registry: createRegistry(events) } }
`,
		"binary.ts": `import { createConnectTransport } from '@connectrpc/connect-web'

export const transport = createConnectTransport({ baseUrl: '/', useBinaryFormat: true })
`,
		"grpcweb.ts": `import { createGrpcWebTransport } from '@connectrpc/connect-web'

export const transport = createGrpcWebTransport({ baseUrl: '/' })
`,
		"mentioned.ts": `// createConnectTransport({ baseUrl: '/' }) is what a page would write.
/* createConnectTransport({ baseUrl: '/' }) */
export const help = 'call createConnectTransport({ baseUrl }) to reach a server'
export const more = ` + "`" + `or createConnectTransport({ baseUrl: ${"'/'"} })` + "`" + `
`,
		"main.test.ts": `import { createConnectTransport } from '@connectrpc/connect-web'

export const transport = createConnectTransport({ baseUrl: '/' })
`,

		// And one that is told, so that the rest are not told for want of
		// anything being asked.
		"control.ts": `import { createConnectTransport } from '@connectrpc/connect-web'

export const transport = createConnectTransport({ baseUrl: '/' })
`,
	} {
		script(t, dir, name, src)
	}

	x.Equal([]string{"ts/src/control.ts"}, toldOf(t, l), "a page that hands the registry, or needs none, was told")
}

// toldOf is the files of the page the doctor says hand no registry, in order.
func toldOf(t *testing.T, l pdcli.Layout) []string {
	t.Helper()

	var vs []string
	for _, v := range pdcli.Doctor(t.Context(), l) {
		if name, _, ok := strings.Cut(v.What, " "+told); ok {
			vs = append(vs, name)
		}
	}

	return vs
}

// TestAPageThatHandsNoRegistryIsTold, in each of the ways a page does not:
// nothing said, said only in a comment, `jsonOptions` written out with no
// registry in it, a registry handed to the panel and not to the transport, a
// transport under a name of the file's own, and gRPC-web told to speak JSON.
// Each file is told on its own, the import written from where it is.
func TestAPageThatHandsNoRegistryIsTold(t *testing.T) {
	x := require.New(t)

	dir, l := keeping(t, x)
	for name, src := range map[string]string{
		"bare.ts": `import { createConnectTransport } from '@connectrpc/connect-web'

export const transport = createConnectTransport({ baseUrl: '/' })
`,
		"commented.ts": `import { createConnectTransport } from '@connectrpc/connect-web'
import { events } from '../gen/events.js'

export const transport = createConnectTransport({
	baseUrl: '/',
	// jsonOptions: { registry: events },
	/* jsonOptions: { registry: events }, */
})
`,
		"empty.ts": `import { createConnectTransport } from '@connectrpc/connect-web'

export const transport = createConnectTransport({ baseUrl: '/', jsonOptions: { ignoreUnknownFields: true } })
`,
		"panel.tsx": `import { createConnectTransport } from '@connectrpc/connect-web'
import { Devtools } from '@lesomnus/payday/react/devtools'
import { events } from '../gen/events.js'

export const transport = createConnectTransport({ baseUrl: '/' })
export const panel = <Devtools entities={[]} registry={events} />
`,
		"aliased.ts": `import { type Transport, createConnectTransport as connect } from '@connectrpc/connect-web'

export const transport: Transport = connect({ baseUrl: '/' })
`,
		"lib/json.ts": `import { createGrpcWebTransport } from '@connectrpc/connect-web'

export const transport = createGrpcWebTransport({ baseUrl: '/', useBinaryFormat: false })
`,
	} {
		script(t, dir, name, src)
	}

	vs := map[string]pdcli.Finding{}
	for _, v := range pdcli.Doctor(t.Context(), l) {
		if name, _, ok := strings.Cut(v.What, " "+told); ok {
			vs[name] = v
		}
	}

	for _, name := range []string{"bare.ts", "commented.ts", "empty.ts", "panel.tsx", "aliased.ts", "lib/json.ts"} {
		v, ok := vs["ts/src/"+name]
		x.True(ok, "%s was not told", name)
		x.Contains(v.Fix, "jsonOptions: { registry: events }", name)
	}
	x.Len(vs, 6, "told of what it was not given")

	x.Contains(vs["ts/src/aliased.ts"].Fix, "connect({\n", "the fix calls it what the file does")
	x.Contains(vs["ts/src/lib/json.ts"].Fix, "createGrpcWebTransport({\n")
	x.Contains(vs["ts/src/lib/json.ts"].Fix, "import { events } from '../../gen/events.js'", "the import is from where the file is")
	x.Contains(vs["ts/src/bare.ts"].Fix, "import { events } from '../gen/events.js'")
}
