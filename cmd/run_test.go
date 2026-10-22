package cmd

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// execute runs the CLI with its flags reset and returns its stdout.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // keep the user's own config out of it

	reset := func(f *pflag.Flag) {
		if s, ok := f.Value.(pflag.SliceValue); ok {
			s.Replace(nil)
		} else {
			f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	rootCmd.PersistentFlags().VisitAll(reset)
	runCmd.Flags().VisitAll(reset)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return out.String(), err
}

func TestRunDrawsEveryRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	out, err := execute(t, "run", "--repeat", "2", srv.URL, srv.URL+"/missing")
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if n := strings.Count(out, " 200  "); n != 2 {
		t.Errorf("drew %d rows with status 200, want 2\n%s", n, out)
	}
	if n := strings.Count(out, " 404  "); n != 2 {
		t.Errorf("drew %d rows with status 404, want 2\n%s", n, out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("wrote escape codes to something that is not a terminal\n%s", out)
	}
}

func TestRunReportsFailedRequests(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + ln.Addr().String()
	ln.Close()

	out, err := execute(t, "run", refused)
	if err == nil || err.Error() != "1 of 1 requests failed" {
		t.Errorf("error %v, want 1 of 1 requests failed", err)
	}
	if !strings.Contains(out, " ERR  ") {
		t.Errorf("the failed request was not drawn\n%s", out)
	}
}

// lines decodes NDJSON output, failing on anything that is not an object.
func lines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var got []map[string]any
	for l := range strings.Lines(out) {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %q is not a JSON object: %v", l, err)
		}
		got = append(got, m)
	}
	return got
}

func TestRunJSONWritesOneLinePerRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	out, err := execute(t, "run", "--json", "--repeat", "2", srv.URL, srv.URL+"/missing")
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	got := lines(t, out)
	if len(got) != 4 {
		t.Fatalf("wrote %d lines, want 4\n%s", len(got), out)
	}
	for i, l := range got {
		if len(l) != len(got[0]) {
			t.Errorf("line %d has %d keys, the first has %d\n%s", i, len(l), len(got[0]), out)
		}
		// Sequential requests complete in the order they were sent.
		if l["seq"] != float64(i%2) || l["round"] != float64(i/2) {
			t.Errorf("line %d is seq %v of round %v", i, l["seq"], l["round"])
		}
		if want := float64(200 + 204*(i%2)); l["status"] != want || l["error"] != "" {
			t.Errorf("line %d has status %v and error %q, want %v and no error", i, l["status"], l["error"], want)
		}
	}
	if got[0]["reused"] != false || got[2]["reused"] != true {
		t.Errorf("reused is %v then %v for the same URL, want false then true", got[0]["reused"], got[2]["reused"])
	}
	if got[0]["conn_id"] != float64(1) || got[2]["conn_id"] != float64(1) {
		t.Errorf("conn_id is %v then %v for the same URL, want 1 both times", got[0]["conn_id"], got[2]["conn_id"])
	}
}

func TestRunJSONKeepsFailedRequests(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + ln.Addr().String()
	ln.Close()

	out, err := execute(t, "run", "--json", refused)
	if err == nil || err.Error() != "1 of 1 requests failed" {
		t.Errorf("error %v, want 1 of 1 requests failed", err)
	}
	got := lines(t, out)
	if len(got) != 1 {
		t.Fatalf("wrote %d lines, want 1\n%s", len(got), out)
	}
	if l := got[0]; l["status"] != float64(0) || l["error"] == "" || l["proto"] != "" || l["conn_id"] != float64(0) {
		t.Errorf("status %v, proto %q, conn_id %v, error %q; want 0, empty, 0 and an error", l["status"], l["proto"], l["conn_id"], l["error"])
	}
}

// redirects serves a 301 from / to /final, and a 302 back to / from /loop.
func redirects(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/final", http.StatusMovedPermanently)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunFollowsRedirects(t *testing.T) {
	srv := redirects(t)

	out, err := execute(t, "run", srv.URL)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	for _, row := range []string{" 1  301  #1  " + srv.URL + " ", "    200  #1  ↳ http://"} {
		if !strings.Contains(out, row) {
			t.Errorf("no row starts %q\n%s", row, out)
		}
	}

	out, err = execute(t, "run", "--max-redirects", "0", srv.URL)
	if err != nil || strings.Contains(out, "↳") || !strings.Contains(out, " 301  ") {
		t.Errorf("with --max-redirects 0, error %v and output\n%s\nwant the 301 alone", err, out)
	}
}

func TestRunStopsAtTheRedirectLimit(t *testing.T) {
	srv := redirects(t)

	out, err := execute(t, "run", "--max-redirects", "1", srv.URL+"/loop")
	if err == nil || err.Error() != "1 of 2 requests failed" {
		t.Errorf("error %v, want 1 of 2 requests failed", err)
	}
	if !strings.Contains(out, "stopped after 1 redirect\n") {
		t.Errorf("the limit is not reported under its row\n%s", out)
	}
}

func TestRunJSONMarksEachHop(t *testing.T) {
	srv := redirects(t)

	out, err := execute(t, "run", "--json", srv.URL)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	got := lines(t, out)
	if len(got) != 2 {
		t.Fatalf("wrote %d lines, want 2\n%s", len(got), out)
	}
	for i, l := range got {
		if l["seq"] != float64(0) || l["hop"] != float64(i) {
			t.Errorf("line %d is seq %v hop %v, want seq 0 hop %d", i, l["seq"], l["hop"], i)
		}
	}
	if loc, ok := got[1]["location"]; got[0]["location"] != "/final" || !ok || loc != nil {
		t.Errorf("location is %v then %v, want /final then null", got[0]["location"], got[1]["location"])
	}
}

func TestRunSendsHeaders(t *testing.T) {
	var got []http.Header
	var hosts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header)
		hosts = append(hosts, r.Host)
	}))
	defer srv.Close()

	out, err := execute(t, "run", "-H", "X-Test: a, b", "--header", "x-test:c", "-H", "Host: example.com", srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	for i, h := range got {
		if v := h.Values("X-Test"); len(v) != 2 || v[0] != "a, b" || v[1] != "c" {
			t.Errorf("request %d had X-Test %q, want \"a, b\" and \"c\"", i, v)
		}
		if hosts[i] != "example.com" {
			t.Errorf("request %d was for host %q, want example.com", i, hosts[i])
		}
	}
}

func TestRunJSONShowsRequestHeadersWithoutSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	out, err := execute(t, "run", "--json", "-H", "Authorization: Bearer secret", srv.URL)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "secret") {
		t.Errorf("the token is in the output\n%s", out)
	}
	h, _ := lines(t, out)[0]["request_headers"].(map[string]any)
	want := map[string]any{"Authorization": []any{"Bearer xxxxx"}, "Accept-Encoding": []any{"gzip"}}
	if !reflect.DeepEqual(h, want) {
		t.Errorf("request_headers is %v, want %v", h, want)
	}
}

func TestRunRejectsBadInput(t *testing.T) {
	tests := [][]string{
		{"run"},
		{"run", "example.com"},
		{"run", "--color", "sometimes", "http://example.com"},
		{"run", "-H", "X-Test", "http://example.com"},
	}
	for _, args := range tests {
		if out, err := execute(t, args...); err == nil || out != "" {
			t.Errorf("%v: error %v, output %q; want an error and no output", args, err, out)
		}
	}
}
