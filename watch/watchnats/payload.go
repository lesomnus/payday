package watchnats

import (
	"bytes"
	"encoding/binary"
	"errors"
	"time"
	"uuid"

	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/watch/internal/wire"
)

const frameHeader = 24
const chunkSize = 64 << 10
const fragmentTimeout = 30 * time.Second
const recentLimit = 4096

type publication struct {
	subjects []string
	frames   [][]byte
	size     int
}

// Each mutation gets a transport ID, independent of its row ID. Multiple native
// interests may overlap and several routes may name the same change; only those
// copies are suppressed. Outbox retries remain distinct publications.
func (b *Broker) encode(v watch.Event) ([]publication, int, error) {
	size := 0
	out := make([]publication, 0, len(v.Changes))
	limit := min(chunkSize, int(b.conn.MaxPayload())-frameHeader)
	if limit < 1 {
		return nil, 0, errors.New("watchnats: server payload limit is too small")
	}
	for _, c := range v.Changes {
		data, err := wire.Encode(watch.Event{Actor: v.Actor, Tenant: v.Tenant, Method: v.Method, Changes: []watch.Change{c}})
		if err != nil {
			return nil, 0, err
		}
		subjects := b.subjects(c)
		count := (len(data) + limit - 1) / limit
		n := (len(data) + count*frameHeader) * len(subjects)
		if n > wire.MaxWire-size {
			return nil, 0, errors.New("watchnats: publication exceeds queue byte budget")
		}
		size += n
		id := uuid.NewV7()
		frames := make([][]byte, 0, count)
		for i := range count {
			part := data[i*limit : min(len(data), (i+1)*limit)]
			f := make([]byte, frameHeader+len(part))
			copy(f, id[:])
			binary.BigEndian.PutUint32(f[16:20], uint32(i))
			binary.BigEndian.PutUint32(f[20:24], uint32(count))
			copy(f[frameHeader:], part)
			frames = append(frames, f)
		}
		out = append(out, publication{subjects: subjects, frames: frames, size: n})
	}
	return out, size, nil
}

type partial struct {
	parts    [][]byte
	received int
	size     int
	deadline time.Time
}
type receiver struct {
	pending map[[16]byte]*partial
	size    int
	recent  map[[16]byte]bool
	order   [recentLimit][16]byte
	next    int
}

func (r *receiver) accept(data []byte) (*watch.Event, error) {
	if len(data) <= frameHeader || len(data) > frameHeader+chunkSize {
		return nil, errors.New("watchnats: invalid frame size")
	}
	id := [16]byte(data[:16])
	index := int(binary.BigEndian.Uint32(data[16:20]))
	count := int(binary.BigEndian.Uint32(data[20:24]))
	body := data[frameHeader:]
	if count < 1 || count > wire.MaxWire || index >= count {
		return nil, errors.New("watchnats: invalid fragment")
	}
	if r.recent[id] {
		return nil, nil
	}
	if r.pending == nil {
		r.pending = make(map[[16]byte]*partial)
	}
	p := r.pending[id]
	if p == nil {
		// Bound the index allocation as well as the bytes. Very small server payload
		// limits are refused by New, so a publication cannot need millions of slots.
		if count > wire.MaxWire/1024+1 || len(r.pending) >= 64 {
			return nil, errors.New("watchnats: incomplete publication budget exhausted")
		}
		p = &partial{parts: make([][]byte, count), deadline: time.Now().Add(fragmentTimeout)}
		r.pending[id] = p
	}
	if len(p.parts) != count {
		return nil, errors.New("watchnats: inconsistent fragments")
	}
	if existing := p.parts[index]; existing != nil {
		if !bytes.Equal(existing, body) {
			return nil, errors.New("watchnats: conflicting fragment")
		}
	} else {
		p.parts[index] = body
		p.received++
		p.size += len(body)
		r.size += len(body)
	}
	if r.size > wire.MaxWire {
		return nil, errors.New("watchnats: incomplete payload budget exhausted")
	}
	if p.received != count {
		return nil, nil
	}
	var joined []byte
	if count == 1 {
		joined = p.parts[0]
	} else {
		joined = make([]byte, 0, p.size)
		for _, part := range p.parts {
			joined = append(joined, part...)
		}
	}
	delete(r.pending, id)
	r.size -= p.size
	v, err := wire.Decode(string(joined))
	if err != nil {
		return nil, err
	}
	if r.recent == nil {
		r.recent = make(map[[16]byte]bool)
	}
	delete(r.recent, r.order[r.next])
	r.order[r.next] = id
	r.recent[id] = true
	r.next = (r.next + 1) % recentLimit
	return &v, nil
}
func (r *receiver) expired(now time.Time) bool {
	for _, p := range r.pending {
		if !now.Before(p.deadline) {
			return true
		}
	}
	return false
}
