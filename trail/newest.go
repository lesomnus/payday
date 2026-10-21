package trail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
)

// Newest is the archive as a check of the live rows reads it (payday#102,
// #108): for a thing the trail in the database says nothing about any more,
// the newest row of the archive about it.
//
// # Why a check reads the archive at all
//
// A row nobody has written since its trail rows were archived -- most rows,
// in a deployment a year old -- is otherwise a row the trail says nothing
// about, and so is a row somebody **added** in the database past the servers.
// Counted together, the second is lost among the first. Read from the archive,
// the first are checked as any row is, and what is left is the finding.
//
// # What it reads
//
// The chunks the manifest accounts for, as [Policy.Read] does, each checked
// against its digest as it is read: a chunk somebody put into the archive, or
// put back from a backup, is not one a check takes a row's word from. And only
// their heads. The digest of a row and the fields it covers are kept in the
// clear beside its object, while what it says is sealed, so no tenant's key is
// opened -- a check reads every tenant's rows, and needs nothing of any of
// them but a digest that means nothing without the deployment's key.
//
// # Listed once
//
// A check asks about one kind at a time, and listing the archive reads a label
// for every chunk -- a request each, on S3. So [Policy.Newest] lists it once,
// and [Newest.Of] reads the chunks of the kind it is asked about.
type Newest struct {
	a flob.Stores

	// chunks is every chunk the manifest accounts for, by kind; "" is a chunk
	// that says nothing of its kind, which a format before kinds wrote.
	chunks map[string][]Chunk
}

// Last is what the newest row of the archive about one thing keeps of the row
// its write left: its digest, the fields the digest covers, and the line the
// digest is bound to (payday#115). Nothing of what it sealed.
type Last struct {
	Created      time.Time
	Action       string
	Digest       []byte
	DigestFields []uint32

	// Tenant is the tenant the line is filed under, which is where a line
	// carried forward from it is filed too.
	Tenant pdid.Id

	// row is its identifier, which orders two rows written at one moment as
	// the database orders them.
	row []byte
}

// Line is the line of the trail this was, as its digest is bound to it.
func (v Last) Line() audit.Line {
	var id [16]byte
	copy(id[:], v.row)

	return audit.Line{Id: id, Created: v.Created, Action: v.Action}
}

// after answers whether `v` is a newer row than `was`.
func (v Last) after(was Last) bool {
	if c := v.Created.Compare(was.Created); c != 0 {
		return c > 0
	}

	return bytes.Compare(v.row, was.row) > 0
}

// Newest lists the chunks of the archive the manifest accounts for, for a
// check of the live rows to read by kind. See [Newest].
//
// A chunk the manifest does not account for is left out, and said so in the
// log, as [Policy.Read] leaves it out.
func (p Policy) Newest(ctx context.Context, s Store) (*Newest, error) {
	if p.Archive == nil {
		return nil, errors.New("no archive to read")
	}
	if s == nil {
		return nil, errors.New("no store to read the archive's manifest from")
	}

	x, err := p.start(s)
	if err != nil {
		return nil, err
	}

	nss, err := chunkNamespaces(ctx, x.raw())
	if err != nil {
		return nil, err
	}

	cs, rest, err := x.chunks(ctx, nss, true)
	if err != nil {
		return nil, err
	}
	if len(rest) > 0 {
		log.From(ctx).WarnContext(ctx, "trail: chunks the manifest does not account for, left out of what a check of the rows reads",
			"chunks", len(rest), "see", "`trail verify`")
	}

	n := &Newest{a: x.raw(), chunks: map[string][]Chunk{}}
	for _, c := range cs {
		n.chunks[c.Kind] = append(n.chunks[c.Kind], c)
	}

	return n, nil
}

// Of is the newest row the archive holds about each of `ids`, all of them
// things of `kind`, and nothing for one it holds nothing about.
//
// It reads every chunk of the kind, in every namespace: a row about a thing is
// filed under its tenant, under the shared namespace when it named another,
// and under the actor's tenant when the thing is behind no wall. Reading where
// it might be rather than where it should be is what makes a row that is not
// there a finding.
func (n *Newest) Of(ctx context.Context, kind pdid.Domain, ids []pdid.Id) (map[pdid.Id]Last, error) {
	want := make(map[string]pdid.Id, len(ids))
	for _, v := range ids {
		want[encoded(v)] = v
	}

	return n.lines(ctx, kind, func(h head) (pdid.Id, bool) {
		id, ok := want[h.Object]
		return id, ok
	})
}

// All is the newest row the archive holds about every thing of `kind` it holds
// a row about: what a check of the rows reads to find a thing the trail says
// is a row and is not (payday#115), which no list of identifiers it already
// has could name.
func (n *Newest) All(ctx context.Context, kind pdid.Domain) (map[pdid.Id]Last, error) {
	return n.lines(ctx, kind, func(h head) (pdid.Id, bool) {
		id, err := objectOf(h)
		if err != nil || id.Domain() != kind {
			return pdid.Nil, false
		}

		return id, true
	})
}

// lines is the newest row of the chunks of `kind` about each thing `which`
// names.
func (n *Newest) lines(ctx context.Context, kind pdid.Domain, which func(head) (pdid.Id, bool)) (map[pdid.Id]Last, error) {
	out := map[pdid.Id]Last{}
	for _, c := range slices.Concat(n.chunks[nameOf(kind)], n.chunks[""]) {
		err := readChunk(ctx, n.a, c, nil, nil, true, func(h head, _ []byte) error {
			id, ok := which(h)
			if !ok {
				return nil
			}

			v, err := lastOf(h)
			if err != nil {
				return fmt.Errorf("a row about %s: %w", id, err)
			}
			if was, ok := out[id]; ok && !v.after(was) {
				return nil
			}

			out[id] = v
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

// objectOf is the thing a row's head is about.
func objectOf(h head) (pdid.Id, error) {
	b, err := base64.StdEncoding.DecodeString(h.Object)
	if err != nil || len(b) != 16 {
		return pdid.Nil, fmt.Errorf("a row about %q, which is no identifier", h.Object)
	}

	return pdid.Id(b), nil
}

// lastOf is what a row's head says of the row its write left.
func lastOf(h head) (Last, error) {
	created, err := time.Parse(time.RFC3339Nano, h.Created)
	if err != nil {
		return Last{}, fmt.Errorf("when it was written: %w", err)
	}

	row, err := base64.StdEncoding.DecodeString(h.Id)
	if err != nil {
		return Last{}, fmt.Errorf("its identifier: %w", err)
	}

	digest, err := base64.StdEncoding.DecodeString(h.Digest)
	if err != nil {
		return Last{}, fmt.Errorf("its digest: %w", err)
	}

	var tenant pdid.Id
	if h.Tenant != "" {
		b, err := base64.StdEncoding.DecodeString(h.Tenant)
		if err != nil || len(b) != 16 {
			return Last{}, fmt.Errorf("its tenant: %q is no identifier", h.Tenant)
		}
		tenant = pdid.Id(b)
	}

	return Last{
		Created:      created,
		Action:       h.Action,
		Digest:       digest,
		DigestFields: h.DigestFields,
		Tenant:       tenant,
		row:          row,
	}, nil
}

// Carried is a line a pass writes before it destroys the newest line about a
// thing that is still a row (payday#115): what that line said of the row,
// carried onto a line of the database's own, so that a row nobody wrote since
// its window ran out is still one the trail accounts for. See [Store.Carry].
//
// Sum is the destroyed line's digest, and the store binds it to the line it
// writes ([audit.Digest.Carry]) once it has minted it: the row's MAC is
// carried as it was recorded, and only the line's is new.
type Carried struct {
	Object       pdid.Id
	Tenant       pdid.Id
	Sum          []byte
	DigestFields []uint32
}

// CarriedAction is what a line a pass carries forward says was done.
const CarriedAction = "payday.trail.carry"
