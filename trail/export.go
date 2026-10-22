package trail

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/lesomnus/flob"
	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/pdid"
)

// ExportFormat is what a bundle of a tenant's trail says it is
// ([Policy.ExportTenant]).
const ExportFormat = "payday-trail-export/1"

// The files of a bundle: the manifest, which is written last, and the
// directories the rest are in.
const (
	exportManifest = "manifest.json"
	exportDatabase = "database"
	exportArchive  = "archive"
	exportRows     = "rows"
)

// Export is who takes a tenant's trail out and why, and who it is for.
type Export struct {
	// By is who exports it, and Why the request it answers: a case, a
	// demand to preserve. Both are required, and both are kept, in the
	// manifest and in the act's receipt.
	By  string
	Why string

	// To is who may read it: the public keys the bundle is encrypted to, as
	// `age-keygen -y` prints one, or a plugin's -- a key in a YubiKey,
	// `age1yubikey1…`, which opens it with the device. At least one: a bundle
	// is never written in the clear.
	To []age.Recipient
}

// ExportManifest is what a bundle says of itself, signed by the deployment's
// key ([Policy.Signer]).
type ExportManifest struct {
	Format string    `json:"format"`
	Tenant string    `json:"tenant"`
	At     time.Time `json:"at"`
	By     string    `json:"by"`
	Why    string    `json:"why"`

	// Files is every file of the bundle but the manifest, in the order it
	// was written, by its digest and size.
	Files []ExportFile `json:"files"`

	// Database and Archive are how many rows of the tenant's the bundle
	// holds: in the database when it was written, and in the archive.
	Database int `json:"database"`
	Archive  int `json:"archive"`

	// Chunks is every chunk of the archive the tenant's rows were read from.
	Chunks []ExportChunk `json:"chunks,omitempty"`

	// Checkpoint is the newest the deployment vouched for when the bundle was
	// written, which the chunks were held to, and what its witnesses
	// answered. Nil for a deployment with no archive, or none taken yet.
	Checkpoint *ExportCheckpoint `json:"checkpoint,omitempty"`

	// Retired is the tenant's retirement when it had retired: when, and until
	// when its floors keep what they keep.
	Retired *Retirement `json:"retired,omitempty"`
}

// ExportFile is one file of a bundle.
type ExportFile struct {
	Name   string      `json:"name"`
	Digest flob.Digest `json:"digest"`
	Size   int64       `json:"size"`
}

// ExportChunk is one chunk of the archive a bundle read the tenant's rows
// from.
type ExportChunk struct {
	Namespace string      `json:"ns"`
	Digest    flob.Digest `json:"digest"`

	// Rows is how many of its rows are the tenant's, under `rows/` in the
	// bundle as they read: opened, and of a chunk others share, the tenant's
	// alone.
	Rows int `json:"rows"`

	// Whole is a chunk of the tenant's own, whose bytes are in the bundle as
	// the archive keeps them, under `archive/`; one it shares with others, or
	// a file from before, is not.
	Whole bool `json:"whole,omitempty"`

	// Covered is a chunk the checkpoint lists: the deployment said the
	// archive held it, signed, and its witnesses heard so.
	Covered bool `json:"covered"`
}

// ExportCheckpoint is the checkpoint a bundle names: its number, what the
// checkpoint store keeps it under, when it was taken, and what each witness
// answered when it heard it.
type ExportCheckpoint struct {
	K         int               `json:"k"`
	Digest    flob.Digest       `json:"digest"`
	At        time.Time         `json:"at"`
	Witnessed []ExportWitnessed `json:"witnessed,omitempty"`
}

// ExportWitnessed is a witness's receipt of a checkpoint, as the checkpoint
// store keeps it ([Witness]): in the witness's own form, a log's inclusion
// proof, a store's copy.
type ExportWitnessed struct {
	Witness string          `json:"witness"`
	Receipt json.RawMessage `json:"receipt,omitempty"`
}

// Exported is a bundle's manifest, the digest of it as signed, which the
// act's receipt names, and the public half of the key that signed it, PKIX.
type Exported struct {
	ExportManifest
	Digest flob.Digest
	Key    []byte
}

// exportEnvelope is the manifest as a bundle carries it: its bytes, and the
// signature over exactly those bytes with the public half of the key that made
// it.
type exportEnvelope struct {
	Manifest json.RawMessage `json:"manifest"`
	Key      []byte          `json:"key"`
	Sig      []byte          `json:"sig"`
}

// ErrExportAltered is a bundle that is not what its manifest says: a file
// whose bytes are not its digest, one it does not list or one missing, or a
// manifest no trusted key signed.
var ErrExportAltered = errors.New("the bundle is not what its manifest says")

// ExportTenant writes the whole of a tenant's trail as one bundle, encrypted
// to whoever it is for, for keeping somewhere the deployment does not reach: a
// demand to preserve what a retired tenant left, which nothing in the
// deployment holds past its floors, or a live tenant's for a case of its own.
//
// # What it holds
//
// Every row of the trail that names the tenant -- the ones it may read, by the
// wall's rule -- wherever it is:
//
//   - `database/`: the rows still in the database, its `Audit` and what the
//     store reads beside it (an app's access record), a page a file, as the
//     archive would write them;
//   - `archive/<namespace>/<digest>`: each chunk of the tenant's own
//     namespace, its bytes as the archive keeps them, which the checkpoint
//     the manifest names lists;
//   - `rows/<namespace>/<digest>.jsonl`: the tenant's rows of each chunk it
//     was read from, as they read -- opened, if the archive seals them, and
//     of a chunk it shares with others or a file from before, its own alone.
//     What another tenant's rows said is never in it.
//
// So it reads without the deployment's keys: whoever it was encrypted to
// reads every row. Sealed rows are opened as it is written, which takes the
// master's private half ([Keys.Identities]); without it a bundle of sealed
// rows is refused ([ErrSealed]).
//
// # What it says of itself
//
// Last, `manifest.json`: every file's digest and size, every chunk read, the
// newest checkpoint the deployment vouches for -- its number and digest, and
// what each of its witnesses answered -- and whether each chunk is one it
// lists, all signed with [Policy.Signer]. [OpenTenantExport] holds a bundle
// to the manifest and the manifest to a key; see it for holding a bundle to
// the chain and to its witnesses later, when the deployment may be gone.
//
// # And what it is
//
// It hands people's data out of the deployment, so it is an act of the
// trail's: in the trail's turn on [Policy.Lease], it writes a [Receipt],
// `export-tenant`, saying who, why and the manifest's digest, and ends in a
// checkpoint. An app records it as it records a download of people's data --
// its access record, with the reason -- since nothing here knows who a row
// is about.
//
// The bundle is written through `w` encrypted, and never otherwise: there is
// no clear copy of it on a disk to forget. One that fails part way is half a
// bundle, which [OpenTenantExport] refuses; throw it away.
func (p Policy) ExportTenant(ctx context.Context, s Store, tenant pdid.Id, e Export, w io.Writer) (_ Exported, err error) {
	switch {
	case tenant.IsZero():
		return Exported{}, errors.New("no tenant")
	case strings.TrimSpace(e.By) == "":
		return Exported{}, errors.New("an export says who takes it out")
	case strings.TrimSpace(e.Why) == "":
		return Exported{}, errors.New("an export says why it is taken out")
	case len(e.To) == 0:
		return Exported{}, errors.New("an export is written encrypted to whoever it is for, and names nobody")
	case p.Signer == nil:
		return Exported{}, fmt.Errorf("an export's manifest is signed: %w", ErrNoKey)
	case s == nil:
		return Exported{}, errors.New("no store to read the tenant's rows from")
	}

	x, err := p.act(s)
	if err != nil {
		return Exported{}, err
	}
	ctx, give, err := x.turn(ctx)
	if err != nil {
		return Exported{}, err
	}
	defer give()
	defer x.cover(ctx, "export-tenant", &err)

	enc, err := age.Encrypt(w, e.To...)
	if err != nil {
		return Exported{}, fmt.Errorf("encrypt to whoever it is for: %w", err)
	}
	b := &bundle{w: tar.NewWriter(enc)}

	m := ExportManifest{
		Format: ExportFormat,
		Tenant: tenant.String(),
		At:     x.now.UTC(),
		By:     strings.TrimSpace(e.By),
		Why:    strings.TrimSpace(e.Why),
		Files:  []ExportFile{},
	}

	if m.Database, err = x.exportDatabase(ctx, tenant, b); err != nil {
		return Exported{}, err
	}
	if p.Archive != nil {
		if err := x.exportArchive(ctx, tenant, b, &m); err != nil {
			return Exported{}, err
		}
	}
	m.Files = b.files

	out, env, err := p.signExport(m)
	if err != nil {
		return Exported{}, err
	}
	if err := b.add(exportManifest, env); err != nil {
		return Exported{}, err
	}
	if err := b.w.Close(); err != nil {
		return Exported{}, err
	}
	if err := enc.Close(); err != nil {
		return Exported{}, err
	}

	log.From(ctx).InfoContext(ctx, "trail: a tenant's trail, exported", "tenant", tenant, "by", m.By, "why", m.Why,
		"of", out.Digest, "database", m.Database, "archive", m.Archive)
	if p.Archive != nil {
		x.r.add(Receipt{Act: "export-tenant", Where: "archive", Tenant: tenant.String(),
			By: m.By, Why: m.Why, Of: string(out.Digest), Rows: m.Database + m.Archive, Chunks: len(m.Chunks)})
		if err := x.r.write(ctx, x.p.Archive); err != nil {
			return out, err
		}
	}

	return out, nil
}

// bundle is a bundle being written: its tar stream, and what is in it.
type bundle struct {
	w     *tar.Writer
	files []ExportFile
}

// add writes one file whose bytes are at hand.
func (b *bundle) add(name string, v []byte) error {
	return b.copy(name, int64(len(v)), bytes.NewReader(v))
}

// copy writes one file of `size` bytes from `r`, and keeps its digest.
func (b *bundle) copy(name string, size int64, r io.Reader) error {
	if err := b.w.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: time.Unix(0, 0)}); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	h := flob.Hash()
	n, err := io.Copy(b.w, io.TeeReader(r, h))
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if n != size {
		return fmt.Errorf("%s: %d bytes of %d", name, n, size)
	}

	if name != exportManifest {
		b.files = append(b.files, ExportFile{Name: name, Digest: flob.Digest(flob.Canonical.String() + ":" + hex.EncodeToString(h.Sum(nil))), Size: size})
	}

	return nil
}

// exportDatabase writes every row of the database that names the tenant, a
// page a file, and answers how many.
func (x *pass) exportDatabase(ctx context.Context, tenant pdid.Id, b *bundle) (int, error) {
	n, page := 0, 0
	for _, whose := range []Whose{Alone, Together} {
		at := Cursor{}
		for {
			vs, err := x.s.Older(ctx, Scope{Whose: whose, Tenant: tenant}, forever, at, Batch)
			if err != nil {
				return n, fmt.Errorf("the tenant's rows in the database: %w", err)
			}
			if len(vs) == 0 {
				break
			}

			buf := &bytes.Buffer{}
			for _, v := range vs {
				buf.Write(v.Doc)
				buf.WriteByte('\n')
				at = Cursor{Created: v.Created, Key: v.Key}
			}
			page++
			if err := b.add(fmt.Sprintf("%s/%06d.jsonl", exportDatabase, page), buf.Bytes()); err != nil {
				return n, err
			}
			n += len(vs)

			if len(vs) < Batch {
				break
			}
		}
	}

	return n, nil
}

// exportArchive writes the chunks of the tenant's own namespace as they are
// kept, and each chunk's rows of the tenant as they read; and names the
// checkpoint they are held to, and the tenant's retirement.
func (x *pass) exportArchive(ctx context.Context, tenant pdid.Id, b *bundle, m *ExportManifest) error {
	p := x.p
	if x.m == nil {
		return nil
	}
	if err := x.trust(ctx); err != nil {
		return err
	}

	// The newest checkpoint the deployment vouches for, and what it lists.
	covered := map[blobKey]bool{}
	ch, err := p.readChain(ctx)
	if err != nil {
		return err
	}
	if k := ch.newest(); k > 0 {
		vs := ch.numbered(k)
		v, err := ch.whole(ctx, vs[0])
		if err != nil {
			return fmt.Errorf("checkpoint %d: %w", k, err)
		}
		for _, b := range v.c.Blobs {
			covered[blobKey{b.Namespace, b.Digest}] = true
		}

		c := &ExportCheckpoint{K: v.c.K, Digest: v.d, At: v.c.At}
		for _, name := range slices.Sorted(maps.Keys(ch.receipts[v.d])) {
			c.Witnessed = append(c.Witnessed, ExportWitnessed{Witness: name, Receipt: ch.receipts[v.d][name]})
		}
		m.Checkpoint = c
	}

	retired, err := x.retired(ctx)
	if err != nil {
		return err
	}
	if at, ok := retired[tenant]; ok {
		m.Retired = &Retirement{At: at, Until: at.Add(p.longestFloor()).UTC()}
	}

	// What the manifest accounts for, as a read of the tenant takes it.
	cs, _, err := x.chunks(ctx, []string{tenant.String(), SharedNamespace, LegacyNamespace}, true)
	if err != nil {
		return err
	}
	slices.SortFunc(cs, chunkOrder)

	open := newOpener(p.Keys)
	seen := map[string]bool{}
	for _, c := range cs {
		keep, ok := tenantsOf(c, tenant)
		if !ok {
			continue
		}

		whole := c.Namespace == tenant.String()
		if whole {
			if err := x.exportChunk(ctx, c, b); err != nil {
				return err
			}
		}

		buf := &bytes.Buffer{}
		n := 0
		err := readChunk(ctx, x.raw(), c, seen, keep, true, func(h head, doc []byte) error {
			doc, err := open.open(ctx, doc, h)
			if err != nil {
				return err
			}

			buf.Write(doc)
			buf.WriteByte('\n')
			n++

			return nil
		})
		if err != nil {
			return err
		}
		if n == 0 && !whole {
			continue
		}
		if n > 0 {
			if err := b.add(path.Join(exportRows, c.Namespace, c.Digest.Encoded()+".jsonl"), buf.Bytes()); err != nil {
				return err
			}
		}

		m.Archive += n
		m.Chunks = append(m.Chunks, ExportChunk{Namespace: c.Namespace, Digest: c.Digest, Rows: n, Whole: whole, Covered: covered[blobKey{c.Namespace, c.Digest}]})
	}

	return nil
}

// exportChunk writes one chunk's bytes as the archive keeps them, and only
// while they are its digest.
func (x *pass) exportChunk(ctx context.Context, c Chunk, b *bundle) error {
	r, info, err := x.raw().Use(c.Namespace).Open(ctx, c.Digest)
	if err != nil {
		return fmt.Errorf("%s: %w", c, err)
	}
	defer r.Close()

	size, err := info.Size(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", c, err)
	}

	v := c.Digest.Verifier()
	if err := b.copy(path.Join(exportArchive, c.Namespace, c.Digest.Encoded()), size, io.TeeReader(r, v)); err != nil {
		return err
	}
	if !v.Verified() {
		return fmt.Errorf("%s: %w", c, ErrAltered)
	}

	return nil
}

// signExport signs a manifest, and answers it with its digest as signed, and
// the envelope a bundle carries it in.
func (p Policy) signExport(m ExportManifest) (Exported, []byte, error) {
	doc, err := json.Marshal(m)
	if err != nil {
		return Exported{}, nil, err
	}

	der, sig, err := sign(p.Signer, doc)
	if err != nil {
		return Exported{}, nil, err
	}
	env, err := json.Marshal(exportEnvelope{Manifest: doc, Key: der, Sig: sig})
	if err != nil {
		return Exported{}, nil, err
	}

	return Exported{ExportManifest: m, Digest: flob.DigestFromBytes(doc), Key: der}, env, nil
}

// OpenTenantExport reads a bundle [Policy.ExportTenant] wrote, with an
// identity it was encrypted to, and answers its manifest once every file is
// what the manifest says -- each there, each its digest and size, nothing
// else -- and the manifest is signed by one of `trusted`, the deployment's
// keys ([Policy.Trust], `audit.checkpoints.trust`). Anything else is
// [ErrExportAltered], or an identity it was not encrypted to.
//
// `fn`, when not nil, is handed each file as it is read, by its name in the
// bundle. The manifest comes last, so a file is handed over before it is
// known to be what the manifest says: whoever keeps what `fn` is handed keeps
// it until this answers.
//
// # Holding it to the chain and its witnesses
//
// The signature says the deployment's key wrote the manifest, and nothing
// more: whoever holds the key could write another. What says the archive held
// these chunks before anybody asked is the checkpoint the manifest names. To
// hold a bundle to it later -- from the checkpoint store's write-once copy, the
// deployment gone or not:
//
//  1. Read the checkpoint the manifest names, by its digest, from the
//     checkpoint store, and check its signature against the deployment's key
//     and its number against the manifest's.
//  2. Check that each chunk the manifest calls `whole` is among the blobs it
//     lists, under its namespace and digest. This opens nothing: a chunk is
//     named by the digest of its bytes, which are in the bundle as the
//     archive kept them.
//  3. Hold the checkpoint to each witness's receipt, as the witness says to:
//     a log's inclusion proof against the log, a store's copy against the
//     store. A witness heard it when it was taken, from somewhere the
//     deployment does not reach.
//
// What the rows of a chunk said is under `rows/`, opened; whoever can open the
// chunk under `archive/` can hold the one to the other. The rows that were
// still in the database (`database/`) are newer than any checkpoint, and the
// manifest's signature is all that vouches for them.
func OpenTenantExport(r io.Reader, ids []age.Identity, trusted []crypto.PublicKey, fn func(name string, r io.Reader) error) (Exported, error) {
	if len(ids) == 0 {
		return Exported{}, errors.New("no identity to open the bundle with")
	}

	dec, err := age.Decrypt(r, ids...)
	if err != nil {
		return Exported{}, fmt.Errorf("open the bundle: %w", err)
	}

	got := map[string]ExportFile{}
	var env []byte
	tr := tar.NewReader(dec)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Exported{}, fmt.Errorf("%w: %v", ErrExportAltered, err)
		}
		if h.Typeflag != tar.TypeReg {
			return Exported{}, fmt.Errorf("%w: %s is not a file", ErrExportAltered, h.Name)
		}
		if _, ok := got[h.Name]; ok || env != nil {
			return Exported{}, fmt.Errorf("%w: %s after what it was", ErrExportAltered, h.Name)
		}

		if h.Name == exportManifest {
			if env, err = io.ReadAll(io.LimitReader(tr, 64<<20)); err != nil {
				return Exported{}, fmt.Errorf("%w: %v", ErrExportAltered, err)
			}
			continue
		}

		d := flob.Hash()
		src := io.TeeReader(tr, d)
		if fn != nil {
			err = fn(h.Name, src)
		}
		if err == nil {
			_, err = io.Copy(io.Discard, src)
		}
		if err != nil {
			return Exported{}, fmt.Errorf("%s: %w", h.Name, err)
		}
		got[h.Name] = ExportFile{Name: h.Name, Digest: flob.Digest(flob.Canonical.String() + ":" + hex.EncodeToString(d.Sum(nil))), Size: h.Size}
	}
	if env == nil {
		return Exported{}, fmt.Errorf("%w: no manifest -- half a bundle, written by an export that did not finish?", ErrExportAltered)
	}

	var v exportEnvelope
	if err := json.Unmarshal(env, &v); err != nil {
		return Exported{}, fmt.Errorf("%w: the manifest: %v", ErrExportAltered, err)
	}
	if !trusts(trusted, v.Key) {
		return Exported{}, fmt.Errorf("%w: the manifest is signed by a key not trusted", ErrExportAltered)
	}
	if !verified(v.Key, v.Manifest, v.Sig) {
		return Exported{}, fmt.Errorf("%w: the manifest's signature is not the key's", ErrExportAltered)
	}

	var m ExportManifest
	if err := json.Unmarshal(v.Manifest, &m); err != nil {
		return Exported{}, fmt.Errorf("%w: the manifest: %v", ErrExportAltered, err)
	}
	if m.Format != ExportFormat {
		return Exported{}, fmt.Errorf("%w: a bundle of %q, and this reads %q", ErrExportAltered, m.Format, ExportFormat)
	}

	for _, f := range m.Files {
		g, ok := got[f.Name]
		switch {
		case !ok:
			return Exported{}, fmt.Errorf("%w: %s is not in it", ErrExportAltered, f.Name)
		case g != f:
			return Exported{}, fmt.Errorf("%w: %s is not its digest", ErrExportAltered, f.Name)
		}
		delete(got, f.Name)
	}
	for name := range got {
		return Exported{}, fmt.Errorf("%w: %s is not in the manifest", ErrExportAltered, name)
	}

	return Exported{ExportManifest: m, Digest: flob.DigestFromBytes(v.Manifest), Key: v.Key}, nil
}
