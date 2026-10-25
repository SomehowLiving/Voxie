package callreport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

// Load reads turn and call records from files and directories (a
// directory means its calls-*.jsonl). Lines that aren't records are skipped.
func Load(paths []string) (turns []Turn, calls []Summary, err error) {
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, nil, err
		}
		if st.IsDir() {
			m, _ := filepath.Glob(filepath.Join(p, "calls-*.jsonl"))
			sort.Strings(m)
			files = append(files, m...)
		} else {
			files = append(files, p)
		}
	}
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return nil, nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			var kind struct{ Type string }
			if json.Unmarshal(sc.Bytes(), &kind) != nil {
				continue
			}
			switch kind.Type {
			case "turn":
				var t Turn
				if json.Unmarshal(sc.Bytes(), &t) == nil {
					turns = append(turns, t)
				}
			case "call":
				var s Summary
				if json.Unmarshal(sc.Bytes(), &s) == nil {
					calls = append(calls, s)
				}
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return turns, calls, nil
}

// group is the numbers for one row of a table.
type group struct {
	turns, answered, clean int
	totals                 []int64
	signals                map[string]int
}

// answered: the turn needed a reply of its own (not merged into the next).
func (g *group) add(t Turn) {
	g.turns++
	if t.Superseded {
		return
	}
	g.answered++
	if len(t.Signals) == 0 {
		g.clean++
	}
	if t.Ms != nil && !t.Approx {
		g.totals = append(g.totals, t.Ms.Total)
	}
	if g.signals == nil {
		g.signals = map[string]int{}
	}
	for _, s := range t.Signals {
		g.signals[s]++
	}
}

var tableSignals = []string{SignalSlow, SignalSilent, SignalRepeat, SignalCheckIn, SignalCutEarly, SignalLangFlip, SignalLowConfidence}

// Summarize prints the report: calls, the headline (answered turns with no
// sign of trouble), and the same numbers per language, voice and listener.
func Summarize(w io.Writer, turns []Turn, calls []Summary) {
	if len(turns) == 0 && len(calls) == 0 {
		fmt.Fprintln(w, "No calls recorded yet.")
		return
	}
	ended := map[string]int{}
	hungUp, succeeded, judged := 0, 0, 0
	var minutes float64
	for _, c := range calls {
		ended[c.EndedBy]++
		hungUp += c.Signals[SignalHungUpMidReply]
		minutes += c.DurationS / 60
		if c.Outcome != nil && c.Outcome.Success != nil {
			judged++
			if *c.Outcome.Success {
				succeeded++
			}
		}
	}
	fmt.Fprintf(w, "Calls: %d, %.0f minutes; ended by the caller %d, the agent %d, an error %d", len(calls), minutes, ended["caller"], ended["agent"], ended["error"])
	if hungUp > 0 {
		fmt.Fprintf(w, "; %d hung up mid-reply", hungUp)
	}
	fmt.Fprintln(w)
	if judged > 0 {
		fmt.Fprintf(w, "Outcome (reported by the app): %d of %d calls succeeded\n", succeeded, judged)
	}

	var all group
	for _, t := range turns {
		all.add(t)
	}
	if all.answered > 0 {
		fmt.Fprintf(w, "\nNo sign of trouble on %s of %d answered turns. Reply time p50 %s, p95 %s.\n",
			pct(all.clean, all.answered), all.answered, ms(percentile(all.totals, 50)), ms(percentile(all.totals, 95)))
	}

	table(w, "By language", turns, func(t Turn) string { return t.Lang })
	table(w, "By voice", turns, func(t Turn) string { return t.Voice })
	table(w, "By listener", turns, func(t Turn) string { return t.Listener })
	fmt.Fprintln(w, "\nSignals: slow = first audio over 3s; silent = no reply; repeat = the caller said it again;")
	fmt.Fprintln(w, "check-in = \"hello? are you there?\"; cut early = cut off within 1.5s; flip = language A→B→A;")
	fmt.Fprintln(w, "unsure = low speech-to-text confidence. Reply times leave out turns whose speech end wasn't measured.")
}

func table(w io.Writer, title string, turns []Turn, key func(Turn) string) {
	groups := map[string]*group{}
	for _, t := range turns {
		k := key(t)
		if k == "" {
			k = "-"
		}
		if groups[k] == nil {
			groups[k] = &group{}
		}
		groups[k].add(t)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if a, b := groups[keys[i]].answered, groups[keys[j]].answered; a != b {
			return a > b
		}
		return keys[i] < keys[j]
	})
	fmt.Fprintf(w, "\n%s\n", title)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "\tturns\tno trouble\tp50\tp95\tslow\tsilent\trepeat\tcheck-in\tcut early\tflip\tunsure\t")
	for _, k := range keys {
		g := groups[k]
		row := []string{k, fmt.Sprint(g.answered), pct(g.clean, g.answered), ms(percentile(g.totals, 50)), ms(percentile(g.totals, 95))}
		for _, s := range tableSignals {
			row = append(row, fmt.Sprint(g.signals[s]))
		}
		fmt.Fprintln(tw, strings.Join(row, "\t")+"\t")
	}
	tw.Flush()
}

func pct(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", (n*100+of/2)/of)
}

func ms(v int64) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1fs", float64(v)/1000)
}
