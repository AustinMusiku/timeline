// Package record defines what a run of timeline measured. Collectors produce
// records and renderers consume them.
package record

import (
	"net/http"
	"time"
)

// Run is every request from one invocation, placed on a shared time axis
// that begins at Started.
type Run struct {
	Started time.Time
	Entries []Entry
}

// Entry is one request and what happened to it. It follows the layout of a HAR entry.
type Entry struct {
	// Seq is the request's position in the sequence and Round is which
	// repeat of the sequence it belongs to. Both are zero-based.
	Seq   int
	Round int
	// Hop is how many redirects led to this request. Zero is the request
	// as it was given.
	Hop int

	// Started is the wall-clock time the request began.
	Started time.Time
	// Offset is measured from Run.Started on the monotonic clock,
	// which makes it the one to draw bars with.
	Offset time.Duration

	Request  Request
	Response *Response // nil when the request failed before a response arrived
	Timings  Timings
	Conn     Conn
	Dial     Dial
	// Attempts is how many times the request asked for a connection. More
	// than one means it was retried, and the earlier tries are in Blocked.
	Attempts int

	// Error is empty on success. A failed entry keeps the timings of the
	// phases it got through.
	Error string
}

type Request struct {
	Method string
	URL    string
}

type Response struct {
	Status int
	Proto  string // "HTTP/1.1", "HTTP/2.0"
	Header http.Header
	// BodySize counts the body bytes as the server sent them, before any
	// content decoding.
	BodySize int64
}

// Timings splits a request into consecutive and non-overlapping phases.
// A bar is drawn by laying them end to end. A phase that did not happen is zero.
//
// Connect is the TCP handshake alone. HAR folds TLS into its connect, so
// HAR's connect = Connect+TLS.
type Timings struct {
	Blocked time.Duration // before any network activity, such as waiting for a free connection
	DNS     time.Duration
	Connect time.Duration
	TLS     time.Duration
	Send    time.Duration // writing the request
	Wait    time.Duration // request written, waiting for the first response byte
	Receive time.Duration // first response byte to the end of the body
}

// Total is the duration of the whole request.
func (t Timings) Total() time.Duration {
	return t.Blocked + t.DNS + t.Connect + t.TLS + t.Send + t.Wait + t.Receive
}

// Conn describes the connection a request went out on.
type Conn struct {
	// ID numbers the connections of a run from 1, in the order they were
	// first used. Zero means no connection was made.
	ID int
	// Reused marks a warm request, one sent on a connection that an earlier
	// request opened. DNS, Connect and TLS are zero in these cases.
	Reused     bool
	RemoteAddr string // ip:port of the server, empty if no connection was made
	LocalAddr  string // ip:port on this machine

	TLSVersion string // "TLS 1.3", empty on a plain connection
	// TLSResumed means the handshake that opened the connection resumed an
	// earlier session.
	TLSResumed bool
	ALPN       string // "h2", "http/1.1", empty if none was negotiated
}

// Dial is the lookup and connects a request made to open its connection.
// It is zero for a warm request.
type Dial struct {
	Resolved []string // IPs the lookup returned, empty if there was none
	Connects int      // addresses a connect was started to
}
