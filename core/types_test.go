package core

import "testing"

func TestGenHexToken(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		tk := GenHexToken()
		if len(tk) != 64 {
			t.Fatalf("want 64 chars, got %d", len(tk))
		}
		if seen[tk] {
			t.Fatal("duplicate token")
		}
		seen[tk] = true
	}
}
