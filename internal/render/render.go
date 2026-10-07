// Package render draws records in the terminal.
package render

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AustinMusiku/timeline/internal/record"
)

type Options struct {
	// Width is how many columns to fill. Values below 1 mean 80.
	Width int
}

// phases lists the parts of a bar in the order they happen.
var phases = [...]struct {
	name   string
	letter string
}{
	{"blocked", "-"},
	{"dns", "d"},
	{"connect", "c"},
	{"tls", "t"},
	{"send", "s"},
	{"wait", "w"},
	{"receive", "r"},
}

func split(t record.Timings) [len(phases)]time.Duration {
	return [...]time.Duration{t.Blocked, t.DNS, t.Connect, t.TLS, t.Send, t.Wait, t.Receive}
}

const (
	statusW   = 3
	totalW    = 7
	minLabelW = 10
	minCols   = 10
)

// Waterfall writes one row per entry, with every bar on the same time axis.
func Waterfall(w io.Writer, run *record.Run, opts Options) error {
	if len(run.Entries) == 0 {
		return nil
	}
	width := opts.Width
	if width < 1 {
		width = 80
	}

	var span time.Duration
	seqW, labelW := 0, 0
	labels := make([]string, len(run.Entries))
	for i, e := range run.Entries {
		span = max(span, e.Offset+e.Timings.Total())
		seqW = max(seqW, len(strconv.Itoa(e.Seq+1)))
		labels[i] = e.Request.URL
		if e.Request.Method != http.MethodGet {
			labels[i] = e.Request.Method + " " + labels[i]
		}
		labelW = max(labelW, utf8.RuneCountInString(labels[i]))
	}
	span = max(span, 1)

	// A row reads: seq, status, label, total, bar.
	fixed := 1 + seqW + 2 + statusW + 2 + 2 + totalW + 2
	labelW = max(min(labelW, (width-fixed)/2), minLabelW)
	cols := max(width-fixed-labelW, minCols)
	perCol := float64(span) / float64(cols)

	var b strings.Builder
	indent := strings.Repeat(" ", fixed+labelW)
	ticks, ruler := axis(span, cols)
	fmt.Fprintf(&b, "%s%s\n%s%s\n", indent, ticks, indent, ruler)

	for i, e := range run.Entries {
		if i > 0 && e.Round != run.Entries[i-1].Round {
			b.WriteByte('\n')
		}

		status := "ERR"
		if e.Response != nil {
			status = strconv.Itoa(e.Response.Status)
		}
		fmt.Fprintf(&b, " %*d  %*s  %-*s  %*s  %s\n",
			seqW, e.Seq+1, statusW, status, labelW, clip(labels[i], labelW),
			totalW, human(e.Timings.Total()), letters(e, cols, perCol))
		if e.Error != "" {
			fmt.Fprintf(&b, "%*s%s\n", 1+seqW+2+statusW+2, "", e.Error)
		}
	}

	key := make([]string, len(phases))
	for i, p := range phases {
		key[i] = p.letter + " " + p.name
	}
	fmt.Fprintf(&b, "\n %s\n", strings.Join(key, "  "))

	_, err := io.WriteString(w, b.String())
	return err
}

// letters draws a bar as one letter per column, at least one column wide.
func letters(e record.Entry, cols int, perCol float64) string {
	durs := split(e.Timings)
	start := float64(e.Offset)
	end := start + float64(e.Timings.Total())

	first := min(int(start/perCol), cols-1)
	last := min(max(int(math.Ceil(end/perCol))-1, first), cols-1)

	// Each column shows the phase that took up most of it.
	cell := func(c int) int {
		lo, hi := max(start, float64(c)*perCol), min(end, float64(c+1)*perCol)
		if hi <= lo {
			lo, hi = start, end
		}
		best, most := 0, 0.0
		at := start
		for i, d := range durs {
			next := at + float64(d)
			if overlap := min(next, hi) - max(at, lo); overlap > most {
				best, most = i, overlap
			}
			at = next
		}
		return best
	}

	var b strings.Builder
	b.WriteString(strings.Repeat(" ", first))
	for c := first; c <= last; c++ {
		b.WriteString(phases[cell(c)].letter)
	}
	return b.String()
}

// axis returns the tick labels and the ruler that sits under them.
func axis(span time.Duration, cols int) (labels, ruler string) {
	step := tickStep(span, max(cols/10, 1))
	perCol := float64(span) / float64(cols)

	lab := []rune(strings.Repeat(" ", cols))
	rul := []rune(strings.Repeat("─", cols))
	free := 0
	for t := time.Duration(0); t <= span; t += step {
		col := int(float64(t) / perCol)
		if col >= cols {
			break
		}
		rul[col] = '┬'
		// A label too close to the right edge backs up to end on its tick.
		s := []rune(tickLabel(t))
		if at := min(col, cols-len(s)); at >= free {
			copy(lab[at:], s)
			free = at + len(s) + 1
		}
	}
	return strings.TrimRight(string(lab), " "), string(rul)
}

// tickStep returns the smallest 1, 2 or 5 times a power of ten that cuts
// span into at most n intervals.
func tickStep(span time.Duration, n int) time.Duration {
	for mag := time.Nanosecond; ; mag *= 10 {
		for _, m := range []time.Duration{1, 2, 5} {
			if step := m * mag; step*time.Duration(n) >= span {
				return step
			}
		}
	}
}

func tickLabel(t time.Duration) string {
	switch {
	case t == 0:
		return "0"
	case t < time.Second:
		return fmt.Sprintf("%gms", float64(t)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%gs", t.Seconds())
	}
}

// human formats a duration to about three significant figures.
func human(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	switch {
	case ms >= 999.5:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case ms >= 9.95:
		return fmt.Sprintf("%.0fms", ms)
	default:
		return fmt.Sprintf("%.1fms", ms)
	}
}

// clip shortens s to n columns by cutting out its middle.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	head := n / 2
	return string(r[:head]) + "…" + string(r[len(r)-(n-head-1):])
}
