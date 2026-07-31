#!/usr/bin/env bash
# A guided tour of pgpilot: read/write routing, read-your-writes fencing, and
# the metrics endpoint. Prerequisites: Docker, and `make up` (the dev cluster).
#
# Record it as an asciinema cast with:
#   asciinema rec docs/demo.cast --overwrite -c ./docs/demo.sh
# and embed the resulting docs/demo.cast in a README or an asciinema.org upload.
set -euo pipefail
cd "$(dirname "$0")/.."

TMP="$(mktemp -d)"
PGPILOT_PID=""
cleanup() { [ -n "$PGPILOT_PID" ] && kill "$PGPILOT_PID" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT

say()  { printf '\n\033[1;36m# %s\033[0m\n' "$*"; sleep 1; }
run()  { printf '\033[1;32m$ %s\033[0m\n' "$*"; sleep 0.6; eval "$*"; sleep 0.8; }
# Run SQL through pgpilot from inside the primary container (it has psql).
sql()  { docker exec -e PGPASSWORD=pgpilot pgpilot-primary \
           psql -h host.docker.internal -p 6432 -U pgpilot -d pgpilot -tAc "$1"; }

say "pgpilot sits between clients and a primary + two replicas. Build and start it."
go build -o bin/pgpilot ./cmd/pgpilot
cat >"$TMP/pgpilot.json" <<'EOF'
{
  "listen": "127.0.0.1:6432",
  "primary": "127.0.0.1:55432",
  "replicas": ["127.0.0.1:55433", "127.0.0.1:55434"],
  "users": [{"name": "pgpilot", "password": "pgpilot"}],
  "pool": {"mode": "transaction", "max_size": 20},
  "fencing": {"mode": "strict"},
  "routing": {"policy": "round-robin"},
  "observability": {"metrics_addr": "127.0.0.1:9090"}
}
EOF
run "./bin/pgpilot -config $TMP/pgpilot.json -log-level warn &"
PGPILOT_PID=$!
sleep 2

say "Reads are routed to replicas. inet_server_addr() shows the backend's IP;"
say "pg_is_in_recovery() = t means a replica served it."
run "docker exec -e PGPASSWORD=pgpilot pgpilot-primary psql -h host.docker.internal -p 6432 -U pgpilot -d pgpilot -c \"SELECT inet_server_addr() AS backend, pg_is_in_recovery() AS replica\""
run "docker exec -e PGPASSWORD=pgpilot pgpilot-primary psql -h host.docker.internal -p 6432 -U pgpilot -d pgpilot -c \"SELECT inet_server_addr() AS backend, pg_is_in_recovery() AS replica\""

say "Writes go to the primary (pg_is_in_recovery() = f)."
run "docker exec -e PGPASSWORD=pgpilot pgpilot-primary psql -h host.docker.internal -p 6432 -U pgpilot -d pgpilot -c \"CREATE TABLE IF NOT EXISTS demo(id int primary key, note text)\""

say "Read-your-writes: write then read in ONE session. Strict fencing serves the"
say "read from the primary until replicas catch up, so it is never stale."
run "docker exec -e PGPASSWORD=pgpilot pgpilot-primary psql -h host.docker.internal -p 6432 -U pgpilot -d pgpilot -c \"INSERT INTO demo VALUES (1,'hello') ON CONFLICT (id) DO UPDATE SET note='hello'\" -c \"SELECT note FROM demo WHERE id=1\""

say "The metrics endpoint shows where queries went."
run "curl -s http://127.0.0.1:9090/metrics | grep -E 'pgpilot_routing_decisions_total|pgpilot_read_failovers_total' | grep -v '^#'"

say "That's pgpilot: transparent routing with read-your-writes, observable end to end."
