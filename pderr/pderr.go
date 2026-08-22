// Package pderr is the shape of a refusal: which field was wrong, and why.
//
// A server talking to a server needs one sentence. A form does not. It has to
// know which box to draw the red line around, and there is no way to get that
// out of
//
//	rpc error: code = InvalidArgument desc = slug.tenant: alias: must not be empty
//
// except by parsing it, which means the message becomes a wire format nobody
// declared and every rewording of it breaks a page. So the field path travels
// beside the message rather than inside it, in the `google.rpc.BadRequest` that
// is already in the dependency tree and already understood by every gRPC
// tool -- [Violation] here, `errdetails.BadRequest_FieldViolation` on the wire.
//
// # The message is for whoever is reading the code
//
// Not for whoever is filling in the form. payday does not carry a language, a
// tone or a set of translations, and a framework that starts writing sentences
// for end users has taken all three on. What the UI matches against is the code
// and the field path; the words it shows are the app's, from a table the app
// owns.
//
// That is why there is no `Message` for humans here and no locale anywhere. It
// also means a violation whose field path a UI does not recognise falls back to
// the app's general wording rather than leaking developer prose onto a page --
// which is a thing to get right in the app, and not something this package can
// prevent.
//
// # Paths compose, so the wrapping does too
//
// A field path is relative to the request, and a request is a tree. Whatever
// validates one part of it knows the rule and cannot know where that part sat:
// `slug` refuses "Arm 03" and names no field at all, because it was handed a
// string and never saw the request. [At] is the step that says where the part
// was -- it moves everything an error carries underneath a name -- and it is
// the only way violations are meant to be built up, because doing it by hand is
// how a path ends up naming a box that is not there.
//
// Each half saying only its own half is what lets the two compose, and it means
// the path a form gets is as long as the wrapping that was done and no longer.
// A refusal handed up untouched keeps its message and loses its box, which is
// the case to watch at the edge of code that does not use this package: a
// `status.Errorf` composing the path into the text leaves nothing beside the
// message to place, so whoever handed the part over is the one who can put it
// back.
//
// # What it will not do
//
// [At] does not turn somebody else's refusal into an InvalidArgument. An error
// that already answers Unimplemented or NotFound comes back untouched, because
// the code is what a caller acts on -- retry, go get a credential, give up --
// and a wrapper that overwrote it would be telling them to go and fix a field
// over something that was never about a field.
package pderr

import (
	"fmt"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Violation is one field of a request that was not acceptable.
type Violation struct {
	// Field is where it was, written as a path from the root of the request in
	// the proto field names: "alias", "slug.tenant.alias", "items[2].alias".
	//
	// Empty is allowed and means the request as a whole -- two fields that are
	// each fine and contradict each other has nowhere else to go.
	Field string

	// Why is the rule that was broken, for whoever is reading the code. See the
	// package comment for why it is not for whoever is reading the screen.
	Why string
}

func (v Violation) String() string {
	if v.Field == "" {
		return v.Why
	}

	return v.Field + ": " + v.Why
}

// Invalid answers with an InvalidArgument carrying `vs`.
//
// The message is the violations joined, so a caller that only ever prints the
// error sees what it would have seen from a `status.Errorf` written by hand.
// Nothing is lost by not looking, which is what keeps this worth using at
// sites that have no UI in mind.
func Invalid(vs ...Violation) error {
	if len(vs) == 0 {
		// A refusal that does not say what was wrong with anything. It is a bug
		// at the call site rather than something to hand a caller, and the
		// alternative -- an InvalidArgument with an empty message -- is the
		// silence this package exists to remove.
		return status.Error(codes.Internal, "a request was refused and nothing said why")
	}

	ws := make([]string, len(vs))
	fs := make([]*errdetails.BadRequest_FieldViolation, len(vs))
	for i, v := range vs {
		ws[i] = v.String()
		fs[i] = &errdetails.BadRequest_FieldViolation{Field: v.Field, Description: v.Why}
	}

	s, err := status.New(codes.InvalidArgument, strings.Join(ws, "; ")).
		WithDetails(&errdetails.BadRequest{FieldViolations: fs})
	if err != nil {
		// Attaching a detail can only fail if it cannot be marshalled, and this
		// one is two strings. Keeping the refusal is worth more than the path.
		return status.Error(codes.InvalidArgument, strings.Join(ws, "; "))
	}

	return s.Err()
}

// Invalidf is [Invalid] for the one-violation case, which is most of them.
func Invalidf(field string, format string, args ...any) error {
	return Invalid(Violation{Field: field, Why: fmt.Sprintf(format, args...)})
}

// At moves everything `err` says about a field to underneath `field`.
//
// It is what a validator calls on the way out of a nested one: whatever came
// back was about a path relative to the part that was handed over, and this is
// the step that says where that part was. An error carrying no violations
// becomes one, at `field`, saying what the error said -- so a hand-written
// `errors.New` at the bottom of a stack of these still lands on the right box.
//
// It answers nil for nil, and hands back anything that is already refusing with
// a code of its own; see the package comment.
func At(field string, err error) error {
	if err == nil {
		return nil
	}
	if c := status.Code(err); c != codes.InvalidArgument && c != codes.Unknown {
		return err
	}

	vs := Violations(err)
	if len(vs) == 0 {
		return Invalid(Violation{Field: field, Why: err.Error()})
	}

	for i, v := range vs {
		vs[i].Field = join(field, v.Field)
	}

	return Invalid(vs...)
}

// join puts a path underneath a name.
//
// The bracket is why this is not a `strings.Join`: "items" under "[2].alias" is
// "items[2].alias" and not "items.[2].alias", and an index is how a repeated
// field is named in the path `google.rpc.BadRequest` documents.
func join(field string, sub string) string {
	switch {
	case field == "":
		return sub
	case sub == "":
		return field
	case sub[0] == '[':
		return field + sub
	default:
		return field + "." + sub
	}
}

// Violations answers with what `err` says was wrong, and nil for an error that
// does not say.
//
// Nil for an error that carries nothing is the whole of the answer a caller
// needs: a UI with no violations to place has a message and a code, which is
// what it had before any of this, and it renders whatever it renders for a
// refusal it cannot place.
//
// It looks through wrapping, so an error that was passed up through `%w` still
// answers.
func Violations(err error) []Violation {
	s, ok := status.FromError(err)
	if !ok {
		return nil
	}

	var vs []Violation
	for _, d := range s.Details() {
		v, ok := d.(*errdetails.BadRequest)
		if !ok {
			continue
		}

		for _, f := range v.GetFieldViolations() {
			vs = append(vs, Violation{Field: f.GetField(), Why: f.GetDescription()})
		}
	}

	return vs
}
