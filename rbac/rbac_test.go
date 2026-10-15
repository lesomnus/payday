package rbac_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lesomnus/payday/rbac"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func rules() rbac.Rules {
	return rbac.Rules{
		Roles:    map[string]rbac.Role{"@acme/reader": {Methods: []string{"/app.ManifestService/Get"}}},
		Groups:   map[string]rbac.Group{"@acme/robots": {Members: []string{"@acme/kamino"}}},
		Bindings: []rbac.Binding{{Role: "@acme/reader", Group: "@acme/robots"}},
	}
}

func TestBindingsAndIsolation(t *testing.T) {
	r := rules()
	s, err := rbac.New(r)
	require.NoError(t, err)
	r.Roles["@acme/reader"].Methods[0] = "/*.*/*"
	gs, err := s.Grants(t.Context(), "@acme/kamino")
	require.NoError(t, err)
	require.True(t, rbac.Allows(gs, "/app.ManifestService/Get"))
	require.False(t, rbac.Allows(gs, "/app.ManifestService/Erase"))
	gs[0].Methods[0] = "/*.*/*"
	gs, err = s.Grants(t.Context(), "@acme/kamino")
	require.NoError(t, err)
	require.False(t, rbac.Allows(gs, "/app.ManifestService/Erase"))
	gs, err = s.Grants(t.Context(), "@other/kamino")
	require.NoError(t, err)
	require.Empty(t, gs)
	r = rules()
	r.Bindings[0] = rbac.Binding{Role: "@acme/reader", Holder: "@acme/direct"}
	s, err = rbac.New(r)
	require.NoError(t, err)
	gs, err = s.Grants(t.Context(), "@acme/kamino")
	require.NoError(t, err)
	require.Empty(t, gs, "membership alone grants nothing")
	gs, err = s.Grants(t.Context(), "@acme/direct")
	require.NoError(t, err)
	require.True(t, rbac.Allows(gs, "/app.ManifestService/Get"))
}

func TestInvalidPolicyIsRejected(t *testing.T) {
	for name, edit := range map[string]func(*rbac.Rules){
		"missing role":         func(r *rbac.Rules) { r.Bindings[0].Role = "@acme/missing" },
		"missing group":        func(r *rbac.Rules) { r.Bindings[0].Group = "@acme/missing" },
		"two subjects":         func(r *rbac.Rules) { r.Bindings[0].Holder = "@acme/other" },
		"no subject":           func(r *rbac.Rules) { r.Bindings[0].Group = "" },
		"cross tenant member":  func(r *rbac.Rules) { r.Groups["@acme/robots"] = rbac.Group{Members: []string{"@other/kamino"}} },
		"cross tenant binding": func(r *rbac.Rules) { r.Bindings[0] = rbac.Binding{Role: "@acme/reader", Holder: "@other/kamino"} },
		"unqualified":          func(r *rbac.Rules) { r.Bindings[0] = rbac.Binding{Role: "@acme/reader", Holder: "kamino"} },
		"invalid glob":         func(r *rbac.Rules) { r.Roles["@acme/reader"] = rbac.Role{Methods: []string{"*"}} },
	} {
		t.Run(name, func(t *testing.T) { r := rules(); edit(&r); _, err := rbac.New(r); require.Error(t, err) })
	}
	for _, p := range []string{"/app.ManifestService/*", "/app.*/Get", "/*.*/*", "/google.protobuf.Any/Pack"} {
		require.NoError(t, rbac.ValidateMethod(p))
	}
	for _, p := range []string{"*", "**", "!/app.X/Get", "/app.X/*Get*", "/app.X/**", "/app.X/", "/app.X/Get/more"} {
		require.Error(t, rbac.ValidateMethod(p))
	}
}

func TestReplacementAndSources(t *testing.T) {
	s, err := rbac.NewStore(rules())
	require.NoError(t, err)
	pinned := s.Pin(t.Context())
	_, changed := s.Current()
	require.NoError(t, s.Replace(rules()))
	select {
	case <-changed:
		t.Fatal("unchanged policy interrupted streams")
	default:
	}
	invalid := rules()
	invalid.Bindings[0].Role = "bad"
	require.Error(t, s.Replace(invalid))
	select {
	case <-changed:
		t.Fatal("invalid policy replaced current policy")
	default:
	}
	require.NoError(t, s.Replace(rbac.Rules{}))
	select {
	case <-changed:
	default:
		t.Fatal("replacement did not notify streams")
	}
	gs, err := s.Grants(t.Context(), "@acme/kamino")
	require.NoError(t, err)
	require.Empty(t, gs)
	gs, err = s.Grants(pinned, "@acme/kamino")
	require.NoError(t, err)
	require.NotEmpty(t, gs)
	other, _ := rbac.New(rules())
	gs, err = (rbac.Union{s, other}).Grants(t.Context(), "@acme/kamino")
	require.NoError(t, err)
	require.NotEmpty(t, gs)
	broken := rbac.SourceFunc(func(context.Context, string) ([]rbac.Grant, error) { return nil, errors.New("unavailable") })
	_, err = (rbac.Union{other, broken}).Grants(t.Context(), "@acme/kamino")
	require.Error(t, err)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				_, e := s.Grants(t.Context(), "@acme/kamino")
				require.NoError(t, e)
			}
		})
	}
	for range 50 {
		require.NoError(t, s.Replace(rules()))
		require.NoError(t, s.Replace(rbac.Rules{}))
	}
	wg.Wait()
}

type stream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s stream) Context() context.Context { return s.ctx }

func TestPolicyReplacementEndsStreams(t *testing.T) {
	s, err := rbac.NewStore(rules())
	require.NoError(t, err)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Interceptor().Stream[0](nil, stream{ctx: t.Context()}, &grpc.StreamServerInfo{}, func(_ any, ss grpc.ServerStream) error {
			close(ready)
			<-ss.Context().Done()
			return ss.Context().Err()
		})
	}()
	<-ready
	require.NoError(t, s.Replace(rbac.Rules{}))
	require.Equal(t, codes.Unauthenticated, status.Code(<-done))
}

func TestApplicationValidationKeepsPreviousSnapshot(t *testing.T) {
	deny := false
	s, err := rbac.NewStore(rules(), func(rbac.Rules) error {
		if deny {
			return errors.New("application refused")
		}
		return nil
	})
	require.NoError(t, err)
	_, changed := s.Current()
	deny = true
	require.ErrorContains(t, s.Replace(rbac.Rules{}), "application refused")
	select {
	case <-changed:
		t.Fatal("rejected policy interrupted streams")
	default:
	}
	gs, err := s.Grants(t.Context(), "@acme/kamino")
	require.NoError(t, err)
	require.NotEmpty(t, gs)
	deny = false
	require.NoError(t, s.Replace(rbac.Rules{}))
}

func TestCanonicalNamesAndIDs(t *testing.T) {
	for _, name := range []string{"@acme/robot", "@0199e000-0000-8000-8001-000000000001/0199e000-0000-8000-8002-000000000002"} {
		_, err := rbac.Name(name)
		require.NoError(t, err)
	}
	for _, name := range []string{"@/robot", "@acme/", "@Acme/robot", "@acme/robot#holder", "@acme/robot/other", "@00000000-0000-0000-0000-000000000000/robot"} {
		_, err := rbac.Name(name)
		require.Error(t, err)
	}
}
