package util

import (
	"testing"
)

func TestTailBuffer(t *testing.T) {
	b := NewTailBuffer(5)
	for _, s := range []string{"ab", "cd", "", "efgh", "i"} {
		n, err := b.Write([]byte(s))
		if err != nil || n != len(s) {
			t.Fatalf("%d %v", n, err)
		}
	}
	if got := b.String(); got != "efghi" {
		t.Fatalf("%q", got)
	}

	// A write longer than the limit keeps only its end.
	b.Write([]byte("0123456789"))
	if got := b.String(); got != "56789" {
		t.Fatalf("%q", got)
	}
	if got := b.Total(); got != 19 {
		t.Fatalf("%d", got)
	}
}
