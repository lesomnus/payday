package pdid

import (
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Mint answers with the key a row is to be stored under.
//
// It is the whole of what payday puts into the write path of the generated
// servers, and it has two jobs. A request that named no identifier gets a fresh
// one of the right domain. A request that named one is **checked**: an
// identifier of another kind, or one this app did not make, is refused here --
// before a statement is built, with a message that says what it actually was.
//
// The checking is not optional politeness. A request may say what identifier it
// wants, so without this a caller could store a Robot under an identifier whose
// domain says Holder, and every later reading of that domain -- the audit
// trail's, a reference check's -- would be reading the caller's word rather
// than the schema's.
//
// The zero identifier is refused for the same reason it always was: the trail
// writes it for a write nobody asked for, so a row that held it could act and
// be recorded as the deployment writing to itself.
func Mint(d Domain, given uuid.UUID, ok bool) (uuid.UUID, error) {
	if !ok {
		return New(d).Uuid(), nil
	}
	if given == uuid.Nil {
		return uuid.Nil, &NobodyError{}
	}

	got, is := Of(given)
	if !is {
		return uuid.Nil, &ShapeError{Id: given}
	}
	if got != d {
		return uuid.Nil, &DomainError{Want: d, Got: got}
	}

	return given, nil
}

// DomainError is an identifier of the wrong kind. It carries a code, so a
// handler that answers with it says InvalidArgument rather than Unknown, and it
// matches [ErrDomain], so a test can say what it is without matching on text.
type DomainError struct {
	Want, Got Domain
}

func (e *DomainError) Error() string {
	return fmt.Sprintf("id: names a %s, and this is a %s", e.Got, e.Want)
}

func (e *DomainError) Is(target error) bool { return target == ErrDomain }

func (e *DomainError) GRPCStatus() *status.Status {
	return status.New(codes.InvalidArgument, e.Error())
}

// ShapeError is sixteen bytes that are a UUID but not one of ours, so nothing
// can be said about what they name.
type ShapeError struct {
	Id uuid.UUID
}

func (e *ShapeError) Error() string {
	return fmt.Sprintf("id: %s is not an identifier this app makes", e.Id)
}

func (e *ShapeError) Is(target error) bool { return target == ErrNotAnId }

func (e *ShapeError) GRPCStatus() *status.Status {
	return status.New(codes.InvalidArgument, e.Error())
}

// NobodyError is the zero identifier, which names nobody and so may name
// nothing.
type NobodyError struct{}

func (e *NobodyError) Error() string {
	return "id: that one names nobody, so nothing may hold it"
}

func (e *NobodyError) GRPCStatus() *status.Status {
	return status.New(codes.InvalidArgument, e.Error())
}

// NoDomain is an entity nothing declared a domain for.
//
// It is Internal and not InvalidArgument: the caller asked for something
// perfectly ordinary and the app cannot say what kind of thing it is, which is
// the app disagreeing with its own schema.
func NoDomain(entity string) error { return &NoDomainError{Entity: entity} }

type NoDomainError struct {
	Entity string
}

func (e *NoDomainError) Error() string {
	return fmt.Sprintf("%s: no domain was declared, so nothing can be said about what its identifiers name", e.Entity)
}

func (e *NoDomainError) GRPCStatus() *status.Status {
	return status.New(codes.Internal, e.Error())
}
