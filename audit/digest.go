package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

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
// The row less what the schema declared `personal`, which is cleared before it
// is digested: a keyed hash of a person's address would outlive their erasure
// for as long as the trail's line does, and lines outlive their rows. A field
// declared `secret` is covered (payday#115): a keyed MAC of a verifier or a
// token's hash is neither a copy of it nor a way to test a guess at it without
// the key, and a credential changed past the servers is the change most worth
// finding. And it covers the fields the build that wrote it knew -- they are
// recorded beside it (`Audit.digest_fields`) and bound into it -- so a column a
// later migration adds is outside every digest made before it, and a column a
// migration rewrote is a difference, which is the point. A digest a build
// before this made covered no secret, and is checked over what it covered.
//
// # And what a seal covers
//
// The personal fields are covered by a seal of the row instead ([Digest.Seal]):
// a MAC of every field of it, kept beside the row in the database alone, for
// as long as the row is, and blanked when the person is forgotten. It lives
// with what it vouches for rather than with the trail's line, which is copied
// to the archive and kept for years after the row has gone. Whether a write
// sealed the row is said by its digest ([Sealing]), so that a seal gone from
// a row is a finding and not a row from before seals.
//
// # Bound to the line that keeps it (payday#115)
//
// A digest says what a row was; the row of the trail that keeps it -- its
// line, [Line] -- says which line it is, when it was written and what was
// done. Version 1 bound nothing of the line, so a digest could be copied from
// an older line onto the newest, and a check compared the row with whatever
// the copy said: a row put back to what it once was, and the trail made to
// agree, by anybody who can write the database. So a digest is two MACs now.
// One is of the row, as before. The other is of the line, over the first: its
// key, its moment, its action, the thing it is about. A digest copied onto
// another line is then not that line's, which is a finding of its own.
//
// Two MACs rather than one of everything, because of what a pass does at the
// end of a window: a chunk of the archive is destroyed while the row its
// newest line vouched for is still there, and what that line said of the row
// is carried onto a line of the pass's own ([Digest.Carry]). The row's MAC is
// carried as it was recorded and the line's is made for the new line. Made
// again of the row as it is now, it would be a pass accepting whatever the row
// had been made into.
//
// # A write that left no row
//
// An erase that took its row keeps a digest of its line alone ([Digest.Gone]):
// that the row went is what it says. A check that finds the row there anyway
// finds that, and so does one that finds no row where the trail says one was
// left.
//
// # The keys
//
// Each MAC names the key it was made under by a [Fingerprint]. One under a key
// the check holds is checked; one under a key the deployment said it let go
// ([Digest.Retire]) is counted; one under any other is a finding. A check
// handed a key other than the one the trail was written under finds every
// row, and not none of them.
type Digest struct {
	key      []byte
	print    Fingerprint
	previous map[Fingerprint][]byte
	retired  map[Fingerprint]bool
	since    time.Time
}

// DigestVersion is the first byte of every digest [Digest.Of] makes.
//
// 3 is a digest bound to its line that says whether the write it records
// sealed the row (payday#115): a byte of flags after the version, bound into
// the line's MAC with it, so that neither can be changed by anybody without
// the key. 2 is the same two MACs with no flags, and 1 the row's MAC alone;
// both are still checked and no longer made. A line that keeps one of them is
// the line it was written as, or the database's word for it, which is all
// they ever had -- and it says its write sealed nothing.
const DigestVersion byte = 3

const (
	// part is a fingerprint and a MAC, which is what each half of a digest
	// is.
	part = 8 + sha256.Size

	// onlyRow is a version-1 digest: the version and the row's part.
	onlyRow = 1 + part

	// onlyLine is a version-2 digest of a write that left no row: the
	// version and the line's part.
	onlyLine = 1 + part

	// lineAndRow is a version-2 digest of the row a write left: the line's
	// part, then the row's.
	lineAndRow = 1 + 2*part

	// head is what a version-3 digest begins with: the version and its
	// flags.
	head = 2

	// onlyLine3 and lineAndRow3 are [onlyLine] and [lineAndRow] of version
	// 3, after its flags.
	onlyLine3   = head + part
	lineAndRow3 = head + 2*part
)

// sealed is the flag of a version-3 digest that says the write it records
// sealed the row it left ([Digest.OfSealed]).
const sealed byte = 1 << 0

// minDigestKey is the least a key may be: the HMAC's own block of entropy.
const minDigestKey = 32

// Fingerprint names a key in the digests made under it: eight bytes of a
// SHA-256 of the key, under a label of its own. It says which key, and nothing
// of what the key is.
type Fingerprint [8]byte

// FingerprintOf is the fingerprint of `key`, which is what an operator writes
// into `audit.digest.retired` before letting the key go.
func FingerprintOf(key []byte) Fingerprint {
	h := sha256.New()
	h.Write([]byte("payday.trail.digest.key\x00"))
	h.Write(key)

	var v Fingerprint
	copy(v[:], h.Sum(nil))
	return v
}

// String is the fingerprint in hexadecimal, as configuration writes one.
func (f Fingerprint) String() string { return hex.EncodeToString(f[:]) }

// ParseFingerprint reads a fingerprint as [Fingerprint.String] writes one.
func ParseFingerprint(v string) (Fingerprint, error) {
	b, err := hex.DecodeString(v)
	if err != nil || len(b) != len(Fingerprint{}) {
		return Fingerprint{}, fmt.Errorf("%q is not a key's fingerprint: sixteen hexadecimal digits", v)
	}

	return Fingerprint(b), nil
}

// Line is the row of the trail a digest is bound to, as far as the digest is
// concerned: the line's own key, when it was written, and what it says was
// done.
//
// Its moment is bound to the microsecond, which is what every database a trail
// is kept in holds, so that the moment a digest was made over is the one read
// back. A writer stores it truncated to that, rather than leaving a database
// to round.
type Line struct {
	Id      [16]byte
	Created time.Time
	Action  string
}

// NewDigest is a digest under `key`, which checks digests made under any of
// `previous` too -- the keys it has replaced, kept for checking alone.
func NewDigest(key []byte, previous ...[]byte) (*Digest, error) {
	if len(key) < minDigestKey {
		return nil, fmt.Errorf("a digest key is at least %d bytes, and this is %d", minDigestKey, len(key))
	}

	d := &Digest{key: slices.Clone(key), print: FingerprintOf(key), previous: map[Fingerprint][]byte{}}
	for i, v := range previous {
		if len(v) < minDigestKey {
			return nil, fmt.Errorf("previous[%d]: a digest key is at least %d bytes, and this is %d", i, minDigestKey, len(v))
		}

		d.previous[FingerprintOf(v)] = slices.Clone(v)
	}

	return d, nil
}

// Fingerprint is the fingerprint of the key this makes digests under.
func (d *Digest) Fingerprint() Fingerprint { return d.print }

// Since is this digest, told that its deployment began keeping digests at
// `at`: a line written before then that keeps none was written before there
// was anything to keep, and a check counts it rather than finding it. Zero is
// from the beginning, which is where a deployment that had a key from its
// first write is, and what nothing said is.
//
// # Why it is told, and not read off the trail
//
// Whatever the database says of when digests began, whoever can write the
// database can say. Take the digests off a kind's first lines, or put a
// marker row back by a month, and every row they were about reads as from
// before -- unchecked, by a check that says it checked. Configuration is
// beside the key, out of that reach, and a moment an operator wrote down is
// one they can be asked about.
//
// What it cannot do is tell a line from before apart from one somebody made
// look like it: a row whose whole trail was put back before `at`, with no
// digest, reads as from before. That is the price of saying anything about a
// deployment that had rows before it had a key, and it ends when those rows
// are accepted once and this is taken out.
func (d *Digest) Since(at time.Time) *Digest {
	v := *d
	v.since = at

	return &v
}

// Retire is this digest, told that the keys `prints` name were let go: a
// digest made under one is counted rather than found, since nothing can check
// it and the deployment said why. Any other key it does not hold is a finding.
//
// A key is held or let go, so the fingerprint of one this holds is refused.
func (d *Digest) Retire(prints ...Fingerprint) (*Digest, error) {
	v := *d
	v.retired = maps.Clone(d.retired)
	if v.retired == nil {
		v.retired = map[Fingerprint]bool{}
	}
	for _, p := range prints {
		if _, held := d.previous[p]; held || p == d.print {
			return nil, fmt.Errorf("%s is the fingerprint of a key this holds; a key is held or let go, not both", p)
		}

		v.retired[p] = true
	}

	return &v, nil
}

// Of is the digest the line `line` keeps of the row its write left: `row`, the
// message of the thing `object` names, over the fields `covered` lists. `row`
// is the caller's, and is not changed. It says the write sealed nothing; see
// [Digest.OfSealed] for a row whose kind declares a field personal.
func (d *Digest) Of(object pdid.Id, row proto.Message, covered []uint32, line Line) ([]byte, error) {
	r, err := d.rowPart(object, row, covered)
	if err != nil {
		return nil, err
	}

	return d.bind(object, line, 0, r), nil
}

// OfSealed is [Digest.Of] for a row whose kind declares a field personal: the
// digest, which says the write sealed the row, and the row's seal -- a MAC of
// every field `all` lists, the personal ones among them, bound to the same
// line ([Digest.Seal]). The seal is the row's to keep, beside it, and the
// digest the line's.
func (d *Digest) OfSealed(object pdid.Id, row proto.Message, covered, all []uint32, line Line) ([]byte, []byte, error) {
	r, err := d.rowPart(object, row, covered)
	if err != nil {
		return nil, nil, err
	}
	seal, err := d.Seal(object, row, all, line)
	if err != nil {
		return nil, nil, err
	}

	return d.bind(object, line, sealed, r), seal, nil
}

// rowPart is the row's part of a digest: the key's fingerprint and the row's
// MAC under it.
func (d *Digest) rowPart(object pdid.Id, row proto.Message, covered []uint32) ([]byte, error) {
	mac, err := rowMAC(d.key, object, row, covered)
	if err != nil {
		return nil, err
	}

	r := make([]byte, 0, part)
	r = append(r, d.print[:]...)
	return append(r, mac...), nil
}

// Gone is the digest the line `line` keeps of a write that left no row of
// `object`: an erase that took it.
func (d *Digest) Gone(object pdid.Id, line Line) []byte {
	return d.bind(object, line, 0, nil)
}

// Carry is `sum` -- the digest an older line keeps of `object` -- bound to
// `line` instead, which is what a pass writes for a row whose newest line it
// is about to destroy. What it says of the row is what the older line said:
// the row's MAC is copied as it was recorded, never made again, and so is
// whether the write sealed the row; only the line's MAC is new. One of version
// 1 or 2 is carried as version 3, saying it sealed nothing, which it did not.
func (d *Digest) Carry(sum []byte, object pdid.Id, line Line) ([]byte, error) {
	switch {
	case len(sum) == onlyRow && sum[0] == 1:
		return d.bind(object, line, 0, sum[1:]), nil
	case len(sum) == onlyLine && sum[0] == 2:
		return d.bind(object, line, 0, nil), nil
	case len(sum) == lineAndRow && sum[0] == 2:
		return d.bind(object, line, 0, sum[onlyLine:]), nil
	case len(sum) == onlyLine3 && sum[0] == DigestVersion:
		return d.bind(object, line, sum[1], nil), nil
	case len(sum) == lineAndRow3 && sum[0] == DigestVersion:
		return d.bind(object, line, sum[1], sum[onlyLine3:]), nil
	}

	return nil, ErrDigest
}

// bind is a version-3 digest: its flags, the line's part, made now, and `r`,
// the row's, or nothing for a write that left no row.
func (d *Digest) bind(object pdid.Id, line Line, flags byte, r []byte) []byte {
	out := make([]byte, 0, lineAndRow3)
	out = append(out, DigestVersion, flags)
	out = append(out, d.print[:]...)
	out = append(out, lineMAC3(d.key, object, line, flags, r)...)

	return append(out, r...)
}

// Sealing is whether `sum`, a line's digest, says the write it records sealed
// the row: one of version 3 whose flags say so ([Digest.OfSealed]). It reads
// the flags and checks nothing, so a check asks it once [Digest.Check] has
// found the line's MAC, which the flags are bound into, good.
func Sealing(sum []byte) bool {
	return (len(sum) == onlyLine3 || len(sum) == lineAndRow3) && sum[0] == DigestVersion && sum[1]&sealed != 0
}

// Verdict is what [Digest.Check] found.
type Verdict int

const (
	// Matches is a row that is the one its digest was made of.
	Matches Verdict = iota

	// Differs is a row that is not: changed since, by something the trail
	// did not record. To [Digest.CheckGone], a row that is not there where
	// the trail says one was left.
	Differs

	// KeyNotHeld is a digest made under a key this was not given and was not
	// told was let go: a check with the wrong key, or a digest nobody here
	// made.
	KeyNotHeld

	// Retired is a digest made under a key the deployment let go
	// ([Digest.Retire]), so nothing can check it.
	Retired

	// Forged is a digest that is not the line's that keeps it: copied there
	// from another line, or not one payday makes at all. To
	// [Digest.CheckSeal], a seal that is not of the newest line's write.
	Forged

	// Forgotten is a seal that says the row's personal fields were forgotten
	// ([Digest.Forgotten]): what the person was is vouched for by nothing any
	// more, which is what forgetting them is.
	Forgotten
)

func (v Verdict) String() string {
	switch v {
	case Matches:
		return "matches"
	case Differs:
		return "differs"
	case KeyNotHeld:
		return "key not held"
	case Retired:
		return "retired"
	case Forged:
		return "forged"
	case Forgotten:
		return "forgotten"
	}

	return fmt.Sprintf("Verdict(%d)", int(v))
}

// ErrDigest is a digest that is not one any version of payday makes: of a
// version nothing made, or of the wrong length.
var ErrDigest = errors.New("not a digest any version of payday makes")

// Check says whether `sum`, the digest the line `line` keeps, is the digest of
// `row` -- the row of the thing `object` names, as it is now -- over the
// fields `covered` lists.
func (d *Digest) Check(sum []byte, object pdid.Id, row proto.Message, covered []uint32, line Line) (Verdict, error) {
	r, _, v, err := d.open(sum, object, line)
	switch {
	case v != Matches:
		return v, err
	case len(r) == 0:
		// The line says the write took the row, and here it is: brought back
		// by something that is not on the trail.
		return Differs, nil
	}

	key, v := d.keyOf(Fingerprint(r[:8]))
	if v != Matches {
		return v, nil
	}

	mac, err := rowMAC(key, object, row, covered)
	if err != nil {
		return Differs, err
	}
	if !hmac.Equal(mac, r[8:]) {
		return Differs, nil
	}

	return Matches, nil
}

// CheckGone says whether `sum`, the digest the line `line` keeps of `object`,
// says what is so of a thing that is no row: that the write it was took it.
// One that says a row was left is [Differs] -- the row went past the trail --
// whatever key that row was digested under.
func (d *Digest) CheckGone(sum []byte, object pdid.Id, line Line) (Verdict, error) {
	r, _, v, err := d.open(sum, object, line)
	switch {
	case v != Matches:
		return v, err
	case len(r) > 0:
		return Differs, nil
	}

	return Matches, nil
}

// open checks the line's part of `sum` and answers with the row's -- the
// whole of a version-1 digest, which is bound to no line, and nothing for one
// of a write that left no row -- and the digest's flags, which only version 3
// has.
func (d *Digest) open(sum []byte, object pdid.Id, line Line) ([]byte, byte, Verdict, error) {
	switch {
	case len(sum) == onlyRow && sum[0] == 1:
		return sum[1:], 0, Matches, nil

	case (len(sum) == onlyLine || len(sum) == lineAndRow) && sum[0] == 2:
		key, v := d.keyOf(Fingerprint(sum[1:9]))
		if v != Matches {
			return nil, 0, v, nil
		}

		r := sum[onlyLine:]
		if !hmac.Equal(lineMAC(key, object, line, r), sum[9:onlyLine]) {
			return nil, 0, Forged, nil
		}

		return r, 0, Matches, nil

	case (len(sum) == onlyLine3 || len(sum) == lineAndRow3) && sum[0] == DigestVersion:
		key, v := d.keyOf(Fingerprint(sum[head : head+8]))
		if v != Matches {
			return nil, 0, v, nil
		}

		r := sum[onlyLine3:]
		if !hmac.Equal(lineMAC3(key, object, line, sum[1], r), sum[head+8:onlyLine3]) {
			return nil, 0, Forged, nil
		}

		return r, sum[1], Matches, nil
	}

	return nil, 0, Forged, ErrDigest
}

// keyOf is the key a fingerprint names, and why there is none: let go, or
// never held.
func (d *Digest) keyOf(p Fingerprint) ([]byte, Verdict) {
	if p == d.print {
		return d.key, Matches
	}
	if v, ok := d.previous[p]; ok {
		return v, Matches
	}
	if d.retired[p] {
		return nil, Retired
	}

	return nil, KeyNotHeld
}

// rowMAC is the row's MAC: of what it is about and what it covers, and then of
// the row restricted to that, marshalled deterministically. The list it covers
// is bound into it, so that taking a field out of the list does not take a
// change to that field out of the comparison. It is version 1's whole digest,
// unchanged, which is what lets a version-1 digest be carried.
func rowMAC(key []byte, object pdid.Id, row proto.Message, covered []uint32) ([]byte, error) {
	return macOfRow(key, "payday.trail.digest\x00", object, row, covered)
}

// macOfRow is a MAC under `key`, labelled, of what `row` is about, the fields
// it covers, and the row restricted to them.
func macOfRow(key []byte, label string, object pdid.Id, row proto.Message, covered []uint32) ([]byte, error) {
	m := proto.Clone(row)
	restrict(m.ProtoReflect(), covered)

	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, err
	}

	h := hmac.New(sha256.New, key)
	h.Write([]byte(label))
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

// lineMAC is a version-2 line's MAC: of which line it is, when it was written,
// what it says was done and what it is about, and then of the row's part --
// `r`, or nothing for a write that left no row. Everything before `r` is of a
// fixed length or says its own, so no two lines are one input.
func lineMAC(key []byte, object pdid.Id, line Line, r []byte) []byte {
	return macOfLine(key, "payday.trail.line\x00", nil, object, line, r)
}

// lineMAC3 is a version-3 line's: [lineMAC] under a label of its own, with
// the digest's flags bound in after it.
func lineMAC3(key []byte, object pdid.Id, line Line, flags byte, r []byte) []byte {
	return macOfLine(key, "payday.trail.line.3\x00", []byte{flags}, object, line, r)
}

// macOfLine is a MAC under `key`, labelled, of `extra` -- of a length the
// label fixes -- and then of the line, what it is about, and `r`.
func macOfLine(key []byte, label string, extra []byte, object pdid.Id, line Line, r []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(label))
	h.Write(extra)
	h.Write(line.Id[:])

	n := make([]byte, 8)
	binary.BigEndian.PutUint64(n, uint64(line.Created.UnixMicro()))
	h.Write(n)
	binary.BigEndian.PutUint32(n[:4], uint32(len(line.Action)))
	h.Write(n[:4])
	h.Write([]byte(line.Action))

	h.Write(object.Bytes())
	h.Write(r)
	return h.Sum(nil)
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
