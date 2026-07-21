package geo_test

import (
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// Fields is a bitset, so the case that matters is a fix carrying one optional
// field being asked about a different one — a plain non-zero test would say
// yes to every field the moment any single one was present.
func TestHasReportsOnlyTheFieldsActuallySet(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		fields geo.Field
		field  geo.Field
		want   bool
	}{
		"none set":            {fields: 0, field: geo.FieldAltitude, want: false},
		"the one set":         {fields: geo.FieldAltitude, field: geo.FieldAltitude, want: true},
		"a different one set": {fields: geo.FieldAltitude, field: geo.FieldSpeed, want: false},
		"one of several": {
			fields: geo.FieldAltitude | geo.FieldSpeed,
			field:  geo.FieldSpeed,
			want:   true,
		},
		"absent among several": {
			fields: geo.FieldAltitude | geo.FieldSpeed,
			field:  geo.FieldHeading,
			want:   false,
		},
		"vertical accuracy set": {
			fields: geo.FieldVerticalAccuracy,
			field:  geo.FieldVerticalAccuracy,
			want:   true,
		},
		"all set": {
			fields: geo.FieldAltitude | geo.FieldVerticalAccuracy | geo.FieldSpeed | geo.FieldHeading,
			field:  geo.FieldHeading,
			want:   true,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fix := geo.Fix{Fields: testCase.fields}
			if got := fix.Has(testCase.field); got != testCase.want {
				t.Fatalf("Has(%d) = %v, want %v", testCase.field, got, testCase.want)
			}
		})
	}
}

// A provider clock running ahead of ours yields a negative age. Age must
// report it as such rather than clamping, because IsFresh is what decides how
// much future to tolerate.
func TestAgeIsSignedRelativeToNow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		stamp time.Time
		want  time.Duration
	}{
		"past":   {stamp: now.Add(-90 * time.Second), want: 90 * time.Second},
		"now":    {stamp: now, want: 0},
		"future": {stamp: now.Add(30 * time.Second), want: -30 * time.Second},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fix := geo.Fix{Timestamp: testCase.stamp}
			if got := fix.Age(now); got != testCase.want {
				t.Fatalf("Age = %v, want %v", got, testCase.want)
			}
		})
	}
}
