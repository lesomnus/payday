package pdcli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAScriptIsReadWithoutItsComments: what the page check reads of a script
// is its code, so a comment is blanked -- to the character, its line breaks
// kept -- and what only looks like one is not: a `//` in a string, in a
// regular expression, or in the text of a template literal. A `/` that
// divides is no regular expression, and one after `return` is. With `blank`,
// what a string says goes too, and the code in a `${}` stays.
func TestAScriptIsReadWithoutItsComments(t *testing.T) {
	for _, tc := range []struct {
		name  string
		src   string
		blank bool
		want  string
	}{
		{"a line", "a // b\nc", false, "a     \nc"},
		{"a block, over lines", "a /* b\nc */ d", false, "a     \n     d"},
		{"a string", "f('http://x') // c", false, "f('http://x')     "},
		{"a regular expression", `r = /\/\// // c`, false, `r = /\/\//     `},
		{"a division", "a / b // c", false, "a / b     "},
		{"after return", "return /'/.test(x) // c", false, "return /'/.test(x)     "},
		{"after return, after a word", "x\nreturn /'/.test(x) // c", false, "x\nreturn /'/.test(x)     "},
		{"a template", "`x ${ y /* c */ } z // t`", false, "`x ${ y         } z // t`"},
		{"a template in a template", "`a ${ `b ${ c /* d */ }` } e`", false, "`a ${ `b ${ c         }` } e`"},
		{"a string, blank", "f('createConnectTransport(') // c", true, "f('                       ')     "},
		{"a template, blank", "`a${ b('c') }d`", true, "` ${ b(' ') } `"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := string(scrub([]byte(tc.src), tc.blank))
			require.Equal(t, tc.want, got)
			require.Len(t, got, len(tc.src), "a position moved")
		})
	}
}
