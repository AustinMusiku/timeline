package render

import (
	"flag"
	"fmt"
	"os"
	"regexp"
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
	ok := func(status int) *record.Response { return &record.Response{Status: status, Proto: "HTTP/2.0"} }
	api := record.Conn{ID: 1, RemoteAddr: "93.184.216.34:443", TLSVersion: "TLS 1.3"}
	warm := api
	warm.Reused = true
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
			Conn:     api,
		},
		{
			Seq: 1, Round: 0, Offset: 650 * ms,
			Request:  record.Request{Method: "POST", URL: sessions},
			Response: ok(201),
			Timings:  record.Timings{Wait: 140 * ms, Receive: 10 * ms},
			Conn:     warm,
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
			Conn:     warm,
		},
		{
			Seq: 1, Round: 1, Offset: 1000 * ms,
			Request:  record.Request{Method: "POST", URL: sessions},
			Response: ok(500),
			Timings:  record.Timings{Wait: 100 * ms},
			Conn:     warm,
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

func TestWaterfallColor(t *testing.T) {
	plain := strings.Split(draw(t, sample(), Options{Width: 80}), "\n")
	colored := draw(t, sample(), Options{Width: 80, Color: true})

	for _, want := range []string{
		"\x1b[31mERR\x1b[0m", // a failed request
		"\x1b[31m500\x1b[0m", // a server error
		" 200  ",             // a success is left uncoloured
		"\x1b[90m█\x1b[0m blocked  \x1b[36m█\x1b[0m dns",
	} {
		if !strings.Contains(colored, want) {
			t.Errorf("coloured output is missing %q", want)
		}
	}

	// Colour changes how the bars are drawn and nothing to their left.
	const chartAt = 52
	stripped := strings.Split(regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(colored, ""), "\n")
	if len(stripped) != len(plain) {
		t.Fatalf("coloured output has %d lines, plain has %d", len(stripped), len(plain))
	}
	for i := range plain {
		got, want := []rune(stripped[i]), []rune(plain[i])
		if len(want) < chartAt || want[1] < '1' || want[1] > '9' {
			continue // not a row, which begins with its number
		}
		if string(got[:chartAt]) != string(want[:chartAt]) {
			t.Errorf("line %d differs before the chart:\n got %q\nwant %q", i+1, string(got[:chartAt]), string(want[:chartAt]))
		}
	}
}

func TestConnectionsAreListed(t *testing.T) {
	req := record.Request{Method: "GET", URL: "https://example.com"}
	entry := func(proto string, c record.Conn) record.Entry {
		e := record.Entry{Request: req, Timings: record.Timings{Wait: 10 * ms}, Conn: c}
		if proto != "" {
			e.Response = &record.Response{Status: 200, Proto: proto}
		}
		return e
	}
	run := &record.Run{Entries: []record.Entry{
		entry("HTTP/1.1", record.Conn{ID: 2, RemoteAddr: "93.184.216.34:443", TLSVersion: "TLS 1.3", TLSResumed: true}),
		entry("", record.Conn{ID: 1, RemoteAddr: "[2606:2800:220:1::1]:443", TLSVersion: "TLS 1.2"}),
		entry("HTTP/2.0", record.Conn{ID: 1, RemoteAddr: "[2606:2800:220:1::1]:443", TLSVersion: "TLS 1.2", Reused: true}),
		entry("HTTP/1.1", record.Conn{ID: 10, RemoteAddr: "127.0.0.1:8080"}),
		{Request: req, Error: "no such host"},
	}}
	got := draw(t, run, Options{Width: 80})

	want := "\n" +
		" #1   [2606:2800:220:1::1]:443  HTTP/2.0  TLS 1.2\n" +
		" #2   93.184.216.34:443         HTTP/1.1  TLS 1.3  resumed\n" +
		" #10  127.0.0.1:8080            HTTP/1.1\n" +
		"\n"
	if !strings.Contains(got, want) {
		t.Errorf("connections are not listed as\n%s\ngot:\n%s", want, got)
	}
	for _, row := range []string{" 1  200  #2   https", " 1  200  #10  https", " 1  ERR       https"} {
		if !strings.Contains(got, row) {
			t.Errorf("no row starts %q\n%s", row, got)
		}
	}

	if got := draw(t, &record.Run{Entries: run.Entries[4:]}, Options{Width: 80}); strings.Contains(got, "#") {
		t.Errorf("a run with no connections still shows them\n%s", got)
	}
}

func TestOverlappingRowsAreSpacedApart(t *testing.T) {
	req := record.Request{Method: "GET", URL: "https://example.com"}
	ok := &record.Response{Status: 200}
	overlapping := &record.Run{Entries: []record.Entry{
		{Seq: 0, Request: req, Response: ok, Timings: record.Timings{Wait: 100 * ms}},
		{Seq: 1, Request: req, Response: ok, Timings: record.Timings{Wait: 80 * ms}},
		{Seq: 0, Round: 1, Offset: 100 * ms, Request: req, Response: ok, Timings: record.Timings{Wait: 50 * ms}},
	}}

	// rows returns how many blank lines come before each row.
	rows := func(out string) (gaps []int) {
		blank := 0
		for _, line := range strings.Split(out, "\n")[2:] {
			switch {
			case line == "":
				blank++
			case line[0] == ' ' && line[1] >= '1' && line[1] <= '9':
				gaps = append(gaps, blank)
				blank = 0
			}
		}
		return gaps
	}

	tests := []struct {
		name string
		run  *record.Run
		opts Options
		want string
	}{
		{"overlapping bars in colour", overlapping, Options{Color: true}, "[0 1 2]"},
		{"overlapping bars as letters", overlapping, Options{}, "[0 0 1]"},
		{"bars one after another in colour", sample(), Options{Color: true}, "[0 0 0 1 0 0]"},
	}
	for _, tc := range tests {
		if got := fmt.Sprint(rows(draw(t, tc.run, tc.opts))); got != tc.want {
			t.Errorf("%s: blank lines before each row are %s, want %s", tc.name, got, tc.want)
		}
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

// picture spells out a bar an eighth at a time, with dots for empty space.
func picture(first int, cells []cell) string {
	glyph := func(phase int) string {
		if phase == empty {
			return "."
		}
		return phases[phase].letter
	}
	cols := make([]string, 0, first+len(cells))
	for range first {
		cols = append(cols, "........")
	}
	for _, c := range cells {
		cols = append(cols, strings.Repeat(glyph(c.left), c.k)+strings.Repeat(glyph(c.right), 8-c.k))
	}
	return strings.Join(cols, "|")
}

func TestSubcells(t *testing.T) {
	// Ten columns of 10ms each, so an eighth of a column is 1.25ms.
	const us = time.Microsecond
	tests := []struct {
		name    string
		offset  time.Duration
		timings record.Timings
		want    string
	}{
		{
			name:    "a change of phase falls inside a column",
			timings: record.Timings{DNS: 4 * ms, Connect: 6 * ms, Wait: 20 * ms},
			want:    "dddccccc|wwwwwwww|wwwwwwww",
		},
		{
			name:    "a bar starts and ends between columns",
			offset:  13 * ms,
			timings: record.Timings{Wait: 20 * ms},
			want:    "........|..wwwwww|wwwwwwww|ww......",
		},
		{
			name:    "the phases are laid out in order",
			timings: record.Timings{Blocked: 5 * ms, DNS: 5 * ms, Connect: 5 * ms, TLS: 5 * ms, Send: 5 * ms, Wait: 5 * ms, Receive: 5 * ms},
			want:    "----dddd|cccctttt|sssswwww|rrrr....",
		},
		{
			name:    "a request too short to see is widened in its longest phase",
			offset:  20 * ms,
			timings: record.Timings{DNS: 20 * us, Wait: 80 * us},
			want:    "........|........|ww......",
		},
		{
			name:    "a widened request stays on the chart",
			offset:  99900 * us,
			timings: record.Timings{Wait: 100 * us},
			want:    strings.Repeat("........|", 9) + "......ww",
		},
		{
			name:    "three phases in a column lose the shortest",
			timings: record.Timings{DNS: 2500 * us, Connect: 2500 * us, TLS: 5 * ms, Wait: 10 * ms},
			want:    "cccctttt|wwwwwwww",
		},
		{
			name:    "a short phase at the start of a bar survives",
			offset:  3750 * us,
			timings: record.Timings{DNS: 3750 * us, Connect: 12500 * us},
			want:    "...ddddd|cccccccc",
		},
		{
			name:    "a thin gap before the bar gives way to a phase",
			offset:  1250 * us,
			timings: record.Timings{DNS: 3750 * us, Connect: 15 * ms},
			want:    "ddddcccc|cccccccc",
		},
		{
			// A tenth of an eighth, placed across a rounding point.
			name:    "a phase too short to draw is not rounded up to an eighth",
			offset:  562500 * time.Nanosecond,
			timings: record.Timings{Send: 125 * us, Wait: 19312500 * time.Nanosecond},
			want:    "wwwwwwww|wwwwwwww",
		},
		{
			name:    "short phases at the end leave the bar its full length",
			timings: record.Timings{Wait: 19 * ms, Receive: 500 * us},
			want:    "wwwwwwww|wwwwwwww",
		},
		{
			name:    "a request inside one column keeps its start",
			offset:  2500 * us,
			timings: record.Timings{Wait: 5 * ms},
			want:    "..wwwwww",
		},
		{
			name:    "a request inside one column keeps its end when that costs less",
			offset:  1250 * us,
			timings: record.Timings{Wait: 2500 * us},
			want:    "www.....",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := record.Entry{Offset: tc.offset, Timings: tc.timings}
			if got := picture(subcells(e, 10, float64(10*ms))); got != tc.want {
				t.Errorf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestBlocks(t *testing.T) {
	const (
		dns, connect, tls, send, wait = 1, 2, 3, 4, 5
	)
	tests := []struct {
		name string
		c    cell
		want string
	}{
		{"one phase into the next", cell{dns, connect, 3}, "\x1b[36;43m▍\x1b[0m"},
		{"the end of a bar", cell{wait, empty, 2}, "\x1b[32m▎\x1b[0m"},
		{"the start of a bar", cell{empty, wait, 2}, "\x1b[7;32m▎\x1b[0m"},
		{"out of the text colour", cell{send, wait, 4}, "\x1b[39;42m▌\x1b[0m"},
		{"into the text colour", cell{tls, send, 4}, "\x1b[7;39;45m▌\x1b[0m"},
	}
	for _, tc := range tests {
		if got := edge(tc.c); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	// Whole columns of one phase share a single colour change.
	e := record.Entry{Offset: 13 * ms, Timings: record.Timings{Wait: 30 * ms}}
	want := " \x1b[7;32m▎\x1b[0m\x1b[32m██\x1b[0m\x1b[32m▎\x1b[0m"
	if got := blocks(e, 10, float64(10*ms)); got != want {
		t.Errorf("blocks:\n got %q\nwant %q", got, want)
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
