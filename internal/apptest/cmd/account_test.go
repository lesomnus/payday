package cmd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lesomnus/protobuf-patch/patch"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"uuid"

	"github.com/lesomnus/payday/account"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/batch"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/holder"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/predicate"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The account layer (payday#184), in the app: an account signs in once and is
// a member of every tenant it has a holder in, a call is served as its
// membership of the tenant it names, and `HolderService.Memberships` is the
// one method an account calls as itself.
//
// Everything here runs on SQLite and, with `PDTEST_POSTGRES`, on PostgreSQL:
// a membership is a query by the account's column, and an account's call is a
// wall made of predicates, both of which are SQL somebody has to have run.

// accountKey is what the test's credential names an account in. A deployment
// signs one in with a session; payday keeps none, so which credentials name
// accounts is the app's to say ([cmd.Server.Accounts]).
const accountKey = "apptest-account"

// accountsSignIn reads an account out of [accountKey], and names nobody without it.
func accountsSignIn() auth.Handler {
	return auth.HandlerFunc(func(ctx context.Context) (auth.Identity, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		vs := md.Get(accountKey)
		if len(vs) == 0 {
			return auth.Identity{}, auth.ErrNoCredential
		}

		return auth.Identity{Method: "account", Id: vs[0], Grant: frame.Whole()}, nil
	})
}

// accounts is the app with three tenants and three accounts in them: `both`
// in acme and hooli, `one` in acme alone, and `none` in no tenant at all --
// and a robot in each tenant, which is what a call that reaches somebody
// else's tenant would find.
type accounts struct {
	*built

	acme, hooli, globex pdid.Id
	both, one, none     pdid.Id

	bothAcme, bothHooli, oneAcme pdid.Id

	robots map[pdid.Id][]byte
	thing  []byte

	conn *grpc.ClientConn
}

func withAccounts(t *testing.T) (*accounts, context.Context) {
	t.Helper()
	x := require.New(t)
	b, ctx := build(t)

	// Who a credential names is the app's to say, and here it is the test's:
	// an account in [accountKey], and anybody else as `Plain` believes them.
	b.Auth = auth.Seq(accountsSignIn(), auth.Plain())
	b.Accounts = func(id auth.Identity) bool { return id.Method == "account" }

	w := &accounts{built: b, acme: b.Tenant, robots: map[pdid.Id][]byte{}}
	for _, v := range []struct {
		at    *pdid.Id
		alias string
	}{{&w.hooli, "hooli"}, {&w.globex, "globex"}} {
		t, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: v.alias}.Build())
		x.NoError(err)
		*v.at = must(pdid.From(t.GetId()))
	}

	// An account is the identity provider's, named here by its identifier:
	// minted as one of a directory's holders would be.
	w.both, w.one, w.none = pdid.New(pd.HolderDomain), pdid.New(pd.HolderDomain), pdid.New(pd.HolderDomain)

	// Through the server the deployment holds, which is the only one that may
	// say which account a membership is of.
	member := func(of, in pdid.Id, alias string) pdid.Id {
		v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: in.Bytes()}.Build(),
			Alias:   alias,
			Name:    alias,
			Account: of.Bytes(),
		}.Build())
		x.NoError(err)

		return must(pdid.From(v.GetId()))
	}
	w.bothAcme = member(w.both, w.acme, "both")
	w.bothHooli = member(w.both, w.hooli, "both")
	w.oneAcme = member(w.one, w.acme, "one")

	for _, in := range []pdid.Id{w.acme, w.hooli, w.globex} {
		v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
			Alias:  "arm",
		}.Build())
		x.NoError(err)
		w.robots[in] = v.GetId()
	}

	// And a row behind no wall at all, which is not therefore an account's.
	v, err := b.Ungated.Thing().Add(ctx, app.ThingAddRequest_builder{Alias: "shared"}.Build())
	x.NoError(err)
	w.thing = v.GetId()

	w.conn = pdtest.Serve(t, b.grpc(t))

	return w, ctx
}

// as is a call `a` makes, naming the tenant it is in when there is one.
func (w *accounts) as(ctx context.Context, a pdid.Id, tenant ...string) context.Context {
	kv := []string{accountKey, a.String()}
	for _, v := range tenant {
		kv = append(kv, cmd.TenantHeader, v)
	}

	return metadata.AppendToOutgoingContext(ctx, kv...)
}

// robotsSeen is the robots a call is answered with, by the tenant they are in.
func (w *accounts) robotsSeen(t *testing.T, ctx context.Context) ([]pdid.Id, error) {
	t.Helper()

	vs, err := app.NewRobotServiceClient(w.conn).List(ctx, app.RobotListRequest_builder{}.Build())
	if err != nil {
		return nil, err
	}

	var out []pdid.Id
	for _, v := range vs.GetItems() {
		out = append(out, must(pdid.From(v.GetTenant().GetId())))
	}

	return out, nil
}

// TestAnAccountIsServedAsTheMembershipItNames: the tenant a call names, by
// alias or by identifier, and the account's only one when it names none.
func TestAnAccountIsServedAsTheMembershipItNames(t *testing.T) {
	w, ctx := withAccounts(t)

	for _, tc := range []struct {
		name   string
		of     pdid.Id
		tenant []string
		sees   pdid.Id
	}{
		{"by alias", w.both, []string{"hooli"}, w.hooli},
		{"by alias, with its @", w.both, []string{"@acme"}, w.acme},
		{"by identifier", w.both, []string{w.hooli.String()}, w.hooli},
		{"its only one, naming none", w.one, nil, w.acme},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			seen, err := w.robotsSeen(t, w.as(ctx, tc.of, tc.tenant...))
			x.NoError(err)
			x.Equal([]pdid.Id{tc.sees}, seen, "the membership's tenant, and nobody else's")
		})
	}

	t.Run("as the membership, with its account on it", func(t *testing.T) {
		x := require.New(t)

		// The trail says who made a write, which is the membership.
		v, err := app.NewRobotServiceClient(w.conn).Add(w.as(ctx, w.both, "hooli"), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: w.hooli.Bytes()}.Build(),
			Alias:  "made-as-both",
		}.Build())
		x.NoError(err)

		rows := w.trailOf(t, ctx, v.GetId())
		x.NotEmpty(rows)
		x.Equal(w.bothHooli.Uuid(), rows[0].ActorId)
		x.Equal(w.hooli.Uuid(), rows[0].ActorTenantId)
	})
}

// TestATenantTheAccountIsNotInIsRefusedAndNotRepointed.
//
// Naming a tenant the account has no membership of is refused with the one
// answer, whether or not there is such a tenant -- the header is no way to
// learn which there are -- and never served as one of the account's other
// memberships instead.
func TestATenantTheAccountIsNotInIsRefusedAndNotRepointed(t *testing.T) {
	w, ctx := withAccounts(t)

	for _, tc := range []struct {
		name string
		of   pdid.Id
		in   string
	}{
		{"a tenant there is, by alias", w.one, "hooli"},
		{"a tenant there is, by identifier", w.one, w.hooli.String()},
		{"a tenant nobody of it is in", w.both, "globex"},
		{"a tenant there is not", w.both, "initech"},
		{"an identifier of no tenant", w.both, pdid.New(pd.TenantDomain).String()},
		{"a holder's identifier", w.both, w.bothAcme.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			seen, err := w.robotsSeen(t, w.as(ctx, tc.of, tc.in))
			x.Equal(codes.PermissionDenied, status.Code(err))
			x.True(strings.HasPrefix(status.Convert(err).Message(), "this account is not in"), status.Convert(err).Message())
			x.Empty(seen)
		})
	}
}

func TestACallThatNamesNoTenantIsTheAccountsOnlyOne(t *testing.T) {
	w, ctx := withAccounts(t)

	_, err := w.robotsSeen(t, w.as(ctx, w.both))
	require.Equal(t, codes.InvalidArgument, status.Code(err), "two memberships, and the caller has to say which")
	require.Contains(t, err.Error(), cmd.TenantHeader)

	_, err = w.robotsSeen(t, w.as(ctx, w.none))
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "no membership to be served as")
}

// TestAnAccountsOwnMethodIsServedAsTheAccount: `Memberships` answers every
// tenant the account is in, which no membership could, and nothing of anybody
// else's.
func TestAnAccountsOwnMethodIsServedAsTheAccount(t *testing.T) {
	w, ctx := withAccounts(t)
	c := app.NewHolderServiceClient(w.conn)

	for _, tc := range []struct {
		name    string
		of      pdid.Id
		tenant  []string
		holders []pdid.Id
		in      []string
	}{
		{"of both its tenants", w.both, nil, []pdid.Id{w.bothAcme, w.bothHooli}, []string{"acme", "hooli"}},
		{"whatever the call names", w.both, []string{"hooli"}, []pdid.Id{w.bothAcme, w.bothHooli}, []string{"acme", "hooli"}},
		{"of its one", w.one, nil, []pdid.Id{w.oneAcme}, []string{"acme"}},
		{"of none, before it is anybody's member", w.none, nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			res, err := c.Memberships(w.as(ctx, tc.of, tc.tenant...), app.HolderMembershipsRequest_builder{}.Build())
			x.NoError(err)

			var hs []pdid.Id
			var in []string
			for _, v := range res.GetItems() {
				hs = append(hs, must(pdid.From(v.GetId())))
				in = append(in, v.GetTenant().GetAlias())
				x.Equal(tc.of.Bytes(), v.GetAccount())
			}
			x.ElementsMatch(tc.holders, hs, "the account's own holders: not the seed's admin, not another account's")
			x.ElementsMatch(tc.in, in)
		})
	}

	t.Run("and a membership is not asked it", func(t *testing.T) {
		_, err := c.Memberships(auth.PlainProvider(w.bothAcme.String()).Provide(ctx), app.HolderMembershipsRequest_builder{}.Build())
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
	})
}

// methodsOf is every method of every service this app serves, as gRPC
// registers them: what a test that means "any generated method" has to call,
// rather than a list somebody keeps.
type methodsOf struct {
	descs []*grpc.ServiceDesc
	impls []any
}

func (m *methodsOf) RegisterService(d *grpc.ServiceDesc, impl any) {
	m.descs = append(m.descs, d)
	m.impls = append(m.impls, impl)
}

// called is a method's answer, with the code it ended in.
type called struct {
	method string
	code   codes.Code
	answer []proto.Message
}

// each calls every method of `s` that `which` takes with an empty request and
// the context `of` makes for it, and answers with how each ended. A stream is
// called too, and one that is served -- a watch -- waits for a change until its
// context is done, so a test that would let one through leaves it out with
// `which`.
func each(s app.Server, which func(method string) bool, of func(method string) context.Context) []called {
	var m methodsOf
	app.RegisterServer(&m, s)

	var out []called
	for i, d := range m.descs {
		for _, v := range d.Methods {
			name := "/" + d.ServiceName + "/" + v.MethodName
			if !which(name) {
				continue
			}
			res, err := v.Handler(m.impls[i], of(name), func(any) error { return nil }, nil)
			c := called{method: name, code: status.Code(err)}
			if v, ok := res.(proto.Message); ok && err == nil {
				c.answer = append(c.answer, v)
			}
			out = append(out, c)
		}
		for _, v := range d.Streams {
			name := "/" + d.ServiceName + "/" + v.StreamName
			if !which(name) {
				continue
			}
			ss := &streamed{ctx: of(name)}
			err := v.Handler(m.impls[i], ss)
			out = append(out, called{method: name, code: status.Code(err), answer: ss.msgs})
		}
	}

	return out
}

// streamed is a stream that is asked for an empty request and keeps what it is
// sent.
type streamed struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []proto.Message
}

func (s *streamed) Context() context.Context     { return s.ctx }
func (s *streamed) RecvMsg(any) error            { return nil }
func (s *streamed) SetHeader(metadata.MD) error  { return nil }
func (s *streamed) SendHeader(metadata.MD) error { return nil }
func (s *streamed) SetTrailer(metadata.MD)       {}
func (s *streamed) SendMsg(m any) error {
	if v, ok := m.(proto.Message); ok {
		s.msgs = append(s.msgs, v)
	}

	return nil
}

// TestAnAccountFrameReadsNoTenantsRowsThroughAnyMethod is the gate's half of
// a frame with no tenant, asked of every method this app serves.
//
// A call framed as an account -- by a resolver that made one for a method it
// should not have, or by anything that put one in a context -- is refused
// every method the schema does not mark the account's own, by the generated
// gate, whatever ran in front of it: here nothing did. Its own method is
// served, and reads what it declared.
func TestAnAccountFrameReadsNoTenantsRowsThroughAnyMethod(t *testing.T) {
	w, ctx := withAccounts(t)
	framed := func(context.Context) context.Context {
		return frame.Into(ctx, frame.OfAccount(w.both, frame.Whole(), func(string) bool { return true }))
	}

	every := func(string) bool { return true }
	cs := each(w.Walled, every, func(method string) context.Context {
		// Dispatched as itself, as gRPC would, and with nothing dispatched:
		// both are refused, and the second is a call made in this process.
		return batch.AsOp(framed(ctx), method)
	})
	cs = append(cs, each(w.Walled, every, func(string) context.Context { return framed(ctx) })...)

	x := require.New(t)
	x.Greater(len(cs), 2*50, "every method of every service, twice: far more than a list somebody keeps")
	for _, c := range cs {
		if pd.AccountMethod(c.method) {
			x.Equal(codes.OK, c.code, c.method)
			continue
		}
		x.Equal(codes.PermissionDenied, c.code, "%s answered an account's frame", c.method)
		x.Empty(c.answer, c.method)
	}

	// And over the wire, with the interceptors in front: a resolver that
	// answered an account's frame for every call is refused at the gate's
	// interceptor before anything is read, and the batch per operation.
	t.Run("over the wire", func(t *testing.T) {
		x := require.New(t)

		anybody := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
			return frame.OfAccount(w.both, frame.Grant{}, func(string) bool { return true }), nil
		})
		chain := []grpc.ServerOption{
			grpc.ChainUnaryInterceptor(auth.InterceptorUnary(accountsSignIn(), anybody, nil), gate.Unary(nil)),
			grpc.ChainStreamInterceptor(auth.InterceptorStream(accountsSignIn(), anybody, nil), gate.Stream(nil)),
		}
		g := grpc.NewServer(chain...)
		app.RegisterServer(g, w.Walled)
		conn := pdtest.Serve(t, g)
		as := w.as(ctx, w.both, "acme")

		_, err := app.NewRobotServiceClient(conn).Get(as, app.RobotGetRequest_builder{
			Ref: app.RobotRef_builder{Id: w.robots[w.acme]}.Build(),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))

		_, err = app.NewThingServiceClient(conn).Get(as, app.ThingGetRequest_builder{
			Ref: app.ThingRef_builder{Id: w.thing}.Build(),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "a global row is not therefore an account's")

		// Without the interceptors the gate refuses it itself.
		bare := grpc.NewServer(
			grpc.ChainUnaryInterceptor(auth.InterceptorUnary(accountsSignIn(), anybody, nil)),
			grpc.ChainStreamInterceptor(auth.InterceptorStream(accountsSignIn(), anybody, nil)))
		app.RegisterServer(bare, w.Walled)
		_, err = app.NewTenantServiceClient(pdtest.Serve(t, bare)).List(as, app.TenantListRequest_builder{}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))

		// And a batch asks of each operation.
		_, err = w.batched(t, batch.Guard{Closed: func(string) bool { return false }}).Do(
			batch.AsOp(framed(ctx), "/payday.BatchService/Do"),
			pdpb.BatchRequest_builder{Ops: []*pdpb.Op{op(t, app.RobotService_Get_FullMethodName, app.RobotGetRequest_builder{
				Ref: app.RobotRef_builder{Id: w.robots[w.acme]}.Build(),
			}.Build())}}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})
}

// TestAnAccountReadsNothingItDidNotDeclare.
//
// Its own method passes the gate, and what the layer serving it reads through
// the walled servers is the wall's -- which has no tenant to narrow by, and so
// answers every entity with nothing, a global one too, until the layer says
// what the method reads.
func TestAnAccountReadsNothingItDidNotDeclare(t *testing.T) {
	w, ctx := withAccounts(t)
	own := app.HolderService_Memberships_FullMethodName
	as := batch.AsOp(frame.Into(ctx, frame.OfAccount(w.both, frame.Whole(), pd.AccountMethod)), own)

	// Every List there is, with nothing declared: nothing, of any of them.
	// And the deployment's own server sees what is there, so the empty
	// answers above are the wall and not an empty database.
	lists := func(ctx context.Context) map[string]int {
		out := map[string]int{}
		listing := func(m string) bool { return strings.HasSuffix(m, "/List") }
		for _, c := range each(w.Walled, listing, func(string) context.Context { return ctx }) {
			if c.code != codes.OK {
				continue
			}
			for _, v := range c.answer {
				m := v.ProtoReflect()
				if f := m.Descriptor().Fields().ByName("items"); f != nil {
					out[c.method] += m.Get(f).List().Len()
				}
			}
		}

		return out
	}

	x := require.New(t)
	for method, n := range lists(as) {
		x.Zero(n, "%s answered an account with rows it did not declare", method)
	}
	x.NotEmpty(lists(as), "every List was refused, which says nothing about the wall")

	there, err := w.Ungated.Robot().List(ctx, app.RobotListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(there.GetItems(), 3)

	for _, get := range []func(ctx context.Context) error{
		func(ctx context.Context) error {
			_, err := w.Walled.Robot().Get(ctx, app.RobotGetRequest_builder{Ref: app.RobotRef_builder{Id: w.robots[w.acme]}.Build()}.Build())
			return err
		},
		func(ctx context.Context) error {
			_, err := w.Walled.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: app.TenantRef_builder{Id: w.acme.Bytes()}.Build()}.Build())
			return err
		},
		func(ctx context.Context) error {
			_, err := w.Walled.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: w.bothAcme.Bytes()}.Build()}.Build())
			return err
		},
		func(ctx context.Context) error {
			_, err := w.Walled.Thing().Get(ctx, app.ThingGetRequest_builder{Ref: app.ThingRef_builder{Id: w.thing}.Build()}.Build())
			return err
		},
	} {
		x.Equal(codes.NotFound, status.Code(get(as)), "a row it did not declare is a row there is not")
	}

	t.Run("and what it declared, it reads", func(t *testing.T) {
		x := require.New(t)

		declared, err := pd.AccountReads(as, own, pd.Reads{
			Holder: func(_ context.Context, a pdid.Id) (predicate.Holder, error) {
				return holder.Account(a.Uuid()), nil
			},
		})
		x.NoError(err)

		vs, err := w.Walled.Holder().List(declared, app.HolderListRequest_builder{}.Build())
		x.NoError(err)
		var hs []pdid.Id
		for _, v := range vs.GetItems() {
			hs = append(hs, must(pdid.From(v.GetId())))
		}
		x.ElementsMatch([]pdid.Id{w.bothAcme, w.bothHooli}, hs)

		// And nothing else for it.
		_, err = w.Walled.Holder().Get(declared, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: w.oneAcme.Bytes()}.Build()}.Build())
		x.Equal(codes.NotFound, status.Code(err))
		_, err = w.Walled.Robot().Get(declared, app.RobotGetRequest_builder{Ref: app.RobotRef_builder{Id: w.robots[w.acme]}.Build()}.Build())
		x.Equal(codes.NotFound, status.Code(err))

		// A declaration is for a call an account makes as itself: a
		// membership's reads are its tenant's, whatever it carries.
		member := frame.Into(declared, frame.New(w.oneAcme, w.acme, frame.Whole()).WithScope(frame.Only(w.acme)))
		vs, err = w.Walled.Holder().List(batch.AsOp(member, app.HolderService_List_FullMethodName), app.HolderListRequest_builder{}.Build())
		x.NoError(err)
		for _, v := range vs.GetItems() {
			x.Equal(w.acme.Bytes(), v.GetTenant().GetId())
		}
	})

	t.Run("and it declares for a method that is the account's own", func(t *testing.T) {
		_, err := pd.AccountReads(as, app.RobotService_List_FullMethodName, pd.Reads{})
		require.Equal(t, codes.Internal, status.Code(err))
	})
}

// TestTheAccessRecordNamesTheAccount (payday#111): a call an account makes as
// itself has no holder to name, so its row names the account, with no tenant
// beside it -- filed under each tenant whose people it showed.
func TestTheAccessRecordNamesTheAccount(t *testing.T) {
	x := require.New(t)
	w, ctx := withAccounts(t)

	_, err := app.NewHolderServiceClient(w.conn).Memberships(w.as(ctx, w.both), app.HolderMembershipsRequest_builder{}.Build())
	x.NoError(err)

	got := map[uuid.UUID][][]byte{}
	for _, v := range w.accessed(t, ctx) {
		if v.Action != app.HolderService_Memberships_FullMethodName {
			continue
		}
		x.Equal(w.both.Uuid(), v.ActorId, "the account, as who made the call")
		x.Equal(uuid.UUID{}, v.ActorTenantId, "in no tenant, which no membership's row is")
		got[v.TenantId] = append(got[v.TenantId], v.Subjects...)
	}
	x.Equal(map[uuid.UUID][][]byte{
		w.acme.Uuid():  subjects(w.bothAcme),
		w.hooli.Uuid(): subjects(w.bothHooli),
	}, got, "a row in each tenant whose people it showed, each naming them")
}

// TestWhichAccountAMembershipIsOfIsTheDeployments.
//
// It is what a call is resolved by. A request that could set it could make a
// holder somebody else's membership; one that could move it could take a
// membership over. So the gate refuses it to every write a caller asks for,
// and the deployment writes it through the server it holds.
func TestWhichAccountAMembershipIsOfIsTheDeployments(t *testing.T) {
	w, ctx := withAccounts(t)
	as := w.built.as(ctx) // the seed's admin of acme, a membership
	mine := app.HolderRef_builder{Id: w.Holder.Bytes()}.Build()

	for _, tc := range []struct {
		name  string
		write func() error
	}{
		{"an Add naming one", func() error {
			_, err := w.Walled.Holder().Add(as, app.HolderAddRequest_builder{
				Tenant:  app.TenantRef_builder{Id: w.acme.Bytes()}.Build(),
				Alias:   "planted",
				Account: w.none.Bytes(),
			}.Build())
			return err
		}},
		{"a Patch moving it", func() error {
			_, err := w.Walled.Holder().Patch(as, app.HolderPatchRequest_builder{
				Ref:              mine,
				Account:          w.one.Bytes(),
				DateUpdatedForce: z.Ptr(true),
			}.Build())
			return err
		}},
		{"a Patch clearing it", func() error {
			_, err := w.Walled.Holder().Patch(as, app.HolderPatchRequest_builder{
				Ref:              app.HolderRef_builder{Id: w.oneAcme.Bytes()}.Build(),
				AccountNull:      z.Ptr(true),
				DateUpdatedForce: z.Ptr(true),
			}.Build())
			return err
		}},
		{"a document moving it", func() error {
			_, err := w.Walled.Holder().Apply(as, app.HolderApplyRequest_builder{
				Ref:   mine,
				Patch: patch.MustNewUntyped(patch.Target(patch.Name("account")).Assign(patch.Bytes(w.one.Bytes()))),
			}.Build())
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			err := tc.write()
			x.Equal(codes.InvalidArgument, status.Code(err))
			x.Contains(err.Error(), "account")
		})
	}

	x := require.New(t)
	ms, err := pd.Memberships(w.Ent).Of(ctx, w.one, account.Selector{}, 0)
	x.NoError(err)
	x.Equal([]account.Membership{{Holder: w.oneAcme, Tenant: w.acme}}, ms, "nothing moved")

	// As a membership over the wire, which is the same gate.
	_, err = app.NewHolderServiceClient(w.conn).Add(w.as(ctx, w.one), app.HolderAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: w.acme.Bytes()}.Build(),
		Alias:   "planted",
		Account: w.none.Bytes(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// TestAnAccountsMembershipsAndAHoldersAccount are the two helpers `pd gen`
// writes: what an account is in, and whose a holder is.
func TestAnAccountsMembershipsAndAHoldersAccount(t *testing.T) {
	x := require.New(t)
	w, ctx := withAccounts(t)
	ms := pd.Memberships(w.Ent)

	all, err := ms.Of(ctx, w.both, account.Selector{}, 0)
	x.NoError(err)
	x.Equal([]account.Membership{{Holder: w.bothAcme, Tenant: w.acme}, {Holder: w.bothHooli, Tenant: w.hooli}}, all, "oldest first")

	for _, in := range []account.Selector{{Alias: "hooli"}, {Id: w.hooli}, {Id: w.hooli, Alias: "hooli"}} {
		vs, err := ms.Of(ctx, w.both, in, 0)
		x.NoError(err)
		x.Equal([]account.Membership{{Holder: w.bothHooli, Tenant: w.hooli}}, vs, in.String())
	}
	vs, err := ms.Of(ctx, w.both, account.Selector{Id: w.hooli, Alias: "acme"}, 0)
	x.NoError(err)
	x.Empty(vs, "both have to hold of one tenant")

	vs, err = ms.Of(ctx, w.both, account.Selector{}, 1)
	x.NoError(err)
	x.Len(vs, 1)

	vs, err = ms.Of(ctx, pdid.Nil, account.Selector{}, 0)
	x.NoError(err)
	x.Empty(vs, "the Nil account is nobody's, and a holder that names none is no account's membership")

	// An erased one is a membership no longer.
	_, err = w.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: w.bothHooli.Bytes()}.Build())
	x.NoError(err)
	vs, err = ms.Of(ctx, w.both, account.Selector{}, 0)
	x.NoError(err)
	x.Equal([]account.Membership{{Holder: w.bothAcme, Tenant: w.acme}}, vs)
	seen, err := w.robotsSeen(t, w.as(ctx, w.both))
	x.NoError(err)
	x.Equal([]pdid.Id{w.acme}, seen, "and it is the account's only one now")

	h, err := w.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: w.oneAcme.Bytes()}.Build()}.Build())
	x.NoError(err)
	a, ok := pd.HolderAccount(h)
	x.True(ok)
	x.Equal(w.one, a)

	h, err = w.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: w.Holder.Bytes()}.Build()}.Build())
	x.NoError(err)
	_, ok = pd.HolderAccount(h)
	x.False(ok, "the seed's admin is no account's")
}
