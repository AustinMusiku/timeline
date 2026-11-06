package render

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AustinMusiku/timeline/internal/record"
)

// times holds a request's total, then each phase.
type times [1 + len(phases)]time.Duration

// try is one round of a request, with its redirects added in.
type try struct {
	round  int
	times  times
	failed bool
}

// summaryRow is one line of numbers under a request.
type summaryRow struct {
	name  string
	cells []string
	note  string
}

// Summary sets each request's first round beside the median of its later
// ones, a blank line below whatever came before. It writes nothing for a
// run of one round.
func Summary(w io.Writer, run *record.Run, opts Options) error {
	type key struct{ seq, round int }
	labels, byKey := map[int]string{}, map[key]*try{}
	rounds := 0
	for _, e := range run.Entries {
		rounds = max(rounds, e.Round+1)
		if e.Hop == 0 {
			labels[e.Seq] = label(e.Request)
		}
		k := key{e.Seq, e.Round}
		t := byKey[k]
		if t == nil {
			t = &try{round: e.Round}
			byKey[k] = t
		}
		t.times[0] += e.Timings.Total()
		for i, d := range split(e.Timings) {
			t.times[i+1] += d
		}
		t.failed = t.failed || e.Error != ""
	}
	if rounds < 2 {
		return nil
	}

	tries := map[int][]*try{}
	for _, k := range slices.SortedFunc(maps.Keys(byKey), func(a, b key) int { return a.round - b.round }) {
		tries[k.seq] = append(tries[k.seq], byKey[k])
	}
	seqs := slices.Sorted(maps.Keys(tries))
	rows := map[int][]summaryRow{}
	for _, seq := range seqs {
		ts := tries[seq]
		if ts[0].round == 0 {
			rows[seq] = append(rows[seq], summaryRow{name: "round 1", cells: cells(ts[:1])})
			ts = ts[1:]
		}
		if len(ts) > 0 {
			rows[seq] = append(rows[seq], later(ts))
		}
	}

	heads := []string{"total"}
	for _, p := range phases {
		heads = append(heads, p.name)
	}
	nameW, widths := 0, make([]int, len(heads))
	for i, h := range heads {
		widths[i] = len(h)
	}
	for _, rs := range rows {
		for _, r := range rs {
			nameW = max(nameW, utf8.RuneCountInString(r.name))
			for i, c := range r.cells {
				widths[i] = max(widths[i], len(c))
			}
		}
	}

	seqW := len(strconv.Itoa(seqs[len(seqs)-1] + 1))
	lead := 1 + seqW + 2 // past the request's number
	width := opts.Width
	if width < 1 {
		width = 80
	}
	in := ink(opts.Color)

	var b strings.Builder
	var head strings.Builder
	for i, h := range heads {
		fmt.Fprintf(&head, "  %*s", widths[i], h)
	}
	fmt.Fprintf(&b, "\n%*s%s\n", lead+nameW, "", in.paint(gray, head.String()))
	for _, seq := range seqs {
		name := labels[seq]
		if !opts.FullLabels {
			name = clip(name, max(width-lead, minLabelW))
		}
		fmt.Fprintf(&b, " %*d  %s\n", seqW, seq+1, name)
		for _, r := range rows[seq] {
			line := fmt.Sprintf("%*s%-*s", lead, "", nameW, r.name)
			for i, c := range r.cells {
				pad := strings.Repeat(" ", 2+widths[i]-len(c))
				if c == "failed" {
					c = in.paint(red, c)
				}
				line += pad + c
			}
			if r.note != "" {
				line += "  " + r.note
			}
			b.WriteString(strings.TrimRight(line, " ") + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// later sums up the rounds after the first: the median of those that
// didn't fail.
func later(ts []*try) summaryRow {
	first, last := ts[0].round+1, ts[len(ts)-1].round+1
	if len(ts) == 1 {
		return summaryRow{name: fmt.Sprintf("round %d", first), cells: cells(ts)}
	}
	var ok []*try
	for _, t := range ts {
		if !t.failed {
			ok = append(ok, t)
		}
	}
	switch failed := len(ts) - len(ok); {
	case len(ok) == 0:
		return summaryRow{name: fmt.Sprintf("rounds %d–%d", first, last), cells: []string{"failed"}}
	case failed > 0:
		return summaryRow{
			name:  fmt.Sprintf("median %d–%d", first, last),
			cells: cells(ok),
			note:  fmt.Sprintf("(%d of %d failed)", failed, len(ts)),
		}
	}
	return summaryRow{name: fmt.Sprintf("median %d–%d", first, last), cells: cells(ok)}
}

// cells formats the median of each column, with "-" for a phase too short
// to show. A lone failed try reads "failed".
func cells(ts []*try) []string {
	if len(ts) == 1 && ts[0].failed {
		return []string{"failed"}
	}
	out := make([]string, len(times{}))
	for i := range out {
		col := make([]time.Duration, len(ts))
		for j, t := range ts {
			col[j] = t.times[i]
		}
		out[i] = "-"
		if d := median(col); d >= 50*time.Microsecond || i == 0 {
			out[i] = human(d)
		}
	}
	return out
}

func median(ds []time.Duration) time.Duration {
	s := slices.Sorted(slices.Values(ds))
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
