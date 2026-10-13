package cmd

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// execute runs the CLI with its flags reset and returns its stdout.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // keep the user's own config out of it

	reset := func(f *pflag.Flag) {
		f.Value.Set(f.DefValue)
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

func TestRunRejectsBadInput(t *testing.T) {
	tests := [][]string{
		{"run"},
		{"run", "example.com"},
		{"run", "--color", "sometimes", "http://example.com"},
	}
	for _, args := range tests {
		if out, err := execute(t, args...); err == nil || out != "" {
			t.Errorf("%v: error %v, output %q; want an error and no output", args, err, out)
		}
	}
}
