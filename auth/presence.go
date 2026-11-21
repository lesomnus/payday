package auth

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// These metadata fields carry references and claims, never proof or secrets.
const (
	PresenceHeader = "payday-presence"
	SubjectHeader  = "payday-subject"
	RecordedHeader = "payday-recorded"
)

// PresenceCall is a claim to represent a person under an app-owned presence or
// delegation. Only Caller has been verified. Reference, Subject and Recorded
// come from the request and must not be trusted as proof by themselves.
type PresenceCall struct {
	Caller     frame.Principal
	Credential Identity
	Reference  string
	Subject    pdid.Id
	Recorded   time.Time
	Method     string
	// Request is available for unary calls; streams carry only their metadata
	// at authentication time. Treat it as read-only.
	Request proto.Message
}

// Presence resolves an app-verified presence to the person represented. The
// app verifies the reference's binding to Caller, proof, validity and revocation,
// and explicitly decides whether a late write under an ended presence is valid.
// Payday imposes no default grace period and never trusts a device's assertion
// that a person was present. Errors fail the call; use ErrUnavailable for an
// unavailable verifier, so a temporary outage is not mistaken for sign-out.
type Presence func(context.Context, PresenceCall) (*frame.Frame, error)

type authenticationCall struct {
	method  string
	request proto.Message
}
type authenticationCallKey struct{}

// Represent composes ordinary credential resolution with app-owned presence
// verification. With no presence metadata it is exactly next. With metadata,
// the effective Actor is the verified person and Caller keeps the credential
// holder. The credential's grant still comes from authentication, not Presence.
func Represent(next Resolver, verify Presence) Resolver {
	return ResolverFunc(func(ctx context.Context, identity Identity) (*frame.Frame, error) {
		caller, err := next.Resolve(ctx, identity)
		if err != nil || caller == nil {
			return caller, err
		}
		md, _ := metadata.FromIncomingContext(ctx)
		reference, err := single(md, PresenceHeader)
		if err != nil {
			return nil, err
		}
		if reference == "" {
			return caller, nil
		}
		if verify == nil || caller.Actor.IsZero() || caller.AsAccount() || caller.Caller != nil {
			return nil, status.Error(codes.PermissionDenied, "auth: this credential cannot represent a person")
		}
		if identity.TenantId != "" && identity.TenantId != caller.Tenant.String() {
			return nil, status.Error(codes.PermissionDenied, "auth: credential names the wrong caller tenant")
		}
		subject, err := single(md, SubjectHeader)
		if err != nil {
			return nil, err
		}
		id, err := pdid.Parse(subject)
		if err != nil || id.IsZero() {
			return nil, status.Error(codes.PermissionDenied, "auth: presence requires a valid subject")
		}
		recorded, err := single(md, RecordedHeader)
		if err != nil {
			return nil, err
		}
		var at time.Time
		if recorded != "" {
			at, err = time.Parse(time.RFC3339Nano, recorded)
			if err != nil {
				return nil, status.Error(codes.PermissionDenied, "auth: invalid presence recording time")
			}
		}
		call, _ := ctx.Value(authenticationCallKey{}).(authenticationCall)
		actor, err := verify(ctx, PresenceCall{Caller: caller.Principal(), Credential: identity, Reference: reference, Subject: id, Recorded: at, Method: call.method, Request: call.request})
		if err != nil {
			if errors.Is(err, ErrNoCredential) {
				return nil, status.Error(codes.PermissionDenied, "auth: the presence was not verified")
			}
			return nil, err
		}
		if actor == nil || actor.Actor != id || actor.AsAccount() || actor.Caller != nil {
			return nil, status.Error(codes.PermissionDenied, "auth: presence did not verify the requested person")
		}
		if !actor.PresenceExpires.IsZero() && !time.Now().Before(actor.PresenceExpires) {
			return nil, status.Error(codes.PermissionDenied, "auth: this presence has expired")
		}
		select {
		case <-actor.PresenceChanged:
			return nil, status.Error(codes.PermissionDenied, "auth: this presence was revoked")
		default:
		}
		f := *actor
		principal := caller.Principal()
		f.Caller = &principal
		f.Presence = reference
		f.Scope = frame.Nothing
		return &f, nil
	})
}

func single(md metadata.MD, name string) (string, error) {
	vs := md.Get(name)
	if len(vs) == 0 {
		return "", nil
	}
	if len(vs) != 1 || vs[0] == "" || len(vs[0]) > 512 || !utf8.ValidString(vs[0]) || strings.ContainsAny(vs[0], "\x00\r\n") {
		return "", status.Error(codes.PermissionDenied, "auth: invalid presence metadata")
	}
	return vs[0], nil
}
