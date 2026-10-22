package witnessrekor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	merkleproof "github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/lesomnus/payday/trail"
)

// Check holds the chain to the log (trail.Checker).
//
// Every receipt the store keeps from it is the entry of its checkpoint -- the
// statement's digest, signed by a key the deployment signs with -- proved in
// the tree the log signed when it answered, which costs the log nothing: the
// proof is in the receipt. And the trees the log signed are one tree going
// on: the newest receipt's to the tree the log answers with now, asked every
// time, and each receipt's to the next, asked once a process for the newest
// two and for all of them by a full verification. So a verification asks the
// log for its tree now and one proof or two -- a v2 log's proof is its tiles,
// a few -- however long the history. A receipt that is none of that is
// `forged`, and a log whose tree does not go on from what it signed is
// `rewritten`. On a v1 log every entry the deployment's keys signed is also a
// checkpoint the store keeps, as far as its search answers; see searchedV1.
// A checkpoint with no receipt is the pass's to tell the log again.
//
// The log's tree now is asked first: a log that does not answer is an error,
// a witness that did not answer, and finds nothing.
func (w *Witness) Check(ctx context.Context, chain string, kept []trail.Checked, full bool) ([]trail.Finding, error) {
	if chain != "" {
		return nil, nil
	}

	fresh, err := w.fresh(ctx)
	if err != nil {
		return nil, err
	}

	fs := []trail.Finding{}
	heads := []head{}
	for _, c := range kept {
		if c.Receipt == nil {
			continue
		}

		h, note := w.checkReceipt(c)
		if note != "" {
			fs = append(fs, trail.Finding{Kind: "forged", Checkpoint: c.K, Digest: c.Digest, Note: "the log's receipt of it: " + note})
			continue
		}
		heads = append(heads, h)
	}

	// By origin -- a log sharded by year signs each shard's apart -- and
	// size, the same head once.
	slices.SortFunc(heads, func(a, b head) int {
		if a.origin != b.origin {
			return compareStrings(a.origin, b.origin)
		}
		return int(a.size - b.size)
	})
	heads = slices.CompactFunc(heads, func(a, b head) bool { return a.origin == b.origin && a.size == b.size && bytes.Equal(a.root, b.root) })

	pairs := [][2]head{}
	for i := 0; i+1 < len(heads); i++ {
		if heads[i].origin == heads[i+1].origin {
			pairs = append(pairs, [2]head{heads[i], heads[i+1]})
		}
	}
	var newest *head
	for i := range heads {
		if heads[i].origin == fresh.origin {
			newest = &heads[i]
		}
	}
	if !full {
		// The newest two, which a pass's verification proves once each as
		// the receipts come; the rest a full verification proves.
		kept := [][2]head{}
		for _, p := range pairs {
			if newest != nil && p[1].origin == newest.origin && p[1].size == newest.size {
				kept = append(kept, p)
			}
		}
		pairs = kept
	}
	if newest != nil {
		pairs = append(pairs, [2]head{*newest, fresh})
	}

	for _, p := range pairs {
		if err := w.consistent(ctx, p[0], p[1], full); err != nil {
			fs = append(fs, trail.Finding{Kind: "rewritten", Note: fmt.Sprintf(
				"the log's tree of %d does not go on from the tree of %d it signed when it heard a checkpoint: %v", p[1].size, p[0].size, err)})
		}
	}

	if !w.v2 {
		more, err := w.searchedV1(ctx, kept)
		if err != nil {
			return nil, err
		}
		fs = append(fs, more...)
	}

	return fs, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}

func (w *Witness) fresh(ctx context.Context) (head, error) {
	if w.v2 {
		return w.freshV2(ctx)
	}

	return w.freshV1(ctx)
}

// checkReceipt answers the head of the tree a receipt's entry is proved in,
// or what is wrong with it.
func (w *Witness) checkReceipt(c trail.Checked) (head, string) {
	var r receipt
	if err := json.Unmarshal(c.Receipt, &r); err != nil {
		return head{}, "it does not read"
	}

	var e entry
	var err error
	switch {
	case r.V == 1 && !w.v2:
		e, err = readV1(r.Entry)
	case r.V == 2 && w.v2:
		e, err = readV2(r.Entry)
	default:
		return head{}, fmt.Sprintf("it is of a v%d log, and the log is another", r.V)
	}
	if err != nil {
		return head{}, "it does not read: " + err.Error()
	}

	bound := boundV1
	if w.v2 {
		bound = boundV2
	}
	if note := bound(c, e.body); note != "" {
		return head{}, note
	}

	leaf := rfc6962.DefaultHasher.HashLeaf(e.body)
	if err := merkleproof.VerifyInclusion(rfc6962.DefaultHasher, uint64(e.proof.index), uint64(e.proof.size), leaf, e.proof.hashes, e.proof.root); err != nil {
		return head{}, "it does not prove its entry is in the log's tree: " + err.Error()
	}

	h, err := w.readHead(e.proof.head)
	if err != nil {
		return head{}, err.Error()
	}
	if h.size != e.proof.size || !bytes.Equal(h.root, e.proof.root) {
		return head{}, "the tree's head it keeps is of another tree than its proof"
	}

	return h, ""
}

// consistent proves the log's tree `b` goes on from `a`, which a process
// asks of the log once for two heads it signed, and again when `again`.
func (w *Witness) consistent(ctx context.Context, a, b head, again bool) error {
	switch {
	case a.size == b.size:
		if !bytes.Equal(a.root, b.root) {
			return errors.New("two trees of one size, and two roots")
		}
		return nil
	case a.size > b.size:
		return fmt.Errorf("the tree of %d is smaller than one of %d it signed before", b.size, a.size)
	}

	k := fmt.Sprintf("%s %d %x %d %x", a.origin, a.size, a.root, b.size, b.root)
	if _, ok := w.proved.Load(k); ok && !again {
		return nil
	}

	var err error
	if w.v2 {
		err = w.consistentV2(ctx, a, b)
	} else {
		err = w.consistentV1(ctx, a, b)
	}
	if err == nil {
		w.proved.Store(k, true)
	}

	return err
}

func verifyConsistency(a, b head, hashes [][]byte) error {
	return merkleproof.VerifyConsistency(rfc6962.DefaultHasher, uint64(a.size), uint64(b.size), hashes, a.root, b.root)
}

// holds answers whether `key` is one of `keys`.
func holds(keys [][]byte, key []byte) bool {
	return slices.ContainsFunc(keys, func(k []byte) bool { return bytes.Equal(k, key) })
}
