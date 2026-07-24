package detect_test

import (
	"testing"

	"github.com/sachhg/pgpilot/internal/detect"
)

func TestBreaksTxPooling(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want detect.Feature // "" means safe
	}{
		{"plain select", "SELECT 1", ""},
		{"insert", "INSERT INTO t VALUES (1)", ""},
		{"update", "UPDATE t SET x = 1 WHERE id = 2", ""},
		{"begin", "BEGIN", ""},
		{"commit", "COMMIT", ""},
		{"select for update", "SELECT * FROM t FOR UPDATE", ""},
		{"cte with insert", "WITH x AS (INSERT INTO t VALUES (1) RETURNING *) SELECT * FROM x", ""},
		{"set local is safe", "SET LOCAL statement_timeout = '5s'", ""},
		{"permanent table is safe", "CREATE TABLE t (id int)", ""},

		{"session set", "SET search_path = myschema", detect.FeatureSessionGUC},
		{"session set session", "SET SESSION statement_timeout = 0", detect.FeatureSessionGUC},
		{"reset", "RESET search_path", detect.FeatureSessionGUC},
		{"temp table", "CREATE TEMP TABLE t (id int)", detect.FeatureTempTable},
		{"temporary table", "CREATE TEMPORARY TABLE t (id int)", detect.FeatureTempTable},
		{"listen", "LISTEN channel", detect.FeatureListenNotify},
		{"notify", "NOTIFY channel", detect.FeatureListenNotify},
		{"unlisten", "UNLISTEN channel", detect.FeatureListenNotify},
		{"prepare", "PREPARE p AS SELECT $1", detect.FeaturePreparedStatement},
		{"unparseable", "SELCT 1 FRM", detect.FeatureUnparseable},

		{"breaking in a multi-statement", "SELECT 1; SET search_path = x", detect.FeatureSessionGUC},
		{"safe multi-statement", "SELECT 1; SELECT 2", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, broke := detect.BreaksTxPooling(tc.sql)
			if tc.want == "" {
				if broke {
					t.Errorf("BreaksTxPooling(%q) = %q, true; want safe", tc.sql, got)
				}
				return
			}
			if !broke || got != tc.want {
				t.Errorf("BreaksTxPooling(%q) = %q, %v; want %q, true", tc.sql, got, broke, tc.want)
			}
		})
	}
}
