package trailpkcs11

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Scheme is what `audit.checkpoints.key` begins with to name a key here.
const Scheme = "pkcs11"

// URI is a key as RFC 7512 names one: which library, which token, which
// object, and where the PIN is.
//
//	pkcs11:token=payday;object=checkpoints?module-path=/usr/lib/softhsm/libsofthsm2.so&pin-source=/run/secrets/hsm-pin
//
// Every attribute the RFC has is read, and one it does not have is refused
// rather than ignored: a misspelt `objet=` would otherwise match every key on
// the token.
type URI struct {
	// Module is the library, `module-path`. Required: `module-name`, which
	// asks the system where its modules are, is refused with a hint.
	Module string

	// Library is what the library says of itself (`library-manufacturer`,
	// `library-description`, `library-version`), for a module path that
	// could be more than one.
	Library map[string]string

	// Slot is `slot-id`, and whether one was given.
	Slot    uint
	HasSlot bool

	// Match is what the slot and the token in it must say, by the
	// attribute's name: `token`, `manufacturer`, `serial`, `model`,
	// `slot-description`, `slot-manufacturer`.
	Match map[string]string

	// Object is the key's `CKA_LABEL`, and ID its `CKA_ID`; one or the other
	// unless the token holds a single private key.
	Object    string
	HasObject bool
	ID        []byte

	// Pin is what logs in, from `pin-value` or the file `pin-source` names.
	// Empty is a token that wants no login.
	Pin string
}

// path and query attributes the RFC has, and where each goes.
var (
	tokenAttrs   = []string{"token", "manufacturer", "serial", "model", "slot-description", "slot-manufacturer"}
	libraryAttrs = []string{"library-manufacturer", "library-description", "library-version"}
)

// ParseURI reads a key's URI. An error never carries the PIN.
func ParseURI(v string) (*URI, error) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok || !strings.EqualFold(scheme, Scheme) {
		return nil, fmt.Errorf("not a %s: URI", Scheme)
	}

	path, query, _ := strings.Cut(rest, "?")
	u := &URI{Library: map[string]string{}, Match: map[string]string{}}
	seen := map[string]bool{}
	attrs := func(v, sep string, set func(k, v string) error) error {
		if v == "" {
			return nil
		}
		for _, a := range strings.Split(v, sep) {
			k, val, ok := strings.Cut(a, "=")
			if !ok {
				return fmt.Errorf("%q is not an attribute and a value", k)
			}
			if seen[k] {
				return fmt.Errorf("%s is said twice", k)
			}
			seen[k] = true

			d, err := url.PathUnescape(val)
			if err != nil {
				return fmt.Errorf("%s: not percent-encoded", k)
			}
			if err := set(k, d); err != nil {
				return err
			}
		}

		return nil
	}

	err := attrs(path, ";", func(k, v string) error {
		switch {
		case k == "object":
			u.Object, u.HasObject = v, true
		case k == "id":
			u.ID = []byte(v)
		case k == "type":
			if v != "private" {
				return fmt.Errorf("type=%s, and a key that signs is a private one", v)
			}
		case k == "slot-id":
			n, err := strconv.ParseUint(v, 10, 0)
			if err != nil {
				return fmt.Errorf("slot-id=%s is not a number", v)
			}
			u.Slot, u.HasSlot = uint(n), true
		case slices.Contains(tokenAttrs, k):
			u.Match[k] = v
		case slices.Contains(libraryAttrs, k):
			u.Library[k] = v
		default:
			return fmt.Errorf("%s is not an attribute RFC 7512 has", k)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	pinSource := ""
	err = attrs(query, "&", func(k, v string) error {
		switch k {
		case "module-path":
			u.Module = v
		case "module-name":
			return errors.New("module-name asks the system where its modules are, which this does not; name the library by its path: module-path=…")
		case "pin-value":
			u.Pin = v
		case "pin-source":
			pinSource = v
		default:
			return fmt.Errorf("%s is not a query attribute RFC 7512 has", k)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	if u.Module == "" {
		return nil, errors.New("no module-path: which library speaks to the token")
	}
	if pinSource != "" {
		if seen["pin-value"] {
			return nil, errors.New("pin-value and pin-source both, which is two PINs")
		}
		if u.Pin, err = readPin(pinSource); err != nil {
			return nil, err
		}
	}

	return u, nil
}

// readPin reads the PIN from the file `pin-source` names, as a path or a
// `file:` URI, as a secret is mounted. A line's end is not the PIN's.
func readPin(v string) (string, error) {
	if p, ok := strings.CutPrefix(v, "file:"); ok {
		v = p
		if strings.HasPrefix(v, "//") {
			u, err := url.Parse("file:" + v)
			if err != nil || (u.Host != "" && u.Host != "localhost") {
				return "", errors.New("pin-source: a file: URI names a file on this host")
			}
			v = u.Path
		}
	}
	if strings.HasPrefix(v, "|") {
		return "", errors.New("pin-source: a command is not run for a PIN; name the file it is in")
	}

	b, err := os.ReadFile(v)
	if err != nil {
		return "", fmt.Errorf("pin-source: %w", err)
	}

	return strings.TrimRight(string(b), "\r\n"), nil
}
