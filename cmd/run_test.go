package cmd

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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

func TestRunDrawsWholeURLsWhenPiped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	long := srv.URL + "/a/path/far/too/long/to/fit/in/half/of/eighty/columns?and=a&query=string"
	out, err := execute(t, "run", long)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, long) {
		t.Errorf("the URL was shortened\n%s", out)
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

func TestRunExitStatus(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + ln.Addr().String()
	ln.Close()

	dir := t.TempDir()
	badConfig := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(badConfig, []byte("repeat: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		args []string
		want int
	}{
		{[]string{srv.URL, srv.URL + "/missing"}, 0},
		{[]string{srv.URL, refused}, 1},
		{[]string{"--json", refused}, 1},
		{[]string{"--repet", "2", srv.URL}, 2},
		{[]string{"-n", "abc", srv.URL}, 2},
		{[]string{"--config", filepath.Join(dir, "missing.yaml"), srv.URL}, 2},
		{[]string{"--config", badConfig, srv.URL}, 2},
		{[]string{"-f", filepath.Join(dir, "missing.http")}, 2},
		{[]string{srv.URL, "example.com"}, 2},
	}
	for _, tc := range tests {
		before := hits.Load()
		_, err := execute(t, append([]string{"run"}, tc.args...)...)
		if got := exitStatus(err); got != tc.want {
			t.Errorf("%v: exit status %d (%v), want %d", tc.args, got, err, tc.want)
		}
		// 2 promises that nothing was sent.
		if sent := hits.Load() - before; tc.want == 2 && sent != 0 {
			t.Errorf("%v: exit status 2, but the server saw %d requests", tc.args, sent)
		}
	}
}

func TestRunExitsWithTheSignalThatStoppedIt(t *testing.T) {
	// The run is listening for signals while a request is in flight.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
		<-r.Context().Done()
	}))
	defer srv.Close()

	out, err := execute(t, "run", srv.URL, srv.URL)
	if got := exitStatus(err); got != 143 {
		t.Errorf("exit status %d (%v), want 143", got, err)
	}
	if !strings.Contains(out, " ERR  ") || !strings.Contains(out, "interrupted\n") {
		t.Errorf("the interrupted request was not drawn as interrupted\n%s", out)
	}
	if err == nil || err.Error() != "interrupted after sending 1 of 2 requests" {
		t.Errorf("error %v, want interrupted after sending 1 of 2 requests", err)
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

func TestRunSendsTheMethod(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/done", http.StatusSeeOther)
		}
	}))
	defer srv.Close()

	out, err := execute(t, "run", "-X", "POST", srv.URL, srv.URL+"/other")
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	// A 303 turns the POST into a GET.
	if want := []string{"POST", "GET", "POST"}; !slices.Equal(methods, want) {
		t.Errorf("server saw %v, want %v", methods, want)
	}
	if !strings.Contains(out, " 200  #1  POST http://") {
		t.Errorf("the method is not drawn\n%s", out)
	}
}

func TestRunSendsBasicAuth(t *testing.T) {
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		if user, pass, ok := r.BasicAuth(); ok && (user != "me" || pass != "se:cret") {
			t.Errorf("server saw %q and %q, want me and se:cret", user, pass)
		}
	}))
	defer srv.Close()

	out, err := execute(t, "run", "--json", "-u", "me:se:cret", srv.URL)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "se:cret") || !strings.Contains(out, `"Authorization":["Basic xxxxx"]`) {
		t.Errorf("the JSON does not hide the credentials\n%s", out)
	}

	out, err = execute(t, "run", "-u", "me:se:cret", "-H", "Authorization: Bearer x", srv.URL)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if len(auths) != 2 || !strings.HasPrefix(auths[0], "Basic ") || auths[1] != "Bearer x" {
		t.Errorf("server saw Authorization %q, want basic auth then the -H value", auths)
	}
}

func TestRunAsksForAMissingPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, _ := r.BasicAuth(); user != "me" || pass != "secret" {
			t.Errorf("server saw %q and %q, want me and secret", user, pass)
		}
	}))
	defer srv.Close()

	ask := askPassword
	defer func() { askPassword = ask }()
	var asked []string
	askPassword = func(user string) (string, error) {
		asked = append(asked, user)
		return "secret", nil
	}

	if out, err := execute(t, "run", "-u", "me", srv.URL); err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if out, err := execute(t, "run", "-u", "me:secret", srv.URL); err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if !slices.Equal(asked, []string{"me"}) {
		t.Errorf("asked for a password for %q, want only for me without one", asked)
	}

	askPassword = func(string) (string, error) { return "", errors.New("no terminal") }
	if out, err := execute(t, "run", "-u", "me", srv.URL); err == nil || out != "" {
		t.Errorf("error %v, output %q; want an error and no output", err, out)
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

// echo records each request it gets as its method, Content-Type and body.
func echo(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, fmt.Sprintf("%s %s %s", r.Method, r.Header.Get("Content-Type"), b))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestRunSendsData(t *testing.T) {
	file := filepath.Join(t.TempDir(), "body")
	if err := os.WriteFile(file, []byte("c=3\r\n&d=4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootCmd.SetIn(strings.NewReader("e=5\n"))
	defer rootCmd.SetIn(nil)

	tests := []struct {
		args []string
		want string
	}{
		{[]string{"-d", "a=1", "-d", "b=2"}, "POST application/x-www-form-urlencoded a=1&b=2"},
		{[]string{"-d", "@" + file, "--data", "@-"}, "POST application/x-www-form-urlencoded c=3&d=4&e=5"},
		{[]string{"-X", "PUT", "-H", "Content-Type: application/json", "-d", `{"a":1}`}, `PUT application/json {"a":1}`},
		{[]string{"-d", ""}, "POST application/x-www-form-urlencoded "},
		{[]string{"--data-raw", "@" + file}, "POST application/x-www-form-urlencoded @" + file},
		{[]string{"--data-raw", "a=1", "--data-binary", "@" + file, "-d", "b=2"}, "POST application/x-www-form-urlencoded a=1&c=3\r\n&d=4\n&b=2"},
	}
	for _, tc := range tests {
		srv, got := echo(t)
		out, err := execute(t, append(append([]string{"run", "--json"}, tc.args...), srv.URL)...)
		if err != nil {
			t.Fatalf("%v: run failed: %v\n%s", tc.args, err, out)
		}
		if len(*got) != 1 || (*got)[0] != tc.want {
			t.Errorf("%v: server saw %q, want %q", tc.args, *got, tc.want)
		}
		size := float64(len(tc.want) - strings.LastIndex(tc.want, " ") - 1)
		if l := lines(t, out)[0]; l["request_body_bytes"] != size {
			t.Errorf("%v: request_body_bytes is %v, want %v", tc.args, l["request_body_bytes"], size)
		}
	}
}

func TestRunRejectsBadInput(t *testing.T) {
	tests := [][]string{
		{"run"},
		{"run", "example.com"},
		{"run", "--color", "sometimes", "http://example.com"},
		{"run", "-H", "X-Test", "http://example.com"},
		{"run", "-X", "NOT A METHOD", "http://example.com"},
		{"run", "-d", "@no/such/file", "http://example.com"},
	}
	for _, args := range tests {
		if out, err := execute(t, args...); err == nil || out != "" {
			t.Errorf("%v: error %v, output %q; want an error and no output", args, err, out)
		}
	}
}

func TestRunSaysWhatAFlagWants(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"-n", "abc"}, `--repeat wants a whole number, not "abc"`},
		{[]string{"--concurrency=1.5"}, `--concurrency wants a whole number, not "1.5"`},
		{[]string{"--timeout", "5"}, `--timeout wants a duration such as 5s or 500ms, not "5"`},
		{[]string{"--json=maybe"}, `--json wants true or false, not "maybe"`},
	}
	for _, tc := range tests {
		_, err := execute(t, append(append([]string{"run"}, tc.args...), "http://example.com")...)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%v: error %v, want %s", tc.args, err, tc.want)
		}
	}
}

func TestRunAnswersCurlOnlyFlags(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"-L"}, "unknown flag -L: timeline already follows redirects, up to --max-redirects"},
		{[]string{"--location"}, "unknown flag --location: timeline already follows redirects, up to --max-redirects"},
		{[]string{"-sS"}, "unknown flag -s: timeline has no progress meter, and always shows errors"},
		{[]string{"-o", "/dev/null"}, "unknown flag -o: timeline never prints the body"},
		{[]string{"--max-time", "5"}, "unknown flag --max-time: --timeout sets the time limit for each request"},
		{[]string{"--nope"}, "unknown flag: --nope"},
	}
	for _, tc := range tests {
		_, err := execute(t, append(append([]string{"run"}, tc.args...), "http://example.com")...)
		if err == nil || err.Error() != tc.want || exitStatus(err) != 2 {
			t.Errorf("%v: error %v (status %d), want %s (status 2)", tc.args, err, exitStatus(err), tc.want)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	if out, err := execute(t, "run", "--compressed", srv.URL); err != nil {
		t.Errorf("--compressed: %v\n%s", err, out)
	}
}

func TestRunNamesTheFlagOfAFileItCannotRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"-d", "@" + missing}, "--data: open " + missing},
		{[]string{"--data-binary", "@" + missing}, "--data-binary: open " + missing},
		{[]string{"--config", missing}, "--config: open " + missing},
	}
	for _, tc := range tests {
		_, err := execute(t, append(append([]string{"run"}, tc.args...), "http://example.com")...)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%v: error %v, want it to start %q", tc.args, err, tc.want)
		}
	}
}

func TestRunInsecureSkipsTheCertificateCheck(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	out, err := execute(t, "run", srv.URL)
	if err == nil || !strings.Contains(out, "certificate") {
		t.Errorf("without -k: error %v and output\n%s\nwant a certificate failure", err, out)
	}
	if out, err := execute(t, "run", "-k", srv.URL); err != nil {
		t.Errorf("with -k: %v\n%s", err, out)
	}
}

func TestRunTrustsTheCACert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, pemCert, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, "run", "--cacert", ca, srv.URL); err != nil {
		t.Errorf("with --cacert: %v\n%s", err, out)
	}
	t.Setenv("TIMELINE_CACERT", ca)
	if out, err := execute(t, "run", srv.URL); err != nil {
		t.Errorf("with TIMELINE_CACERT: %v\n%s", err, out)
	}

	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{empty, filepath.Join(dir, "missing.pem")} {
		if out, err := execute(t, "run", "--cacert", file, srv.URL); err == nil || out != "" {
			t.Errorf("--cacert %s: error %v, output %q; want an error and no output", file, err, out)
		}
	}
}

// clientCert writes a self-signed client certificate, its key, and both in one
// file, and returns the certificate.
func clientCert(t *testing.T) (certFile, keyFile, both string, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "timeline test client"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if cert, err = x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	dir := t.TempDir()
	certFile, keyFile, both = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), filepath.Join(dir, "both.pem")
	for file, b := range map[string][]byte{certFile: certPEM, keyFile: keyPEM, both: append(certPEM, keyPEM...)} {
		if err := os.WriteFile(file, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile, both, cert
}

func TestRunSendsAClientCertificate(t *testing.T) {
	certFile, keyFile, both, cert := clientCert(t)

	var seen []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: x509.NewCertPool()}
	srv.TLS.ClientCAs.AddCert(cert)
	srv.StartTLS()
	defer srv.Close()

	if out, err := execute(t, "run", "-k", srv.URL); err == nil {
		t.Errorf("without --cert the server let the request in\n%s", out)
	}
	for _, args := range [][]string{{"--cert", certFile, "--key", keyFile}, {"--cert", both}} {
		if out, err := execute(t, append(append([]string{"run", "-k"}, args...), srv.URL)...); err != nil {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
	}
	if want := []string{"timeline test client", "timeline test client"}; !slices.Equal(seen, want) {
		t.Errorf("server saw client certificates %q, want %q", seen, want)
	}

	for _, args := range [][]string{{"--key", keyFile}, {"--cert", keyFile}, {"--cert", certFile}} {
		if out, err := execute(t, append(append([]string{"run", "-k"}, args...), srv.URL)...); err == nil || out != "" {
			t.Errorf("%v: error %v, output %q; want an error and no output", args, err, out)
		}
	}
}

// sequenceFile writes a .http file for srv and returns its path.
func sequenceFile(t *testing.T, srv *httptest.Server, body string) string {
	t.Helper()
	t.Setenv("BASE", srv.URL)
	file := filepath.Join(t.TempDir(), "flow.http")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

const flow = `# @name login
POST {{BASE}}/login
Content-Type: application/json

{"user": "me"}

###
# @name account
GET {{BASE}}/account
Authorization: Bearer {{$processEnv TOKEN}}
`

func TestRunReadsASequenceFile(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, fmt.Sprintf("%s %s %q %q %q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Extra"), b))
	}))
	defer srv.Close()
	t.Setenv("TOKEN", "secret")
	file := sequenceFile(t, srv, flow)

	out, err := execute(t, "run", "-n", "2", "-f", file, "-H", "X-Extra: 1", "-H", "Authorization: Basic other")
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	login := `POST /login "Basic other" "1" "{\"user\": \"me\"}"`
	account := `GET /account "Bearer secret" "1" ""`
	if want := []string{login, account, login, account}; !slices.Equal(got, want) {
		t.Errorf("server saw\n%q\nwant\n%q", got, want)
	}
	for _, row := range []string{" 1  200  #1  login ", " 2  200  #1  account "} {
		if !strings.Contains(out, row) {
			t.Errorf("no row contains %q\n%s", row, out)
		}
	}

	out, err = execute(t, "run", "--json", "-f", file)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if l := lines(t, out); len(l) != 2 || l[0]["name"] != "login" || l[1]["name"] != "account" {
		t.Errorf("the JSON does not name the requests\n%s", out)
	}
	if strings.Contains(out, "secret") {
		t.Errorf("the token is in the output\n%s", out)
	}
}

func TestRunReadsASequenceFromStdin(t *testing.T) {
	srv, got := echo(t)
	t.Setenv("BASE", srv.URL)
	rootCmd.SetIn(strings.NewReader("POST {{BASE}}\n\nhello\n"))
	defer rootCmd.SetIn(nil)

	if out, err := execute(t, "run", "-f", "-"); err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if want := []string{"POST  hello"}; !slices.Equal(*got, want) {
		t.Errorf("server saw %q, want %q", *got, want)
	}
}

func TestRunRejectsBadSequences(t *testing.T) {
	srv, _ := echo(t)
	file := sequenceFile(t, srv, flow)
	t.Setenv("TOKEN", "secret")
	empty := sequenceFile(t, srv, "# nothing here\n")
	broken := sequenceFile(t, srv, "GET {{BASE}}\n# @no-redirect\n")

	tests := []struct {
		args []string
		want string
	}{
		{[]string{"-f", file, srv.URL}, "give URLs or --file, not both"},
		{[]string{"-f", file, "-X", "PUT"}, "--method can't be used with --file"},
		{[]string{"-f", file, "-d", "a=1"}, "a body flag can't be used with --file"},
		{[]string{"-f", file, "--data-raw", "a=1"}, "a body flag can't be used with --file"},
		{[]string{"-f", empty}, "no requests in " + empty},
		{[]string{"-f", filepath.Join(t.TempDir(), "missing.http")}, "--file: open "},
		{[]string{"-f", broken}, broken + ":2: @no-redirect is not supported"},
	}
	for _, tc := range tests {
		out, err := execute(t, append([]string{"run"}, tc.args...)...)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) || out != "" {
			t.Errorf("%v: error %v, output %q; want an error starting %q and no output", tc.args, err, out, tc.want)
		}
	}
}

const loginFlow = `# @name login
@password = hunter2
POST {{BASE}}/token
Content-Type: application/json

{"email": "me@example.com", "password": "{{password}}"}

###
# @name account
GET {{BASE}}/account
Authorization: Bearer {{login.response.body.access_token}}
`

func TestRunUsesAValueFromAnEarlierResponse(t *testing.T) {
	var failLogin bool
	var extras []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if failLogin {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			io.WriteString(w, `{"access_token": "tok-secret-123"}`)
		case "/account":
			extras = append(extras, r.Header.Get("X-Extra"))
			if r.Header.Get("Authorization") != "Bearer tok-secret-123" {
				w.WriteHeader(http.StatusUnauthorized)
			}
		}
	}))
	defer srv.Close()
	file := sequenceFile(t, srv, loginFlow)

	out, err := execute(t, "run", "--json", "-n", "2", "-H", "X-Extra: 1", "-f", file)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	for i, l := range lines(t, out) {
		if l["status"] != float64(200) {
			t.Errorf("line %d (%v) has status %v, want 200", i, l["name"], l["status"])
		}
	}
	if !slices.Equal(extras, []string{"1", "1"}) {
		t.Errorf("account got X-Extra %q, want -H applied both rounds", extras)
	}

	failLogin = true
	out, err = execute(t, "run", "-f", file)
	// A 500 is a response; only the step that could not be built failed.
	if err == nil || err.Error() != "1 of 2 requests failed" {
		t.Errorf("error %v, want 1 of 2 requests failed", err)
	}
	for _, want := range []string{" 2  ERR  ", "{{login.response.body.access_token}}: login got 500 and the body is not JSON"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "tok-secret-123") || strings.Contains(out, "hunter2") {
		t.Errorf("a secret is in the output\n%s", out)
	}
}

func TestRunKeepsCookies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "secret-session", Path: "/"})
		}
	}))
	defer srv.Close()

	out, err := execute(t, "run", "--json", "--cookies", srv.URL+"/login", srv.URL+"/me")
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	h, _ := lines(t, out)[1]["request_headers"].(map[string]any)
	if want := []any{"session=xxxxx"}; !reflect.DeepEqual(h["Cookie"], want) {
		t.Errorf("/me sent Cookie %v, want %v", h["Cookie"], want)
	}
	if strings.Contains(out, "secret-session") {
		t.Errorf("the cookie's value is in the output\n%s", out)
	}

	out, err = execute(t, "run", "--json", srv.URL+"/login", srv.URL+"/me")
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if h, _ := lines(t, out)[1]["request_headers"].(map[string]any); h["Cookie"] != nil {
		t.Errorf("without --cookies /me sent Cookie %v", h["Cookie"])
	}
}
