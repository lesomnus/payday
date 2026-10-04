package pdcmd

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

// Redacted is what [NewCmdConfig] prints in place of a secret.
const Redacted = "<redacted>"

// redacted is a loaded configuration as `config` prints it: every value a
// deployment keeps secret replaced, so that what it prints can be pasted into a
// ticket -- the use [NewCmdConfigEnv] already names for itself.
//
// # By the name of the field, and not by a tag
//
// What is secret is a fact the app's configuration knows and payday does not:
// an app's `roster.token`, a seeded password, a database's DSN. A tag would be
// a thing every app has to remember on every field, and the field it forgot is
// the one in the ticket. So the rule is the name, which every one of those
// already has: `token`, `password`, `secret`, `seal`, `key`, `keys` and the
// `_token`/`_password`/`_secret`/`_key` of anything longer -- and a `dsn`'s
// password, which is inside a value rather than beside it.
//
// A reference is printed as it is. `file:/run/key` and `env:KEY` say where a
// secret is, which is what somebody reading the output is trying to find out.
func redacted(v any) (any, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}

	var doc any
	if err := yaml.UnmarshalWithOptions(b, &doc, yaml.UseOrderedMap()); err != nil {
		return nil, err
	}

	return redact(doc, false), nil
}

func redact(v any, secret bool) any {
	switch x := v.(type) {
	case yaml.MapSlice:
		for i, item := range x {
			k := strings.ToLower(fmt.Sprint(item.Key))
			switch {
			case secretKey(k):
				x[i].Value = redact(item.Value, true)
			case k == "dsn":
				x[i].Value = redactDsn(item.Value)
			default:
				x[i].Value = redact(item.Value, secret)
			}
		}

		return x
	case []any:
		for i := range x {
			x[i] = redact(x[i], secret)
		}

		return x
	case string:
		if secret && x != "" && !reference(x) {
			return Redacted
		}

		return x
	default:
		if secret && x != nil {
			return Redacted
		}

		return x
	}
}

func secretKey(k string) bool {
	switch k {
	case "token", "password", "secret", "secrets", "seal", "key", "keys", "credential", "credentials":
		return true
	}
	for _, suffix := range []string{"_token", "_password", "_secret", "_key", "_keys"} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}

	return false
}

func reference(v string) bool {
	return strings.HasPrefix(v, "file:") || strings.HasPrefix(v, "env:")
}

var dsnPassword = regexp.MustCompile(`(?i)(password=)([^ &]*)`)

// redactDsn is a DSN with its password replaced, in either of the two shapes a
// driver takes one: a URL's user information, or a `password=` parameter.
func redactDsn(v any) any {
	s, ok := v.(string)
	if !ok || s == "" {
		return v
	}

	if u, err := url.Parse(s); err == nil && u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), Redacted)
			s = u.String()
		}
	}

	return dsnPassword.ReplaceAllString(s, "${1}"+Redacted)
}
