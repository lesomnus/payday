package trail

import (
	"archive/tar"
	"bytes"
	"crypto"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// exported is a bundle as whoever it was for reads it: every file, by name.
func exported(t *testing.T, b []byte, id age.Identity, trusted ...crypto.PublicKey) (Exported, map[string][]byte, error) {
	t.Helper()

	files := map[string][]byte{}
	v, err := OpenTenantExport(bytes.NewReader(b), []age.Identity{id}, trusted, func(name string, r io.Reader) error {
		b, err := io.ReadAll(r)
		files[name] = b
		return err
	})

	return v, files, err
}

// sayings is what every row of these files says, by what it says: under
// `database/` and `rows/`, each a line.
func sayings(t *testing.T, files map[string][]byte) []string {
	t.Helper()

	out := []string{}
	for name, b := range files {
		if !strings.HasPrefix(name, exportDatabase+"/") && !strings.HasPrefix(name, exportRows+"/") {
			continue
		}
		for _, doc := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			h, err := headOf(doc)
			require.NoError(t, err)
			require.Empty(t, h.Sealed, "%s holds a row still sealed", name)
			v, err := base64.StdEncoding.DecodeString(h.Value)
			require.NoError(t, err)
			out = append(out, string(v))
		}
	}

	return out
}

// TestATenantsTrailIsExportedForWhoeverItIsFor (#115).
//
// Every row of the trail that names the tenant goes into one bundle: what is
// still in the database, and what the archive holds -- the chunks of its own
// as they are kept, and each chunk's rows of the tenant opened, its own and
// those of a chunk it shares, and nothing of another tenant's. It is written
// encrypted to whoever it is for, and reads with their identity alone. Its
// manifest says every file's digest, the newest checkpoint and what its
// witness answered, and is signed; the act leaves a receipt naming it.
func TestATenantsTrailIsExportedForWhoeverItIsFor(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	mine, theirs, third := newTenant(), newTenant(), newTenant()
	k, master := sealing(t)
	arch := flob.NewMemStores()
	p := Policy{Signer: signer, Archive: arch, Keep: Keep{Retain: 30 * day}, Keys: k, Witnesses: map[string]Witness{"ear": &ear{}}}

	s := &fakeStore{}
	s.add(fakeRow{tenant: mine.Uuid(), domain: person, created: ago(40 * day), value: "archived, and sealed"})
	s.add(fakeRow{tenant: theirs.Uuid(), actor: mine.Uuid(), domain: person, created: ago(40 * day), value: "a transfer they filed"})
	s.add(fakeRow{tenant: theirs.Uuid(), domain: person, created: ago(40 * day), value: "somebody else's"})
	s.add(fakeRow{tenant: theirs.Uuid(), actor: third.Uuid(), domain: person, created: ago(40 * day), value: "two others' transfer"})
	p.Pass(ctx, s)
	s.add(fakeRow{tenant: mine.Uuid(), domain: person, created: ago(day), value: "still in the database"})
	s.add(fakeRow{tenant: theirs.Uuid(), actor: mine.Uuid(), domain: person, created: ago(day), value: "a transfer since"})

	newest, _, err := s.Latest(ctx)
	x.NoError(err)

	// An operator at a shell, with the master to open what is sealed.
	op := p
	op.Keys = &Keys{Store: k.Store, Master: k.Master, Identities: []age.Identity{master}}
	whom, err := age.GenerateX25519Identity()
	x.NoError(err)
	out := &bytes.Buffer{}
	v, err := op.ExportTenant(ctx, s, mine, Export{By: "an operator", Why: "case 2026-4", To: []age.Recipient{whom.Recipient()}}, out)
	x.NoError(err)

	x.True(bytes.HasPrefix(out.Bytes(), []byte("age-encryption.org/v1")), "not written encrypted")
	for _, said := range []string{"still in the database", "archived, and sealed"} {
		x.NotContains(out.String(), base64.StdEncoding.EncodeToString([]byte(said)), "in the clear")
	}

	got, files, err := exported(t, out.Bytes(), whom, signer.Public())
	x.NoError(err)
	x.Equal(v.Digest, got.Digest)
	x.Equal(mine.String(), got.Tenant)
	x.Equal("an operator", got.By)
	x.Equal("case 2026-4", got.Why)
	x.Equal(2, got.Database, "the rows still in the database")
	x.Equal(2, got.Archive, "the rows the archive holds")
	x.ElementsMatch([]string{"archived, and sealed", "a transfer they filed", "still in the database", "a transfer since"}, sayings(t, files),
		"what the tenant's trail said, every row of it and nobody else's")

	// Its own chunk whole, as the archive keeps it; the one it shares, its
	// rows alone. Both listed by the newest checkpoint, which its witness
	// heard.
	x.Len(got.Chunks, 2)
	for _, c := range got.Chunks {
		x.True(c.Covered, "%s is not one the checkpoint lists", c.Digest)
		x.Equal(1, c.Rows)
		raw, ok := files[exportArchive+"/"+c.Namespace+"/"+c.Digest.Encoded()]
		if c.Namespace == mine.String() {
			x.True(c.Whole)
			x.True(ok, "its own chunk is not in it")
			x.Equal(c.Digest, flob.DigestFromBytes(raw), "its own chunk is not as the archive keeps it")
		} else {
			x.Equal(SharedNamespace, c.Namespace)
			x.False(c.Whole)
			x.False(ok, "a chunk it shares is in it whole, and somebody else's rows with it")
		}
	}
	x.NotNil(got.Checkpoint)
	x.Equal(newest, got.Checkpoint.K)
	x.Len(got.Checkpoint.Witnessed, 1)
	x.Equal("ear", got.Checkpoint.Witnessed[0].Witness)
	x.NotEmpty(got.Checkpoint.Witnessed[0].Receipt)
	x.Nil(got.Retired)

	// It is an act of the trail's, which says who took it out, why, and the
	// manifest it wrote.
	exports := 0
	for _, r := range receiptsIn(t, arch) {
		if r.Act == "export-tenant" && r.Tenant == mine.String() {
			exports++
			x.Equal("an operator", r.By)
			x.Equal("case 2026-4", r.Why)
			x.Equal(string(v.Digest), r.Of)
			x.Equal(4, r.Rows)
		}
	}
	x.Equal(1, exports, "no receipt says it was taken out")
	after, _, err := s.Latest(ctx)
	x.NoError(err)
	x.Equal(newest+1, after, "the export's receipt is covered by no checkpoint")
}

// TestABundleIsWhatItsManifestSays: a bundle is read only when it is what its
// manifest says, all of it, and the manifest is signed by a key trusted. Half
// a bundle -- an export that did not finish -- is refused too, and so is one
// opened with an identity it was not encrypted to.
func TestABundleIsWhatItsManifestSays(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	mine := newTenant()
	p := Policy{Signer: signer, Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}}
	s := &fakeStore{}
	s.add(fakeRow{tenant: mine.Uuid(), domain: person, created: ago(40 * day), value: "archived"})
	p.Pass(ctx, s)
	s.add(fakeRow{tenant: mine.Uuid(), domain: person, created: ago(day), value: "still in the database"})

	whom, err := age.GenerateX25519Identity()
	x.NoError(err)
	out := &bytes.Buffer{}
	_, err = p.ExportTenant(ctx, s, mine, Export{By: "an operator", Why: "a case", To: []age.Recipient{whom.Recipient()}}, out)
	x.NoError(err)
	_, _, err = exported(t, out.Bytes(), whom, signer.Public())
	x.NoError(err)

	// The bundle written again, as somebody holding the identity could.
	again := func(fn func(name string, b []byte) []byte, extra ...string) []byte {
		dec, err := age.Decrypt(bytes.NewReader(out.Bytes()), whom)
		x.NoError(err)
		buf := &bytes.Buffer{}
		enc, err := age.Encrypt(buf, whom.Recipient())
		x.NoError(err)
		tw := tar.NewWriter(enc)
		put := func(name string, b []byte) {
			x.NoError(tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b))}))
			_, err := tw.Write(b)
			x.NoError(err)
		}
		for _, name := range extra {
			put(name, []byte("{}\n"))
		}
		tr := tar.NewReader(dec)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			x.NoError(err)
			b, err := io.ReadAll(tr)
			x.NoError(err)
			if b = fn(h.Name, b); b != nil {
				put(h.Name, b)
			}
		}
		x.NoError(tw.Close())
		x.NoError(enc.Close())
		return buf.Bytes()
	}
	same := func(_ string, b []byte) []byte { return b }

	_, _, err = exported(t, again(same), whom, signer.Public())
	x.NoError(err, "written again as it was, and refused")

	_, _, err = exported(t, again(func(name string, b []byte) []byte {
		if strings.HasPrefix(name, exportDatabase+"/") {
			return bytes.Replace(b, []byte(base64.StdEncoding.EncodeToString([]byte("still in the database"))), []byte(base64.StdEncoding.EncodeToString([]byte("something else entirely"))), 1)
		}
		return b
	}), whom, signer.Public())
	x.ErrorIs(err, ErrExportAltered, "a row changed")

	_, _, err = exported(t, again(func(name string, b []byte) []byte {
		if strings.HasPrefix(name, exportArchive+"/") {
			return nil
		}
		return b
	}), whom, signer.Public())
	x.ErrorIs(err, ErrExportAltered, "a chunk taken out")
	x.ErrorContains(err, "is not in it")

	_, _, err = exported(t, again(func(name string, b []byte) []byte {
		if name == exportManifest {
			x.Contains(string(b), `"why":"a case"`)
			return bytes.Replace(b, []byte(`"why":"a case"`), []byte(`"why":"no case"`), 1)
		}
		return b
	}), whom, signer.Public())
	x.ErrorIs(err, ErrExportAltered, "a manifest changed after it was signed")

	_, _, err = exported(t, again(same, exportRows+"/planted.jsonl"), whom, signer.Public())
	x.ErrorIs(err, ErrExportAltered, "a file put in")

	_, _, err = exported(t, again(func(name string, b []byte) []byte {
		if name == exportManifest {
			return nil
		}
		return b
	}), whom, signer.Public())
	x.ErrorIs(err, ErrExportAltered, "half a bundle")
	x.ErrorContains(err, "no manifest")

	_, _, err = exported(t, out.Bytes(), whom, newKey(t).Public())
	x.ErrorIs(err, ErrExportAltered, "a manifest no trusted key signed")

	somebody, err := age.GenerateX25519Identity()
	x.NoError(err)
	_, _, err = exported(t, out.Bytes(), somebody, signer.Public())
	x.Error(err, "opened by somebody it was not for")
}

// TestAnExportTakesOutNothingNotItsDigest: a chunk whose bytes are not its
// digest any more is what somebody wrote under its name, and an export stops
// at it rather than hand it out as the archive's.
func TestAnExportTakesOutNothingNotItsDigest(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	mine := newTenant()
	dir := t.TempDir()
	p := Policy{Signer: signer, Archive: flob.NewOsStores(dir), Keep: Keep{Retain: 30 * day}}
	s := &fakeStore{}
	r := s.add(fakeRow{tenant: mine.Uuid(), domain: person, created: ago(40 * day), value: "what it said"})
	p.Pass(ctx, s)

	cs, err := chunksIn(ctx, p.Archive, mine.String())
	x.NoError(err)
	x.Len(cs, 1)
	r.value = "what somebody else says it said"
	path := blobPath(dir, cs[0].Digest)
	x.NoError(os.Chmod(path, 0o600))
	x.NoError(os.WriteFile(path, gz(t, r.doc()), 0o600))

	whom, err := age.GenerateX25519Identity()
	x.NoError(err)
	_, err = p.ExportTenant(ctx, s, mine, Export{By: "an operator", Why: "a case", To: []age.Recipient{whom.Recipient()}}, io.Discard)
	x.ErrorIs(err, ErrAltered)
}

// TestAnExportSaysWhoAndWhyAndIsForSomebody: an export takes who takes it
// out, why, whoever it is for, and the key that signs it; and a tenant whose
// rows the archive sealed is exported opened or not at all.
func TestAnExportSaysWhoAndWhyAndIsForSomebody(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	mine := newTenant()
	whom, err := age.GenerateX25519Identity()
	x.NoError(err)
	to := []age.Recipient{whom.Recipient()}

	p := Policy{Signer: signer, Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}}
	s := &fakeStore{}
	s.add(fakeRow{tenant: mine.Uuid(), domain: person, created: ago(40 * day), value: "archived"})
	p.Pass(ctx, s)
	_, err = p.ExportTenant(ctx, s, mine, Export{By: "an operator", Why: "a case", To: to}, io.Discard)
	x.NoError(err)

	for want, e := range map[string]Export{
		"who takes it out":  {Why: "a case", To: to},
		"why it is taken":   {By: "an operator", To: to},
		"and names nobody":  {By: "an operator", Why: "a case"},
		"and names nobody ": {By: "an operator", Why: "a case", To: []age.Recipient{}},
	} {
		_, err := p.ExportTenant(ctx, s, mine, e, io.Discard)
		x.ErrorContains(err, strings.TrimSpace(want), "%+v", e)
	}

	unsigned := p
	unsigned.Signer = nil
	_, err = unsigned.ExportTenant(ctx, s, mine, Export{By: "an operator", Why: "a case", To: to}, io.Discard)
	x.ErrorIs(err, ErrNoKey)

	// The serving process holds no master, and a sealed row read without it
	// is no row anybody can read.
	k, _ := sealing(t)
	sealed := Policy{Signer: signer, Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Keys: k}
	s = &fakeStore{}
	s.add(fakeRow{tenant: mine.Uuid(), domain: person, created: ago(40 * day), value: "sealed"})
	sealed.Pass(ctx, s)
	_, err = sealed.ExportTenant(ctx, s, mine, Export{By: "an operator", Why: "a case", To: to}, io.Discard)
	x.ErrorIs(err, ErrSealed)
}

// TestARetiredTenantsTrailIsExportedUntilItsFloorsPass: what a retirement
// kept is exported as any trail is, and the manifest says when the tenant
// retired and until when its floors keep what they keep -- which `Retired`
// says too.
func TestARetiredTenantsTrailIsExportedUntilItsFloorsPass(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	gone := newTenant()
	p := Policy{Signer: signer, Archive: flob.NewMemStores(), Keep: Keep{Retain: time.Nanosecond},
		MinBy: map[pdid.Domain]Floor{robot: {Destroy: 365 * day}}}
	s := &fakeStore{}
	s.add(fakeRow{tenant: gone.Uuid(), domain: robot, created: ago(day), value: "what a floor is on"})
	s.add(fakeRow{tenant: gone.Uuid(), domain: person, created: ago(day), value: "what no floor is on"})
	p.Pass(ctx, s)
	_, err := p.RetireTenant(ctx, s, gone)
	x.NoError(err)

	retired, err := p.Retired(ctx, s)
	x.NoError(err)
	r, ok := retired[gone]
	x.True(ok)
	x.Equal(r.At.Add(365*day), r.Until)

	whom, err := age.GenerateX25519Identity()
	x.NoError(err)
	out := &bytes.Buffer{}
	_, err = p.ExportTenant(ctx, s, gone, Export{By: "an operator", Why: "a demand to preserve", To: []age.Recipient{whom.Recipient()}}, out)
	x.NoError(err)

	got, files, err := exported(t, out.Bytes(), whom, signer.Public())
	x.NoError(err)
	x.Equal([]string{"what a floor is on"}, sayings(t, files))
	x.NotNil(got.Retired)
	x.Equal(r, *got.Retired)
}
