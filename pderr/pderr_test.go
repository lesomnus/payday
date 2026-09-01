package pderr_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/slug"
)

// TestAViolationSaysWhereAndWhy is the whole of what this is for.
func TestAViolationSaysWhereAndWhy(t *testing.T) {
	x := require.New(t)

	err := pderr.Invalid(
		pderr.Violation{Field: "alias", Why: "must not be empty"},
		pderr.Violation{Field: "tenant.alias", Why: "must begin with a lowercase letter"},
	)
	x.Equal(codes.InvalidArgument, status.Code(err))

	vs := pderr.Violations(err)
	x.Len(vs, 2)
	x.Equal("alias", vs[0].Field)
	x.Equal("must not be empty", vs[0].Why)
	x.Equal("tenant.alias", vs[1].Field)

	// And a caller that never looks at the details is no worse off than it was
	// before there were any, which is what makes this worth using at a site
	// that has no page in front of it.
	x.Contains(err.Error(), "alias: must not be empty")
	x.Contains(err.Error(), "tenant.alias: must begin with a lowercase letter")
}

// TestTheDetailSurvivesTheWire is the one that could not be replaced by reading
// the code.
//
// A status detail is an `Any` that has to be marshalled, sent as trailing
// metadata and unmarshalled on the other side. Everything above works in a
// process whether or not that is true; a page is never in the process.
func TestTheDetailSurvivesTheWire(t *testing.T) {
	x := require.New(t)

	s := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(s, refusing{})
	c := grpc_health_v1.NewHealthClient(pdtest.Serve(t, s))

	_, err := c.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	x.Error(err)
	x.Equal(codes.InvalidArgument, status.Code(err))

	vs := pderr.Violations(err)
	x.Len(vs, 1)
	x.Equal("service", vs[0].Field)
	x.Equal("must not be empty", vs[0].Why)
}

type refusing struct {
	grpc_health_v1.UnimplementedHealthServer
}

func (refusing) Check(context.Context, *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	return nil, pderr.Invalidf("service", "must not be empty")
}

// TestAPathIsBuiltFromWhereItWasFound is why nothing writes a whole path by
// hand.
//
// The validator that finds it knows the name of the field it was handed and not
// where that field was; the one that handed it over knows the other half. Each
// says its own half and neither has to know the shape of the whole request.
func TestAPathIsBuiltFromWhereItWasFound(t *testing.T) {
	x := require.New(t)

	// What a validator of a `TenantRef` says.
	inner := pderr.Invalidf("alias", "must not be empty")

	// What the one holding a `HolderRefBySlug` says about where that was.
	outer := pderr.At("slug.tenant", inner)

	vs := pderr.Violations(outer)
	x.Len(vs, 1)
	x.Equal("slug.tenant.alias", vs[0].Field)
	x.Contains(outer.Error(), "slug.tenant.alias: must not be empty")
}

// TestAnIndexIsNotAField is the one case where a "." is wrong.
func TestAnIndexIsNotAField(t *testing.T) {
	x := require.New(t)

	err := pderr.At("items", pderr.Invalidf("[2].alias", "must not be empty"))
	x.Equal("items[2].alias", pderr.Violations(err)[0].Field)
}

// TestAnErrorThatSaysNothingStillLandsOnABox is what makes this adoptable one
// site at a time.
//
// The bottom of a validation stack is usually an `errors.New` written before
// any of this existed. It becomes a violation at the field it was found in
// rather than being thrown away, so a page works before every layer has been
// converted.
func TestAnErrorThatSaysNothingStillLandsOnABox(t *testing.T) {
	x := require.New(t)

	err := pderr.At("alias", errors.New("must not be empty"))
	x.Equal(codes.InvalidArgument, status.Code(err))

	vs := pderr.Violations(err)
	x.Len(vs, 1)
	x.Equal("alias", vs[0].Field)
	x.Equal("must not be empty", vs[0].Why)
}

// TestSomebodyElsesRefusalIsNotAboutAField is the rule that keeps this from
// making things worse.
//
// A code is what a caller acts on. Wrapping everything that comes back in an
// InvalidArgument would tell a caller to go and fix a field over a row that is
// not there or an RPC that is not implemented -- and it would do it silently,
// because the message would still read correctly.
func TestSomebodyElsesRefusalIsNotAboutAField(t *testing.T) {
	x := require.New(t)

	for _, c := range []codes.Code{
		codes.NotFound,
		codes.Unimplemented,
		codes.PermissionDenied,
		codes.Unavailable,
	} {
		err := pderr.At("slug.tenant", status.Error(c, "no"))
		x.Equal(c, status.Code(err), "%s was rewritten", c)
		x.Empty(pderr.Violations(err))
	}
}

// TestAnErrorWithNothingToPlaceIsNotAFailure is the answer a Ui gets for every
// refusal that was never about a form.
func TestAnErrorWithNothingToPlaceIsNotAFailure(t *testing.T) {
	x := require.New(t)

	x.Nil(pderr.Violations(nil))
	x.Nil(pderr.Violations(errors.New("no")))
	x.Nil(pderr.Violations(status.Error(codes.NotFound, "no")))
}

// TestRefusingWithoutSayingWhyIsABug is the one shape that is not handed to a
// caller.
func TestRefusingWithoutSayingWhyIsABug(t *testing.T) {
	x := require.New(t)

	err := pderr.Invalid()
	x.Equal(codes.Internal, status.Code(err))
}

// TestANameThatIsNotOneSaysWhyAndNotWhere is `slug` carrying its own rule and
// declining to guess the rest.
//
// A package below gRpc knows the rule and cannot know the request. It says the
// first and leaves the second to whoever had the message, which is what makes
// the two compose instead of each being half right.
func TestANameThatIsNotOneSaysWhyAndNotWhere(t *testing.T) {
	x := require.New(t)

	_, err := slug.ParseAlias("Not An Alias")
	x.Error(err)
	x.ErrorIs(err, slug.ErrAlias)
	x.Equal(codes.InvalidArgument, status.Code(err))

	vs := pderr.Violations(err)
	x.Len(vs, 1)
	x.Empty(vs[0].Field, "a package that has not seen a request named a field of one")
	x.Contains(vs[0].Why, "lowercase")

	// And it lands wherever the caller says it was, whatever that box is
	// called -- which is the case a guess of "alias" would have got wrong.
	x.Equal("alias", pderr.Violations(pderr.At("alias", err))[0].Field)
	x.Equal("tenant.alias", pderr.Violations(pderr.At("tenant.alias", err))[0].Field)
	x.Equal("nickname", pderr.Violations(pderr.At("nickname", err))[0].Field)
}
