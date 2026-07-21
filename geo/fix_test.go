package geo

import (
	"testing"
	"time"
)

// Fields is a bitset, so the case that matters is a fix carrying one optional
// field being asked about a different one — a plain non-zero test would say
// yes to every field the moment any single one was present.
func TestHasReportsOnlyTheFieldsActuallySet(t *testing.T) {
	cases := map[string]struct {
		fields Field
		field  Field
		want   bool
	}{
		"none set":            {fields: 0, field: FieldAltitude, want: false},
		"the one set":         {fields: FieldAltitude, field: FieldAltitude, want: true},
		"a different one set": {fields: FieldAltitude, field: FieldSpeed, want: false},
		"one of several": {
			fields: FieldAltitude | FieldSpeed,
			field:  FieldSpeed,
			want:   true,
		},
		"absent among several": {
			fields: FieldAltitude | FieldSpeed,
			field:  FieldHeading,
			want:   false,
		},
		"vertical accuracy set": {
			fields: FieldVerticalAccuracy,
			field:  FieldVerticalAccuracy,
			want:   true,
		},
		"all set": {
			fields: FieldAltitude | FieldVerticalAccuracy | FieldSpeed | FieldHeading,
			field:  FieldHeading,
			want:   true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fix := Fix{Fields: tc.fields}
			if got := fix.Has(tc.field); got != tc.want {
				t.Fatalf("Has(%d) = %v, want %v", tc.field, got, tc.want)
			}
		})
	}
}

// A provider clock running ahead of ours yields a negative age. Age must
// report it as such rather than clamping, because IsFresh is what decides how
// much future to tolerate.
func TestAgeIsSignedRelativeToNow(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		stamp time.Time
		want  time.Duration
	}{
		"past":   {stamp: now.Add(-90 * time.Second), want: 90 * time.Second},
		"now":    {stamp: now, want: 0},
		"future": {stamp: now.Add(30 * time.Second), want: -30 * time.Second},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fix := Fix{Timestamp: tc.stamp}
			if got := fix.Age(now); got != tc.want {
				t.Fatalf("Age = %v, want %v", got, tc.want)
			}
		})
	}
}
