// Package detect identifies the client features that make transaction pooling
// unsafe. In transaction mode a backend connection is shared across clients
// between transactions, so anything that leaves session state on the connection
// — a prepared statement, a temporary table, a LISTEN registration, or a
// session-level GUC — would leak into the next client. pgpilot detects these
// with pg_query (the real PostgreSQL parser, never regex) and pins the session
// to its backend when it sees one.
package detect

import (
	pg "github.com/pganalyze/pg_query_go/v6"
)

// Feature is a client feature that breaks transaction pooling.
type Feature string

const (
	// FeaturePreparedStatement is a SQL-level PREPARE (protocol-level prepared
	// statements are detected separately, from the extended-protocol messages).
	FeaturePreparedStatement Feature = "prepared statement"
	// FeatureTempTable is CREATE TEMP/TEMPORARY TABLE.
	FeatureTempTable Feature = "temporary table"
	// FeatureListenNotify is LISTEN, NOTIFY, or UNLISTEN.
	FeatureListenNotify Feature = "LISTEN/NOTIFY"
	// FeatureSessionGUC is a session-level SET or RESET (SET LOCAL is safe).
	FeatureSessionGUC Feature = "session GUC"
	// FeatureUnparseable is returned for SQL pgpilot cannot parse; it is treated
	// as breaking so an unrecognized statement never silently leaks state.
	FeatureUnparseable Feature = "unparseable statement"
)

// BreaksTxPooling reports the first feature in sql that makes transaction
// pooling unsafe, or ("", false) if the statement is safe to pool. sql is a
// simple-query string, which may contain multiple statements.
func BreaksTxPooling(sql string) (Feature, bool) {
	result, err := pg.Parse(sql)
	if err != nil {
		return FeatureUnparseable, true
	}
	for _, raw := range result.Stmts {
		if f, ok := breakingStmt(raw.Stmt); ok {
			return f, true
		}
	}
	return "", false
}

func breakingStmt(n *pg.Node) (Feature, bool) {
	switch {
	case n.GetPrepareStmt() != nil:
		return FeaturePreparedStatement, true
	case n.GetListenStmt() != nil, n.GetNotifyStmt() != nil, n.GetUnlistenStmt() != nil:
		return FeatureListenNotify, true
	case n.GetCreateStmt() != nil:
		if c := n.GetCreateStmt(); c.Relation != nil && c.Relation.Relpersistence == "t" {
			return FeatureTempTable, true
		}
	case n.GetVariableSetStmt() != nil:
		// SET LOCAL is scoped to the current transaction and is safe; a plain
		// SET or RESET changes the connection's session state.
		if v := n.GetVariableSetStmt(); !v.IsLocal {
			return FeatureSessionGUC, true
		}
	}
	return "", false
}
