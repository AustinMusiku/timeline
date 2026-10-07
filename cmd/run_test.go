package cmd

import (
	"bytes"
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
