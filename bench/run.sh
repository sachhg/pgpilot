#!/usr/bin/env bash
# Benchmark driver: compares a direct backend connection, pgbouncer, and pgpilot
# (strict and relaxed fencing) on a read-only and a read-heavy mixed workload,
# then renders a results table and charts.
#
# Prerequisites: `make up` (the cluster) and `make bench-up` (pgbouncer).
# Tunables (env): DURATION, CONNS, ROWS, WRITE_RATIO.
set -euo pipefail
cd "$(dirname "$0")/.."

DURATION="${DURATION:-20s}"
CONNS="${CONNS:-16}"
ROWS="${ROWS:-100000}"
WRITE_RATIO="${WRITE_RATIO:-0.2}"

RESULTS="bench/results"
TMP="$(mktemp -d)"
trap 'kill ${PIDS:-} 2>/dev/null || true; rm -rf "$TMP"' EXIT

mkdir -p "$RESULTS"
rm -f "$RESULTS"/*.json "$RESULTS"/*.svg "$RESULTS/results.md"

echo "== building loadgen and pgpilot =="
go build -o bin/loadgen ./cmd/loadgen
go build -o bin/pgpilot ./cmd/pgpilot
go build -o bin/benchreport ./cmd/benchreport

DIRECT="postgres://pgpilot:pgpilot@127.0.0.1:55432/pgpilot?sslmode=disable"
PGBOUNCER="postgres://pgpilot:pgpilot@127.0.0.1:6433/pgpilot?sslmode=disable"
PGPILOT_STRICT="postgres://pgpilot:pgpilot@127.0.0.1:6432/pgpilot?sslmode=disable"
PGPILOT_RELAXED="postgres://pgpilot:pgpilot@127.0.0.1:6434/pgpilot?sslmode=disable"
METRICS_STRICT="http://127.0.0.1:9090/metrics"
METRICS_RELAXED="http://127.0.0.1:9091/metrics"

# Two pgpilot instances against the same cluster: strict and relaxed fencing.
gen_config() {
  local listen="$1" metrics="$2" mode="$3"
  cat >"$TMP/pgpilot-$mode.json" <<EOF
{
  "listen": "127.0.0.1:$listen",
  "primary": "127.0.0.1:55432",
  "replicas": ["127.0.0.1:55433", "127.0.0.1:55434"],
  "users": [{"name": "pgpilot", "password": "pgpilot"}],
  "pool": {"mode": "transaction", "max_size": 24, "acquire_timeout": "5s", "idle_timeout": "2s"},
  "fencing": {"mode": "$mode", "bounded_ms": 100},
  "routing": {"policy": "least-in-flight"},
  "observability": {"metrics_addr": "127.0.0.1:$( [ "$mode" = strict ] && echo 9090 || echo 9091 )"}
}
EOF
}
gen_config 6432 9090 strict
gen_config 6434 9091 relaxed

echo "== starting pgpilot (strict :6432, relaxed :6434) =="
./bin/pgpilot -config "$TMP/pgpilot-strict.json"  -log-level warn >"$TMP/strict.log"  2>&1 &
PIDS="$!"
./bin/pgpilot -config "$TMP/pgpilot-relaxed.json" -log-level warn >"$TMP/relaxed.log" 2>&1 &
PIDS="$PIDS $!"

# Wait for both metrics endpoints to answer.
for url in "$METRICS_STRICT" "$METRICS_RELAXED"; do
  for _ in $(seq 1 50); do
    if curl -sf -o /dev/null "$url"; then break; fi
    sleep 0.2
  done
done

echo "== seeding $ROWS rows =="
./bin/loadgen -conn "$DIRECT" -mode setup -rows "$ROWS"

# run <scenario> <target-label> <conn> <write-ratio> [metrics-url]
run() {
  local scenario="$1" label="$2" conn="$3" wr="$4" metrics="${5:-}"
  echo "-- $scenario / $label (write-ratio=$wr) --"
  # Warm the pools and (for pgpilot) the routing policy before measuring.
  ./bin/loadgen -conn "$conn" -target "$label" -duration 2s -conns "$CONNS" \
    -write-ratio "$wr" -rows "$ROWS" >/dev/null 2>&1 || true
  ./bin/loadgen -conn "$conn" -target "$label" -duration "$DURATION" -conns "$CONNS" \
    -write-ratio "$wr" -rows "$ROWS" ${metrics:+-metrics-url "$metrics"} \
    -json >"$RESULTS/${scenario}__${label}.json"
  # Let idle backend connections drain before the next target so a long-lived
  # pgpilot/pgbouncer pool does not starve the primary's connection slots.
  sleep 3
}

echo "== scenario: read-only (write-ratio 0) =="
run readonly direct          "$DIRECT"          0
run readonly pgbouncer       "$PGBOUNCER"       0
run readonly pgpilot-strict  "$PGPILOT_STRICT"  0 "$METRICS_STRICT"
run readonly pgpilot-relaxed "$PGPILOT_RELAXED" 0 "$METRICS_RELAXED"

echo "== scenario: read-heavy mixed (write-ratio $WRITE_RATIO) =="
run mixed direct          "$DIRECT"          "$WRITE_RATIO"
run mixed pgbouncer       "$PGBOUNCER"       "$WRITE_RATIO"
run mixed pgpilot-strict  "$PGPILOT_STRICT"  "$WRITE_RATIO" "$METRICS_STRICT"
run mixed pgpilot-relaxed "$PGPILOT_RELAXED" "$WRITE_RATIO" "$METRICS_RELAXED"

echo "== pgbench (TPC-B, write-heavy) baseline =="
# Standard pgbench, run from the primary container against each target's host
# port. TPC-B is write-heavy, so it mostly exercises the primary path and proxy
# overhead; the custom generator above covers the read-routing story.
DURATION_SECS="${DURATION%s}"
PGBENCH="docker exec -e PGPASSWORD=pgpilot pgpilot-primary pgbench -h host.docker.internal -U pgpilot"
echo "-- initializing pgbench (scale 10) --"
$PGBENCH -p 55432 -i -s 10 -q pgpilot >/dev/null 2>&1

{
  echo "# pgbench (TPC-B, write-heavy) baseline"
  echo
  echo "| target | tps | latency avg |"
  echo "| --- | ---: | ---: |"
} >"$RESULTS/pgbench.md"

pgbench_run() {
  local label="$1" port="$2"
  echo "-- pgbench / $label --"
  local out tps lat
  out="$($PGBENCH -p "$port" -c "$CONNS" -T "$DURATION_SECS" -n pgpilot 2>&1)"
  tps="$(echo "$out" | awk '/tps =/{printf "%.0f", $3}')"
  lat="$(echo "$out" | awk '/latency average/{print $4}')"
  echo "| $label | ${tps:-n/a} | ${lat:-n/a} ms |" >>"$RESULTS/pgbench.md"
  sleep 3
}
pgbench_run direct         55432
pgbench_run pgbouncer      6433
pgbench_run pgpilot-strict 6432

echo "== capturing environment =="
{
  echo "date_utc: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "uname: $(uname -srm)"
  if command -v sysctl >/dev/null; then
    echo "cpu: $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
    echo "cores: $(sysctl -n hw.ncpu 2>/dev/null || echo unknown)"
    echo "mem_bytes: $(sysctl -n hw.memsize 2>/dev/null || echo unknown)"
  fi
  echo "go: $(go version | awk '{print $3}')"
  echo "docker: $(docker --version)"
  echo "postgres_image: postgres:16"
  echo "pgbouncer_image: edoburu/pgbouncer:v1.24.1-p1"
  echo "workload: duration=$DURATION conns=$CONNS rows=$ROWS write_ratio=$WRITE_RATIO"
} >"$RESULTS/env.txt"

echo "== rendering report =="
./bin/benchreport -in "$RESULTS" -out "$RESULTS"
echo "done -> $RESULTS/results.md"
