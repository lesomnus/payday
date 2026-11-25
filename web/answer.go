package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"connectrpc.com/vanguard"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/grpcx"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// readable is the transcoder's JSON codec, with what it cannot read said to be
// the caller's.
//
// The codec is vanguard's own, protojson with unknown fields discarded, and
// only its refusal changes. As vanguard has it, a body that is not JSON, or
// JSON that is not the request -- `[]`, a string where a number goes -- is an
// error with no code, which it answers as `unknown`, 500: a page told the
// server failed, for a request the page got wrong. A Connect error with
// `invalid_argument` in it is 400, what every other request that is wrong is
// answered.
func readable(res vanguard.TypeResolver) vanguard.Codec {
	return readableJSON{vanguard.NewJSONCodec(res)}
}

type readableJSON struct{ *vanguard.JSONCodec }

func (c readableJSON) Unmarshal(b []byte, m proto.Message) error {
	if err := c.JSONCodec.Unmarshal(b, m); err != nil {
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("the body is not JSON of %s: %w", m.ProtoReflect().Descriptor().FullName(), err))
	}

	return nil
}

// answered is the transcoder, with what it answers that is not an answer
// written as the JSON every other failure is.
//
// Three kinds, and none is a status a handler chose:
//
//   - the transcoder's own refusals before any handler -- a method the service
//     does not have, a protocol or a content type it does not speak, an HTTP
//     method the call does not take -- which it writes as a line of text, `Not
//     Found`, where a Connect client reads the body for its code. A content
//     type it does not speak it says as `Not Found` too, which for a method the
//     server has is not so: that is 415;
//   - a message larger than the server takes, which gRPC refuses as
//     `ResourceExhausted` and the transcoder answers 429, the rate limit's
//     status. A client told 429 waits and asks again, and the same request is
//     refused again however long it waits: it is 413, and the code in the body
//     stays `resource_exhausted`, as a gRPC caller of the same request is told;
//   - a protobuf body that does not parse. The transcoder hands a protobuf body
//     to gRPC as it came, and grpc-go answers what it cannot read `Internal`, a
//     server failing: it is the caller's, 400 `invalid_argument`, as a JSON body
//     that does not read is ([readable]).
//
// A message too large is told by grpc-go's sentence, the one thing that tells
// it from a rate limit; TestWhatCannotBeReadIsTheCallers holds the words, so a
// grpc-go that changes them fails there. A body that does not parse is told by
// payday's own codec's words instead ([grpcx.Unparsed]): grpc-go's sentence
// around them is not the same from one release to the next.
func answered(h http.Handler, at map[string]grpc.ServiceInfo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := &answer{ResponseWriter: w, r: r, rpc: true, known: method(at, r.URL.Path)}
		h.ServeHTTP(a, r)
		a.finish()
	})
}

// answer passes a response through, but for one that is a failure written as
// text, or gRPC's refusal of a message too large, which it holds and writes
// again in [answer.finish].
type answer struct {
	http.ResponseWriter
	r *http.Request

	// rpc is whether this is the transcoder's answer, where a 429 may be a
	// message too large and a 500 a body gRPC could not read.
	rpc bool

	// known is whether the path names a method this server has, so that a
	// `Not Found` for it is a content type and not a route.
	known bool

	// held is the status of a response being held, and 0 for one that is
	// passed through.
	held int
	body bytes.Buffer

	passed bool
}

func (a *answer) WriteHeader(code int) {
	if a.passed || a.held != 0 {
		a.ResponseWriter.WriteHeader(code)
		return
	}

	ct := a.Header().Get("Content-Type")
	switch {
	case code >= 400 && strings.HasPrefix(ct, "text/plain"):
		a.held = code
	case a.rpc && (code == http.StatusTooManyRequests || code == http.StatusInternalServerError) && strings.HasPrefix(ct, "application/json"):
		a.held = code
	default:
		a.passed = true
		a.ResponseWriter.WriteHeader(code)
	}
}

func (a *answer) Write(b []byte) (int, error) {
	if !a.passed && a.held == 0 {
		a.WriteHeader(http.StatusOK)
	}
	if a.held != 0 {
		return a.body.Write(b)
	}

	return a.ResponseWriter.Write(b)
}

// Flush is the writer's, for a stream; a response held is not one.
func (a *answer) Flush() {
	if a.held != 0 {
		return
	}
	if f, ok := a.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (a *answer) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// finish writes what was held.
func (a *answer) finish() {
	if a.held == 0 {
		return
	}

	switch a.held {
	case http.StatusTooManyRequests:
		if max, ok := tooLarge(a.body.Bytes()); ok {
			a.fail(http.StatusRequestEntityTooLarge, connect.CodeResourceExhausted,
				fmt.Sprintf("the request is larger than this server takes, %s bytes", max))
			return
		}

		// The rate limit's, or a handler's: as it was.
		a.ResponseWriter.WriteHeader(a.held)
		_, _ = a.ResponseWriter.Write(a.body.Bytes())

	case http.StatusInternalServerError:
		if why, ok := unread(a.body.Bytes()); ok {
			a.fail(http.StatusBadRequest, connect.CodeInvalidArgument,
				"the body is not a protobuf of "+why)
			return
		}

		// A handler's, or the server's: as it was.
		a.ResponseWriter.WriteHeader(a.held)
		_, _ = a.ResponseWriter.Write(a.body.Bytes())

	case http.StatusNotFound:
		if a.known {
			a.fail(http.StatusUnsupportedMediaType, connect.CodeInvalidArgument,
				fmt.Sprintf("%s does not take a body of %q", a.r.URL.Path, a.r.Header.Get("Content-Type")))
			return
		}

		a.fail(http.StatusNotFound, connect.CodeUnimplemented,
			fmt.Sprintf("%s is not a route of this server", a.r.URL.Path))

	case http.StatusMethodNotAllowed:
		msg := fmt.Sprintf("%s does not take %s", a.r.URL.Path, a.r.Method)
		if allow := a.Header().Get("Allow"); allow != "" {
			msg += "; it takes " + allow
		}
		a.fail(http.StatusMethodNotAllowed, connect.CodeUnimplemented, msg)

	default:
		a.fail(a.held, connect.CodeUnknown, strings.TrimSpace(a.body.String()))
	}
}

// fail writes the error as Connect writes one: `{"code": ..., "message": ...}`,
// which is what a page's client reads its code from.
func (a *answer) fail(status int, code connect.Code, msg string) {
	h := a.Header()
	h.Del("Content-Length")
	h.Del("X-Content-Type-Options")
	h.Set("Content-Type", "application/json")

	b, _ := json.Marshal(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code.String(), msg})

	a.ResponseWriter.WriteHeader(status)
	_, _ = a.ResponseWriter.Write(b)
}

// input is the name of the request a path's method takes, as the refusal of a
// JSON body names it ([readable]), or the path where it is not known.
func input(path string) string {
	service, m, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return path
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return path
	}
	md := sd.Methods().ByName(protoreflect.Name(m))
	if md == nil {
		return path
	}

	return string(md.Input().FullName())
}

// unread is whether a Connect error is the refusal of a request the server
// could not unmarshal, and of which message and why, in the protobuf library's
// words.
func unread(body []byte) (string, bool) {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.Code != connect.CodeInternal.String() {
		return "", false
	}

	// Found wherever it is: what grpc-go writes around it is one release's
	// sentence, and the codec's words are payday's. See [grpcx.Unparsed].
	_, why, ok := strings.Cut(e.Message, grpcx.Unparsed)
	return why, ok
}

// tooLarge is whether a Connect error is gRPC's refusal of a message larger
// than the server takes, and the largest it takes.
//
// Read off gRPC's sentence, which is the one thing that tells it from the rate
// limit's: both are `ResourceExhausted`. TestWhatCannotBeReadIsTheCallers
// holds it to grpc-go's words, so a release that changes them fails there.
func tooLarge(body []byte) (string, bool) {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.Code != connect.CodeResourceExhausted.String() {
		return "", false
	}

	for _, prefix := range []string{
		"grpc: received message larger than max (",
		"grpc: received message after decompression larger than max (",
	} {
		rest, ok := strings.CutPrefix(e.Message, prefix)
		if !ok {
			continue
		}

		// "(5242885 vs. 4194304)": what came, and the most it takes.
		_, max, _ := strings.Cut(strings.TrimSuffix(rest, ")"), " vs. ")
		return max, true
	}

	return "", false
}
