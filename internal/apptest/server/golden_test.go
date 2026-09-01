package server_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"uuid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
)

// told is the app with both of the things that differ per run handed in: what
// identifier a row gets, and what a stamp holds.
//
// The minter answers off the entity's name rather than a counter, so a test
// that adds rows in another order still gets the same file -- a golden test
// that depends on the order of its own setup is one that fails for the wrong
// reason later.
func told(t *testing.T, a *App) app.Server {
	t.Helper()

	ks := map[string]pdid.Id{
		"app.Tenant": pdid.MustParse("0199c3f4-2a10-8001-8a03-9f2e1c4d5b01"),
		"app.Robot":  pdid.MustParse("0199c3f4-2a10-8007-8a03-9f2e1c4d5b07"),
		"app.Cell":   pdid.MustParse("0199c3f4-2a10-800a-8a03-9f2e1c4d5b0a"),
	}

	s, err := bare.NewServer(a.Db,
		bare.WithClock(pdtest.Clock()),
		bare.WithMinter(bare.MinterFunc(func(_ context.Context, entity string, _ uuid.UUID, _ bool) (uuid.UUID, error) {
			v, ok := ks[entity]
			require.Truef(t, ok, "this test has no identifier for %s", entity)

			return v.Uuid(), nil
		})),
	)
	require.NoError(t, err)

	return s
}

// TestTheWholeAnswerIsWrittenDown is what the two seams are for.
//
// A generated server answers with everything the schema declared, and the way
// that answer goes wrong is a field nobody was asserting -- an edge that came
// back empty, a version that did not move, a column an overlay renamed. Picking
// fields to assert on covers the ones somebody thought of; comparing the whole
// message covers the one added after this was written.
//
// It is possible only because neither of the two things that differ per run is
// left to chance. `WithMinter` came from upstream a while ago; `WithClock` is
// the other half, and until it existed the most a test could say about a
// timestamp was that it was near now.
func TestTheWholeAnswerIsWrittenDown(t *testing.T) {
	x := require.New(t)
	a := New(t)
	ctx := t.Context()

	s := told(t, a)

	tenant, err := s.Tenant().Add(ctx, app.TenantAddRequest_builder{
		Alias: "acme",
		Name:  "Acme",
	}.Build())
	x.NoError(err)

	robot, err := s.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	// A file each, because a golden file is named after the test that wrote it
	// -- and what these two say about each other is part of the answer: the
	// robot's tenant is the tenant's key, and a diff shows it.
	t.Run("the tenant", func(t *testing.T) { pdtest.Golden(t, tenant) })
	t.Run("the robot", func(t *testing.T) { pdtest.Golden(t, robot) })
}
