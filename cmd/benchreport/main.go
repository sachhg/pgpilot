// Command benchreport turns the loadgen JSON results in a directory into a
// Markdown results table and SVG charts. Result files are named
// "<scenario>__<target>.json"; benchreport groups them by scenario, orders the
// targets canonically, and writes results.md plus a throughput and a latency
// chart per scenario.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sachhg/pgpilot/internal/bench"
)

// targetOrder is the canonical left-to-right order of targets in tables/charts.
var targetOrder = []string{"direct", "pgbouncer", "pgpilot-strict", "pgpilot-relaxed", "pgpilot"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "benchreport:", err)
		os.Exit(1)
	}
}

func run() error {
	in := flag.String("in", "bench/results", "directory of <scenario>__<target>.json result files")
	out := flag.String("out", "bench/results", "directory to write results.md and charts into")
	flag.Parse()

	files, err := filepath.Glob(filepath.Join(*in, "*.json"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no result files in %s", *in)
	}

	scenarios, order, err := load(files)
	if err != nil {
		return err
	}

	var md strings.Builder
	md.WriteString("# Benchmark results\n\n")
	for _, sc := range order {
		results := sortTargets(scenarios[sc])
		md.WriteString("## " + sc + "\n\n")
		md.WriteString(markdownTable(results))
		md.WriteString("\n")

		tput := filepath.Join(*out, "throughput-"+sc+".svg")
		lat := filepath.Join(*out, "latency-"+sc+".svg")
		if err := os.WriteFile(tput, []byte(throughputChart(sc, results)), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(lat, []byte(latencyChart(sc, results)), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(&md, "![throughput](throughput-%s.svg)\n\n![latency](latency-%s.svg)\n\n", sc, sc)
	}
	return os.WriteFile(filepath.Join(*out, "results.md"), []byte(md.String()), 0o600)
}

// load reads each result file, grouping by scenario (the filename before "__")
// and returning the scenarios in first-seen order.
func load(files []string) (map[string][]bench.Result, []string, error) {
	scenarios := map[string][]bench.Result{}
	var order []string
	sort.Strings(files)
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".json")
		sc, _, ok := strings.Cut(base, "__")
		if !ok {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, err
		}
		var r bench.Result
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", f, err)
		}
		if _, seen := scenarios[sc]; !seen {
			order = append(order, sc)
		}
		scenarios[sc] = append(scenarios[sc], r)
	}
	return scenarios, order, nil
}

func sortTargets(rs []bench.Result) []bench.Result {
	rank := func(t string) int {
		for i, name := range targetOrder {
			if t == name {
				return i
			}
		}
		return len(targetOrder)
	}
	sort.SliceStable(rs, func(i, j int) bool { return rank(rs[i].Target) < rank(rs[j].Target) })
	return rs
}

func markdownTable(rs []bench.Result) string {
	var b strings.Builder
	b.WriteString("| target | tps | p50 | p95 | p99 | fence-fallback |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, r := range rs {
		fallback := "—"
		if r.FenceFallbackRate > 0 {
			fallback = fmt.Sprintf("%.1f%%", r.FenceFallbackRate*100)
		}
		fmt.Fprintf(&b, "| %s | %.0f | %s | %s | %s | %s |\n",
			r.Target, r.TPS, dur(r.P50), dur(r.P95), dur(r.P99), fallback)
	}
	return b.String()
}

func throughputChart(scenario string, rs []bench.Result) string {
	groups := make([]string, len(rs))
	vals := make([]float64, len(rs))
	for i, r := range rs {
		groups[i] = r.Target
		vals[i] = r.TPS
	}
	return bench.BarChartSVG("Throughput — "+scenario, "transactions/sec", groups,
		[]bench.Series{{Name: "tps", Values: vals}})
}

func latencyChart(scenario string, rs []bench.Result) string {
	groups := make([]string, len(rs))
	p50 := make([]float64, len(rs))
	p95 := make([]float64, len(rs))
	p99 := make([]float64, len(rs))
	for i, r := range rs {
		groups[i] = r.Target
		p50[i] = ms(r.P50)
		p95[i] = ms(r.P95)
		p99[i] = ms(r.P99)
	}
	return bench.BarChartSVG("Latency — "+scenario, "milliseconds", groups, []bench.Series{
		{Name: "p50", Values: p50},
		{Name: "p95", Values: p95},
		{Name: "p99", Values: p99},
	})
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func dur(d time.Duration) string { return d.Round(10 * time.Microsecond).String() }
