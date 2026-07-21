package protocol

import "testing"

func TestTxStatus_ValidAndInTransaction(t *testing.T) {
	cases := []struct {
		s     TxStatus
		valid bool
		inTx  bool
	}{
		{StatusIdle, true, false},
		{StatusInTx, true, true},
		{StatusInFailedTx, true, true},
		{TxStatus('X'), false, false},
		{TxStatus(0), false, false},
	}
	for _, c := range cases {
		if got := c.s.Valid(); got != c.valid {
			t.Errorf("TxStatus(%q).Valid() = %v, want %v", byte(c.s), got, c.valid)
		}
		if got := c.s.InTransaction(); got != c.inTx {
			t.Errorf("TxStatus(%q).InTransaction() = %v, want %v", byte(c.s), got, c.inTx)
		}
	}
}

func TestTxStatus_String(t *testing.T) {
	if got := StatusIdle.String(); got != "idle" {
		t.Errorf("StatusIdle.String() = %q", got)
	}
	if got := StatusInTx.String(); got != "in transaction" {
		t.Errorf("StatusInTx.String() = %q", got)
	}
	if got := StatusInFailedTx.String(); got != "in failed transaction" {
		t.Errorf("StatusInFailedTx.String() = %q", got)
	}
}

func TestTxTracker(t *testing.T) {
	var tr TxTracker

	if _, seen := tr.Status(); seen {
		t.Error("fresh tracker reports a status")
	}
	if tr.InTransaction() {
		t.Error("fresh tracker reports in-transaction")
	}

	if changed := tr.Update(StatusIdle); !changed {
		t.Error("first Update should report changed")
	}
	if changed := tr.Update(StatusIdle); changed {
		t.Error("repeat Update to the same status should not report changed")
	}
	if changed := tr.Update(StatusInTx); !changed {
		t.Error("Update to a new status should report changed")
	}
	if !tr.InTransaction() {
		t.Error("tracker should be in transaction after StatusInTx")
	}
	st, seen := tr.Status()
	if !seen || st != StatusInTx {
		t.Errorf("Status() = %q, %v; want 'T', true", byte(st), seen)
	}
}
