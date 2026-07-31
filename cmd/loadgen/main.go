// Command loadgen is pgpilot's read-heavy benchmark driver. In setup mode it
// seeds the bench table; in run mode it drives a concurrent point-read/point-
// write workload against a connection string (a direct backend, pgpilot, or
// pgbouncer) and prints the throughput and latency percentiles. Given pgpilot's
// /metrics URL it also reports the fence-fallback rate observed during the run.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sachhg/pgpilot/internal/bench"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func run() error {
	conn := flag.String("conn", "", "pgx connection string (required)")
	mode := flag.String("mode", "run", "setup | run")
	rows := flag.Int("rows", 100000, "bench table size (setup and run)")
	duration := flag.Duration("duration", 30*time.Second, "run duration")
	conns := flag.Int("conns", 16, "concurrent connections")
	writeRatio := flag.Float64("write-ratio", 0.1, "fraction of transactions that are writes")
	target := flag.String("target", "pgpilot", "label for the target under test")
	metricsURL := flag.String("metrics-url", "", "pgpilot /metrics URL, to report the fence-fallback rate")
	jsonOut := flag.Bool("json", false, "emit the result as JSON")
	flag.Parse()

	if *conn == "" {
		return fmt.Errorf("-conn is required")
	}
	ctx := context.Background()

	switch *mode {
	case "setup":
		if err := bench.SetupSchema(ctx, *conn, *rows); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "seeded %d rows\n", *rows)
		return nil
	case "run":
		before := scrapeFallback(*metricsURL)
		res, err := bench.Run(ctx, *target, *conn, bench.Workload{
			Duration:    *duration,
			Connections: *conns,
			WriteRatio:  *writeRatio,
			Rows:        *rows,
		})
		if err != nil {
			return err
		}
		after := scrapeFallback(*metricsURL)
		res.FenceFallbackRate = fallbackRate(before, after)
		return output(res, *jsonOut)
	default:
		return fmt.Errorf("unknown mode %q (want setup or run)", *mode)
	}
}

func output(res bench.Result, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Printf("%-10s  tps=%.0f  p50=%s  p95=%s  p99=%s  mean=%s  ok=%d  err=%d",
		res.Target, res.TPS, dur(res.P50), dur(res.P95), dur(res.P99), dur(res.Mean),
		res.Transactions, res.Errors)
	if res.FenceFallbackRate > 0 {
		fmt.Printf("  fence-fallback=%.1f%%", res.FenceFallbackRate*100)
	}
	fmt.Println()
	return nil
}

func dur(d time.Duration) string { return d.Round(10 * time.Microsecond).String() }

// fallbackCounters holds the two metric values needed for the fence-fallback
// rate: reads served by a replica and reads that fell back to the primary.
type fallbackCounters struct {
	replicaReads float64
	fallbacks    float64
}

// scrapeFallback fetches pgpilot's metrics and extracts the fence-fallback
// counters. A missing or unreachable URL yields zeros, disabling the rate.
func scrapeFallback(url string) fallbackCounters {
	if url == "" {
		return fallbackCounters{}
	}
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fallbackCounters{}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fallbackCounters{}
	}
	return fallbackCounters{
		replicaReads: metricValue(string(body), `pgpilot_routing_decisions_total{reason="read",target="replica"}`),
		fallbacks:    metricValue(string(body), "pgpilot_fence_fallbacks_total"),
	}
}

// fallbackRate is the fraction of read attempts over the run that fell back to
// the primary, from the before/after counter deltas.
func fallbackRate(before, after fallbackCounters) float64 {
	fell := after.fallbacks - before.fallbacks
	reads := (after.replicaReads - before.replicaReads) + fell
	if reads <= 0 {
		return 0
	}
	return fell / reads
}

// metricValue finds the first Prometheus sample line beginning with series and
// returns its value, or 0 if absent.
func metricValue(body, series string) float64 {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, series) {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0
			}
			v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
			if err != nil {
				return 0
			}
			return v
		}
	}
	return 0
}
