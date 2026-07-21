#!/usr/bin/env bash
# Runs once, during primary initialization (via /docker-entrypoint-initdb.d).
# Creates the replication role, the physical replication slots the standbys
# stream from, and the pg_hba rule that permits replication connections.
set -Eeuo pipefail

# Create the replication role and one physical slot per standby. Dedicated
# slots make the primary retain WAL until each standby has consumed it, so a
# briefly-disconnected replica can always catch up instead of needing a reclone.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-'SQL'
	CREATE ROLE replicator WITH REPLICATION LOGIN;
	SELECT pg_create_physical_replication_slot('replica1_slot');
	SELECT pg_create_physical_replication_slot('replica2_slot');
SQL

# Allow physical replication connections from the compose network.
#
# trust is deliberate and scoped to this dev cluster: the containers are only
# reachable on the isolated `pgnet` bridge, never published for replication.
# Production would use scram-sha-256 over TLS with a per-standby password;
# see docs/adr/0001-dev-cluster-replication.md.
cat >>"$PGDATA/pg_hba.conf" <<-'HBA'

	# pgpilot dev cluster: physical replication from the compose network
	host    replication     replicator      all                     trust
HBA
