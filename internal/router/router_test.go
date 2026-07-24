package router

import "testing"

func TestNew(t *testing.T) {
	cases := []struct {
		name     string
		wantName string
	}{
		{PolicyRoundRobin, "round-robin"},
		{PolicyLeastInFlight, "least-in-flight"},
		{PolicyScored, "scored"},
	}
	for _, tc := range cases {
		p, err := New(tc.name)
		if err != nil {
			t.Fatalf("New(%q): %v", tc.name, err)
		}
		if p.Name() != tc.wantName {
			t.Errorf("New(%q).Name() = %q, want %q", tc.name, p.Name(), tc.wantName)
		}
	}
}

func TestNew_Unknown(t *testing.T) {
	if _, err := New("bogus"); err == nil {
		t.Error("New(bogus) succeeded, want an error")
	}
}
