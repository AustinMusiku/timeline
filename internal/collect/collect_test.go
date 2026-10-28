package collect

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"slices"
	"strings"
	"sync"
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

	m := &marks{start: time.Now(), seen: &conns{}}
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

	var e record.Entry
	m.finish(&e)
	if e.Conn.Reused {
		t.Error("request is marked reused, but the retry dialled")
	}
	if e.Attempts != 2 || e.Dial.Connects != 1 {
		t.Errorf("got %d attempts and %d connects, want 2 and 1", e.Attempts, e.Dial.Connects)
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
		name       string
		tls, h2    bool
		proto      string
		tlsVersion string
		alpn       string
	}{
		{name: "http", proto: "HTTP/1.1"},
		// The test client offers no protocols unless it is set up for h2.
		{name: "https", tls: true, proto: "HTTP/1.1", tlsVersion: "TLS 1.3"},
		{name: "h2", tls: true, h2: true, proto: "HTTP/2.0", tlsVersion: "TLS 1.3", alpn: "h2"},
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
				if e.Conn.ID != 1 {
					t.Errorf("entry %d: connection id %d, want 1", i, e.Conn.ID)
				}
				if want := srv.Listener.Addr().String(); e.Conn.LocalAddr == "" || e.Conn.LocalAddr == want {
					t.Errorf("entry %d: local address %q, want this end of the connection to %s", i, e.Conn.LocalAddr, want)
				}
				if e.Attempts != 1 || e.Dial.Connects != 1-i {
					t.Errorf("entry %d: %d attempts and %d connects, want 1 and %d", i, e.Attempts, e.Dial.Connects, 1-i)
				}
				if len(e.Dial.Resolved) != 0 {
					t.Errorf("entry %d: resolved %v, but the URL has an IP address", i, e.Dial.Resolved)
				}
				if c := e.Conn; c.TLSVersion != tc.tlsVersion || c.ALPN != tc.alpn || c.TLSResumed {
					t.Errorf("entry %d: tls %q, alpn %q, resumed %v; want %q, %q and a full handshake", i, c.TLSVersion, c.ALPN, c.TLSResumed, tc.tlsVersion, tc.alpn)
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

func TestConnectionsAreNumbered(t *testing.T) {
	tests := []struct {
		name string
		h2   bool
		want int // connections the warm round needs
	}{
		{name: "http/1.1 takes one request at a time", want: 3},
		{name: "h2 shares one connection", h2: true, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(50 * time.Millisecond)
				hello(w, r)
			}))
			srv.EnableHTTP2 = tc.h2
			srv.StartTLS()
			defer srv.Close()

			u := Request{URL: srv.URL}
			got, err := run(t.Context(), transport(srv), []Request{u, u, u}, Options{Repeat: 2, Concurrency: 3})
			if err != nil {
				t.Fatal(err)
			}

			cold, warm := map[int]bool{}, map[int]bool{}
			for i, e := range got.Entries {
				if e.Error != "" {
					t.Fatalf("entry %d failed: %s", i, e.Error)
				}
				if e.Conn.ID < 1 {
					t.Errorf("entry %d: connection id %d, want 1 or more", i, e.Conn.ID)
				}
				if e.Round == 0 {
					cold[e.Conn.ID] = true
				} else {
					warm[e.Conn.ID] = true
				}
			}
			if len(warm) != tc.want {
				t.Errorf("warm round used %d connections, want %d", len(warm), tc.want)
			}
			for id := range warm {
				if !cold[id] {
					t.Errorf("warm round used connection %d, which the cold round did not", id)
				}
			}
		})
	}
}

func TestLookupBelongsToTheRequestThatMadeIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(hello))
	defer srv.Close()
	ip, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	got, err := run(t.Context(), transport(srv), []Request{{URL: "http://localhost:" + port}}, Options{Repeat: 2})
	if err != nil {
		t.Fatal(err)
	}
	cold, warm := got.Entries[0], got.Entries[1]
	if cold.Error != "" || warm.Error != "" {
		t.Fatalf("requests failed: %q, %q", cold.Error, warm.Error)
	}
	if !slices.Contains(cold.Dial.Resolved, ip) {
		t.Errorf("cold request resolved %v, want it to include %s", cold.Dial.Resolved, ip)
	}
	if len(warm.Dial.Resolved) != 0 {
		t.Errorf("warm request resolved %v, but made no lookup", warm.Dial.Resolved)
	}
}

func TestRedialResumesTheTLSSession(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		hello(w, r)
	}))
	defer srv.Close()

	got, err := run(t.Context(), transport(srv), []Request{{URL: srv.URL}}, Options{Repeat: 2})
	if err != nil {
		t.Fatal(err)
	}
	first, second := got.Entries[0], got.Entries[1]
	if first.Error != "" || second.Error != "" {
		t.Fatalf("requests failed: %q, %q", first.Error, second.Error)
	}
	if first.Conn.TLSResumed {
		t.Error("the first handshake of the run resumed a session")
	}
	if c := second.Conn; c.Reused || c.ID != 2 || !c.TLSResumed {
		t.Errorf("second request: reused=%v id=%d resumed=%v, want a new connection 2 that resumed", c.Reused, c.ID, c.TLSResumed)
	}
	if second.Timings.TLS <= 0 {
		t.Errorf("second request: tls=%v, want a handshake", second.Timings.TLS)
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
	if e := got.Entries[0]; e.Error == "" || e.Response != nil || e.Conn.ID != 0 {
		t.Errorf("refused request: error %q, response %v, connection %d; want an error, no response and no connection", e.Error, e.Response, e.Conn.ID)
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

func TestRedirectsAreFollowedHopByHop(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(hello))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/next", http.StatusMovedPermanently)
		case "/next":
			http.Redirect(w, r, other.URL+"/done", http.StatusFound)
		}
	}))
	defer srv.Close()

	seen := 0
	opts := Options{MaxRedirects: 10, OnEntry: func(record.Entry) { seen++ }}
	got, err := run(t.Context(), transport(srv), []Request{{URL: srv.URL}, {URL: other.URL}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		seq, hop, status int
		url              string
	}{
		{0, 0, 301, srv.URL},
		{0, 1, 302, srv.URL + "/next"},
		{0, 2, 200, other.URL + "/done"},
		{1, 0, 200, other.URL},
	}
	if len(got.Entries) != len(want) || seen != len(want) {
		t.Fatalf("got %d entries and saw %d, want %d: %+v", len(got.Entries), seen, len(want), got.Entries)
	}
	for i, w := range want {
		e := got.Entries[i]
		if e.Error != "" || e.Response == nil {
			t.Fatalf("entry %d failed: %s", i, e.Error)
		}
		if e.Seq != w.seq || e.Hop != w.hop || e.Response.Status != w.status || e.Request.URL != w.url {
			t.Errorf("entry %d is seq %d hop %d, %d from %s; want seq %d hop %d, %d from %s",
				i, e.Seq, e.Hop, e.Response.Status, e.Request.URL, w.seq, w.hop, w.status, w.url)
		}
		if i > 0 {
			prev := got.Entries[i-1]
			if end := prev.Offset + prev.Timings.Total(); e.Offset < end {
				t.Errorf("entry %d started at %v, before entry %d ended at %v", i, e.Offset, i-1, end)
			}
		}
	}
	if a, b := got.Entries[1].Conn.ID, got.Entries[2].Conn.ID; a == b {
		t.Errorf("the hop to another server reused connection #%d", a)
	}
}

func TestRedirectsStopAtTheLimit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer srv.Close()

	got, err := run(t.Context(), transport(srv), []Request{{URL: srv.URL}}, Options{MaxRedirects: 2})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.Entries); n != 3 || hits.Load() != 3 {
		t.Fatalf("got %d entries and the server saw %d requests, want 3 of each", n, hits.Load())
	}
	for i, e := range got.Entries {
		want := ""
		if i == 2 {
			want = "stopped after 2 redirects"
		}
		if e.Response == nil || e.Response.Status != http.StatusFound || e.Error != want {
			t.Errorf("entry %d has response %v and error %q, want a 302 and %q", i, e.Response, e.Error, want)
		}
	}
}

func TestRedirect(t *testing.T) {
	header := http.Header{"Authorization": {"Bearer x"}, "Cookie": {"a=1"}, "Accept": {"text/html"}}
	tests := []struct {
		name     string
		method   string
		status   int
		location string
		want     string // method and URL, empty for no redirect
		creds    bool
	}{
		{"a relative location", "GET", 302, "z?q=1", "GET https://a.example/x/z?q=1", true},
		{"303 turns a POST into a GET", "POST", 303, "/done", "GET https://a.example/done", true},
		{"307 keeps the method", "POST", 307, "/again", "POST https://a.example/again", true},
		{"301 keeps a HEAD", "HEAD", 301, "/moved", "HEAD https://a.example/moved", true},
		{"another host gets no credentials", "GET", 308, "https://b.example/", "GET https://b.example/", false},
		{"no location", "GET", 302, "", "", false},
		{"not a redirect", "GET", 201, "/made", "", false},
		{"304 is not a redirect", "GET", 304, "/x", "", false},
		{"a scheme it cannot send", "GET", 302, "ftp://a.example/", "", false},
	}
	for _, tc := range tests {
		req, err := newRequest(Request{Method: tc.method, URL: "https://a.example/x/y", Header: header})
		if err != nil {
			t.Fatal(err)
		}
		resp := &record.Response{Status: tc.status, Header: http.Header{}}
		if tc.location != "" {
			resp.Header.Set("Location", tc.location)
		}

		next := redirect(req, resp)
		got := ""
		if next != nil {
			got = next.Method + " " + next.URL.String()
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
			continue
		}
		if next == nil {
			continue
		}
		if creds := next.Header.Get("Authorization") != "" && next.Header.Get("Cookie") != ""; creds != tc.creds {
			t.Errorf("%s: credentials sent is %v, want %v", tc.name, creds, tc.creds)
		}
		if next.Header.Get("Accept") != "text/html" {
			t.Errorf("%s: the Accept header was dropped", tc.name)
		}
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

func TestOnEntrySeesEachEntryAsItFinishes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(100 * time.Millisecond)
		}
		hello(w, r)
	}))
	defer srv.Close()

	var seen []record.Entry
	opts := Options{Repeat: 2, Concurrency: 2, OnEntry: func(e record.Entry) { seen = append(seen, e) }}
	got, err := run(t.Context(), transport(srv), []Request{{URL: srv.URL + "/slow"}, {URL: srv.URL}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(got.Entries) {
		t.Fatalf("saw %d entries, want %d", len(seen), len(got.Entries))
	}
	// The slow request is sent first and finishes last, in both rounds.
	for i, e := range seen {
		if e.Error != "" {
			t.Fatalf("request failed: %s", e.Error)
		}
		if e.Seq != 1-i%2 || e.Round != i/2 {
			t.Errorf("entry %d is seq %d of round %d, want seq %d of round %d", i, e.Seq, e.Round, 1-i%2, i/2)
		}
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
		{URL: "ftp://me:secret@example.com"},
		{URL: "http://me:secret@example.com/%zz"},
	}
	for _, r := range tests {
		got, err := Run(t.Context(), []Request{r}, Options{})
		if err == nil || got != nil {
			t.Errorf("%+v: got run %v and error %v, want only an error", r, got, err)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%+v: error %q gives the password away", r, err)
		}
	}
}

func TestBodyIsSentWithEverySend(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, fmt.Sprintf("%s %d %s", r.Method, r.ContentLength, b))
		mu.Unlock()
	}))
	defer srv.Close()

	req := Request{Method: "POST", URL: srv.URL, Body: []byte("a=1")}
	got, err := run(t.Context(), transport(srv), []Request{req, req}, Options{Repeat: 2, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got.Entries {
		if e.Error != "" {
			t.Fatalf("request failed: %s", e.Error)
		}
	}
	want := []string{"POST 3 a=1", "POST 3 a=1", "POST 3 a=1", "POST 3 a=1"}
	if !slices.Equal(bodies, want) {
		t.Errorf("server saw %q, want %q", bodies, want)
	}
}

func TestRedirectsKeepOrDropTheBody(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/303":
			http.Redirect(w, r, "/echo", http.StatusSeeOther)
		case "/307":
			http.Redirect(w, r, "/echo", http.StatusTemporaryRedirect)
		case "/echo":
			b, _ := io.ReadAll(r.Body)
			got = append(got, fmt.Sprintf("%s %q %q", r.Method, r.Header.Get("Content-Type"), b))
		}
	}))
	defer srv.Close()

	header := http.Header{"Content-Type": {"text/plain"}}
	reqs := []Request{
		{Method: "POST", URL: srv.URL + "/303", Header: header, Body: []byte("hi")},
		{Method: "POST", URL: srv.URL + "/307", Header: header, Body: []byte("hi")},
	}
	res, err := run(t.Context(), transport(srv), reqs, Options{MaxRedirects: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`GET "" ""`, `POST "text/plain" "hi"`}
	if !slices.Equal(got, want) {
		t.Errorf("the hops arrived as %q, want %q", got, want)
	}
	var sizes []int64
	for _, e := range res.Entries {
		sizes = append(sizes, e.Request.BodySize)
	}
	if want := []int64{2, 0, 2, 2}; !slices.Equal(sizes, want) {
		t.Errorf("recorded body sizes %v, want %v", sizes, want)
	}
}

// A connection the server drops after reading a request is retried on a new
// one, which needs the body again.
func TestRetrySendsTheBodyAgain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	bodies := make(chan string, 3)
	go func() {
		for n := 0; ; n++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				rd := bufio.NewReader(conn)
				for i := 0; ; i++ {
					req, err := http.ReadRequest(rd)
					if err != nil {
						return
					}
					b, _ := io.ReadAll(req.Body)
					bodies <- string(b)
					// The first connection drops the second request it gets.
					if n == 0 && i == 1 {
						return
					}
					io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
				}
			}()
		}
	}()

	// The key tells the client it may send the POST again.
	req := Request{
		Method: "POST", URL: "http://" + ln.Addr().String(),
		Header: http.Header{"Idempotency-Key": {"1"}}, Body: []byte("again"),
	}
	got, err := run(t.Context(), http.DefaultTransport.(*http.Transport).Clone(), []Request{req, req}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if e := got.Entries[1]; e.Error != "" || e.Attempts != 2 {
		t.Fatalf("second request: error %q after %d attempts, want success after 2", e.Error, e.Attempts)
	}
	// Two sends and the retry. The channel stays open for the server's goroutines.
	for range 3 {
		select {
		case b := <-bodies:
			if b != "again" {
				t.Errorf("server read the body %q, want again", b)
			}
		case <-time.After(time.Second):
			t.Fatal("server read fewer than 3 bodies")
		}
	}
}

func TestTLSConfigIsUsedAndLeftAlone(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(hello))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	cfg := &tls.Config{RootCAs: roots}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	got, err := run(t.Context(), tr, []Request{{URL: srv.URL}}, Options{TLS: cfg})
	if err != nil {
		t.Fatal(err)
	}
	e := got.Entries[0]
	if e.Error != "" {
		t.Fatalf("request failed: %s", e.Error)
	}
	// A TLS config of its own must not cost the run HTTP/2.
	if e.Response.Proto != "HTTP/2.0" || e.Conn.ALPN != "h2" {
		t.Errorf("got %s with ALPN %q, want HTTP/2.0 and h2", e.Response.Proto, e.Conn.ALPN)
	}
	if tr.TLSClientConfig.ClientSessionCache == nil {
		t.Error("the run has no TLS session cache")
	}
	if cfg.ClientSessionCache != nil {
		t.Error("the run changed the config it was given")
	}
}
