package render

import (
	"flag"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AustinMusiku/timeline/internal/record"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const ms = time.Millisecond

// sample is two rounds of three requests, one of which fails.
func sample() *record.Run {
	ok := func(status int) *record.Response { return &record.Response{Status: status} }
	const (
		users    = "https://api.example.com/v1/users"
		sessions = "https://api.example.com/v1/sessions"
		script   = "https://cdn.example.net/app.js"
		noHost   = "dial tcp: lookup cdn.example.net: no such host"
	)
	return &record.Run{Entries: []record.Entry{
		{
			Seq: 0, Round: 0, Offset: 0,
			Request:  record.Request{Method: "GET", URL: users},
			Response: ok(200),
			Timings:  record.Timings{Blocked: 1 * ms, DNS: 24 * ms, Connect: 150 * ms, TLS: 275 * ms, Wait: 180 * ms, Receive: 20 * ms},
		},
		{
			Seq: 1, Round: 0, Offset: 650 * ms,
			Request:  record.Request{Method: "POST", URL: sessions},
			Response: ok(201),
			Timings:  record.Timings{Wait: 140 * ms, Receive: 10 * ms},
			Conn:     record.Conn{Reused: true},
		},
		{
			Seq: 2, Round: 0, Offset: 800 * ms,
			Request: record.Request{Method: "GET", URL: script},
			Timings: record.Timings{DNS: 30 * ms},
			Error:   noHost,
		},
		{
			Seq: 0, Round: 1, Offset: 830 * ms,
			Request:  record.Request{Method: "GET", URL: users},
			Response: ok(200),
			Timings:  record.Timings{Wait: 170 * ms},
			Conn:     record.Conn{Reused: true},
		},
		{
			Seq: 1, Round: 1, Offset: 1000 * ms,
			Request:  record.Request{Method: "POST", URL: sessions},
			Response: ok(500),
			Timings:  record.Timings{Wait: 100 * ms},
			Conn:     record.Conn{Reused: true},
		},
		{
			Seq: 2, Round: 1, Offset: 1100 * ms,
			Request: record.Request{Method: "GET", URL: script},
			Timings: record.Timings{DNS: 20 * ms},
			Error:   noHost,
		},
	}}
}

func draw(t *testing.T, run *record.Run, opts Options) string {
	t.Helper()
	var b strings.Builder
	if err := Waterfall(&b, run, opts); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestWaterfallPlain(t *testing.T) {
	const golden = "testdata/waterfall.txt"
	got := draw(t, sample(), Options{Width: 80})

	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s; run with -update if the change is meant\n got:\n%s\nwant:\n%s", golden, got, want)
	}
	if strings.Contains(got, "\x1b") {
		t.Error("plain output contains escape codes")
	}
}

func TestWaterfallOfNothing(t *testing.T) {
	if got := draw(t, &record.Run{}, Options{}); got != "" {
		t.Errorf("an empty run drew %q", got)
	}
}

func TestLetters(t *testing.T) {
	// Ten columns of 10ms each.
	tests := []struct {
		name    string
		offset  time.Duration
		timings record.Timings
		want    string
	}{
		{"fills the columns it spans", 0, record.Timings{Wait: 100 * ms}, "wwwwwwwwww"},
		{"starts at its offset", 50 * ms, record.Timings{Wait: 30 * ms}, "     www"},
		{"takes a column it only partly covers", 0, record.Timings{Wait: 11 * ms}, "ww"},
		{"is never narrower than one cell", 20 * ms, record.Timings{Wait: 1 * ms}, "  w"},
		{"shows the phase that fills most of each column", 0, record.Timings{DNS: 4 * ms, Connect: 6 * ms, Wait: 20 * ms}, "cww"},
		{"lays the phases out in order", 0, record.Timings{Blocked: 10 * ms, DNS: 10 * ms, Connect: 10 * ms, TLS: 10 * ms, Send: 10 * ms, Wait: 10 * ms, Receive: 10 * ms}, "-dctswr"},
		{"stays on the chart at the right edge", 100 * ms, record.Timings{Wait: 5 * ms}, "         w"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := record.Entry{Offset: tc.offset, Timings: tc.timings}
			if got := letters(e, 10, float64(10*ms)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAxis(t *testing.T) {
	tests := []struct {
		name   string
		span   time.Duration
		labels string
	}{
		{"labels sit on their ticks", 1120 * ms, "0" + strings.Repeat(" ", 12) + "500ms" + strings.Repeat(" ", 8) + "1s"},
		{"the last label backs up to fit", 1030 * ms, "0" + strings.Repeat(" ", 13) + "500ms" + strings.Repeat(" ", 9) + "1s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			labels, ruler := axis(tc.span, 30)
			if labels != tc.labels {
				t.Errorf("labels:\n got %q\nwant %q", labels, tc.labels)
			}
			if n := utf8.RuneCountInString(ruler); n != 30 || strings.Count(ruler, "┬") != 3 {
				t.Errorf("ruler %q is %d columns with %d ticks, want 30 with 3", ruler, n, strings.Count(ruler, "┬"))
			}
		})
	}
}

func TestTickStep(t *testing.T) {
	tests := []struct {
		span time.Duration
		n    int
		want time.Duration
	}{
		{2070 * ms, 3, time.Second},
		{2070 * ms, 7, 500 * ms},
		{650 * ms, 3, 500 * ms},
		{90 * ms, 4, 50 * ms},
		{800 * time.Microsecond, 4, 200 * time.Microsecond},
		{1, 1, 1},
	}
	for _, tc := range tests {
		if got := tickStep(tc.span, tc.n); got != tc.want {
			t.Errorf("tickStep(%v, %d) = %v, want %v", tc.span, tc.n, got, tc.want)
		}
	}
}

func TestLabels(t *testing.T) {
	ticks := map[time.Duration]string{
		0:                      "0",
		200 * time.Microsecond: "0.2ms",
		500 * ms:               "500ms",
		time.Second:            "1s",
		1500 * ms:              "1.5s",
		120 * time.Second:      "120s",
	}
	for d, want := range ticks {
		if got := tickLabel(d); got != want {
			t.Errorf("tickLabel(%v) = %q, want %q", d, got, want)
		}
	}

	totals := map[time.Duration]string{
		0:                         "0.0ms",
		300 * time.Microsecond:    "0.3ms",
		9400 * time.Microsecond:   "9.4ms",
		23 * ms:                   "23ms",
		651 * ms:                  "651ms",
		999600 * time.Microsecond: "1.00s",
		1250 * ms:                 "1.25s",
		75 * time.Second:          "75.00s",
	}
	for d, want := range totals {
		if got := human(d); got != want {
			t.Errorf("human(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want string
	}{
		{"https://example.com", 30, "https://example.com"},
		{"https://api.example.com/v1/users", 30, "https://api.exa…e.com/v1/users"},
		{"https://api.example.com/v1/users", 11, "https…users"},
	}
	for _, tc := range tests {
		got := clip(tc.s, tc.n)
		if got != tc.want {
			t.Errorf("clip(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
		if n := utf8.RuneCountInString(got); n > tc.n {
			t.Errorf("clip(%q, %d) is %d columns wide", tc.s, tc.n, n)
		}
	}
}
