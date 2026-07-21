package protocol

import (
	"fmt"
	"sync"
)

// TxStatus is the transaction status a backend reports in the ReadyForQuery
// message's indicator byte.
type TxStatus byte

// The transaction statuses a backend reports in a ReadyForQuery message.
const (
	StatusIdle       TxStatus = 'I' // not in a transaction block
	StatusInTx       TxStatus = 'T' // in a transaction block
	StatusInFailedTx TxStatus = 'E' // in a failed transaction block (until rollback)
)

// Valid reports whether s is one of the three defined statuses.
func (s TxStatus) Valid() bool {
	switch s {
	case StatusIdle, StatusInTx, StatusInFailedTx:
		return true
	default:
		return false
	}
}

// InTransaction reports whether s is inside a transaction block, open or failed.
func (s TxStatus) InTransaction() bool {
	return s == StatusInTx || s == StatusInFailedTx
}

// String returns a human-readable description of the status.
func (s TxStatus) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusInTx:
		return "in transaction"
	case StatusInFailedTx:
		return "in failed transaction"
	default:
		return fmt.Sprintf("unknown(%q)", byte(s))
	}
}

// TxTracker records the latest transaction status observed on a session. The
// router (a later phase) reads it to decide whether a session is pinned to its
// current backend. It is safe for concurrent use.
type TxTracker struct {
	mu     sync.Mutex
	status TxStatus
	seen   bool
}

// Update records a new status and reports whether it changed the tracked value.
func (t *TxTracker) Update(s TxStatus) (changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	changed = !t.seen || t.status != s
	t.status = s
	t.seen = true
	return changed
}

// Status returns the latest status and whether any has been recorded yet.
func (t *TxTracker) Status() (TxStatus, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status, t.seen
}

// InTransaction reports whether the session is currently inside a transaction.
func (t *TxTracker) InTransaction() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen && t.status.InTransaction()
}
