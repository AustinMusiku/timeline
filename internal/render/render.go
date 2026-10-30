// Package render draws records in the terminal.
package render

import (
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AustinMusiku/timeline/internal/record"
)

type Options struct {
	// Width is how many columns to fill. Values below 1 mean 80.
	Width int
	// Color draws the bars as coloured blocks instead of letters.
	Color bool
}

// SGR foreground codes; adding 10 gives the background. Red is for failures.
const (
	red       = 31
	green     = 32
	yellow    = 33
	blue      = 34
	magenta   = 35
	cyan      = 36
	textColor = 39 // the terminal's own; it has no background form
	gray      = 90
)

// phases lists the parts of a bar in the order they happen.
var phases = [...]struct {
	name   string
	letter string
	color  int
}{
	{"blocked", "-", gray},
	{"dns", "d", cyan},
	{"connect", "c", yellow},
	{"tls", "t", magenta},
	{"send", "s", textColor},
	{"wait", "w", green},
	{"receive", "r", blue},
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
	in := ink(opts.Color)
	width := opts.Width
	if width < 1 {
		width = 80
	}

	var span time.Duration
	seqW, labelW, connW := 0, 0, 0
	labels := make([]string, len(run.Entries))
	for i, e := range run.Entries {
		span = max(span, e.Offset+e.Timings.Total())
		seqW = max(seqW, len(strconv.Itoa(e.Seq+1)))
		labels[i] = e.Request.Name
		if labels[i] == "" {
			labels[i] = redactURL(e.Request.URL)
			if e.Request.Method != http.MethodGet {
				labels[i] = e.Request.Method + " " + labels[i]
			}
		}
		if e.Hop > 0 {
			labels[i] = "↳ " + labels[i]
		}
		labelW = max(labelW, utf8.RuneCountInString(labels[i]))
		if e.Conn.ID > 0 {
			connW = max(connW, len(connTag(e.Conn.ID)))
		}
	}
	span = max(span, 1)

	// A row reads: seq, status, connection, label, total, bar. The
	// connection is left out of a run that never made one, and the seq of
	// a redirect's hops.
	lead := 1 + seqW + 2 + statusW + 2
	if connW > 0 {
		lead += connW + 2
	}
	fixed := lead + 2 + totalW + 2
	labelW = max(min(labelW, (width-fixed)/2), minLabelW)
	cols := max(width-fixed-labelW, minCols)
	perCol := float64(span) / float64(cols)

	var b strings.Builder
	indent := strings.Repeat(" ", fixed+labelW)
	ticks, ruler := axis(span, cols)
	fmt.Fprintf(&b, "%s%s\n%s%s\n", indent, in.paint(gray, ticks), indent, in.paint(gray, ruler))

	// Coloured bars that overlap in time would run together, so space the
	// rows out.
	spaced := false
	for i := 1; opts.Color && i < len(run.Entries); i++ {
		prev, e := run.Entries[i-1], run.Entries[i]
		if e.Round == prev.Round && e.Offset < prev.Offset+prev.Timings.Total() {
			spaced = true
		}
	}

	for i, e := range run.Entries {
		if i > 0 && spaced {
			b.WriteByte('\n')
		}
		if i > 0 && e.Round != run.Entries[i-1].Round {
			b.WriteByte('\n')
		}

		status, failed := "ERR", true
		if e.Response != nil {
			status = strconv.Itoa(e.Response.Status)
			failed = e.Error != "" || e.Response.Status >= 400
		}
		status = fmt.Sprintf("%*s", statusW, status)
		if failed {
			status = in.paint(red, status)
		}

		bar := letters(e, cols, perCol)
		if opts.Color {
			bar = blocks(e, cols, perCol)
		}
		conn := ""
		if connW > 0 {
			if e.Conn.ID > 0 {
				conn = connTag(e.Conn.ID)
			}
			conn = in.paint(gray, conn) + strings.Repeat(" ", connW-len(conn)+2)
		}
		seq := ""
		if e.Hop == 0 {
			seq = strconv.Itoa(e.Seq + 1)
		}
		fmt.Fprintf(&b, " %*s  %s  %s%-*s  %*s  %s\n",
			seqW, seq, status, conn, labelW, clip(labels[i], labelW),
			totalW, human(e.Timings.Total()), bar)
		if e.Error != "" {
			fmt.Fprintf(&b, "%*s%s\n", lead, "", e.Error)
		}
	}

	if connW > 0 {
		b.WriteByte('\n')
		for _, line := range connections(run, connW, in) {
			fmt.Fprintf(&b, " %s\n", line)
		}
	}

	key := make([]string, len(phases))
	for i, p := range phases {
		swatch := p.letter
		if opts.Color {
			swatch = sgr("█", p.color)
		}
		key[i] = swatch + " " + p.name
	}
	fmt.Fprintf(&b, "\n %s\n", strings.Join(key, "  "))

	_, err := io.WriteString(w, b.String())
	return err
}

func connTag(id int) string { return "#" + strconv.Itoa(id) }

// connections describes each connection of the run on a line of its own.
func connections(run *record.Run, tagW int, in ink) []string {
	conns, protos := map[int]record.Conn{}, map[int]string{}
	addrW := 0
	for _, e := range run.Entries {
		id := e.Conn.ID
		if id == 0 {
			continue
		}
		if _, ok := conns[id]; !ok {
			conns[id] = e.Conn
			addrW = max(addrW, len(e.Conn.RemoteAddr))
		}
		if e.Response != nil && protos[id] == "" {
			protos[id] = e.Response.Proto
		}
	}

	lines := make([]string, 0, len(conns))
	for _, id := range slices.Sorted(maps.Keys(conns)) {
		c := conns[id]
		tag := connTag(id)
		parts := []string{
			in.paint(gray, tag) + strings.Repeat(" ", tagW-len(tag)),
			fmt.Sprintf("%-*s", addrW, c.RemoteAddr),
		}
		for _, s := range []string{protos[id], c.TLSVersion} {
			if s != "" {
				parts = append(parts, s)
			}
		}
		if c.TLSResumed {
			parts = append(parts, "resumed")
		}
		lines = append(lines, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	return lines
}

// ink writes colour, or leaves text alone when colour is off.
type ink bool

func (on ink) paint(code int, s string) string {
	if !on || s == "" {
		return s
	}
	return sgr(s, code)
}

// sgr wraps s in the given SGR codes and a reset.
func sgr(s string, codes ...int) string {
	var b strings.Builder
	b.WriteString("\x1b[")
	for i, c := range codes {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(strconv.Itoa(c))
	}
	b.WriteByte('m')
	b.WriteString(s)
	b.WriteString("\x1b[0m")
	return b.String()
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

// eighths fill a cell from its left edge, an eighth at a time.
var eighths = [...]string{" ", "▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"}

const (
	empty      = -1 // no phase
	minEighths = 2  // narrowest bar drawn
)

// cell is one column of a coloured bar: k eighths of left, then right.
type cell struct{ left, right, k int }

// run is n eighths of one phase within a column.
type run struct{ phase, n int }

// blocks draws a bar in colour.
func blocks(e record.Entry, cols int, perCol float64) string {
	first, cells := subcells(e, cols, perCol)

	var b strings.Builder
	b.WriteString(strings.Repeat(" ", first))
	for i := 0; i < len(cells); {
		c := cells[i]
		if c.k < 8 {
			b.WriteString(edge(c))
			i++
			continue
		}
		n := 1
		for i+n < len(cells) && cells[i+n] == c {
			n++
		}
		b.WriteString(sgr(strings.Repeat(eighths[8], n), phases[c.left].color))
		i += n
	}
	return b.String()
}

// edge draws a cell split between two colours. Reverse video is the only
// way to leave the left part empty or to put the text colour on the right.
func edge(c cell) string {
	if c.left == empty || (c.right != empty && phases[c.right].color == textColor) {
		codes := []int{7, phases[c.right].color}
		if c.left != empty {
			codes = append(codes, phases[c.left].color+10)
		}
		return sgr(eighths[c.k], codes...)
	}
	codes := []int{phases[c.left].color}
	if c.right != empty {
		codes = append(codes, phases[c.right].color+10)
	}
	return sgr(eighths[c.k], codes...)
}

// subcells lays out a bar to the nearest eighth of a column.
func subcells(e record.Entry, cols int, perCol float64) (first int, cells []cell) {
	durs := split(e.Timings)

	// edges[i] and edges[i+1] bound phase i, in eighths of a column.
	var edges [len(phases) + 1]int
	eighth := func(t float64) int { return int(math.Round(t / perCol * 8)) }

	at, longest, lastDrawn := float64(e.Offset), 0, -1
	edges[0] = eighth(at)
	for i, d := range durs {
		at += float64(d)
		edges[i+1] = edges[i]
		// Phases under half an eighth are left out.
		if eighth(float64(d)) > 0 {
			edges[i+1] = max(edges[i], eighth(at))
			lastDrawn = i
		}
		if d > durs[longest] {
			longest = i
		}
	}
	// The last phase drawn runs to the end of the request.
	for i := max(lastDrawn, 0) + 1; i < len(edges); i++ {
		edges[i] = eighth(at)
	}

	// Widen a request too short to see.
	if from := edges[0]; edges[len(phases)]-from < minEighths {
		from = min(from, cols*8-minEighths)
		for i := range edges {
			edges[i] = from
			if i > longest {
				edges[i] += minEighths
			}
		}
	}

	first = edges[0] / 8
	for c := first; c <= (edges[len(phases)]-1)/8; c++ {
		lo, hi := c*8, c*8+8
		var runs []run
		if n := edges[0] - lo; n > 0 {
			runs = append(runs, run{empty, n})
		}
		for i := range phases {
			if n := min(edges[i+1], hi) - max(edges[i], lo); n > 0 {
				runs = append(runs, run{i, n})
			}
		}
		if n := hi - edges[len(phases)]; n > 0 {
			runs = append(runs, run{empty, n})
		}

		runs = settle(runs)
		if len(runs) == 1 {
			cells = append(cells, cell{runs[0].phase, runs[0].phase, 8})
		} else {
			cells = append(cells, cell{runs[0].phase, runs[1].phase, runs[0].n})
		}
	}
	return first, cells
}

// settle merges the shortest run into a neighbouring phase until two are
// left, since a cell can show only one edge.
func settle(runs []run) []run {
	for len(runs) > 2 {
		bars := 0
		for _, r := range runs {
			if r.phase != empty {
				bars++
			}
		}

		v := -1
		for i, r := range runs {
			if r.phase != empty && bars == 1 {
				continue
			}
			if v < 0 || r.n < runs[v].n || (r.n == runs[v].n && runs[v].phase == empty) {
				v = i
			}
		}

		into := -1
		for _, i := range []int{v - 1, v + 1} {
			if i < 0 || i >= len(runs) || runs[i].phase == empty {
				continue
			}
			if into < 0 || runs[i].n > runs[into].n {
				into = i
			}
		}
		runs[into].n += runs[v].n
		runs = slices.Delete(runs, v, v+1)
	}
	return runs
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
