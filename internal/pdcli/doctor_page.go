package pdcli

import (
	"bytes"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A page of an app that keeps a past, whose transport cannot read an event.
//
// An Event keeps its value as an `Any`, and Connect's JSON names the message an
// `Any` holds by its type, `{"@type": "type.googleapis.com/app.RobotMoved",
// …}`. protobuf-es reads one only with that type registered, so a transport
// that speaks JSON and is handed no registry fails a List of events -- the
// whole page of it, on the first row that holds a value -- and until the first
// such row is written, nothing says the line is missing (payday#151). `pd new`
// writes it into both transports it makes; a page made before that has neither.
//
// # What it reads
//
// The page's own scripts, `ts/src`, with their comments taken out, and of each
// the transports it makes: `createConnectTransport`, which speaks JSON unless it
// is told `useBinaryFormat: true`, and `createGrpcWebTransport` and
// `createGrpcTransport`, which speak binary -- where an `Any` is bytes and
// needs no registry -- unless told `useBinaryFormat: false`. A test is not the
// page, and is not asked about.
//
// # What counts as handing it one
//
// `jsonOptions`, said anywhere in the file or in a module of the page it
// imports, however it is written: `jsonOptions: { registry: events }`, a
// `jsonOptions` built beside it and passed by name, the options built in
// another file and spread in. Only the one form that can be read whole and
// holds none is not: `jsonOptions: { ... }` written out, with no `registry` and
// nothing spread into it.
//
// That is deliberately generous. What is being asked is whether a line is
// missing, and a page that hands its transport a registry by a way of its own
// -- one it built, under a name of its own -- is a page this has to leave
// alone: told to add what it has, its author learns to read past the doctor.
// One it wrongly leaves alone is no worse off than before anything asked.
// What it cannot see is a module outside the page's own files -- options from
// a package, or from a path a bundler's alias names -- and a file whose
// transports are two, one handed it and one not.
func doctorPage(l Layout) []Finding {
	if l.Past == nil {
		return nil
	}

	src := l.Path(DirTs, "src")
	if _, err := os.Stat(src); err != nil {
		return nil
	}

	pg := &page{l: l, files: map[string]*script{}}

	var vs []Finding
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "__tests__" {
				return fs.SkipDir
			}

			return nil
		}
		if !isScript(p) || isTest(p) {
			return nil
		}

		f := pg.read(p)
		if f == nil || f.speaks == "" || pg.reaches(p, map[string]bool{}) {
			return nil
		}

		vs = append(vs, pg.finding(p, f))

		return nil
	})

	return vs
}

// page is the scripts of one page, read as they are asked about.
type page struct {
	l     Layout
	files map[string]*script
}

// script is what one file of the page says, with its comments taken out.
type script struct {
	// speaks is the transport it makes that speaks JSON, as it calls it, and
	// empty for a file that makes none.
	speaks string

	// carries says it says `jsonOptions` in a way that may hand a registry.
	carries bool

	// imports are the modules it imports by a relative path, found on disk.
	imports []string
}

// The transports, and whether each speaks JSON unless told otherwise: Connect's
// does, and gRPC's and gRPC-web's speak binary, since not every server of
// theirs reads JSON.
var transports = map[string]bool{
	"createConnectTransport": true,
	"createGrpcWebTransport": false,
	"createGrpcTransport":    false,
}

var (
	// A module named in an import or an export of one -- `from '…'` is only
	// ever that -- one imported for what it does, and one imported when asked.
	importFrom    = regexp.MustCompile(`\bfrom\s*['"]([^'"\n]+)['"]`)
	importBare    = regexp.MustCompile(`\bimport\s*['"]([^'"\n]+)['"]`)
	importDynamic = regexp.MustCompile(`\bimport\s*\(\s*['"]([^'"\n]+)['"]\s*\)`)

	// The names a file imports from Connect's transports, to find one under a
	// name of its own.
	importConnect = regexp.MustCompile(`\bimport\s+(?:type\s+)?\{([^}]*)\}\s*from\s*['"]@connectrpc/connect-(?:web|node)['"]`)
	importAs      = regexp.MustCompile(`^(?:type\s+)?([\w$]+)(?:\s+as\s+([\w$]+))?$`)

	binaryFormat = regexp.MustCompile(`\buseBinaryFormat\s*:\s*(true|false)\b`)
	jsonOptions  = regexp.MustCompile(`\bjsonOptions\b`)
	registry     = regexp.MustCompile(`\bregistry\b`)
)

func (pg *page) read(p string) *script {
	if f, ok := pg.files[p]; ok {
		return f
	}

	b, err := os.ReadFile(p)
	if err != nil {
		pg.files[p] = nil
		return nil
	}

	// Comments out, for what it imports; and its strings emptied as well,
	// for what it calls and says -- a transport a string names is not one.
	code := scrub(b, false)
	bare := scrub(b, true)

	f := &script{}
	pg.files[p] = f

	// What the file calls each transport: its own name, and whatever it
	// imported one as.
	calls := map[string]string{}
	for name := range transports {
		calls[name] = name
	}
	for _, m := range importConnect.FindAllSubmatch(code, -1) {
		for _, v := range strings.Split(string(m[1]), ",") {
			n := importAs.FindStringSubmatch(strings.TrimSpace(v))
			if n == nil || n[2] == "" {
				continue
			}
			if _, ok := transports[n[1]]; ok {
				calls[n[2]] = n[1]
			}
		}
	}

	told := map[string]bool{}
	for _, m := range binaryFormat.FindAllSubmatch(bare, -1) {
		told[string(m[1])] = true
	}

	for _, local := range slices.Sorted(maps.Keys(calls)) {
		name := calls[local]

		// A transport's own name may be reached through a namespace --
		// `web.createConnectTransport(` -- and a name of the file's own may
		// not: `socket.connect(` is not the `connect` it imported.
		before := `[^\w$.]`
		if local == name {
			before = `[^\w$]`
		}
		if !regexp.MustCompile(`(?:^|` + before + `)` + regexp.QuoteMeta(local) + `\s*\(`).Match(bare) {
			continue
		}

		json := transports[name]
		if json && told["true"] && !told["false"] {
			json = false
		}
		if !json && told["false"] {
			json = true
		}
		if json {
			f.speaks = local
			break
		}
	}

	f.carries = carries(bare)

	for _, re := range []*regexp.Regexp{importFrom, importBare, importDynamic} {
		for _, m := range re.FindAllSubmatch(code, -1) {
			if q := pg.resolve(p, string(m[1])); q != "" {
				f.imports = append(f.imports, q)
			}
		}
	}

	return f
}

// carries is whether `jsonOptions` is said in a way that may hand a
// registry: anything but a literal that holds no `registry` and has nothing
// spread into it.
func carries(code []byte) bool {
	for _, at := range jsonOptions.FindAllIndex(code, -1) {
		rest := bytes.TrimLeft(code[at[1]:], " \t\r\n")
		if !bytes.HasPrefix(rest, []byte(":")) {
			return true
		}

		rest = bytes.TrimLeft(rest[1:], " \t\r\n")
		if !bytes.HasPrefix(rest, []byte("{")) {
			return true
		}

		v := braced(rest)
		if registry.Match(v) || bytes.Contains(v, []byte("...")) {
			return true
		}
	}

	return false
}

// braced is the object literal `v` begins with, up to its closing brace, or
// all of `v` when it does not close.
func braced(v []byte) []byte {
	depth := 0
	for i := 0; i < len(v); i++ {
		switch c := v[i]; c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return v[:i+1]
			}
		case '\'', '"', '`':
			// Past a string, so that a brace in one is not one of these.
			for i++; i < len(v) && v[i] != c; i++ {
				if v[i] == '\\' {
					i++
				}
			}
		}
	}

	return v
}

// reaches is whether a file of the page, or a module it imports, carries
// `jsonOptions`.
func (pg *page) reaches(p string, seen map[string]bool) bool {
	if seen[p] {
		return false
	}
	seen[p] = true

	f := pg.read(p)
	if f == nil {
		return false
	}
	if f.carries {
		return true
	}
	for _, q := range f.imports {
		if pg.reaches(q, seen) {
			return true
		}
	}

	return false
}

// resolve is the file a relative import names, as a bundler finds it: the path
// itself, the TypeScript a `.js` in it stands for, the path with an extension,
// or its `index`. Empty for a package, for a file not there, and for what `pd
// gen --ts` wrote, which never says what is asked here.
func (pg *page) resolve(from string, spec string) string {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return ""
	}

	base := filepath.Join(filepath.Dir(from), filepath.FromSlash(spec))
	if gen := pg.l.Path(DirTsGen); base == gen || strings.HasPrefix(base, gen+string(filepath.Separator)) {
		return ""
	}

	stem := base
	if ext := filepath.Ext(base); slices.Contains([]string{".js", ".jsx", ".mjs", ".cjs"}, ext) {
		stem = strings.TrimSuffix(base, ext)
	}

	var cands []string
	if isScript(base) {
		cands = append(cands, base)
	}
	for _, ext := range scriptExts {
		cands = append(cands, stem+ext)
	}
	for _, ext := range scriptExts {
		cands = append(cands, filepath.Join(base, "index"+ext))
	}

	for _, v := range cands {
		if st, err := os.Stat(v); err == nil && !st.IsDir() {
			return v
		}
	}

	return ""
}

func (pg *page) finding(p string, f *script) Finding {
	at, err := filepath.Rel(filepath.Dir(p), pg.l.Path(DirTsGen, "events.js"))
	if err != nil {
		at = filepath.Join("..", "gen", "events.js")
	}
	at = filepath.ToSlash(at)
	if !strings.HasPrefix(at, ".") {
		at = "./" + at
	}

	fix := fmt.Sprintf("import { events } from '%s'\n\n%s({\n\t...,\n\tjsonOptions: { registry: events },\n})", at, f.speaks)
	if _, err := os.Stat(pg.l.Path(DirTsGen, "events.ts")); err != nil {
		fix += "\n\n" + pg.l.rel(pg.l.Path(DirTsGen, "events.ts")) + " is what `pd gen --ts` writes, for every app."
	}

	return Finding{
		What: fmt.Sprintf("%s makes a transport that speaks JSON and hands it no registry of this app's events: JSON "+
			"names the message an Event's value holds by its type, and protobuf-es reads one only with the type "+
			"registered, so a List of events fails whole on the first that holds a value", pg.l.rel(p)),
		Fix: fix,
	}
}

var scriptExts = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

func isScript(p string) bool {
	return slices.Contains(scriptExts, filepath.Ext(p)) && !strings.HasSuffix(p, ".d.ts")
}

// isTest is a file a test runner finds: what a test makes is not what the page
// serves.
func isTest(p string) bool {
	name := filepath.Base(p)
	return strings.Contains(name, ".test.") || strings.Contains(name, ".spec.")
}

// scrub is a script with its comments taken out -- each blanked to spaces, its
// line breaks kept -- and nothing else changed: strings, template literals and
// regular expressions are kept as written, and a `//` in one is no comment.
// With `blank`, what a string or a template literal says is blanked too, and
// its quotes and the code in a `${}` kept.
//
// It is a scanner and not a parser, which is all that is asked of it: that a
// transport a comment or a string mentions is not taken for one the page
// makes, and that a line a comment took out is not taken for one that is
// there. So it is wrong in the ways that cost nothing here. A string in JSX
// text -- `Don't` -- ends at the end of its line, and a `//` there ends the
// line.
func scrub(src []byte, blank bool) []byte {
	// What a string holds, as it is kept.
	held := func(out []byte, v ...byte) []byte {
		if !blank {
			return append(out, v...)
		}
		for _, c := range v {
			if c != '\n' {
				c = ' '
			}
			out = append(out, c)
		}

		return out
	}

	out := make([]byte, 0, len(src))

	// Code, or a template literal, and inside each `${` the code it holds: the
	// braces it has opened, so that the `}` that closes it is told from one
	// that closes a block inside it.
	type frame struct {
		template bool
		depth    int
	}
	stack := []frame{{}}

	// The last of the code that is not a space, and the word it ends, if it
	// ends one: what says whether a `/` begins a regular expression. `adj`
	// says the character just before was of a word, with nothing between.
	var prev byte
	var word []byte
	var adj bool

	n := len(src)
	for i := 0; i < n; {
		top := &stack[len(stack)-1]
		c := src[i]
		if !isWord(c) {
			adj = false
		}

		if top.template {
			switch {
			case c == '\\' && i+1 < n:
				out = held(out, c, src[i+1])
				i += 2
			case c == '`':
				stack = stack[:len(stack)-1]
				out = append(out, c)
				i++
				prev, word = c, nil
			case c == '$' && i+1 < n && src[i+1] == '{':
				stack = append(stack, frame{})
				out = append(out, '$', '{')
				i += 2
				prev, word = '{', nil
			default:
				out = held(out, c)
				i++
			}

			continue
		}

		switch {
		case c == '/' && i+1 < n && src[i+1] == '/':
			for ; i < n && src[i] != '\n'; i++ {
				out = append(out, ' ')
			}

		case c == '/' && i+1 < n && src[i+1] == '*':
			out = append(out, ' ', ' ')
			for i += 2; i < n && !(src[i] == '*' && i+1 < n && src[i+1] == '/'); i++ {
				if src[i] == '\n' {
					out = append(out, '\n')
				} else {
					out = append(out, ' ')
				}
			}
			if i < n {
				out = append(out, ' ', ' ')
				i += 2
			}

		case c == '\'' || c == '"':
			j := i + 1
			for j < n && src[j] != c && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j < n && src[j] == c {
				j++
			}
			j = min(j, n)
			out = append(out, c)
			if j-i > 1 && src[j-1] == c {
				out = held(out, src[i+1:j-1]...)
				out = append(out, c)
			} else {
				out = held(out, src[i+1:j]...)
			}
			i = j
			prev, word = c, nil

		case c == '`':
			stack = append(stack, frame{template: true})
			out = append(out, c)
			i++

		case c == '/' && regexMayStart(prev, word):
			j, class := i+1, false
		re:
			for j < n && src[j] != '\n' {
				switch src[j] {
				case '\\':
					j++
				case '[':
					class = true
				case ']':
					class = false
				case '/':
					if !class {
						j++
						break re
					}
				}
				j++
			}
			j = min(j, n)
			out = append(out, src[i:j]...)
			i = j
			prev, word = '/', nil

		default:
			switch {
			case c == '{':
				top.depth++
			case c == '}' && top.depth == 0 && len(stack) > 1:
				stack = stack[:len(stack)-1]
			case c == '}':
				top.depth--
			}

			out = append(out, c)
			i++

			switch {
			case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			case isWord(c):
				if !adj {
					word = word[:0]
				}
				word = append(word, c)
				prev, adj = c, true
			default:
				prev, word = c, nil
			}
		}
	}

	return out
}

func isWord(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// regexMayStart is whether a `/` after `prev` -- and `word`, when `prev` ends
// one -- begins a regular expression rather than divides: at the start, after
// an operator or an opening bracket, and after a keyword that takes an
// expression. After a name, a number, or a closing bracket, it divides.
func regexMayStart(prev byte, word []byte) bool {
	switch {
	case prev == 0:
		return true
	case isWord(prev):
		switch string(word) {
		case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw", "case", "do", "else", "yield", "await":
			return true
		}
		return false
	}

	return !strings.ContainsRune(")]'\"`", rune(prev))
}
