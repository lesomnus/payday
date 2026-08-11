package frame

import "strings"

// Covers reports whether `held` -- a method name or a pattern -- allows `want`.
//
// A method is three parts and a pattern is the same three with any of them
// written as `*`:
//
//	/roster.HolderService/Get     one method
//	/roster.HolderService/*       one service
//	/roster.*/*                   one package
//	/roster.*/Get                 that method wherever it appears
//	/*.*/*                        everything
//
// **A whole part or nothing.** `*Get*` is not a pattern, it is a part whose
// name happens to contain asterisks, and it matches a method called exactly
// that. Partial matching is where this stops being three comparisons and starts
// being a regular expression, and a permission check nobody can evaluate by
// reading it is one nobody reviews.
//
// # It answers about one pattern and never about a set
//
// Whoever asks whether somebody may hand out `P` asks this once per pattern
// they hold, and one of them has to cover `P` on its own. That is not a
// simplification of "does the union cover it" -- it is a different rule, and
// the right one.
//
// Say a deployment has two services and somebody holds `/app.A/*` and
// `/app.B/*`. Between them they can reach every method there is, so the union
// covers `/app.*/*`. Grant it on that basis and the third service added next
// release is covered by a grant made before it existed -- which is precisely
// the widening this exists to refuse. Asked one pattern at a time, neither
// covers `/app.*/*`, and the answer is no.
//
// So the union rule is coverage-as-of-today. This is coverage.
//
// # Anything it cannot read, it refuses
//
// A string that is not shaped like a method covers nothing and is covered by
// nothing, except by being identical -- which keeps an unpackaged service or
// anything else unusual working as the literal it is, without teaching this
// function a second shape to be wrong about.
func Covers(held string, want string) bool {
	// The literal case first, so that anything this cannot parse still behaves
	// exactly as it did when these were compared with ==.
	if held == want {
		return true
	}

	h, ok := parts(held)
	if !ok {
		return false
	}
	w, ok := parts(want)
	if !ok {
		return false
	}

	for i := range h {
		if h[i] == "*" {
			continue
		}
		if h[i] != w[i] {
			return false
		}
	}

	return true
}

// parts splits "/pkg.Service/Method" into its three, and reports whether it was
// one at all.
//
// The package is split off at the **last** dot, because a package has dots in
// it -- `/google.protobuf.Any/Pack` is package `google.protobuf`, service
// `Any`. A first-dot split would call the package `google` and quietly make
// `/google.*/*` mean something nobody asked for.
func parts(v string) ([3]string, bool) {
	var z [3]string

	rest, ok := strings.CutPrefix(v, "/")
	if !ok {
		return z, false
	}

	full, method, ok := strings.Cut(rest, "/")
	if !ok || full == "" || method == "" {
		return z, false
	}
	if strings.Contains(method, "/") {
		return z, false
	}

	i := strings.LastIndex(full, ".")
	if i < 0 {
		// A service with no package. It is a real thing to be and it is not
		// something a pattern can name, so it stays a literal: the `==` above
		// is the only way it matches.
		return z, false
	}

	pkg, service := full[:i], full[i+1:]
	if pkg == "" || service == "" {
		return z, false
	}

	return [3]string{pkg, service, method}, true
}
