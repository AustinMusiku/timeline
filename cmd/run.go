package cmd

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/term"

	"github.com/AustinMusiku/timeline/internal/collect"
	"github.com/AustinMusiku/timeline/internal/record"
	"github.com/AustinMusiku/timeline/internal/render"
	"github.com/AustinMusiku/timeline/internal/sequence"
)

var runCmd = &cobra.Command{
	Use:   "run <url>...",
	Short: "Time a sequence of requests and draw them on one axis",
	Long: `Run sends a GET to each URL in turn and draws every request on one shared
time axis, with each bar split into the phases the request went through.

--method, --header, --user and the body flags apply to every request. --user
with no password asks for one on the terminal. An Authorization from --header
wins over --user. --header, --user and the body flags are read from the command
line only, never from the environment or the config file.

The body flags work as in curl. A body makes the request a POST unless
--method says otherwise, and a form unless --header gives a Content-Type.
--data @file reads a file without its line breaks, and @- reads stdin.
--data-binary @file keeps the line breaks, and --data-raw reads no file.
Several body flags are joined with &, in the order given. A 307 or 308 redirect
sends the body on; a 301, 302 or 303 turns the request into a GET without it.

Connections stay open for the length of the run, so repeating the sequence
with --repeat shows a cold first round followed by warm ones. Each row is
tagged with the connection it used, and the connections are listed under the
chart, so rows with the same tag shared one.

Redirects are followed, up to --max-redirects from each request. Every hop is
a request of its own, drawn on a row under the one that led to it. A redirect
left unfollowed at the limit counts as a failed request.

With --json, stdout carries one line of JSON per request in place of the
drawing, written as each request completes. Lines come in completion order,
and seq, round and offset_ms recover the sequence. Times are milliseconds. A
request that got no response has status 0 and an error. A 4xx or 5xx is a
response, so its error is empty.

The hops of a redirect share a seq, and hop counts them from 0 for the URL
that was given. location is the response's Location header, or null if it had
none.

request_headers maps each header timeline set to its values. Credentials are
hidden: an Authorization keeps its scheme and a Cookie its names. A password in
url or location shows as xxxxx. request_body_bytes is the size of the body
sent; the body itself is never written out.

conn_id numbers the connections of the run from 1, and is 0 for a request
that never got one. reused means the connection was already open, and
tls_resumed that the handshake which opened it resumed an earlier session.
resolved_ips and dial_attempts describe the request's own dial, so they are
empty and 0 on a reused connection. attempts above 1 means the request was
retried.`,
	Example: `  timeline run https://example.com https://example.com/about
  timeline run --repeat 3 https://api.example.com/health
  timeline run --concurrency 4 https://example.com/a https://example.com/b
  timeline run -H "Authorization: Bearer $TOKEN" https://api.example.com/me
  timeline run -X POST -u me https://api.example.com/login
  timeline run -H 'Content-Type: application/json' -d @body.json https://api.example.com/items
  timeline run --json -n 5 https://example.com | jq -s 'map(.wait_ms) | add / length'`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSequence,
}

func init() {
	rootCmd.AddCommand(runCmd)

	f := runCmd.Flags()
	f.IntP("repeat", "n", 1, "times to run the whole sequence")
	f.IntP("concurrency", "c", 1, "requests to keep in flight at once")
	f.Duration("timeout", 30*time.Second, "time limit for each request, 0 for none")
	f.Int("max-redirects", 10, "redirects to follow from each request, 0 for none")
	f.StringP("method", "X", "", "method for every request, GET when empty")
	f.Bool("json", false, "write one line of JSON per request instead of drawing")
	viper.BindPFlags(f)

	// These can carry credentials, so they come from the command line only.
	f.StringArrayP("header", "H", nil, "header to send with every request, as 'Name: value'")
	f.StringP("user", "u", "", "user:password for basic auth on every request")
	f.VarP(dataFlag{&dataParts, data}, "data", "d", "body for every request, @file or @- for stdin; several are joined with &")
	f.Var(dataFlag{&dataParts, dataRaw}, "data-raw", "like --data, but sent as typed even when it starts with @")
	f.Var(dataFlag{&dataParts, dataBinary}, "data-binary", "like --data, but @file keeps its line breaks")

	// Command line only, so no config file can turn certificate checks off.
	f.BoolP("insecure", "k", false, "skip checking the server's certificate")
}

// defaults reads the flags that apply to every request.
func defaults(f *pflag.FlagSet, stdin io.Reader) (sequence.Defaults, error) {
	d := sequence.Defaults{Method: viper.GetString("method")}
	b, err := body(dataParts, stdin)
	if err != nil {
		return d, err
	}

	lines, err := f.GetStringArray("header")
	if err != nil {
		return d, err
	}
	for i, line := range lines {
		name, value, err := sequence.ParseHeader(line)
		if err != nil {
			return d, fmt.Errorf("header %d: %w", i+1, err)
		}
		if d.Header == nil {
			d.Header = http.Header{}
		}
		d.Header.Add(name, value)
	}

	// As in curl, -d makes a form POST unless told otherwise.
	if b != nil {
		d.Body = b
		if d.Method == "" {
			d.Method = http.MethodPost
		}
		if d.Header.Get("Content-Type") == "" {
			if d.Header == nil {
				d.Header = http.Header{}
			}
			d.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}

	userpass, err := f.GetString("user")
	if err != nil || userpass == "" {
		return d, err
	}
	user, password, ok := strings.Cut(userpass, ":")
	if !ok {
		if password, err = askPassword(user); err != nil {
			return d, err
		}
	}
	// As in curl, an Authorization from -H wins.
	if d.Header.Get("Authorization") == "" {
		if d.Header == nil {
			d.Header = http.Header{}
		}
		d.Header.Set("Authorization", sequence.BasicAuth(user, password))
	}
	return d, nil
}

func runSequence(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	color, err := useColor(viper.GetString("color"), out)
	if err != nil {
		return err
	}

	d, err := defaults(cmd.Flags(), cmd.InOrStdin())
	if err != nil {
		return err
	}
	reqs := sequence.FromURLs(args, d)
	repeat := max(viper.GetInt("repeat"), 1)
	tlsConf, err := tlsConfig(cmd.Flags())
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := collect.Options{
		Repeat:       repeat,
		Concurrency:  viper.GetInt("concurrency"),
		Timeout:      viper.GetDuration("timeout"),
		MaxRedirects: viper.GetInt("max-redirects"),
		TLS:          tlsConf,
	}
	asJSON := viper.GetBool("json")
	var writeErr error
	if asJSON {
		opts.OnEntry = func(e record.Entry) {
			if err := render.JSON(out, e); err != nil && writeErr == nil {
				writeErr = err
			}
		}
	}

	run, runErr := collect.Run(ctx, reqs, opts)
	if run == nil {
		return runErr
	}

	if writeErr != nil {
		return writeErr
	}
	if !asJSON {
		// An interrupted run still draws what it collected.
		if err := render.Waterfall(out, run, render.Options{Width: termWidth(out), Color: color}); err != nil {
			return err
		}
	}
	// A redirect's hops belong to the request that led to them.
	sent, failed := 0, 0
	for _, e := range run.Entries {
		if e.Hop == 0 {
			sent++
		}
		if e.Error != "" {
			failed++
		}
	}
	if runErr != nil {
		return fmt.Errorf("interrupted after %d of %d requests", sent, repeat*len(reqs))
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d requests failed", failed, len(run.Entries))
	}
	return nil
}

// askPassword reads a password from the terminal without echoing it.
var askPassword = func(user string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("--user has no password, and there is no terminal to ask for one on")
	}
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}
	// Echo stays off if an interrupt kills the process mid-prompt.
	sigs, done := make(chan os.Signal, 1), make(chan struct{})
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer func() { signal.Stop(sigs); close(done) }()
	go func() {
		select {
		case <-sigs:
			term.Restore(fd, state)
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		case <-done:
		}
	}()

	fmt.Fprintf(os.Stderr, "Password for %s: ", user)
	password, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(password), err
}

func useColor(mode string, out io.Writer) (bool, error) {
	switch mode {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "auto":
		f, ok := out.(*os.File)
		return ok && term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb", nil
	}
	return false, fmt.Errorf("--color must be auto, always or never, not %q", mode)
}

// termWidth returns 0 when out is not a terminal.
func termWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil {
			return w
		}
	}
	return 0
}
