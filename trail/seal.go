package trail

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/lesomnus/flob"
)

// Keys is what seals what the archive's rows say: a key for each tenant, and
// a master each tenant's key is sealed to in turn.
//
// # What is sealed, and what is not
//
// The **contents** of a row, `value` and `patch`, and only those, row by row.
// Everything else -- who acted, what they did, which object, when, whose it
// is -- stays in the clear, because that is what every act on the archive
// decides by: a pass folding a month, a subject's erasure, a tenant leaving, a
// hold that keeps part of a file. None of them needs a key, and only reading
// what a row said does. The cost is the archive's size: the contents no longer
// compress together, and a row carries a key's worth of header.
//
// A row is sealed to the key of **every tenant it names** -- the ones that may
// read it, which is the wall's rule on the trail -- and [DeploymentNamespace]'s
// for a row that names nobody. A tenant that leaves takes its key with it, and
// with the key what every copy of its rows said, wherever the copy is: a row
// that was its alone cannot be read again. A row it shared stays readable to
// the other side, whose evidence it is as much as its own -- which is also
// what keeps a row that another tenant's hold is on readable after this one
// has gone.
//
// # Who holds what
//
// The process that writes the archive holds **public keys only**: the
// tenants', which are in the key store's labels, and the master's, which is
// configuration. A tenant's key is made by the process that first has a row
// of theirs to seal, which holds its private half for as long as it takes to
// seal it to the master. The master's private half, [Keys.Identities], is
// given only to a process that reads what rows said -- an export, an operator
// at a shell -- and never to the one that serves.
//
// # Where the keys are
//
// In a store of their own, a namespace per tenant and [DeploymentNamespace]
// for the rows filed under nobody. Not the archive's: the point of a key that
// can be destroyed is that it is destroyed, and the archive's backups are
// kept for as long as the archive is. The key store's backups are what decide
// how long after a tenant leaves its contents can still be read, so they are
// to be short.
//
// Whoever can write the key store can put a key there that rows will be sealed
// to, as whoever can write the archive can put rows there: it is trusted as the
// archive is, and the manifest does not cover it.
//
// # What destroys a key
//
// A tenant leaving, [Policy.PurgeTenant], and nothing else: it destroys the
// tenant's key **first**, then its rows. Grace keeps the key, because nothing
// before the purge touches it, and a hold refuses it, because a held tenant is
// not purged. A window that destroys every chunk of a tenant leaves its key,
// since the tenant is still there and writing.
type Keys struct {
	// Store is the key store: a namespace per tenant, and never the archive.
	Store flob.Stores

	// Master is who a tenant's key is sealed to, as `age-keygen` writes a
	// public key -- one recipient, or several to have more than one way back.
	// A post-quantum hybrid one (`age1pq1…`) is sealed to once per tenant and
	// is not what the rows carry.
	Master []age.Recipient

	// Identities is the master's private half, which opens a tenant's key: for
	// a process that reads what rows said, and for no other. Empty is a
	// process that writes the archive and cannot read what it wrote.
	Identities []age.Identity
}

var (
	// ErrSealed is a row whose contents are sealed and nothing here to open
	// them: no master key given, or a read of the archive that takes none.
	ErrSealed = errors.New("what this row says is sealed (audit.keys), and reading it takes the master key")

	// ErrKeyGone is a row sealed to a key the key store does not have: one
	// destroyed when its tenant left, or a row that is not this deployment's.
	ErrKeyGone = errors.New("the key this row was sealed to is not in the key store: destroyed with its tenant, or never there")
)

// The labels a key carries, beside [labelFormat] and [labelAt].
const (
	// formatKey is a tenant's key: its age identity, sealed to the master.
	formatKey = "age-key"

	// labelRecipient is the key's public half, which is all sealing needs.
	labelRecipient = "Recipient"
)

// sealedField is where a sealed row keeps what it said, in place of `value`
// and `patch`: their protojson members, as one JSON object, sealed with age
// and written as base64.
//
// No field of `Audit` has this name, and the test app's schema is checked for
// it, since a row is read back by the name.
const sealedField = "sealed"

// keyspaces is the namespaces of the key store a row's contents are sealed
// to: every tenant it names, or [DeploymentNamespace] for a row that names
// nobody.
func keyspaces(h head) []string {
	ids := h.tenants()
	if len(ids) == 0 {
		return []string{DeploymentNamespace}
	}

	out := make([]string, len(ids))
	for i, v := range ids {
		out[i] = v.String()
	}

	return out
}

// key is one key the key store holds.
type key struct {
	digest    flob.Digest
	recipient string
	at        time.Time
}

// keysIn is the keys of one namespace, the oldest first. Two replicas that
// both found none made one each, and both seal to the oldest from then on.
func (k *Keys) keysIn(ctx context.Context, ns string) ([]key, error) {
	w, ok := flob.AsWalker(k.Store.Use(ns))
	if !ok {
		return nil, errors.New("audit.keys: the key store cannot list what it holds")
	}

	out := []key{}
	for v, err := range w.Walk(ctx) {
		if err != nil {
			return out, err
		}

		l, err := v.Labels(ctx)
		if err != nil {
			if errors.Is(err, flob.ErrNotExist) {
				continue
			}

			return out, err
		}
		if l.Get(labelFormat) != formatKey {
			continue
		}

		at, _ := time.Parse(time.RFC3339Nano, l.Get(labelAt))
		out = append(out, key{digest: v.Digest(), recipient: l.Get(labelRecipient), at: at})
	}
	slices.SortFunc(out, func(a, b key) int { return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.digest, b.digest)) })

	return out, nil
}

// make is a new key for a namespace: an X25519 identity, sealed to the master
// and kept, of which the process keeps only the public half.
func (k *Keys) make(ctx context.Context, ns string) error {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}

	buf := &bytes.Buffer{}
	w, err := age.Encrypt(buf, k.Master...)
	if err != nil {
		return fmt.Errorf("audit.keys.master: %w", err)
	}
	if _, err := io.WriteString(w, id.String()+"\n"); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	l := flob.Labels{}
	l.Set(labelFormat, formatKey)
	l.Set(labelRecipient, id.Recipient().String())
	l.Set(labelAt, time.Now().UTC().Format(time.RFC3339Nano))

	if _, err := k.Store.Use(ns).Add(ctx, flob.Meta{Labels: l}, buf); err != nil && !errors.Is(err, flob.ErrAlreadyExists) {
		return fmt.Errorf("audit.keys: %s: %w", ns, err)
	}

	return nil
}

// destroy erases every key of a namespace, and answers how many. `dry` counts
// them and erases nothing.
func (k *Keys) destroy(ctx context.Context, ns string, dry bool) (int, error) {
	ks, err := k.keysIn(ctx, ns)
	if err != nil {
		return 0, err
	}
	if dry {
		return len(ks), nil
	}

	n := 0
	for _, v := range ks {
		if err := k.Store.Use(ns).Erase(ctx, v.digest); err != nil && !errors.Is(err, flob.ErrNotExist) {
			return n, fmt.Errorf("audit.keys: %s: %w", ns, err)
		}

		n++
	}

	return n, nil
}

// identities is a namespace's keys, opened with the master.
func (k *Keys) identities(ctx context.Context, ns string) ([]age.Identity, error) {
	if len(k.Identities) == 0 {
		return nil, ErrSealed
	}

	ks, err := k.keysIn(ctx, ns)
	if err != nil {
		return nil, err
	}

	out := make([]age.Identity, 0, len(ks))
	for _, v := range ks {
		id, err := k.open(ctx, ns, v.digest)
		if err != nil {
			return nil, fmt.Errorf("audit.keys: %s: %w", ns, err)
		}

		out = append(out, id)
	}

	return out, nil
}

func (k *Keys) open(ctx context.Context, ns string, d flob.Digest) (age.Identity, error) {
	r, _, err := k.Store.Use(ns).Open(ctx, d)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	plain, err := age.Decrypt(r, k.Identities...)
	if err != nil {
		var none *age.NoIdentityMatchError
		if errors.As(err, &none) {
			return nil, errors.New("the master key given does not open it")
		}

		return nil, err
	}

	ids, err := age.ParseIdentities(io.LimitReader(plain, 4096))
	if err != nil || len(ids) != 1 {
		return nil, errors.New("it is not a key")
	}

	return ids[0], nil
}

// Open is one row as it was before it was sealed, which is what a file taken
// out of the archive needs: the reads of the archive open what they read, and a
// file somebody saved is opened a row at a time. A row that is not sealed is
// answered as it is.
func (k *Keys) Open(ctx context.Context, doc []byte) ([]byte, error) {
	h, err := headOf(doc)
	if err != nil {
		return nil, err
	}

	return (&opener{k: k}).open(ctx, doc, h)
}

// sealer seals rows on their way into the archive, for one act: the public
// half of each tenant's key, asked for once.
type sealer struct {
	k *Keys

	mu    sync.Mutex
	known map[string]age.Recipient
}

func newSealer(k *Keys) *sealer {
	if k == nil {
		return nil
	}

	return &sealer{k: k, known: map[string]age.Recipient{}}
}

// recipient is the key a namespace's rows are sealed to, made when there is
// none.
func (s *sealer) recipient(ctx context.Context, ns string) (age.Recipient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.known[ns]; ok {
		return r, nil
	}

	ks, err := s.k.keysIn(ctx, ns)
	if err != nil {
		return nil, err
	}
	if len(ks) == 0 {
		if err := s.k.make(ctx, ns); err != nil {
			return nil, err
		}

		// Read again rather than kept: another replica may have made one
		// first, and the oldest is the one everybody seals to.
		if ks, err = s.k.keysIn(ctx, ns); err != nil {
			return nil, err
		}
		if len(ks) == 0 {
			return nil, fmt.Errorf("audit.keys: %s: a key was made and the key store does not have it", ns)
		}
	}

	r, err := age.ParseX25519Recipient(ks[0].recipient)
	if err != nil {
		return nil, fmt.Errorf("audit.keys: %s: %w", ns, err)
	}

	s.known[ns] = r

	return r, nil
}

// seal is a row with what it says sealed, and as it was when it says nothing:
// an event with no contents has nothing to seal. A row sealed already is
// answered as it is.
func (s *sealer) seal(ctx context.Context, doc []byte) ([]byte, error) {
	var v map[string]json.RawMessage
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, err
	}
	if _, ok := v[sealedField]; ok {
		return doc, nil
	}

	contents := map[string]json.RawMessage{}
	for _, f := range []string{"value", "patch"} {
		if c, ok := v[f]; ok {
			contents[f] = c
			delete(v, f)
		}
	}
	if len(contents) == 0 {
		return doc, nil
	}

	h, err := headOf(doc)
	if err != nil {
		return nil, err
	}

	rs := []age.Recipient{}
	for _, ns := range keyspaces(h) {
		r, err := s.recipient(ctx, ns)
		if err != nil {
			return nil, err
		}

		rs = append(rs, r)
	}

	plain, err := json.Marshal(contents)
	if err != nil {
		return nil, err
	}

	buf := &bytes.Buffer{}
	w, err := age.Encrypt(buf, rs...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	sealed, err := json.Marshal(base64.StdEncoding.EncodeToString(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	v[sealedField] = sealed

	return json.Marshal(v)
}

// opener opens sealed rows for one read: each namespace's keys, opened once.
type opener struct {
	k   *Keys
	ids map[string][]age.Identity
}

func newOpener(k *Keys) *opener {
	return &opener{k: k, ids: map[string][]age.Identity{}}
}

// open is a row as it was before it was sealed. A row that is not sealed is
// answered as it is; a sealed one with nothing to open it is [ErrSealed], and
// one whose every key is gone is [ErrKeyGone]. Any of the keys it was sealed
// to opens it.
func (o *opener) open(ctx context.Context, doc []byte, h head) ([]byte, error) {
	if h.Sealed == "" {
		return doc, nil
	}
	if o == nil || o.k == nil {
		return nil, fmt.Errorf("%w; read it through Policy.Read with Keys.Identities, or open it with Keys.Open", ErrSealed)
	}

	nss := keyspaces(h)
	ids := []age.Identity{}
	for _, ns := range nss {
		vs, ok := o.ids[ns]
		if !ok {
			var err error
			if vs, err = o.k.identities(ctx, ns); err != nil {
				return nil, err
			}
			if o.ids != nil {
				o.ids[ns] = vs
			}
		}

		ids = append(ids, vs...)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s: %w", strings.Join(nss, ", "), ErrKeyGone)
	}

	ct, err := base64.StdEncoding.DecodeString(h.Sealed)
	if err != nil {
		return nil, fmt.Errorf("a sealed row: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(ct), ids...)
	if err != nil {
		var none *age.NoIdentityMatchError
		if errors.As(err, &none) {
			return nil, fmt.Errorf("%s: %w", strings.Join(nss, ", "), ErrKeyGone)
		}

		return nil, fmt.Errorf("a sealed row: %w", err)
	}
	plain, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("a sealed row: %w", err)
	}

	var contents map[string]json.RawMessage
	if err := json.Unmarshal(plain, &contents); err != nil {
		return nil, fmt.Errorf("a sealed row: %w", err)
	}

	var v map[string]json.RawMessage
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, err
	}
	delete(v, sealedField)
	for f, c := range contents {
		v[f] = c
	}

	return json.Marshal(v)
}

// valid is what is wrong with a key configuration, at the field an operator
// writes.
func (k *Keys) valid(archive bool) error {
	switch {
	case !archive:
		return errors.New("audit.keys seals what the archive's rows say, and audit.archive names no archive")
	case k.Store == nil:
		return errors.New("audit.keys: no key store; name a directory of its own in audit.keys.dir")
	case len(k.Master) == 0:
		return errors.New("audit.keys.master: no key to seal the tenants' keys to")
	}
	if _, ok := flob.AsWalker(k.Store.Use(DeploymentNamespace)); !ok {
		return errors.New("audit.keys: the key store cannot list what it holds, so no key could be found again")
	}

	return nil
}

// String is how the keys are, for the line a process logs as it comes up.
func (k *Keys) String() string {
	if k == nil {
		return ""
	}

	v := fmt.Sprintf("what rows say is sealed, a key per tenant, sealed to %d master key(s)", len(k.Master))
	if len(k.Identities) > 0 {
		v += "; this process holds the master's private half"
	}

	return v
}
