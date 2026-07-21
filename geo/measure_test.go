package geo_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// Validate folds the NaN, infinity, and range checks into one negated range
// test. That is only correct because NaN compares false against everything, so
// the rejection cases are worth pinning down.
func TestValidateRejectsEveryUnusableCoordinate(t *testing.T) {
	t.Parallel()

	cases := map[string]geo.Fix{
		"NaN latitude":        {Latitude: math.NaN()},
		"NaN longitude":       {Longitude: math.NaN()},
		"NaN accuracy":        {AccuracyMeters: math.NaN()},
		"+Inf latitude":       {Latitude: math.Inf(1)},
		"-Inf latitude":       {Latitude: math.Inf(-1)},
		"+Inf longitude":      {Longitude: math.Inf(1)},
		"-Inf longitude":      {Longitude: math.Inf(-1)},
		"+Inf accuracy":       {AccuracyMeters: math.Inf(1)},
		"-Inf accuracy":       {AccuracyMeters: math.Inf(-1)},
		"latitude above 90":   {Latitude: 90.001},
		"latitude below -90":  {Latitude: -90.001},
		"longitude too big":   {Longitude: 180.001},
		"longitude too small": {Longitude: -180.001},
		"negative accuracy":   {AccuracyMeters: -1},
	}
	for name, fix := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := geo.Validate(fix)
			if !errors.Is(err, geo.ErrInvalidFix) {
				t.Fatalf("Validate(%+v) = %v, want ErrInvalidFix", fix, err)
			}
		})
	}
}

func TestValidateAcceptsCoordinatesOnTheBoundary(t *testing.T) {
	t.Parallel()

	cases := map[string]geo.Fix{
		"null island":              {},
		"north pole":               {Latitude: 90, Longitude: 0},
		"south pole":               {Latitude: -90, Longitude: 0},
		"antimeridian east":        {Latitude: 0, Longitude: 180},
		"antimeridian west":        {Latitude: 0, Longitude: -180},
		"london":                   {Latitude: 51.5074, Longitude: -0.1278, AccuracyMeters: 12.5},
		"huge but finite accuracy": {AccuracyMeters: math.MaxFloat64},
	}
	for name, fix := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := geo.Validate(fix)
			if err != nil {
				t.Fatalf("Validate(%+v) = %v, want nil", fix, err)
			}
		})
	}
}

func TestDistanceIsSymmetricAndZeroForAPoint(t *testing.T) {
	t.Parallel()

	london := geo.Fix{Latitude: 51.5074, Longitude: -0.1278}
	paris := geo.Fix{Latitude: 48.8566, Longitude: 2.3522}

	if got := geo.Distance(london, london); got != 0 {
		t.Fatalf("Distance to itself = %v, want 0", got)
	}

	there, back := geo.Distance(london, paris), geo.Distance(paris, london)
	if math.Abs(there-back) > 1e-6 {
		t.Fatalf("Distance is not symmetric: %v vs %v", there, back)
	}
	// London to Paris is about 344 km; a wrong earth radius or a dropped
	// half-angle would miss by far more than this tolerance.
	if there < 343_000 || there > 345_000 {
		t.Fatalf("Distance = %v m, want roughly 344 km", there)
	}
}

func TestDistanceAcrossTheAntimeridianIsShort(t *testing.T) {
	t.Parallel()

	west := geo.Fix{Latitude: 0, Longitude: 179.999}

	east := geo.Fix{Latitude: 0, Longitude: -179.999}
	if got := geo.Distance(west, east); got > 1000 {
		t.Fatalf("Distance = %v m, want a short hop across the antimeridian", got)
	}
}

func TestIsFresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		timestamp time.Time
		maxAge    time.Duration
		want      bool
	}{
		"within max age":           {now.Add(-30 * time.Second), time.Minute, true},
		"beyond max age":           {now.Add(-2 * time.Minute), time.Minute, false},
		"exactly at max age":       {now.Add(-time.Minute), time.Minute, true},
		"zero max age disables it": {now.Add(-100 * time.Hour), 0, true},
		"ahead within clock skew":  {now.Add(geo.MaxClockSkew / 2), time.Minute, true},
		"ahead beyond clock skew":  {now.Add(2 * geo.MaxClockSkew), time.Minute, false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fix := geo.Fix{Timestamp: testCase.timestamp}
			if got := geo.IsFresh(fix, testCase.maxAge, now); got != testCase.want {
				t.Fatalf("IsFresh = %v, want %v", got, testCase.want)
			}
		})
	}
}

func BenchmarkValidate(b *testing.B) {
	fix := geo.Fix{Latitude: 51.5074, Longitude: -0.1278, AccuracyMeters: 12.5}

	b.ReportAllocs()

	for b.Loop() {
		err := geo.Validate(fix)
		if err != nil {
			b.Fatalf("Validate: %v", err)
		}
	}
}

func BenchmarkDistance(b *testing.B) {
	london := geo.Fix{Latitude: 51.5074, Longitude: -0.1278}
	paris := geo.Fix{Latitude: 48.8566, Longitude: 2.3522}

	b.ReportAllocs()

	for b.Loop() {
		_ = geo.Distance(london, paris)
	}
}
