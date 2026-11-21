package authoidc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/lesomnus/payday/auth"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/metadata"
)

// Keep the signature implementation and rotation logic in go-oidc. Its cache
// misses include bad signatures with known kids, so limiting a kid map alone
// would still let an attacker turn every token into an issuer request.
func limitedClient(ctx context.Context, interval time.Duration) context.Context {
	client := *oauth2.NewClient(ctx, nil)
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = &remoteTransport{base: base, interval: interval, entries: map[string]*remoteFetch{}}
	if client.Timeout == 0 {
		client.Timeout = 10 * time.Second
	}
	return oidc.ClientContext(ctx, &client)
}

type remoteTransport struct {
	base     http.RoundTripper
	interval time.Duration
	mu       sync.Mutex
	entries  map[string]*remoteFetch
}

type remoteFetch struct {
	date     time.Time
	done     chan struct{}
	response *http.Response
	body     []byte
	err      error
}

func (r *remoteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Discovery and JWKS use GET. Do not cache a future token exchange or any
	// other operation somebody adds to the context's HTTP client.
	if req.Method != http.MethodGet {
		return r.base.RoundTrip(req)
	}
	key := req.URL.String()
	r.mu.Lock()
	f := r.entries[key]
	if f != nil {
		select {
		case <-f.done:
			if time.Since(f.date) >= r.interval {
				f = nil
			}
		default: // An outstanding fetch is shared even if its interval elapsed.
		}
	}
	owner := f == nil
	if owner {
		f = &remoteFetch{date: time.Now(), done: make(chan struct{})}
		r.entries[key] = f
	}
	r.mu.Unlock()

	if owner {
		res, err := r.base.RoundTrip(req)
		if err == nil {
			// A compromised issuer cannot make this per-handler cache retain an
			// unbounded response. Ordinary key sets are far below four MiB.
			f.body, err = io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
			res.Body.Close()
			if len(f.body) > 4<<20 {
				f.body = nil
				err = fmt.Errorf("authoidc: remote metadata exceeds four MiB")
			}
			f.response = res
		}
		f.err = err
		close(f.done)
	} else {
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-f.done:
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	res := *f.response
	res.Header = f.response.Header.Clone()
	res.Body = io.NopCloser(bytes.NewReader(f.body))
	res.Request = req
	return &res, nil
}

type lazyHandler struct {
	config  Config
	context context.Context
	mu      sync.Mutex
	handler auth.Handler
	pending *discovery
}

type discovery struct {
	done chan struct{}
	h    auth.Handler
	err  error
}

func (l *lazyHandler) Handle(ctx context.Context) (auth.Identity, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	present := false
	for _, value := range md.Get(auth.Header) {
		if strings.HasPrefix(value, auth.BearerScheme+" ") {
			present = true
			break
		}
	}
	if !present {
		return auth.Identity{}, auth.ErrNoCredential
	}
	if err := ctx.Err(); err != nil {
		return auth.Identity{}, err
	}
	l.mu.Lock()
	if l.handler != nil {
		h := l.handler
		l.mu.Unlock()
		return h.Handle(ctx)
	}
	if l.pending == nil {
		d := &discovery{done: make(chan struct{})}
		l.pending = d
		go func() {
			// One caller abandoning its request must not cancel discovery for
			// everyone else. The HTTP client's timeout still bounds the fetch.
			d.h, d.err = discover(l.context, l.config)
			l.mu.Lock()
			l.handler = d.h
			l.pending = nil
			close(d.done)
			l.mu.Unlock()
		}()
	}
	d := l.pending
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return auth.Identity{}, ctx.Err()
	case <-d.done:
		if d.err != nil {
			return auth.Identity{}, fmt.Errorf("%w: %s", auth.ErrUnavailable, d.err)
		}
		return d.h.Handle(ctx)
	}
}
