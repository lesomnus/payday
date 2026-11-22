package watchpg

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
	"uuid"

	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/watch/internal/wire"
)

// MaxWire bounds one publication and the listener's incomplete payloads. A call
// that changed many rows may arrive as multiple events; neither the old nor the
// new Watch promises atomic receipt. Resources contain public images, never the
// request or response.
const MaxWire = 32 << 20
const fragmentSize = 4500
const fragmentTimeout = 30 * time.Second

type fragment struct {
	ID    string `json:"f"`
	Index int    `json:"i"`
	Count int    `json:"n"`
	Data  []byte `json:"d"`
}

func packets(v watch.Event) ([]string, error) {
	if len(v.Changes) == 0 {
		return nil, nil
	}
	data, err := wire.Encode(v)
	if err != nil {
		return nil, err
	}
	if len(data) < Payload {
		return []string{string(data)}, nil
	}
	id := uuid.NewV7().String()
	count := (len(data) + fragmentSize - 1) / fragmentSize
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		end := min(len(data), (i+1)*fragmentSize)
		b, err := json.Marshal(fragment{ID: id, Index: i, Count: count, Data: data[i*fragmentSize : end]})
		if err != nil {
			return nil, err
		}
		out = append(out, string(b))
	}
	return out, nil
}

type partial struct {
	parts          [][]byte
	received, size int
	started        time.Time
}
type receiver struct {
	pending map[string]*partial
	size    int
}

func (r *receiver) accept(s string) (*watch.Event, error) {
	var f fragment
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		return nil, err
	}
	if f.ID == "" {
		v, err := decode(s)
		return &v, err
	}
	if f.Count < 1 || f.Count > (MaxWire+fragmentSize-1)/fragmentSize || f.Index < 0 || f.Index >= f.Count || len(f.Data) == 0 || len(f.Data) > fragmentSize {
		return nil, errors.New("watchpg: invalid fragment")
	}
	if r.pending == nil {
		r.pending = map[string]*partial{}
	}
	p := r.pending[f.ID]
	if p == nil {
		if len(r.pending) >= 64 {
			return nil, errors.New("watchpg: too many incomplete publications")
		}
		p = &partial{parts: make([][]byte, f.Count), started: time.Now()}
		r.pending[f.ID] = p
	}
	if len(p.parts) != f.Count {
		return nil, errors.New("watchpg: inconsistent fragments")
	}
	if existing := p.parts[f.Index]; existing != nil && !bytes.Equal(existing, f.Data) {
		return nil, errors.New("watchpg: conflicting fragment")
	}
	if p.parts[f.Index] == nil {
		p.parts[f.Index] = f.Data
		p.received++
		p.size += len(f.Data)
		r.size += len(f.Data)
	}
	if r.size > MaxWire {
		return nil, errors.New("watchpg: incomplete payload budget exhausted")
	}
	if p.received != f.Count {
		return nil, nil
	}
	data := make([]byte, 0, p.size)
	for _, b := range p.parts {
		data = append(data, b...)
	}
	delete(r.pending, f.ID)
	r.size -= p.size
	v, err := decode(string(data))
	return &v, err
}

func (r *receiver) deadline() (time.Time, bool) {
	var deadline time.Time
	for _, p := range r.pending {
		d := p.started.Add(fragmentTimeout)
		if deadline.IsZero() || d.Before(deadline) {
			deadline = d
		}
	}
	return deadline, !deadline.IsZero()
}

type change = wire.Change
type envelope = wire.Envelope

func decode(s string) (watch.Event, error) { return wire.Decode(s) }
