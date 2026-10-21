package access

import (
	"context"
	"slices"
	"sync"
)

// Call is what one call has carried so far: what the app said it showed, and
// what it wrote. The interceptor puts one in the call's context; [Saw] and the
// generated recorder add to it.
//
// What it carried is said by rows as it goes -- a write's at its commit, a
// stream's every second -- so it keeps, of each person, whether the call wrote
// their data and what a row says of them already: each row says only what none
// before it did. A person whose data the call both wrote and showed is a row of
// the write; one it showed is a row of the read.
type Call struct {
	mu sync.Mutex

	method  string
	address string

	// downloads are the methods [Config.Downloads] names, for what serves
	// several in one call to ask of each ([IsDownload]).
	downloads []string

	// subjects is everybody the call carried, in the order it did; of is what
	// it did with each.
	subjects []Subject
	of       map[Subject]*carried
}

// carried is what a call did with one person's data, and what its rows say
// of it so far.
type carried struct {
	wrote bool

	// kept is whether a row of the write is in a transaction that is not
	// known to have committed: the call's end settles it ([Call.Settle]).
	kept bool

	// saidWrote is whether a row says the call wrote it; saidCarried whether
	// one says the call carried it otherwise.
	saidWrote   bool
	saidCarried bool
}

type callKey struct{}

// Into answers with a context that carries a new [Call] for `method`.
func Into(ctx context.Context, method string) (context.Context, *Call) {
	c := &Call{method: method, of: map[Subject]*carried{}}
	return context.WithValue(ctx, callKey{}, c), c
}

// Of is the [Call] `ctx` belongs to, if any.
func Of(ctx context.Context) (*Call, bool) {
	c, ok := ctx.Value(callKey{}).(*Call)
	return c, ok
}

// Saw says the call showed the personal data of the people `subjects` name,
// where no declaration can see it: a name rendered into a sentence, a column of
// a file. Each is filed under the tenant its row is in, or the call's when that
// is zero. Outside a call it does nothing.
func Saw(ctx context.Context, subjects ...Subject) {
	if c, ok := Of(ctx); ok {
		c.add(false, subjects...)
	}
}

func (c *Call) add(wrote bool, subjects ...Subject) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, v := range subjects {
		if v.Id.IsZero() {
			continue
		}

		e, ok := c.of[v]
		if !ok {
			e = &carried{}
			c.of[v] = e
			c.subjects = append(c.subjects, v)
		}
		e.wrote = e.wrote || wrote
	}
}

// IsDownload answers whether `method` is one [Config.Downloads] names, as the
// call in `ctx` was told: what a batch asks of each of its operations, since
// gRPC dispatched the batch and not them.
func IsDownload(ctx context.Context, method string) bool {
	c, ok := Of(ctx)
	return ok && slices.Contains(c.downloads, method)
}

// Method is the RPC the call is.
func (c *Call) Method() string { return c.method }

// Address is where the call came from, as [Config.Address] said: what a row
// written at a write's commit says too.
func (c *Call) Address() string { return c.address }

// Wrote says the call's writes wrote the personal data of `subjects`.
func (c *Call) Wrote(subjects ...Subject) { c.add(true, subjects...) }

// Writes answers whether the call wrote anybody's data.
func (c *Call) Writes() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, e := range c.of {
		if e.wrote {
			return true
		}
	}

	return false
}

// Unsaid is what no row of the call says or keeps yet: the people whose data
// its writes wrote, and those whose data it carried otherwise -- showed, or
// named in a request that was refused.
func (c *Call) Unsaid() (wrote, others []Subject) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, v := range c.subjects {
		switch e := c.of[v]; {
		case e.wrote && !e.saidWrote && !e.kept:
			wrote = append(wrote, v)
		case !e.wrote && !e.saidCarried:
			others = append(others, v)
		}
	}

	return wrote, others
}

// Unkept is those of `subjects` the call wrote that no row says or keeps
// yet: what a write that has no commit to wait for writes a row of as it is
// made.
func (c *Call) Unkept(subjects ...Subject) []Subject {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []Subject
	for _, v := range subjects {
		if e, ok := c.of[v]; ok && e.wrote && !e.saidWrote && !e.kept && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}

	return out
}

// Kept says rows of the writes of `subjects` are in a transaction that is not
// known to have committed. A commit that fails takes its rows along, and only
// the call's end can tell whether one did: [Call.Settle] says them then, or
// leaves them for the interceptor to write as the refusal the call ended in.
func (c *Call) Kept(subjects ...Subject) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, v := range subjects {
		if e, ok := c.of[v]; ok {
			e.kept = true
		}
	}
}

// Settle ends what [Call.Kept] kept, once the call has ended: said, when it
// answered -- what it wrote is committed -- and unsaid again when it was
// refused, since the transaction its rows were in may have been rolled back
// or refused at its commit.
func (c *Call) Settle(answered bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, e := range c.of {
		if !e.kept {
			continue
		}

		e.kept = false
		if answered {
			e.saidWrote = true
		}
	}
}

// Said says a row now says `subjects`: that the call wrote their data, or
// that it carried it otherwise.
func (c *Call) Said(wrote bool, subjects ...Subject) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, v := range subjects {
		e, ok := c.of[v]
		if !ok {
			continue
		}
		if wrote {
			e.saidWrote = true
		} else {
			e.saidCarried = true
		}
	}
}

// take is the people whose data the call carried other than by writing it,
// that no row says yet, said at once: for a row that cannot fail to be
// written, as a queued one cannot, so that two asking together say each once
// between them.
func (c *Call) take() []Subject {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []Subject
	for _, v := range c.subjects {
		if e := c.of[v]; !e.wrote && !e.saidCarried {
			e.saidCarried = true
			out = append(out, v)
		}
	}

	return out
}
