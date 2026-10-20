package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdid"
)

// Digest is what a row of the trail keeps of the row its write left, beside
// what the write said: an HMAC of it under the deployment's key, so that the
// live row can be checked against the trail later (payday#102, `Audit.digest`).
// A row somebody changed in the database, past the servers, no longer matches
// the digest of the last write the trail recorded for it.
//
// # Why keyed
//
// The trail's rows in the database are not signed until a pass archives them,
// so a digest anybody can compute is one whoever can write the database can
// compute again for a row they changed, and write over the old one. The key is
// a file the process is given and not a row anything reads, so writing the
// database is not enough to make a row and its digest agree.
//
// # What it covers
//
// The row as the `value` content level would hold it: a field the schema
// declared `secret` or `personal` is cleared before it is digested, since a
// keyed hash of a person's address would outlive their erasure for as long as
// the key does. And it covers the fields the build that wrote it knew -- they
// are recorded beside it (`Audit.digest_fields`) and bound into it -- so a
// column a later migration adds is outside every digest made before it, and a
// column a migration rewrote is a difference, which is the point.
type Digest struct {
	key      []byte
	print    [8]byte
	previous map[[8]byte][]byte
}

// DigestVersion is the first byte of every digest [Digest.Of] makes.
const DigestVersion byte = 1

// digestLen is a digest's length: the version, the key's fingerprint and the
// HMAC.
const digestLen = 1 + 8 + sha256.Size

// minDigestKey is the least a key may be: the HMAC's own block of entropy.
const minDigestKey = 32

// NewDigest is a digest under `key`, which checks digests made under any of
// `previous` too -- the keys it has replaced, kept for checking alone.
func NewDigest(key []byte, previous ...[]byte) (*Digest, error) {
	if len(key) < minDigestKey {
		return nil, fmt.Errorf("a digest key is at least %d bytes, and this is %d", minDigestKey, len(key))
	}

	d := &Digest{key: slices.Clone(key), print: fingerprint(key), previous: map[[8]byte][]byte{}}
	for i, v := range previous {
		if len(v) < minDigestKey {
			return nil, fmt.Errorf("previous[%d]: a digest key is at least %d bytes, and this is %d", i, minDigestKey, len(v))
		}

		d.previous[fingerprint(v)] = slices.Clone(v)
	}

	return d, nil
}

// fingerprint is what names a key in the digests made under it, so that one
// made before a rotation is checked with the key it was made with.
func fingerprint(key []byte) [8]byte {
	h := sha256.New()
	h.Write([]byte("payday.trail.digest.key\x00"))
	h.Write(key)

	var v [8]byte
	copy(v[:], h.Sum(nil))
	return v
}

// Of is the digest of `row`, the message of the thing `object` names, over the
// fields `covered` lists. `row` is the caller's, and is not changed.
func (d *Digest) Of(object pdid.Id, row proto.Message, covered []uint32) ([]byte, error) {
	mac, err := d.mac(d.key, object, row, covered)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, digestLen)
	out = append(out, DigestVersion)
	out = append(out, d.print[:]...)
	return append(out, mac...), nil
}

// Verdict is what [Digest.Check] found.
type Verdict int

const (
	// Matches is a row that is the one its digest was made of.
	Matches Verdict = iota

	// Differs is a row that is not: changed since, by something the trail
	// did not record.
	Differs

	// KeyNotHeld is a digest made under a key this was not given, so it says
	// nothing either way.
	KeyNotHeld
)

func (v Verdict) String() string {
	switch v {
	case Matches:
		return "matches"
	case Differs:
		return "differs"
	case KeyNotHeld:
		return "key not held"
	}

	return fmt.Sprintf("Verdict(%d)", int(v))
}

// ErrDigest is a digest that is not one [Digest.Of] makes: of another version,
// or of the wrong length.
var ErrDigest = errors.New("not a digest this version of payday makes")

// Check says whether `sum` is the digest of `row` -- the live row of the
// thing `object` names -- over the fields `covered` lists, by the key `sum`
// names.
func (d *Digest) Check(sum []byte, object pdid.Id, row proto.Message, covered []uint32) (Verdict, error) {
	if len(sum) != digestLen || sum[0] != DigestVersion {
		return Differs, ErrDigest
	}

	var print [8]byte
	copy(print[:], sum[1:9])

	key := d.key
	if print != d.print {
		v, ok := d.previous[print]
		if !ok {
			return KeyNotHeld, nil
		}
		key = v
	}

	mac, err := d.mac(key, object, row, covered)
	if err != nil {
		return Differs, err
	}
	if !hmac.Equal(mac, sum[9:]) {
		return Differs, nil
	}

	return Matches, nil
}

// mac is the HMAC itself: of what it is about and what it covers, and then of
// the row restricted to that, marshalled deterministically. The list it covers
// is bound into it, so that taking a field out of the list does not take a
// change to that field out of the comparison.
func (d *Digest) mac(key []byte, object pdid.Id, row proto.Message, covered []uint32) ([]byte, error) {
	m := proto.Clone(row)
	restrict(m.ProtoReflect(), covered)

	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, err
	}

	h := hmac.New(sha256.New, key)
	h.Write([]byte("payday.trail.digest\x00"))
	h.Write(object.Bytes())

	n := make([]byte, 4)
	binary.BigEndian.PutUint32(n, uint32(len(covered)))
	h.Write(n)
	for _, v := range covered {
		binary.BigEndian.PutUint32(n, v)
		h.Write(n)
	}

	h.Write(b)
	return h.Sum(nil), nil
}

// restrict clears every field of `m` that `covered` does not list, and what it
// did not know how to read.
func restrict(m protoreflect.Message, covered []uint32) {
	m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if !slices.Contains(covered, uint32(fd.Number())) {
			m.Clear(fd)
		}
		return true
	})
	m.SetUnknown(nil)
}

// Covered is the numbers of the fields of `md`, in order, less the ones in
// `cleared`: what a digest made now covers.
func Covered(md protoreflect.MessageDescriptor, cleared []protoreflect.FieldDescriptor) []uint32 {
	fs := md.Fields()
	out := make([]uint32, 0, fs.Len())
	for i := range fs.Len() {
		fd := fs.Get(i)
		if slices.ContainsFunc(cleared, func(v protoreflect.FieldDescriptor) bool { return v.Number() == fd.Number() }) {
			continue
		}
		out = append(out, uint32(fd.Number()))
	}
	slices.Sort(out)

	return out
}
