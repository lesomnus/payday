package trail

import (
	"bytes"
	"crypto"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/lesomnus/flob"
)

// Signature is what the deployment's key said of one row of the manifest: a
// signature over what the row says, and the public half of the key that made
// it, PKIX. Both are empty on a row written without a key.
//
// # Why a row is signed
//
// A checkpoint is what the archive held when an act ended, and [Policy.Verify]
// recomputes the latest one from the rows it was taken over, so nothing in the
// manifest changes unseen **up to** the latest checkpoint. After it, somebody
// who can write both the database and the archive can change the two together,
// and the next checkpoint takes in whatever they made them say: a chunk put
// there with a row to match, a row relabelled so that a pass destroys its chunk
// early, a row marked erased with its chunk taken away. None of those is a row
// the deployment's key signed.
//
// So every row an act writes carries a signature by [Policy.Signer] over what
// it says, and a row that says anything else is a finding -- `unsigned` or
// `forged` -- and is not what an act decides by: its chunk is to an act what a
// chunk nobody wrote is, neither destroyed, folded, written again nor read. A
// key read into the process is held by whoever holds the process, as it is for
// a checkpoint, and one in a device is not; that is what the signature is
// worth.
//
// # What is not signed
//
// When a row was added and last changed, and the checkpoints it was first in
// and first not in. The times decide only when a pass may settle a row a crash
// left in flight; moving them makes a pass settle one early or late, which
// leaves a finding behind rather than hiding one. `since` and `gone` are
// written by the transaction that numbers a checkpoint, over every row at once,
// and the checkpoints are what they are compared with.
type Signature struct {
	Key []byte
	Sig []byte
}

// Sign is what a [Manifest] is handed with every row it writes: it answers the
// signature to write beside the row, given the row as it was -- the zero
// [Archived] for one being added -- and as it is about to be. An error is a
// write the manifest does not make.
//
// A manifest writes every field a signature is over, together with the
// signature, in one write. What a row says is then what was signed whichever of
// two writers is last, where writing only what changed would leave a row that
// says one writer's labels beside the other's signature.
type Sign func(was, is Archived) (Signature, error)

// statement is the bytes a row's signature is over: which row it is -- its key,
// as fmt prints it -- and everything an act decides by.
//
// Each field is written with its length, so that no two rows that say
// different things are one statement, and the first line says what it is, so
// that a statement is never also a checkpoint, which the same key signs.
func statement(v Archived) []byte {
	b := &bytes.Buffer{}
	b.WriteString("payday.trail.Archived\n")

	field := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
		b.WriteByte('\n')
	}
	field(fmt.Sprint(v.Key))
	field(v.Namespace)
	field(v.Digest)
	field(v.Intent)
	field(v.State)
	for _, k := range slices.Sorted(maps.Keys(v.Labels)) {
		field(k)
		field(v.Labels[k])
	}

	return b.Bytes()
}

// trusted is every key a signature is believed by: [Policy.Trust], and the
// signer's own.
func (p Policy) trusted() []crypto.PublicKey {
	out := slices.Clone(p.Trust)
	if p.Signer != nil {
		out = append(out, p.Signer.Public())
	}

	return out
}

// keyed answers whether the policy holds the manifest's rows to a signature,
// which it does whenever it names a key: its signer's, or one it trusts.
//
// # A row with no signature
//
// It looks the same whether it was written before rows were signed, while the
// deployment had no key, or had its signature taken off since -- and only
// somebody who knows which can say. So with a key, every such row is a finding
// until an operator who has looked accepts it with [Policy.Accept], which signs
// it as it stands; without one, nothing is signed and nothing is held to a
// signature, which is what the manifest was before rows were signed.
//
// The other way was considered: the first pass with a key signing every row it
// finds without one, as the first pass fills an empty manifest. It is safe only
// if nobody can make a manifest look as if it had never been signed, and taking
// the signatures off every row is exactly that, for whoever can write the
// database. Holding that off would take a mark that rows had been signed, kept
// where the database's credentials do not reach and read before every act.
//
// A process that names keys to trust and has none to sign with -- a verifier,
// given the public halves -- holds what it would write itself to the same
// rule: it should not be writing the archive, and a row it writes is a finding.
func (p Policy) keyed() bool { return p.Signer != nil || len(p.Trust) > 0 }

// rowFinding is what is wrong with a row's signature: nothing, or the kind of
// finding it is and why.
func (p Policy) rowFinding(v Archived, trusted []crypto.PublicKey) (string, string) {
	switch {
	case len(v.Signature.Sig) == 0:
		if !p.keyed() {
			return "", ""
		}

		return "unsigned", "no signature, and the policy names a key"

	case !verified(v.Signature.Key, statement(v), v.Signature.Sig):
		return "forged", "its signature is not over what it says"

	case len(trusted) > 0 && !trusts(trusted, v.Signature.Key):
		return "forged", "signed by a key nobody trusts: " + base64.StdEncoding.EncodeToString(v.Signature.Key)
	}

	return "", ""
}

// rowFindingOf is a row whose signature is a finding, as one: under the blob it
// is the row of, or for a row still adding, the namespace and the intent.
func rowFindingOf(v Archived, kind, note string) Finding {
	of := v.State
	if v.State == Adding {
		of = "adding as " + v.Intent
	}

	return Finding{Kind: kind, Namespace: v.Namespace, Digest: flob.Digest(v.Digest), Note: "its row, " + of + ": " + note}
}

// errUnvouched is a row an act was to move along whose own signature is a
// finding.
var errUnvouched = errors.New("a row whose signature is a finding is not moved along; see `trail verify`")

// signing is what the manifest is handed to sign what an act writes: with the
// policy's key, and never out of a row whose own signature is a finding.
//
// Moving a row along -- settling it, erasing its chunk, relabelling it -- is
// the deployment saying what the row says next, and a row it never said
// anything of is not one it can say that about. Otherwise a row put in the
// database beside a blob of somebody's would come out of the next pass signed:
// an add it settles, an erase it finishes.
func (p Policy) signing() Sign {
	trusted := p.trusted()

	return func(was, is Archived) (Signature, error) {
		if was.Key != nil {
			if kind, note := p.rowFinding(was, trusted); kind != "" {
				return Signature{}, fmt.Errorf("%w: %s %s %s: %s", errUnvouched, kind, was.Namespace, was.Digest, note)
			}
		}

		return p.signRow(is)
	}
}

// vouching signs a row as it stands, whatever it said before: for
// [Policy.Accept], which is an operator saying that it is right.
func (p Policy) vouching() Sign {
	return func(_, is Archived) (Signature, error) { return p.signRow(is) }
}

// signRow signs a row with the policy's key, and signs nothing without one.
func (p Policy) signRow(v Archived) (Signature, error) {
	if p.Signer == nil {
		return Signature{}, nil
	}

	der, sig, err := sign(p.Signer, statement(v))
	if err != nil {
		return Signature{}, fmt.Errorf("signing a row of the manifest: %w", err)
	}

	return Signature{Key: der, Sig: sig}, nil
}
