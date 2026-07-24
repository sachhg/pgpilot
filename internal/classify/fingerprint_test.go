package classify_test

import (
	"testing"

	"github.com/sachhg/pgpilot/internal/classify"
)

func TestFingerprint_StableAcrossLiterals(t *testing.T) {
	// Queries differing only in constants share a fingerprint.
	a := classify.Fingerprint("SELECT * FROM users WHERE id = 1")
	b := classify.Fingerprint("SELECT * FROM users WHERE id = 42")
	if a == "" {
		t.Fatal("fingerprint was empty for a valid query")
	}
	if a != b {
		t.Errorf("fingerprints differ across literals: %q vs %q", a, b)
	}
}

func TestFingerprint_DistinguishesShapes(t *testing.T) {
	a := classify.Fingerprint("SELECT * FROM users WHERE id = 1")
	b := classify.Fingerprint("SELECT * FROM orders WHERE id = 1")
	if a == b {
		t.Errorf("different table shapes shared a fingerprint: %q", a)
	}
}

func TestFingerprint_UnparseableIsEmpty(t *testing.T) {
	if fp := classify.Fingerprint("SELECT FROM WHERE ("); fp != "" {
		t.Errorf("unparseable query returned fingerprint %q, want empty", fp)
	}
}

func TestFingerprint_Deterministic(t *testing.T) {
	sql := "SELECT a, b FROM t JOIN u ON t.id = u.id WHERE t.x > 3 ORDER BY a"
	first := classify.Fingerprint(sql)
	for i := 0; i < 5; i++ {
		if got := classify.Fingerprint(sql); got != first {
			t.Fatalf("fingerprint not deterministic: %q vs %q", got, first)
		}
	}
}
