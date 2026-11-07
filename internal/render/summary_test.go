package render

import (
	"strings"
	"testing"
	"time"

	"github.com/AustinMusiku/timeline/internal/record"
)

// rounds is four rounds of three requests: one that gets faster once warm,
// one that redirects and fails once, and one that always fails.
func rounds() *record.Run {
	const (
		users    = "https://api.example.com/v1/users"
		sessions = "https://api.example.com/v1/sessions"
		script   = "https://cdn.example.net/app.js"
	)
	ok := &record.Response{Status: 200}
	var run record.Run
	add := func(seq, round, hop int, url string, t record.Timings, err string) {
		e := record.Entry{Seq: seq, Round: round, Hop: hop, Request: record.Request{Method: "GET", URL: url}, Timings: t, Error: err}
		if seq == 1 && hop == 0 {
			e.Request = record.Request{Name: "login", Method: "POST", URL: sessions}
		}
		if err == "" {
			e.Response = ok
		}
		run.Entries = append(run.Entries, e)
	}
	for round, wait := range []time.Duration{150 * ms, 100 * ms, 300 * ms, 200 * ms} {
		t := record.Timings{Wait: wait, Receive: 10 * ms}
		if round == 0 {
			t.DNS, t.Connect, t.TLS = 20*ms, 100*ms, 200*ms
		}
		add(0, round, 0, users, t, "")

		switch round {
		case 0:
			add(1, round, 0, sessions, record.Timings{Wait: 50 * ms}, "")
			add(1, round, 1, "https://api.example.com/v2/sessions", record.Timings{Connect: 30 * ms, Wait: 70 * ms}, "")
		case 2:
			add(1, round, 0, sessions, record.Timings{Wait: time.Second}, "timed out after 1s")
		default:
			add(1, round, 0, sessions, record.Timings{Wait: 80*ms - time.Duration(round)*10*ms}, "")
		}

		add(2, round, 0, script, record.Timings{DNS: 30 * ms}, "no such host")
	}
	return &run
}

func summary(t *testing.T, run *record.Run, opts Options) string {
	t.Helper()
	var b strings.Builder
	if err := Summary(&b, run, opts); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestSummary(t *testing.T) {
	got := summary(t, rounds(), Options{Width: 80})
	want := `
                 total  blocked   dns  connect    tls  send   wait  receive
 1  https://api.example.com/v1/users
    round 1      480ms        -  20ms    100ms  200ms     -  150ms     10ms
    median 2–4   210ms        -     -        -      -     -  200ms     10ms
 2  login
    round 1      150ms        -     -     30ms      -     -  120ms        -
    median 2–4    60ms        -     -        -      -     -   60ms        -  (1 of 3 failed)
 3  https://cdn.example.net/app.js
    round 1     failed
    rounds 2–4  failed
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSummaryColor(t *testing.T) {
	got := summary(t, rounds(), Options{Width: 80, Color: true})
	if !strings.Contains(got, "  \x1b[31mfailed\x1b[0m\n") {
		t.Errorf("a failure is not red\n%q", got)
	}
}

func TestSummaryOfTwoRounds(t *testing.T) {
	run := rounds()
	run.Entries = run.Entries[:7] // rounds 1 and 2
	got := summary(t, run, Options{Width: 80})
	if !strings.Contains(got, "\n    round 2   110ms ") {
		t.Errorf("the second round is not on its own\n%s", got)
	}
	if strings.Contains(got, "median") {
		t.Errorf("one round has no median\n%s", got)
	}
}

func TestSummaryOfOneRound(t *testing.T) {
	run := rounds()
	run.Entries = run.Entries[:4]
	if got := summary(t, run, Options{Width: 80}); got != "" {
		t.Errorf("summed up a single round:\n%s", got)
	}
}

func TestSummaryClipsLabelsToTheWidth(t *testing.T) {
	run := rounds()
	if got := summary(t, run, Options{Width: 24}); !strings.Contains(got, "\n 1  https://ap…/v1/users\n") {
		t.Errorf("the label was not clipped to 24 columns\n%s", got)
	}
	if got := summary(t, run, Options{Width: 24, FullLabels: true}); !strings.Contains(got, "\n 1  https://api.example.com/v1/users\n") {
		t.Errorf("the label was clipped\n%s", got)
	}
}

func TestMedian(t *testing.T) {
	tests := []struct {
		in   []time.Duration
		want time.Duration
	}{
		{[]time.Duration{5}, 5},
		{[]time.Duration{30, 10, 20}, 20},
		{[]time.Duration{40, 10, 30, 20}, 25},
	}
	for _, tc := range tests {
		if got := median(tc.in); got != tc.want {
			t.Errorf("median(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
