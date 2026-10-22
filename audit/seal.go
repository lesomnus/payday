package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
)

// A row's seal is what vouches for the fields the trail's digest leaves out:
// the ones the schema declared `personal` (payday#115).
//
// # Why not the digest
//
// The digest is kept on a line of the trail, and a line is copied to the
// archive, signed, and kept for as long as the deployment's windows say --
// years after the row it was of has gone, and after the person has been
// forgotten. A keyed MAC of somebody's address kept that long is something of
// them that outlives their erasure: whoever holds the key can test a guess at
// what it was. So the digest leaves a personal field out, and until this a
// personal field changed past the servers -- an address, a name -- passed
// every check.
//
// A seal is the MAC that covers them, kept where it lives exactly as long as
// the row: beside it, in the database alone, never on the trail and never in
// the archive. It is written in the transaction of every write that leaves the
// row, as the trail's line is, removed with the row, and blanked when the
// person is forgotten ([Digest.Forgotten]). What it vouches for, nothing else
// keeps.
//
// # What it covers, and what it is bound to
//
// Every field of the row the build that sealed it knew -- the personal ones
// among them, and the rest, which costs nothing to cover twice -- listed beside
// it and bound into it, as a digest's are. And the line of the trail its write
// recorded: a seal is of one write, so one kept from an earlier write, put back
// with what the row then was, is not the newest line's, which is a finding of
// its own. A pass that carries the line forward carries the seal with it
// ([Digest.CarrySeal]), copying what it says of the row and binding it anew.
//
// # The key
//
// One derived from the digest's: an HMAC, under the digest key, of a label of
// its own. No configuration of its own, so it rotates with the digest key and
// is named by that key's fingerprint, and a key let go is let go for both. And
// none of the trail's MACs -- which leave the database with the archive -- is
// made under the key that makes seals, so nothing the archive holds can be held
// up against what a seal says.

// SealVersion is the first byte of every seal [Digest.Seal] makes.
const SealVersion byte = 1

const (
	// ofRow is a seal of a row, and forgotten one that says the row's
	// personal fields were forgotten: the second byte of a seal.
	ofRow     byte = 0
	forgotten byte = 1

	// sealHead is what a seal begins with: its version and what it is.
	sealHead = 2

	// sealOfRow is a seal of a row: the line's part, then the row's -- each
	// a fingerprint and a MAC, as a digest's are.
	sealOfRow = sealHead + 2*part

	// sealForgotten is a seal that says the row was forgotten: one part.
	sealForgotten = sealHead + part
)

// ErrSeal is a seal that is not one any version of payday makes.
var ErrSeal = errors.New("not a seal any version of payday makes")

// Seal is the seal of `row`, the row of the thing `object` names as the write
// the line `line` records left it, over the fields `all` lists: kept beside
// the row, for as long as it is.
func (d *Digest) Seal(object pdid.Id, row proto.Message, all []uint32, line Line) ([]byte, error) {
	k := sealKey(d.key)
	mac, err := macOfRow(k, "payday.trail.seal.row\x00", object, row, all)
	if err != nil {
		return nil, err
	}

	r := make([]byte, 0, part)
	r = append(r, d.print[:]...)
	r = append(r, mac...)

	return d.bindSeal(k, object, line, r), nil
}

// Forgotten is the seal of a row whose personal fields were forgotten: of the
// thing it is about alone, and of nothing it held. [trail.Policy.Forget] puts
// one in place of each seal it reaches, so that a forgotten row is told from a
// row somebody took the seal off, and no MAC of what the person was is left.
//
// It is bound to no line: the next write the row has seals it again, and none
// it has is anybody's to vouch for until then.
func (d *Digest) Forgotten(object pdid.Id) []byte {
	out := make([]byte, 0, sealForgotten)
	out = append(out, SealVersion, forgotten)
	out = append(out, d.print[:]...)

	return append(out, forgottenMAC(sealKey(d.key), object)...)
}

// CarrySeal is `seal` -- the seal of `object` its newest line's write left --
// bound to `line` instead, which is what a pass writes beside the line it
// carries that one forward to. What it says of the row is copied as it was,
// and only the binding is new; a seal that says the row was forgotten is one
// still.
func (d *Digest) CarrySeal(seal []byte, object pdid.Id, line Line) ([]byte, error) {
	switch {
	case len(seal) == sealOfRow && seal[0] == SealVersion && seal[1] == ofRow:
		return d.bindSeal(sealKey(d.key), object, line, seal[sealHead+part:]), nil
	case len(seal) == sealForgotten && seal[0] == SealVersion && seal[1] == forgotten:
		return d.Forgotten(object), nil
	}

	return nil, ErrSeal
}

// CheckSeal says whether `seal` is the seal of `row` -- the row of the thing
// `object` names, as it is now -- over the fields `all` lists, made by the
// write the line `line` records: [Matches], or [Differs] for a row changed
// since, or [Forged] for a seal of another line's write; [Forgotten] for one
// that says the person was forgotten; and [KeyNotHeld] or [Retired] for one
// under a key this cannot check.
func (d *Digest) CheckSeal(seal []byte, object pdid.Id, row proto.Message, all []uint32, line Line) (Verdict, error) {
	switch {
	case len(seal) == sealForgotten && seal[0] == SealVersion && seal[1] == forgotten:
		key, v := d.keyOf(Fingerprint(seal[sealHead : sealHead+8]))
		if v != Matches {
			return v, nil
		}
		if !hmac.Equal(forgottenMAC(sealKey(key), object), seal[sealHead+8:]) {
			return Forged, nil
		}

		return Forgotten, nil

	case len(seal) == sealOfRow && seal[0] == SealVersion && seal[1] == ofRow:
		key, v := d.keyOf(Fingerprint(seal[sealHead : sealHead+8]))
		if v != Matches {
			return v, nil
		}
		r := seal[sealHead+part:]
		if !hmac.Equal(sealLineMAC(sealKey(key), object, line, r), seal[sealHead+8:sealHead+part]) {
			return Forged, nil
		}

		key, v = d.keyOf(Fingerprint(r[:8]))
		if v != Matches {
			return v, nil
		}
		mac, err := macOfRow(sealKey(key), "payday.trail.seal.row\x00", object, row, all)
		if err != nil {
			return Differs, err
		}
		if !hmac.Equal(mac, r[8:]) {
			return Differs, nil
		}

		return Matches, nil
	}

	return Forged, ErrSeal
}

// bindSeal is a seal of a row: the line's part, made now under `k`, and `r`,
// the row's.
func (d *Digest) bindSeal(k []byte, object pdid.Id, line Line, r []byte) []byte {
	out := make([]byte, 0, sealOfRow)
	out = append(out, SealVersion, ofRow)
	out = append(out, d.print[:]...)
	out = append(out, sealLineMAC(k, object, line, r)...)

	return append(out, r...)
}

// sealKey is the key seals are made under, from the digest key they are named
// by: see the package's notes on seals.
func sealKey(key []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("payday.trail.seal.key\x00"))
	return h.Sum(nil)
}

// sealLineMAC is the binding of a seal's row part `r` to the line its write
// recorded.
func sealLineMAC(k []byte, object pdid.Id, line Line, r []byte) []byte {
	return macOfLine(k, "payday.trail.seal.line\x00", nil, object, line, r)
}

// forgottenMAC is the MAC of a seal that says `object` was forgotten.
func forgottenMAC(k []byte, object pdid.Id) []byte {
	h := hmac.New(sha256.New, k)
	h.Write([]byte("payday.trail.seal.forgotten\x00"))
	h.Write(object.Bytes())
	return h.Sum(nil)
}

// sealPrints is the fingerprints a seal names: its line part's, and its row
// part's for a seal of a row.
func sealPrints(seal []byte) []Fingerprint {
	switch {
	case len(seal) == sealForgotten && seal[0] == SealVersion:
		return []Fingerprint{Fingerprint(seal[sealHead : sealHead+8])}
	case len(seal) == sealOfRow && seal[0] == SealVersion:
		return []Fingerprint{Fingerprint(seal[sealHead : sealHead+8]), Fingerprint(seal[sealHead+part : sealHead+part+8])}
	}

	return nil
}
