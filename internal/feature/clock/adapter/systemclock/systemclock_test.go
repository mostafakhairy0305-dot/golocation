package systemclock

import (
	"testing"
	"time"
)

// The UTC normalisation is the only thing this adapter does beyond calling
// time.Now, and it is what keeps timestamps comparable with the ones providers
// hand us — so it is worth pinning even though the body is one line.
func TestNowIsTheWallClockInUTC(t *testing.T) {
	before := time.Now()
	got := Clock{}.Now()
	after := time.Now()

	if got.Location() != time.UTC {
		t.Fatalf("Location = %v, want UTC", got.Location())
	}

	if got.Before(before) || got.After(after) {
		t.Fatalf("Now = %v, want between %v and %v", got, before, after)
	}
}

// The zero value is documented as ready to use and shareable, which is what
// lets New install one without allocating.
func TestTheZeroValueIsUsable(t *testing.T) {
	var clock Clock
	if clock.Now().IsZero() {
		t.Fatal("Now on the zero value returned the zero time")
	}
}
