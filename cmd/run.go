package cmd

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"

	"github.com/AustinMusiku/timeline/internal/collect"
	"github.com/AustinMusiku/timeline/internal/record"
	"github.com/AustinMusiku/timeline/internal/render"
)

var runCmd = &cobra.Command{
	Use:   "run <url>...",
	Short: "Time a sequence of requests and draw them on one axis",
	Long: `Run sends a GET to each URL in turn and draws every request on one shared
time axis, with each bar split into the phases the request went through.

Connections stay open for the length of the run, so repeating the sequence
with --repeat shows a cold first round followed by warm ones.

With --json, stdout carries one line of JSON per request in place of the
drawing, written as each request completes. Lines come in completion order,
and seq, round and offset_ms recover the sequence. Times are milliseconds. A
request that got no response has status 0 and an error. A 4xx or 5xx is a
response, so its error is empty.`,
	Example: `  timeline run https://example.com https://example.com/about
  timeline run --repeat 3 https://api.example.com/health
  timeline run --concurrency 4 https://example.com/a https://example.com/b
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
	f.Bool("json", false, "write one line of JSON per request instead of drawing")
	viper.BindPFlags(f)
}

func runSequence(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	color, err := useColor(viper.GetString("color"), out)
	if err != nil {
		return err
	}

	reqs := make([]collect.Request, len(args))
	for i, u := range args {
		reqs[i] = collect.Request{URL: u}
	}
	repeat := max(viper.GetInt("repeat"), 1)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	opts := collect.Options{
		Repeat:      repeat,
		Concurrency: viper.GetInt("concurrency"),
		Timeout:     viper.GetDuration("timeout"),
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
	if runErr != nil {
		return fmt.Errorf("interrupted after %d of %d requests", len(run.Entries), repeat*len(reqs))
	}

	failed := 0
	for _, e := range run.Entries {
		if e.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d requests failed", failed, len(run.Entries))
	}
	return nil
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
