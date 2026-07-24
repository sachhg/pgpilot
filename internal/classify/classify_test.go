package classify_test

import (
	"testing"

	"github.com/sachhg/pgpilot/internal/classify"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want classify.Class
	}{
		// Plain reads.
		{"select", "SELECT 1", classify.Read},
		{"select with predicate", "SELECT * FROM accounts WHERE id = 1", classify.Read},
		{"select aggregate", "SELECT count(*) FROM accounts", classify.Read},
		{"select stable now", "SELECT now()", classify.Read},
		{"read-only cte", "WITH x AS (SELECT * FROM t) SELECT * FROM x", classify.Read},
		{"show", "SHOW search_path", classify.Read},
		{"empty", "", classify.Read},

		// Direct writes.
		{"insert", "INSERT INTO t VALUES (1)", classify.Write},
		{"update", "UPDATE t SET x = 1 WHERE id = 2", classify.Write},
		{"delete", "DELETE FROM t WHERE id = 1", classify.Write},
		{"merge", "MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE", classify.Write},

		// SELECT that must go to the primary.
		{"select for update", "SELECT * FROM t FOR UPDATE", classify.Write},
		{"select for share", "SELECT * FROM t FOR SHARE", classify.Write},
		{"select for no key update", "SELECT id FROM t FOR NO KEY UPDATE", classify.Write},
		{"data-modifying cte", "WITH x AS (INSERT INTO t VALUES (1) RETURNING *) SELECT * FROM x", classify.Write},
		{"update cte", "WITH x AS (UPDATE t SET a = 1 RETURNING *) SELECT * FROM x", classify.Write},
		{"volatile nextval", "SELECT nextval('s')", classify.Write},
		{"volatile setval", "SELECT setval('s', 1)", classify.Write},
		{"volatile random", "SELECT random()", classify.Write},
		{"volatile in predicate", "SELECT * FROM t WHERE id = nextval('s')", classify.Write},
		{"select into", "SELECT * INTO newt FROM t", classify.Write},

		// EXPLAIN.
		{"explain select", "EXPLAIN SELECT 1", classify.Read},
		{"explain analyze select", "EXPLAIN ANALYZE SELECT 1", classify.Read},
		{"explain insert (no analyze)", "EXPLAIN INSERT INTO t VALUES (1)", classify.Read},
		{"explain analyze insert", "EXPLAIN ANALYZE INSERT INTO t VALUES (1)", classify.Write},
		{"explain analyze update parens", "EXPLAIN (ANALYZE) UPDATE t SET x = 1", classify.Write},

		// Multi-statement simple queries.
		{"multi all reads", "SELECT 1; SELECT 2", classify.Read},
		{"multi with a write", "SELECT 1; INSERT INTO t VALUES (1)", classify.Write},

		// Explicit transaction blocks (pinned to the primary).
		{"begin", "BEGIN", classify.Write},
		{"start transaction", "START TRANSACTION", classify.Write},
		{"commit", "COMMIT", classify.Write},
		{"rollback", "ROLLBACK", classify.Write},
		{"savepoint", "SAVEPOINT sp", classify.Write},

		// DDL and session statements.
		{"create table", "CREATE TABLE t (id int)", classify.Write},
		{"truncate", "TRUNCATE t", classify.Write},
		{"set", "SET search_path = x", classify.Write},

		// Unparseable is conservatively a write.
		{"garbage", "SELCT !!! FRM", classify.Write},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify.Classify(tc.sql); got != tc.want {
				t.Errorf("Classify(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}
