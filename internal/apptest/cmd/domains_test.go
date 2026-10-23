package cmd_test

import (
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/payday/internal/apptest"
	entarchived "github.com/lesomnus/payday/internal/apptest/internal/ent/archived"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// declaredDomains is the domains this app's schema gives payday's Archived and
// Access, read off the option as it was compiled -- `robot.proto` says the
// package is the app's -- and payday's, 5 and 6, where it gives none.
//
// This app gives none, and keeps payday's. pdcli's
// TestARenumberedAppKeepsItsTrailAndItsAccessRecord generates a copy of it
// that gives both, and runs the test below in that copy: what is asserted is
// the schema's word, whichever app this is.
func declaredDomains(t *testing.T) (archived, access pdid.Domain) {
	t.Helper()

	a, _ := proto.GetExtension(app.File_app_robot_proto.Options(), pdpb.E_App).(*pdpb.App)
	archived, access = 5, 6
	if n := a.GetDomains().GetArchived(); n != 0 {
		archived = pdid.Domain(n)
	}
	if n := a.GetDomains().GetAccess(); n != 0 {
		access = pdid.Domain(n)
	}

	return archived, access
}

// TestTheManifestAndTheAccessRecordAreTheDomainsTheAppGivesThem (payday#163):
// every identifier of payday's Archived and Access carries the domain the
// app's schema declares for it -- the rows the manifest writes as a pass moves
// the trail into the archive, and the rows of the access record as a call is
// recorded -- and the archive files the access record under its kind's name,
// and reads it back as what it is.
//
// Through the whole stack, because that is where a number written down
// anywhere but the schema would be: a generated `pdid.New(5)`, a document
// recognised by `domain == 6`, a kind named by its number.
func TestTheManifestAndTheAccessRecordAreTheDomainsTheAppGivesThem(t *testing.T) {
	x := require.New(t)
	archived, access := declaredDomains(t)

	x.Equal(archived, pd.ArchivedDomain)
	x.Equal(access, pd.AccessDomain)
	x.Equal("archived", pdid.Domains()[archived])
	x.Equal("access", pdid.Domains()[access])

	// What `audit.by.access` names, which is a word and not a number.
	d, err := trail.DomainOf("access")
	x.NoError(err)
	x.Equal(access, d)

	b, ctx := build(t)
	b.person(t, ctx, "alice")

	conn := pdtest.Serve(t, b.grpc(t))
	_, err = app.NewHolderServiceClient(conn).List(b.travels(ctx), app.HolderListRequest_builder{}.Build())
	x.NoError(err)

	rs := b.accessed(t, ctx)
	x.Len(rs, 1)
	r := rs[0]
	got, ok := pdid.Of(r.Id)
	x.True(ok, "the access record's row is no identifier of this app's")
	x.Equal(access, got, "the access record's row was minted under another domain")
	x.EqualValues(access, r.Domain, "the access record's row is filed under another kind")

	doc, _, err := trail.GenerateKey()
	x.NoError(err)
	key, err := trail.ParseKey(doc)
	x.NoError(err)

	a := flob.NewMemStores()
	s := pd.TrailStore(b.Ent)
	p := trail.Policy{Archive: a, Keep: trail.Keep{Retain: time.Nanosecond}, Signer: key}
	p.Pass(ctx, s)

	x.Empty(b.accessed(t, ctx), "the access record did not leave the database")
	rows, err := b.Ent.Archived.Query().Where(entarchived.NamespaceNEQ("")).All(ctx)
	x.NoError(err)
	x.NotEmpty(rows, "the pass wrote no account of the archive")
	kept := map[string]int{}
	for _, v := range rows {
		got, ok := pdid.Of(v.Id)
		x.True(ok, "a row of the manifest is no identifier of this app's")
		x.Equal(archived, got, "a row of the manifest was minted under another domain")
		kept[v.Labels["Kind"]]++
	}
	x.Equal(1, kept["access"], "the manifest keeps no blob of the access record under its kind's name: %v", kept)

	cs, err := trail.Chunks(ctx, a)
	x.NoError(err)
	kinds := map[string]int{}
	for _, c := range cs {
		kinds[c.Kind] += c.Rows
	}
	x.Equal(1, kinds["access"], "the access record is not filed as its kind: %v", kinds)

	seen := 0
	x.NoError(trail.Read(ctx, a, pd.AccessOf(func(v *app.Access) error {
		got, ok := pdid.Of([16]byte(v.GetId()))
		x.True(ok)
		x.Equal(access, got)
		x.EqualValues(access, v.GetDomain())
		seen++

		return nil
	})))
	x.Equal(1, seen, "the access record was not read back out of the archive as what it is")

	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(len(rows), v.Blobs)
}
