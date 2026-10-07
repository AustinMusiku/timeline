// Package collect sends HTTP requests and measures them into records.
package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/AustinMusiku/timeline/internal/record"
)

// Request is one step of a sequence.
type Request struct {
	Method string // GET when empty
	URL    string
	Header http.Header
}

type Options struct {
	// Repeat is how many times to run the whole sequence. Connections stay
	// open between rounds, so the first round is cold and the rest are warm.
	// Values below 1 mean 1.
	Repeat int
	// Concurrency is how many requests of a round may be in flight at once.
	// Values below 1 mean 1, which sends them strictly one after another.
	Concurrency int
	// Timeout bounds each request, body included. Zero means no limit.
	Timeout time.Duration
}

// Run sends reqs in order, Repeat times over, and returns one entry per
// request sent. A request that fails is recorded in its entry and the run
// carries on. Run itself fails only when the sequence cannot be sent at all,
// or when ctx is cancelled, in which case it also returns the entries
// collected up to that point.
//
// Every run starts with no open connections. Redirects are recorded, not
// followed.
func Run(ctx context.Context, reqs []Request, opts Options) (*record.Run, error) {
	return run(ctx, http.DefaultTransport.(*http.Transport).Clone(), reqs, opts)
}

// run is Run on a transport of the caller's choosing, which it takes over.
func run(ctx context.Context, tr *http.Transport, reqs []Request, opts Options) (*record.Run, error) {
	repeat, concurrency := max(opts.Repeat, 1), max(opts.Concurrency, 1)

	tmpls := make([]*http.Request, len(reqs))
	for i, r := range reqs {
		req, err := newRequest(r)
		if err != nil {
			return nil, fmt.Errorf("request %d: %w", i+1, err)
		}
		tmpls[i] = req
	}

	// Left on, the transport would decode gzip itself and hide both the
	// transferred size and the Content-Encoding header.
	tr.DisableCompression = true

	// Without this, connections beyond the default of two per host would be
	// closed after each round and the next round would not be warm.
	tr.MaxIdleConnsPerHost = max(concurrency, http.DefaultMaxIdleConnsPerHost)
	defer tr.CloseIdleConnections()

	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	started := time.Now()
	entries := make([]record.Entry, repeat*len(tmpls))
	sent := 0

	var wg sync.WaitGroup
	slots := make(chan struct{}, concurrency)
send:
	for round := range repeat {
		for seq, tmpl := range tmpls {
			// In-flight requests share ctx, so a slot frees up promptly
			// after a cancel and this never blocks for long.
			slots <- struct{}{}
			if ctx.Err() != nil {
				break send
			}

			e := &entries[sent]
			sent++

			wg.Go(func() {
				defer func() { <-slots }()
				*e = do(ctx, client, tmpl, opts.Timeout, started)
				e.Seq, e.Round = seq, round
			})
		}
		// A round finishes before the next begins, so that the next one
		// finds its connections idle.
		wg.Wait()
	}
	wg.Wait()

	return &record.Run{Started: started, Entries: entries[:sent]}, ctx.Err()
}

func newRequest(r Request) (*http.Request, error) {
	req, err := http.NewRequest(r.Method, r.URL, nil)
	if err != nil {
		return nil, err
	}
	if (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "" {
		return nil, fmt.Errorf("%q is not an http or https URL", r.URL)
	}
	if r.Header != nil {
		req.Header = r.Header.Clone()
	}
	// With the transport's own gzip handling off, offer gzip by hand so
	// servers answer as they would to an ordinary client.
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	return req, nil
}

// do sends one request and reads its body to the end.
func do(ctx context.Context, client *http.Client, tmpl *http.Request, timeout time.Duration, origin time.Time) record.Entry {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var m marks
	req := tmpl.Clone(httptrace.WithClientTrace(ctx, m.trace()))

	m.start = time.Now()
	e := record.Entry{
		Started: m.start,
		Offset:  m.start.Sub(origin),
		Request: record.Request{Method: req.Method, URL: req.URL.String()},
	}

	resp, err := client.Do(req)
	if err == nil {
		e.Response = &record.Response{Status: resp.StatusCode, Proto: resp.Proto, Header: resp.Header}
		e.Response.BodySize, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if err != nil {
		e.Error = err.Error()
	}
	e.Timings, e.Conn = m.finish()
	return e
}

// marks records when each phase of one request began. The transport calls
// the hooks from several goroutines and may keep calling them after the
// request has returned, hence the lock and the done flag.
type marks struct {
	mu   sync.Mutex
	done bool

	start                                        time.Time
	dns, connect, tls, gotConn, wrote, firstByte time.Time
	conn                                         record.Conn
}

func (m *marks) trace() *httptrace.ClientTrace {
	mark := func(at *time.Time) {
		m.mu.Lock()
		defer m.mu.Unlock()

		if !m.done && at.IsZero() {
			*at = time.Now()
		}
	}

	// Once the request has its connection, any dial still running is for a
	// connection it will not use.
	dial := func(at *time.Time) {
		m.mu.Lock()
		defer m.mu.Unlock()

		if !m.done && m.gotConn.IsZero() && at.IsZero() {
			*at = time.Now()
		}
	}

	return &httptrace.ClientTrace{
		// The transport asks for a connection again when it retries, which
		// it does after finding that a reused one had gone stale. The
		// phases start over and the failed attempt is left in Blocked.
		GetConn: func(string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if !m.done {
				m.dns, m.connect, m.tls = time.Time{}, time.Time{}, time.Time{}
				m.gotConn, m.wrote, m.firstByte = time.Time{}, time.Time{}, time.Time{}
				m.conn = record.Conn{}
			}
		},
		DNSStart:          func(httptrace.DNSStartInfo) { dial(&m.dns) },
		ConnectStart:      func(_, _ string) { dial(&m.connect) },
		TLSHandshakeStart: func() { dial(&m.tls) },
		GotConn: func(info httptrace.GotConnInfo) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if !m.done && m.gotConn.IsZero() {
				m.gotConn = time.Now()
				m.conn = record.Conn{Reused: info.Reused, RemoteAddr: info.Conn.RemoteAddr().String()}
			}
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { mark(&m.wrote) },
		GotFirstResponseByte: func() { mark(&m.firstByte) },
	}
}

// finish stops the hooks and returns what they saw.
func (m *marks) finish() (record.Timings, record.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done = true
	return m.timings(time.Now()), m.conn
}

// timings turns the marks into phases. Every moment from start to end
// belongs to the phase that began most recently, so the phases cover the
// request exactly and a failure lands in the phase it interrupted.
func (m *marks) timings(end time.Time) record.Timings {
	var t record.Timings
	steps := []struct {
		at    time.Time
		phase *time.Duration
	}{
		{m.dns, &t.DNS},
		{m.connect, &t.Connect},
		{m.tls, &t.TLS},
		{m.gotConn, &t.Send},
		{m.wrote, &t.Wait},
		{m.firstByte, &t.Receive},
	}
	if m.conn.Reused {
		// The transport dials while it waits for an idle connection. This
		// request got the idle one, so the time it waited was all Blocked.
		steps = steps[3:]
	}

	cur, phase := m.start, &t.Blocked
	for _, s := range steps {
		if s.at.IsZero() {
			continue
		}
		if s.at.After(cur) {
			*phase += s.at.Sub(cur)
			cur = s.at
		}
		phase = s.phase
	}
	*phase += end.Sub(cur)
	return t
}
