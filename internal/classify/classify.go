// Package classify decides, from a query's parse tree, whether it may be served
// by a replica (a read) or must go to the primary (a write). It uses pg_query
// (the real PostgreSQL parser, never string matching) and errs toward the
// primary: anything it cannot prove is a safe read is treated as a write.
//
// The class is a routing decision, not a data-modification fact. Statements that
// take row locks, modify data through a CTE, call a volatile function, run under
// EXPLAIN ANALYZE, or open an explicit transaction all resolve to Write so the
// router keeps them — and, for a transaction, everything until it ends — on the
// primary.
package classify

import (
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Class is a routing class.
type Class uint8

const (
	// Read may be served by a replica.
	Read Class = iota
	// Write must be served by the primary.
	Write
)

func (c Class) String() string {
	if c == Write {
		return "write"
	}
	return "read"
}

// volatileFuncs is a curated set of common volatile functions. True volatility
// lives in the catalog (pg_proc.provolatile); without a catalog lookup this list
// covers the functions that matter most for routing — those that advance a
// sequence or otherwise must run on the primary.
var volatileFuncs = map[string]struct{}{
	"nextval": {}, "setval": {}, "currval": {}, "lastval": {},
	"random": {}, "gen_random_uuid": {}, "uuid_generate_v4": {},
	"clock_timestamp": {}, "timeofday": {}, "pg_sleep": {},
}

// Classify returns the routing class of a simple-query string, which may contain
// several statements. It is a Write if any statement is a Write.
func Classify(sql string) Class {
	result, err := pg.Parse(sql)
	if err != nil {
		return Write // unparseable: cannot prove it is a read
	}
	for _, raw := range result.Stmts {
		if classifyStmt(raw.Stmt) == Write {
			return Write
		}
	}
	return Read
}

func classifyStmt(n *pg.Node) Class {
	if n == nil {
		return Write
	}

	// EXPLAIN only executes its statement when ANALYZE is set; otherwise it just
	// plans, which is a read regardless of the underlying statement.
	if ex := n.GetExplainStmt(); ex != nil {
		if explainExecutes(ex) {
			return classifyStmt(ex.Query)
		}
		return Read
	}

	switch {
	case n.GetInsertStmt() != nil, n.GetUpdateStmt() != nil,
		n.GetDeleteStmt() != nil, n.GetMergeStmt() != nil:
		return Write
	}

	// A SELECT is a read unless it materializes a table with INTO, locks rows,
	// modifies data through a CTE, or calls a volatile function.
	if sel := n.GetSelectStmt(); sel != nil {
		if sel.IntoClause != nil {
			return Write
		}
		if writesInSubtree(n) {
			return Write
		}
		return Read
	}

	if n.GetVariableShowStmt() != nil { // SHOW
		return Read
	}

	// Transaction control (which also pins an explicit transaction to one
	// backend), DDL, SET, CALL, and anything else go to the primary.
	return Write
}

// explainExecutes reports whether an EXPLAIN runs its statement (EXPLAIN ANALYZE).
func explainExecutes(ex *pg.ExplainStmt) bool {
	for _, opt := range ex.Options {
		if d := opt.GetDefElem(); d != nil && strings.EqualFold(d.Defname, "analyze") {
			return true
		}
	}
	return false
}

// writesInSubtree reports whether root's parse tree contains a data-modifying
// statement (such as a data-modifying CTE), a row-locking SELECT, or a call to a
// volatile function.
func writesInSubtree(root *pg.Node) bool {
	found := false
	walk(root, func(n *pg.Node) {
		if found {
			return
		}
		switch {
		case n.GetInsertStmt() != nil, n.GetUpdateStmt() != nil,
			n.GetDeleteStmt() != nil, n.GetMergeStmt() != nil:
			found = true
		case n.GetSelectStmt() != nil:
			if len(n.GetSelectStmt().LockingClause) > 0 {
				found = true
			}
		}
		if fc := n.GetFuncCall(); fc != nil && isVolatile(fc) {
			found = true
		}
	})
	return found
}

func isVolatile(fc *pg.FuncCall) bool {
	parts := fc.GetFuncname()
	if len(parts) == 0 {
		return false
	}
	last := parts[len(parts)-1].GetString_()
	if last == nil {
		return false
	}
	_, ok := volatileFuncs[strings.ToLower(last.GetSval())]
	return ok
}

// walk visits every pg.Node in root's parse tree, depth-first.
func walk(n *pg.Node, visit func(*pg.Node)) {
	if n == nil {
		return
	}
	visit(n)
	walkChildren(n.ProtoReflect(), visit)
}

func walkChildren(m protoreflect.Message, visit func(*pg.Node)) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return true
		}
		if fd.IsMap() {
			return true // the AST has no map fields
		}
		if fd.IsList() {
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				visitMessage(list.Get(i).Message(), visit)
			}
			return true
		}
		visitMessage(v.Message(), visit)
		return true
	})
}

func visitMessage(m protoreflect.Message, visit func(*pg.Node)) {
	if node, ok := m.Interface().(*pg.Node); ok {
		walk(node, visit)
		return
	}
	walkChildren(m, visit)
}
