package collect

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AustinMusiku/timeline/internal/record"
)

func hello(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "hello") }

// transport returns a transport that trusts srv's certificate.
func transport(srv *httptest.Server) *http.Transport {
	return srv.Client().Transport.(*http.Transport).Clone()
}

func TestTimings(t *testing.T) {
	t0 := time.Now()
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	const ms = time.Millisecond

	tests := []struct {
		name string
		m    *marks
		end  int
		want record.Timings
	}{
		{
			name: "cold https",
			m:    &marks{dns: at(10), connect: at(30), tls: at(50), gotConn: at(90), wrote: at(91), firstByte: at(150)},
			end:  200,
			want: record.Timings{Blocked: 10 * ms, DNS: 20 * ms, Connect: 20 * ms, TLS: 40 * ms, Send: 1 * ms, Wait: 59 * ms, Receive: 50 * ms},
		},
		{
			name: "cold http to an IP address",
			m:    &marks{connect: at(1), gotConn: at(20), wrote: at(21), firstByte: at(60)},
			end:  61,
			want: record.Timings{Blocked: 1 * ms, Connect: 19 * ms, Send: 1 * ms, Wait: 39 * ms, Receive: 1 * ms},
		},
		{
			name: "warm",
			m:    &marks{gotConn: at(1), wrote: at(2), firstByte: at(40), conn: record.Conn{Reused: true}},
			end:  45,
			want: record.Timings{Blocked: 1 * ms, Send: 1 * ms, Wait: 38 * ms, Receive: 5 * ms},
		},
		{
			name: "warm after starting a dial it did not use",
			m:    &marks{dns: at(1), connect: at(2), gotConn: at(5), wrote: at(6), firstByte: at(20), conn: record.Conn{Reused: true}},
			end:  25,
			want: record.Timings{Blocked: 5 * ms, Send: 1 * ms, Wait: 14 * ms, Receive: 5 * ms},
		},
		{
			name: "connect never completes",
			m:    &marks{dns: at(5), connect: at(15)},
			end:  1000,
			want: record.Timings{Blocked: 5 * ms, DNS: 10 * ms, Connect: 985 * ms},
		},
		{
			name: "fails before any network activity",
			m:    &marks{},
			end:  3,
			want: record.Timings{Blocked: 3 * ms},
		},
		{
			name: "response arrives before the request is written",
			m:    &marks{gotConn: at(10), wrote: at(50), firstByte: at(30)},
			end:  60,
			want: record.Timings{Blocked: 10 * ms, Send: 40 * ms, Receive: 10 * ms},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.m.start = at(0)
			got := tc.m.timings(at(tc.end))
			if got != tc.want {
				t.Errorf("timings:\n got %+v\nwant %+v", got, tc.want)
			}
			if total := time.Duration(tc.end) * ms; got.Total() != total {
				t.Errorf("phases add up to %v, want %v", got.Total(), total)
			}
		})
	}
}

func TestRetryStartsThePhasesOver(t *testing.T) {
	conn, _ := net.Pipe()
	defer conn.Close()

	m := &marks{start: time.Now()}
	tr := m.trace()

	// A reused connection that turns out to be stale.
	tr.GetConn("")
	tr.GotConn(httptrace.GotConnInfo{Conn: conn, Reused: true})
	tr.WroteRequest(httptrace.WroteRequestInfo{})

	// The retry on a new one.
	tr.GetConn("")
	if !m.gotConn.IsZero() || !m.wrote.IsZero() || m.conn.Reused {
		t.Fatal("the stale attempt survived the retry")
	}
	tr.ConnectStart("tcp", "")
	tr.GotConn(httptrace.GotConnInfo{Conn: conn})
	tr.TLSHandshakeStart() // a dial that lost the race

	if _, c := m.finish(); c.Reused {
		t.Error("request is marked reused, but the retry dialled")
	}
	if m.connect.IsZero() {
		t.Error("the retry's connect was not recorded")
	}
	if !m.tls.IsZero() {
		t.Error("recorded a dial event from after the connection was handed over")
	}

	tr.GetConn("")
	if m.gotConn.IsZero() {
		t.Error("a hook changed the marks after finish")
	}
}

func TestColdThenWarm(t *testing.T) {
	tests := []struct {
		name    string
		tls, h2 bool
		proto   string
	}{
		{name: "http", proto: "HTTP/1.1"},
		{name: "https", tls: true, proto: "HTTP/1.1"},
		{name: "h2", tls: true, h2: true, proto: "HTTP/2.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewUnstartedServer(http.HandlerFunc(hello))
			srv.EnableHTTP2 = tc.h2
			if tc.tls {
				srv.StartTLS()
			} else {
				srv.Start()
			}
			defer srv.Close()

			got, err := run(t.Context(), transport(srv), []Request{{URL: srv.URL}}, Options{Repeat: 2})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Entries) != 2 {
				t.Fatalf("got %d entries, want 2", len(got.Entries))
			}

			for i, e := range got.Entries {
				if e.Error != "" {
					t.Fatalf("entry %d failed: %s", i, e.Error)
				}
				if r := e.Response; r.Status != 200 || r.Proto != tc.proto || r.BodySize != 5 {
					t.Errorf("entry %d: got %d %s with %d body bytes, want 200 %s with 5", i, r.Status, r.Proto, r.BodySize, tc.proto)
				}
				if want := srv.Listener.Addr().String(); e.Conn.RemoteAddr != want {
					t.Errorf("entry %d: remote address %q, want %q", i, e.Conn.RemoteAddr, want)
				}
				if e.Seq != 0 || e.Round != i {
					t.Errorf("entry %d: seq %d round %d, want seq 0 round %d", i, e.Seq, e.Round, i)
				}
			}

			cold, warm := got.Entries[0], got.Entries[1]
			if cold.Conn.Reused || cold.Timings.Connect <= 0 {
				t.Errorf("cold request: reused=%v connect=%v, want a new connection", cold.Conn.Reused, cold.Timings.Connect)
			}
			if (cold.Timings.TLS > 0) != tc.tls {
				t.Errorf("cold request: tls=%v on a server with tls=%v", cold.Timings.TLS, tc.tls)
			}
			if wt := warm.Timings; !warm.Conn.Reused || wt.DNS+wt.Connect+wt.TLS != 0 {
				t.Errorf("warm request: reused=%v timings=%+v, want a reused connection and no dial", warm.Conn.Reused, wt)
			}
			if warm.Offset < cold.Offset+cold.Timings.Total() {
				t.Errorf("warm request started at %v, before the cold one ended at %v", warm.Offset, cold.Offset+cold.Timings.Total())
			}
		})
	}
}

func TestCompressedBodyIsMeasuredAsSent(t *testing.T) {
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	io.WriteString(zw, strings.Repeat("timeline ", 200))
	zw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			http.Error(w, "gzip was not offered", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(body.Bytes())
	}))
	defer srv.Close()

	got, err := run(t.Context(), transport(srv), []Request{{URL: srv.URL}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := got.Entries[0].Response
	if r == nil {
		t.Fatalf("request failed: %s", got.Entries[0].Error)
	}
	if r.Status != 200 || r.BodySize != int64(body.Len()) || r.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("got %d, %d body bytes, Content-Encoding %q; want 200, %d, gzip",
			r.Status, r.BodySize, r.Header.Get("Content-Encoding"), body.Len())
	}
}

func TestFailedRequestDoesNotStopTheRun(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + ln.Addr().String()
	ln.Close()

	srv := httptest.NewServer(http.HandlerFunc(hello))
	defer srv.Close()

	got, err := run(t.Context(), transport(srv), []Request{{URL: refused}, {URL: srv.URL}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(got.Entries))
	}
	if e := got.Entries[0]; e.Error == "" || e.Response != nil {
		t.Errorf("refused request: error %q, response %v; want an error and no response", e.Error, e.Response)
	} else if strings.Contains(e.Error, refused) {
		t.Errorf("error %q repeats the URL", e.Error)
	}
	if e := got.Entries[1]; e.Error != "" {
		t.Errorf("request after the failure: %s", e.Error)
	}
}

func TestTimeoutLandsInThePhaseItInterrupted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	const timeout = 200 * time.Millisecond
	got, err := run(t.Context(), transport(srv), []Request{{URL: srv.URL}}, Options{Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	e := got.Entries[0]
	if e.Error == "" || e.Response != nil {
		t.Fatalf("error %q, response %v; want an error and no response", e.Error, e.Response)
	}
	if e.Timings.Total() < timeout || e.Timings.Wait < timeout/2 {
		t.Errorf("timings %+v, want nearly all of %v spent in Wait", e.Timings, timeout)
	}
}

func TestRedirectIsRecordedNotFollowed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	got, err := run(t.Context(), transport(srv), []Request{{URL: srv.URL}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := got.Entries[0].Response
	if r == nil {
		t.Fatalf("request failed: %s", got.Entries[0].Error)
	}
	if r.Status != http.StatusFound || r.Header.Get("Location") != "/elsewhere" {
		t.Errorf("got %d to %q, want 302 to /elsewhere", r.Status, r.Header.Get("Location"))
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

func TestConcurrentRequestsOverlap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		hello(w, r)
	}))
	defer srv.Close()

	got, err := run(t.Context(), transport(srv), []Request{{URL: srv.URL}, {URL: srv.URL}}, Options{Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	first, second := got.Entries[0], got.Entries[1]
	if first.Error != "" || second.Error != "" {
		t.Fatalf("requests failed: %q, %q", first.Error, second.Error)
	}
	if second.Seq != 1 {
		t.Errorf("second entry has seq %d, want 1", second.Seq)
	}
	if end := first.Offset + first.Timings.Total(); second.Offset >= end {
		t.Errorf("second request started at %v, after the first ended at %v", second.Offset, end)
	}
}

func TestCancelReturnsWhatWasCollected(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()

	u := Request{URL: srv.URL}
	got, err := run(ctx, transport(srv), []Request{u, u, u}, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want context.Canceled", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Error == "" {
		t.Errorf("got %d entries, want only the interrupted one, marked failed: %+v", len(got.Entries), got.Entries)
	}
}

func TestRejectsASequenceItCannotSend(t *testing.T) {
	tests := []Request{
		{URL: "example.com"},
		{URL: "ftp://example.com"},
		{URL: "http://"},
		{URL: "://"},
		{Method: "NOT A METHOD", URL: "http://example.com"},
	}
	for _, r := range tests {
		got, err := Run(t.Context(), []Request{r}, Options{})
		if err == nil || got != nil {
			t.Errorf("%+v: got run %v and error %v, want only an error", r, got, err)
		}
	}
}
