// Package access is the access record: who read or wrote whose personal data,
// from where, when, and how the call ended (payday#111).
//
// # What it is for
//
// An app that processes personal data is asked to keep a record of each time
// it does -- Korea's PIPA safety measures want the account, the time, where
// the access came from, whose data, and what was done, kept for a year or two,
// reviewed every month, with a reason confirmed for each download -- and the
// trail is not that record. The trail holds writes, and not where they came
// from, and its object is the row that changed rather than the person a page
// showed. A list of people is a hundred people and no write at all.
//
// # What it records, and from what
//
// A row of `Access` for what a call did to somebody's personal data -- wrote
// it, handed it out to be kept elsewhere, or otherwise carried it -- filed
// under the tenant the data was in: a call that wrote one person and showed
// another is a row of each. Whose data a call carried is read off what the
// schema already says (#92) rather than written by each app:
//
//   - the messages of its answer that hold a field declared `personal`, by
//     their keys -- [Config.Subjects], which the generated `pd.Subjects` is;
//   - what the app says it showed where no declaration can see it, a name put
//     into a sentence or a column of a file -- [Saw];
//   - for a write, the rows it wrote -- the generated `pd.AccessRecorder`;
//   - for a refused call, the rows its request named -- [Config.Refs].
//
// A call that carried nobody's data leaves nothing.
//
// # When it is written
//
// A write is recorded in its own transaction, at its commit, as the trail is:
// what holds the write holds its record. A download is recorded before its
// answer leaves, and refused when it cannot be. A read is queued and written a
// second later with the others ([Writer]), which is the one loss there is: a
// process killed outright takes the last second's reads with it. One stopped
// writes what it has first.
package access

import (
	"errors"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// Kind is what a call did to the data it carried.
type Kind string

const (
	Read     Kind = "read"
	Write    Kind = "write"
	Download Kind = "download"
)

// ReasonKey is the metadata a download says what it is for in. A download
// without one is refused; see [Config.Downloads].
//
// Binary metadata, so that a reason is any text a person writes, in any
// language: a header carries ASCII and nothing else, and a browser refuses
// to send one that is not. gRPC carries a `-bin` key base64 and hands it over
// decoded, so a gRPC client sets the text; a client that writes the header
// itself -- Connect from a page, `curl` -- writes it base64, as connect-es's
// `encodeBinaryHeader` does.
const ReasonKey = "payday-reason-bin"

// MaxSubjects is how many subjects one row names: past it, the rest are
// counted and not named. A list of a hundred people names a hundred; an export
// of a hundred thousand says how many it was.
const MaxSubjects = 1000

// Subject is one person whose data a call carried: the key of the row that
// holds it, and the tenant that row is in -- zero for a row behind no wall, or
// one whose tenant the answer did not carry, which is then the call's.
type Subject struct {
	Id     pdid.Id
	Tenant pdid.Id
}

// Record is one row of the access record, as payday decides it. The generated
// `pd.AccessSink` writes it as the app's `Access`.
type Record struct {
	Id pdid.Id

	// Tenant is whose record it is: the tenant the data it carried is in.
	// Actor and ActorTenant are who made the call, as the frame named them.
	Tenant      pdid.Id
	Actor       pdid.Id
	ActorTenant pdid.Id

	Trace  []byte
	Action string
	Kind   Kind

	// Result is `ok`, or the status code the call was refused with.
	Result string

	At time.Time

	// Subjects are at most [MaxSubjects] of them; Count is how many there
	// were.
	Subjects []pdid.Id
	Count    int

	Detail *pdpb.AccessDetail
}

// ErrUnrecorded is a download whose record could not be written: it is
// refused rather than let out unrecorded.
var ErrUnrecorded = errors.New("access: the download could not be recorded")
