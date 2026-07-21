#!/usr/bin/env bash
# Entrypoint for the standby containers.
#
# On a fresh data volume this clones the primary with pg_basebackup and writes
# the standby recovery configuration, then hands off to the stock postgres
# entrypoint. On an existing volume it skips straight to starting postgres, which
# resumes streaming from where it left off using its replication slot.
set -Eeuo pipefail

: "${PGDATA:=/var/lib/postgresql/data}"
: "${PRIMARY_HOST:=primary}"
: "${PRIMARY_PORT:=5432}"
: "${REPLICATION_USER:=replicator}"
: "${REPLICATION_SLOT:?REPLICATION_SLOT must be set}"

bootstrap_standby() {
	echo "replica: waiting for primary ${PRIMARY_HOST}:${PRIMARY_PORT} to accept connections"
	until pg_isready --host="$PRIMARY_HOST" --port="$PRIMARY_PORT" --username="$REPLICATION_USER" --quiet; do
		sleep 1
	done

	echo "replica: cloning primary via pg_basebackup (slot=${REPLICATION_SLOT})"
	rm -rf "${PGDATA:?}"/*
	gosu postgres pg_basebackup \
		--host="$PRIMARY_HOST" \
		--port="$PRIMARY_PORT" \
		--username="$REPLICATION_USER" \
		--pgdata="$PGDATA" \
		--wal-method=stream \
		--slot="$REPLICATION_SLOT" \
		--write-recovery-conf \
		--checkpoint=fast \
		--progress \
		--verbose
	echo "replica: base backup complete; standby.signal written"
}

if [ ! -s "$PGDATA/PG_VERSION" ]; then
	mkdir -p "$PGDATA"
	chown postgres:postgres "$PGDATA"
	chmod 700 "$PGDATA"
	bootstrap_standby
	chown -R postgres:postgres "$PGDATA"
fi

echo "replica: starting postgres as a hot standby"
exec docker-entrypoint.sh "$@"
