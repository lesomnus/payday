package pdid_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
)

// Domains used by the tests. They are registered in TestMain so that
// [pdid.Domain.String] has something to say; nothing else in this package
// depends on them being these particular numbers.
const (
	Tenant pdid.Domain = 1
	Holder pdid.Domain = 2
	Robot  pdid.Domain = 7
)

func TestMain(m *testing.M) {
	pdid.Register("test.Tenant", Tenant, "tenant")
	pdid.Register("test.Holder", Holder, "holder")
	pdid.Register("test.Robot", Robot, "robot")

	m.Run()
}

func TestNew(t *testing.T) {
	t.Run("carries its domain", func(t *testing.T) {
		x := require.New(t)

		v := pdid.New(Robot)
		x.Equal(Robot, v.Domain())

		d, ok := pdid.Of(v.Uuid())
		x.True(ok)
		x.Equal(Robot, d)
	})

	t.Run("is a v8 with the standard variant", func(t *testing.T) {
		x := require.New(t)

		v := pdid.New(Holder).Uuid()
		x.Equal(uuid.Version(8), v.Version())
		x.Equal(uuid.RFC4122, v.Variant())
	})

	t.Run("the domain is the last two digits of the fourth group", func(t *testing.T) {
		x := require.New(t)

		// It is the whole reason byte 9 was chosen: a person reading a log
		// should be able to see what a row was without counting bytes.
		v := pdid.New(Robot)
		gs := strings.Split(v.String(), "-")
		x.Len(gs, 5)
		x.Equal(fmt.Sprintf("%02x", uint8(Robot)), gs[3][2:])
	})

	t.Run("is unique", func(t *testing.T) {
		x := require.New(t)

		const n = 10000
		seen := make(map[pdid.Id]struct{}, n)
		for range n {
			v := pdid.New(Robot)
			_, dup := seen[v]
			x.False(dup, "made the same identifier twice")
			seen[v] = struct{}{}
		}
	})
}

// TestNewIsOrdered is the reason [pdid.New] writes the version into a nibble
// rather than a byte.
//
// `uuid.NewV7` keeps a twelve-bit counter across bytes 6 and 7 so that
// identifiers made inside one millisecond come out in the order they were made.
// Writing the version as a whole byte -- which is the obvious way to do it, and
// what an earlier implementation of this idea did -- takes the top four bits of
// that counter with it, and the ordering holds for 256 per millisecond instead
// of 4096.
//
// The naive version is built here beside the real one so that the test says
// what is being avoided rather than only that it was avoided.
func TestNewIsOrdered(t *testing.T) {
	// More than 256 so that the naive layout has to wrap, and comfortably under
	// 4096 so that the real one does not.
	const n = 2000

	ordered := func(vs []uuid.UUID) bool {
		for i := 1; i < len(vs); i++ {
			if bytes.Compare(vs[i-1][:], vs[i][:]) >= 0 {
				return false
			}
		}
		return true
	}

	t.Run("identifiers made in one millisecond keep their order", func(t *testing.T) {
		vs := make([]uuid.UUID, n)
		for i := range vs {
			vs[i] = pdid.New(Robot).Uuid()
		}

		require.True(t, ordered(vs),
			"the sequence counter was lost; see the note in pdid.New")
	})

	t.Run("and would not if the version were written as a byte", func(t *testing.T) {
		vs := make([]uuid.UUID, n)
		for i := range vs {
			v := uuid.Must(uuid.NewV7())
			v[6] = 0x80 // the naive way: the counter's high nibble goes with it
			v[9] = byte(Robot)
			vs[i] = v
		}

		require.False(t, ordered(vs),
			"this was expected to lose its order; if it no longer does, "+
				"uuid.NewV7 changed and the note in pdid.New should be re-read")
	})
}

func TestOf(t *testing.T) {
	t.Run("refuses a uuid this app did not make", func(t *testing.T) {
		x := require.New(t)

		for _, v := range []uuid.UUID{
			uuid.New(),                 // v4
			uuid.Must(uuid.NewV7()),    // v7
			uuid.Nil,                   // nothing
			uuid.MustParse("00000000-0000-8000-0000-000000000000"), // v8, wrong variant
		} {
			d, ok := pdid.Of(v)
			x.False(ok, "%s was taken for one of ours", v)
			x.Equal(pdid.Unknown, d)
		}
	})

	t.Run("a domain nothing registered still reads", func(t *testing.T) {
		x := require.New(t)

		v := pdid.New(pdid.Domain(200))
		d, ok := pdid.Of(v.Uuid())
		x.True(ok)
		x.Equal(pdid.Domain(200), d)
		x.Equal("domain(200)", d.String())
	})
}

func TestParse(t *testing.T) {
	x := require.New(t)

	v := pdid.New(Holder)

	u, err := pdid.Parse(v.String())
	x.NoError(err)
	x.Equal(v, u)

	u, err = pdid.From(v.Bytes())
	x.NoError(err)
	x.Equal(v, u)

	// A v4 is a perfectly good UUID and is refused anyway: the domain would be
	// whatever its ninth byte happened to be.
	_, err = pdid.Parse(uuid.New().String())
	x.ErrorIs(err, pdid.ErrNotAnId)

	_, err = pdid.From([]byte{1, 2, 3})
	x.Error(err)

	_, err = pdid.Parse("not a uuid")
	x.Error(err)
}

func TestMint(t *testing.T) {
	t.Run("makes one when the request named none", func(t *testing.T) {
		x := require.New(t)

		k, err := pdid.Mint(Robot, uuid.Nil, false)
		x.NoError(err)

		d, ok := pdid.Of(k)
		x.True(ok)
		x.Equal(Robot, d)
	})

	t.Run("keeps one of the right kind", func(t *testing.T) {
		x := require.New(t)

		v := pdid.New(Robot)
		k, err := pdid.Mint(Robot, v.Uuid(), true)
		x.NoError(err)
		x.Equal(v.Uuid(), k)
	})

	t.Run("refuses one of the wrong kind", func(t *testing.T) {
		x := require.New(t)

		v := pdid.New(Holder)
		_, err := pdid.Mint(Robot, v.Uuid(), true)
		x.ErrorIs(err, pdid.ErrDomain)

		// The message says what it actually was, which is the whole point of
		// refusing here rather than letting the query find nothing.
		x.Contains(err.Error(), "holder")
		x.Contains(err.Error(), "robot")
		x.Equal(codes.InvalidArgument, status.Code(err))
	})

	t.Run("refuses one this app did not make", func(t *testing.T) {
		x := require.New(t)

		_, err := pdid.Mint(Robot, uuid.New(), true)
		x.ErrorIs(err, pdid.ErrNotAnId)
		x.Equal(codes.InvalidArgument, status.Code(err))
	})

	t.Run("refuses the one that names nobody", func(t *testing.T) {
		x := require.New(t)

		_, err := pdid.Mint(Robot, uuid.Nil, true)
		x.Error(err)
		x.Equal(codes.InvalidArgument, status.Code(err))

		var e *pdid.NobodyError
		x.True(errors.As(err, &e))
	})
}

func TestRegistry(t *testing.T) {
	t.Run("says what the schema declared", func(t *testing.T) {
		x := require.New(t)

		d, ok := pdid.Lookup("test.Robot")
		x.True(ok)
		x.Equal(Robot, d)

		d, ok = pdid.DomainOf("robot")
		x.True(ok)
		x.Equal(Robot, d)

		x.Equal("robot", Robot.String())

		_, ok = pdid.Lookup("test.Nothing")
		x.False(ok)
	})

	t.Run("registering the same thing again is fine", func(t *testing.T) {
		require.NotPanics(t, func() {
			pdid.Register("test.Robot", Robot, "robot")
		})
	})

	t.Run("refuses to let a number mean two things", func(t *testing.T) {
		x := require.New(t)

		x.Panics(func() { pdid.Register("test.Other", Robot, "other") })
		x.Panics(func() { pdid.Register("test.Robot", pdid.Domain(9), "robot") })
		x.Panics(func() { pdid.Register("test.Zero", pdid.Unknown, "zero") })
	})
}

func TestWithDomain(t *testing.T) {
	x := require.New(t)

	v := pdid.New(Holder)
	u := pdid.WithDomain(v, Robot)

	x.Equal(Robot, u.Domain())
	x.Equal(Holder, v.Domain(), "the original was changed")

	// Everything but the domain byte is left alone.
	a, b := v.Bytes(), u.Bytes()
	a[9], b[9] = 0, 0
	x.Equal(a, b)
}
