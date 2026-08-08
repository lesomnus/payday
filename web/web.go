// Package web is the app on a port a browser can reach.
//
// gRPC is not a protocol a browser speaks. There is no way to write the frames
// from a page -- not a library that is missing, a thing the platform does not
// expose -- so a UI in front of a payday app needs something in between, and
// this is it: the **same** gRPC server, the same interceptors, the same wall,
// answering Connect and gRPC-Web as well.
//
// It is a translation and not a second implementation of anything. A call
// arrives as JSON over POST, is re-encoded as protobuf, and is handed to the
// handler that a gRPC client would have reached; what comes back goes the other
// way. So a screen and a `grpcurl` are talking to one server, and there is no
// second door for a rule to be missing from.
//
// # A second listener, and why
//
// gRPC is HTTP/2 and a browser will not speak cleartext HTTP/2, so this is its
// own address rather than the same one. [config.HttpConfig] is where it is
// said, and nothing is served at all unless an address is written down: an app
// with no UI has no reason to open a port.
//
// **Under TLS this listener carries native gRPC as well**, and that is worth
// knowing rather than guessing at: HTTP/2 arrives by ALPN, the transcoder takes
// gRPC as an incoming protocol like any other, and a `grpcurl` reaches it. So
// the two listeners are a decision about the transport gRPC brings -- Go's
// HTTP/2 server is not grpc-go's, and grpc-go says so about its own
// `ServeHTTP` -- and not about what is reachable where.
//
// # What it is not
//
// It is not authentication and it is not the wall. An origin says which page
// may make the call; who is calling and what they may see is decided in exactly
// the same place it is for anything else, because it **is** the same place.
package web

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/pprof"
	"slices"
	"strings"

	"connectrpc.com/cors"
	"connectrpc.com/vanguard"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
)

// Mux is the second listener: what the configuration asked for, plus whatever
// the app serves over HTTP, with the cross-origin answer over all of it.
//
// It embeds [http.ServeMux], so an app adds a route the way it adds one to
// anything -- `h.Handle("/login", ...)`. payday does not invent a router; what
// it owns here is the two things an app gets wrong by leaving them out, which
// are the transcoder's options and the cross-origin answer.
//
// # Why the CORS is on the whole mux
//
// It used to be on the transcoder alone, and that is the arrangement where an
// app mounts `/login` and finds that **only that route** is refused by the
// browser while every RPC works. The two are not told apart by anything a
// reader can see, so the answer covers everything on this listener.
//
// # What it does not decide
//
// Whether there is anything here. An address with nothing behind it used to be
// refused, and that was right only while payday owned the whole mux: a
// deployment that serves a login endpoint and no browser RPCs is a real one,
// and payday has no way to know the app has routes. See [config.HttpConfig].
type Mux struct {
	*http.ServeMux

	// h is the mux with [Cors] over it, built once rather than per request.
	// It closes over the *inner* mux, so a route added after this was made is
	// dispatched like any other -- a `ServeMux` resolves at request time.
	h http.Handler
}

// New answers with the second listener's mux.
func New(c config.HttpConfig, s *grpc.Server) (*Mux, error) {
	mux := http.NewServeMux()

	if c.AllowWeb {
		h, err := Transcode(s)
		if err != nil {
			return nil, err
		}

		mux.Handle("/", h)
	}
	if c.AllowPprof {
		// One by one rather than by prefix, because `pprof.Index` serves the
		// named profiles itself and the three below are separate handlers.
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	return &Mux{ServeMux: mux, h: Cors(c.Origin(), mux)}, nil
}

func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.h.ServeHTTP(w, r) }

// Transcode is every service registered on `s`, answering the protocols a
// browser can speak.
//
// The two options are the whole of what makes this work against a gRPC server
// rather than a Connect one, and each of them fails in its own way when it is
// left out:
//
//   - the target protocol is gRPC, because that is the only one grpc-go knows.
//     Without it the transcoder hands a Connect request to a handler that reads
//     it as gRPC, and the frames do not line up.
//   - the target codec is protobuf, for the same reason. Without it a JSON body
//     is passed through to a handler that unmarshals it as protobuf, and the
//     error says the wire format is invalid -- which is true, and says nothing
//     about the option that caused it.
//
// The services are read off the server rather than listed, so an entity added
// to the schema is reachable from a page without anybody remembering this file.
func Transcode(s *grpc.Server) (http.Handler, error) {
	names := slices.Sorted(maps.Keys(s.GetServiceInfo()))
	if len(names) == 0 {
		return nil, fmt.Errorf("this server has no services on it")
	}

	vs := make([]*vanguard.Service, 0, len(names))
	for _, name := range names {
		vs = append(vs, vanguard.NewService(name, s,
			vanguard.WithTargetProtocols(vanguard.ProtocolGRPC),
			vanguard.WithTargetCodecs(vanguard.CodecProto),
		))
	}

	t, err := vanguard.NewTranscoder(vs)
	if err != nil {
		return nil, fmt.Errorf("transcode: %w", err)
	}

	return t, nil
}

// Cors answers the preflight and says what a page may send and read.
//
// `is` decides which origins, and nil is none -- which is not "nothing works":
// a page served from the same origin as this listener makes no cross-origin
// request and needs no header from here. What it means is that a UI on a
// **different** origin, which is every `npm run dev`, has to be named.
//
// The header lists are `connectrpc.com/cors`'s rather than written out, with
// `Authorization` added because that is where payday reads a credential from.
// A list written out here is one that is right on the day it is written: a
// protocol adds a header, every app that copied the list refuses it, and the
// symptom is one browser failing on one call.
func Cors(is func(origin string) bool, next http.Handler) http.Handler {
	headers := strings.Join(append(cors.AllowedHeaders(), auth.Header), ", ")
	methods := strings.Join(cors.AllowedMethods(), ", ")
	expose := strings.Join(cors.ExposedHeaders(), ", ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && is != nil && is(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", expose)

			// The answer depends on who asked, so a cache in front of this must
			// not serve one origin's answer to another.
			h.Add("Vary", "Origin")

			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Access-Control-Max-Age", "7200")
				w.WriteHeader(http.StatusNoContent)

				return
			}
		}
		if r.Method == http.MethodOptions {
			// A preflight from an origin nobody named. Answering it without the
			// headers is what a refusal looks like to a browser, and it is
			// better than 404: the console then says the origin was not
			// allowed, rather than that the endpoint is not there.
			w.WriteHeader(http.StatusNoContent)

			return
		}

		next.ServeHTTP(w, r)
	})
}
