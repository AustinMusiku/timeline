// Package collect sends HTTP requests and measures them into records.
package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
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
	// Repeat is how many times to run the whole sequence. Minimum 1.
	Repeat int
	// Concurrency is how many requests may be in flight at once. Minimum 1.
	Concurrency int
	// Timeout bounds each request, body included. Zero means no limit.
	Timeout time.Duration
}

// Run sends reqs in order, Repeat times over, and returns one entry per
// request sent. A failed request is recorded in its entry. If ctx is
// cancelled, Run returns the entries so far along with the error.
// Redirects are not followed.
func Run(ctx context.Context, reqs []Request, opts Options) (*record.Run, error) {
	return run(ctx, http.DefaultTransport.(*http.Transport).Clone(), reqs, opts)
}

// run is Run on the given transport.
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

	// Keep bodies as sent, so sizes and Content-Encoding are the real ones.
	tr.DisableCompression = true

	// Keep every connection of a round open for the next one.
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
			// After a cancel, in-flight requests fail fast and free a slot.
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
		// Finish the round so the next one finds its connections idle.
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
	// The transport no longer offers gzip itself.
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
		// The entry already has the method and URL the client adds.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		e.Error = err.Error()
	}
	e.Timings, e.Conn = m.finish()
	return e
}

// marks records when each phase of one request began. The transport calls
// the hooks from several goroutines, even after the request has returned.
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

	// A dial still running once the request has a connection is not its own.
	dial := func(at *time.Time) {
		m.mu.Lock()
		defer m.mu.Unlock()

		if !m.done && m.gotConn.IsZero() && at.IsZero() {
			*at = time.Now()
		}
	}

	return &httptrace.ClientTrace{
		// A retry asks for a connection again. The phases start over and
		// the failed attempt is left in Blocked.
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

// timings turns the marks into phases. Each moment belongs to the phase
// that began most recently, so a failure lands in the one it interrupted.
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
		// Time spent dialling before an idle connection turned up is Blocked.
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
