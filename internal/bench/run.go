package bench

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tableName is the bench table; SetupSchema (re)creates it.
const tableName = "loadgen_accounts"

// SetupSchema drops and recreates the bench table with rows rows on the backend
// reachable at connString. It must run against a primary (it writes).
func SetupSchema(ctx context.Context, connString string, rows int) error {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return fmt.Errorf("bench: connect for setup: %w", err)
	}
	defer pool.Close()

	stmts := []string{
		"DROP TABLE IF EXISTS " + tableName,
		"CREATE TABLE " + tableName + " (id int PRIMARY KEY, balance bigint NOT NULL)",
		fmt.Sprintf("INSERT INTO %s SELECT g, (g * 100) FROM generate_series(1, %d) g", tableName, rows),
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("bench: setup %q: %w", s, err)
		}
	}
	return nil
}

// Run drives the workload against connString for w.Duration using w.Connections
// concurrent workers and returns the aggregated Result. Each worker runs an
// independent read/write stream; a failed transaction is counted as an error and
// excluded from latency stats. FenceFallbackRate is left zero for the caller to
// fill from pgpilot's metrics.
func Run(ctx context.Context, target, connString string, w Workload) (Result, error) {
	if w.Connections < 1 {
		return Result{}, fmt.Errorf("bench: connections must be >= 1, got %d", w.Connections)
	}
	if w.Rows < 1 {
		return Result{}, fmt.Errorf("bench: rows must be >= 1, got %d", w.Rows)
	}
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return Result{}, fmt.Errorf("bench: parse conn string: %w", err)
	}
	conns := int32(w.Connections) //nolint:gosec // G115: validated >= 1 above and realistically small
	cfg.MaxConns = conns
	cfg.MinConns = conns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return Result{}, fmt.Errorf("bench: connect: %w", err)
	}
	defer pool.Close()

	readSQL := "SELECT balance FROM " + tableName + " WHERE id = $1"
	writeSQL := "UPDATE " + tableName + " SET balance = balance + 1 WHERE id = $1"

	type workerOut struct {
		latencies []time.Duration
		errors    int
	}
	outs := make([]workerOut, w.Connections)

	runCtx, cancel := context.WithTimeout(ctx, w.Duration)
	defer cancel()

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < w.Connections; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// Per-worker RNG for id selection; a benchmark, so weak rand is fine.
			rng := rand.New(rand.NewPCG(uint64(id)+1, 0x9e3779b9)) //nolint:gosec // G404: benchmark id picker, not security
			var lat []time.Duration
			errs := 0
			for n := 0; runCtx.Err() == nil; n++ {
				rowID := rng.IntN(w.Rows) + 1
				t0 := time.Now()
				var execErr error
				if w.isWrite(n) {
					_, execErr = pool.Exec(runCtx, writeSQL, rowID)
				} else {
					var balance int64
					execErr = pool.QueryRow(runCtx, readSQL, rowID).Scan(&balance)
				}
				if execErr != nil {
					if runCtx.Err() != nil {
						break // deadline hit mid-call: not a real error
					}
					errs++
					continue
				}
				lat = append(lat, time.Since(t0))
			}
			outs[id] = workerOut{latencies: lat, errors: errs}
		}(i)
	}
	wg.Wait()
	wall := time.Since(start)

	var all []time.Duration
	totalErrs := 0
	for _, o := range outs {
		all = append(all, o.latencies...)
		totalErrs += o.errors
	}
	return Summarize(target, all, totalErrs, wall), nil
}
