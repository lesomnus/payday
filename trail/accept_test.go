package trail

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"
)

// TestAnAcceptanceIsOfWhatWasLookedAt (#115).
//
// A verification answers its findings and a digest of them, the same however
// often it is asked, and an acceptance names that digest. One that names none,
// or another verification's, is refused; and so is one of findings that are no
// longer all there is to find -- a chunk put there after somebody looked is not
// taken in beside the one they looked at, and nothing is.
func TestAnAcceptanceIsOfWhatWasLookedAt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, p, tenant := manifestedArchive(t, signer)
	put := func(run, value string) flob.Digest {
		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(35 * day), value: value}
		return plant(t, p.Archive, Chunk{Namespace: tenant.String(), Kind: "person", Month: monthOf(r.created), Run: run, Rows: 1,
			First: r.created, Last: r.created}, r.doc())
	}
	first := put("first", "put there")

	looked, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"unaccounted"}, kinds(looked))
	again, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal(looked.Digest, again.Digest)

	a := Acceptance{Of: looked.Digest, By: "an operator", Why: "put there by the restore of 2026-10-01"}
	for name, v := range map[string]Acceptance{
		"of nothing":    {By: a.By, Why: a.Why},
		"by nobody":     {Of: a.Of, Why: a.Why},
		"for no reason": {Of: a.Of, By: a.By},
	} {
		_, err := p.Accept(ctx, s, v)
		x.Error(err, name)
	}

	_, err = p.Accept(ctx, s, Acceptance{Of: flob.DigestFromBytes([]byte("other findings")), By: a.By, Why: a.Why})
	x.ErrorIs(err, ErrUnreviewed)

	second := put("second", "put there after somebody looked")
	_, err = p.Accept(ctx, s, a)
	x.ErrorIs(err, ErrUnreviewed)
	x.Empty(s.state(tenant.String(), first), "an acceptance that was refused took in what was looked at")
	x.Empty(s.state(tenant.String(), second))
	x.Equal(1, latestOf(t, s), "an acceptance that was refused took a checkpoint")

	looked, err = p.Verify(ctx, s, false)
	x.NoError(err)
	a.Of = looked.Digest
	_, err = p.Accept(ctx, s, a)
	x.NoError(err)
	x.Equal(Present, s.state(tenant.String(), first))
	x.Equal(Present, s.state(tenant.String(), second))

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
}

// TestWhatWasAcceptedIsKept (#115).
//
// In the checkpoint the acceptance ends in, which is signed: who, why, the
// digest of what was looked at, how many findings of each kind, and which --
// the first of them, and how many more. In a receipt of its own, as every act
// of the trail's. And a verification lists the latest acceptances, the newest
// first. A checkpoint from before acceptances were kept says only why, and is
// read as that.
func TestWhatWasAcceptedIsKept(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, p, tenant := manifestedArchive(t, signer)
	put := []flob.Digest{}
	for i := range acceptedKeys + 2 {
		r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: ago(35 * day), value: strconv.Itoa(i)}
		put = append(put, plant(t, p.Archive, Chunk{Namespace: tenant.String(), Kind: "person", Month: monthOf(r.created), Run: strconv.Itoa(i), Rows: 1,
			First: r.created, Last: r.created}, r.doc()))
	}

	v, err := accept(t, p, s, Acceptance{By: "an operator", Why: "restored by hand"})
	x.NoError(err)

	c := numbered(t, p, latestOf(t, s)).c
	x.NotNil(c.Accepted)
	x.Equal("an operator", c.Accepted.By)
	x.Equal("restored by hand", c.Accepted.Why)
	x.Equal(v.Digest, c.Accepted.Of)
	x.Nil(c.Accepted.Restored)
	x.Equal(map[string]int{"unaccounted": acceptedKeys + 2}, c.Accepted.Kinds)
	x.Len(c.Accepted.Keys, acceptedKeys)
	x.Equal(2, c.Accepted.More)
	x.Equal("unaccounted "+tenant.String()+" "+string(slices.Min(put)), c.Accepted.Keys[0])

	x.True(slices.ContainsFunc(receiptsIn(t, p.Archive), func(r Receipt) bool {
		return r.Act == "accept" && r.By == "an operator" && r.Why == "restored by hand" && r.Of == string(v.Digest) &&
			r.Findings["unaccounted"] == acceptedKeys+2
	}), "an acceptance left no receipt")

	w, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(w.Ok(), "%v", w.Findings)
	x.Len(w.Accepted, 1)
	x.Equal(c.K, w.Accepted[0].Checkpoint)
	x.Equal("restored by hand", w.Accepted[0].Why)

	for i := range recentAccepted {
		_, err := accept(t, p, s, Acceptance{Why: "looked at, " + strconv.Itoa(i)})
		x.NoError(err)
	}
	w, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Len(w.Accepted, recentAccepted)
	x.Equal("looked at, "+strconv.Itoa(recentAccepted-1), w.Accepted[0].Why)
	x.Greater(w.Accepted[0].Checkpoint, w.Accepted[1].Checkpoint)

	var old Checkpoint
	x.NoError(json.Unmarshal([]byte(`{"k":3,"at":"2026-10-01T00:00:00Z","count":0,"digest":"`+string(digestOf(nil))+`","blobs":[],"accepted":"from before"}`), &old))
	x.Equal("from before", old.Accepted.Why)
}

// TestWhatSigningARowBringsToLightIsNotTakenInUnseen (#115).
//
// A row whose signature is a finding is reported for itself, and its blob is
// compared with nothing. Accepted, it is signed, and then its blob is: a blob
// the row said the archive held, and it does not, is `missing` -- which nobody
// looked at, so it is not taken out of the manifest with the row's signature,
// and is the next verification's to find. And signing is of the rows that were
// looked at: one put there after the verification is left unsigned.
func TestWhatSigningARowBringsToLightIsNotTakenInUnseen(t *testing.T) {
	ctx := t.Context()

	t.Run("a blob the row said was there", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, signer)
		gone := flob.DigestFromBytes([]byte("a chunk nobody can find"))
		s.put(Archived{Namespace: tenant.String(), Digest: string(gone), State: Present})

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unsigned"}, kinds(v))

		_, err = p.Accept(ctx, s, Acceptance{Of: v.Digest, By: "an operator", Why: "a row from before the key"})
		x.NoError(err)
		x.Equal(Present, s.state(tenant.String(), gone), "an acceptance of a row's signature took its blob out, unseen")

		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"missing"}, kinds(v))

		_, err = p.Accept(ctx, s, Acceptance{Of: v.Digest, By: "an operator", Why: "the chunk is gone, and that was looked at"})
		x.NoError(err)
		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
	})
	t.Run("a row put there after", func(t *testing.T) {
		x := require.New(t)
		s, p, tenant := manifestedArchive(t, signer)
		looked := flob.DigestFromBytes([]byte("looked at"))
		s.put(Archived{Namespace: tenant.String(), Digest: string(looked), State: Present})

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)

		since := flob.DigestFromBytes([]byte("put there since"))
		s.put(Archived{Namespace: tenant.String(), Digest: string(since), State: Present})
		x.NoError(p.vouch(ctx, s, v.Findings))

		n := 0
		for _, r := range s.rowsOf() {
			switch flob.Digest(r.Digest) {
			case looked:
				n++
				x.NotEmpty(r.Signature.Sig, "a row that was looked at was not signed")
			case since:
				n++
				x.Empty(r.Signature.Sig, "a row put there after the verification was signed")
			}
		}
		x.Equal(2, n)
	})
}

// TestARestoreNamesTheCheckpointItsBackupHolds (#115).
//
// A database put back as a backup had it, two checkpoints later, is accepted
// only as the restore it is: naming the checkpoint the backup holds, which is
// the latest the database numbered. And its own latest checkpoints are from
// before the backup, so a chunk taken away since -- by somebody who then put
// the database back to hide it, which looks the same -- is what the newest
// checkpoint the deployment signed says the archive held, and holds no more:
// `lost`, and an acceptance of the restore is of it as of every finding.
func TestARestoreNamesTheCheckpointItsBackupHolds(t *testing.T) {
	ctx := t.Context()

	restoredFromOne := func(t *testing.T) (*fakeStore, Policy, string) {
		s, p, tenant := manifestedArchive(t, signer)
		s.mu.Lock()
		backup, counter, at := slices.Clone(s.archived), s.counter, s.counterAt
		s.mu.Unlock()

		for _, v := range []string{"after the backup", "and after that"} {
			s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(2 * day), value: v})
			_, err := Archive(ctx, s, p, Kinds{}, time.Now())
			require.NoError(t, err)
		}
		require.Equal(t, 3, latestOf(t, s))

		s.mu.Lock()
		s.archived, s.counter, s.counterAt = backup, counter, at
		s.mu.Unlock()

		return s, p, tenant.String()
	}

	t.Run("names it", func(t *testing.T) {
		x := require.New(t)
		s, p, _ := restoredFromOne(t)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"behind", "unaccounted"}, kinds(v))
		a := Acceptance{Of: v.Digest, By: "an operator", Why: "restored from the backup of checkpoint 1"}

		_, err = p.Accept(ctx, s, a)
		x.ErrorIs(err, ErrRestored, "a database behind the store was accepted as no restore")
		a.Restored = restored(2)
		_, err = p.Accept(ctx, s, a)
		x.ErrorIs(err, ErrRestored, "a restore was accepted from a backup the database is not")
		x.Equal(1, latestOf(t, s))

		a.Restored = restored(1)
		_, err = p.Accept(ctx, s, a)
		x.NoError(err)
		x.Equal(4, latestOf(t, s))
		x.Equal(1, *numbered(t, p, 4).c.Accepted.Restored)
	})
	t.Run("and only of a database behind", func(t *testing.T) {
		x := require.New(t)
		s, p, _ := manifestedArchive(t, signer)

		_, err := accept(t, p, s, Acceptance{Why: "nothing was restored", Restored: restored(1)})
		x.ErrorIs(err, ErrRestored)
	})
	t.Run("of what the newest checkpoint held, and is gone", func(t *testing.T) {
		x := require.New(t)
		s, p, ns := restoredFromOne(t)

		rowed := map[flob.Digest]bool{}
		for _, v := range s.rowsOf() {
			rowed[flob.Digest(v.Digest)] = true
		}
		var gone Kept
		for _, b := range numbered(t, p, 3).c.Blobs {
			if b.Namespace == ns && !rowed[b.Digest] {
				gone = b
				break
			}
		}
		x.NotEmpty(gone.Digest)
		x.NoError(p.Archive.Use(gone.Namespace).Erase(ctx, gone.Digest))

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"behind", "lost", "unaccounted"}, kinds(v))
		i := slices.IndexFunc(v.Findings, func(f Finding) bool { return f.Kind == "lost" })
		x.Equal(gone.Digest, v.Findings[i].Digest)
		x.Equal(3, v.Findings[i].Checkpoint)

		_, err = accept(t, p, s, Acceptance{Why: "restored; the chunk taken away since was looked at", Restored: restored(1)})
		x.NoError(err)

		w, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(w.Ok(), "%v", w.Findings)
		x.Equal(1, w.Accepted[0].Kinds["lost"])
	})
}
