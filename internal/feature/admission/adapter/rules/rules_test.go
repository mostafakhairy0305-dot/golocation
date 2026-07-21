package rules_test

import (
	"errors"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/admission/adapter/rules"
	admission "github.com/mostafakhairy0305-dot/golocation/internal/feature/admission/port"
)

func fixAt(at time.Time, lat, lon float64) geo.Fix {
	return geo.Fix{Timestamp: at, ReceivedAt: at, Latitude: lat, Longitude: lon, AccuracyMeters: 5}
}

func TestFirstValidFixIsAdmitted(t *testing.T) {
	t.Parallel()

	gate := rules.New(admission.Rules{MinimumInterval: time.Minute, MinimumDistanceMeters: 1000})
	now := time.Now()

	// The thresholds compare against a previous fix, and there is none yet.
	admitted, err := gate.Admit(fixAt(now, 1, 1), now)
	if err != nil || !admitted {
		t.Fatalf("Admit = %v, %v; want true, nil", admitted, err)
	}
}

func TestRedundantFixesAreSkippedWithoutAnError(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		rules  admission.Rules
		second func(base time.Time) geo.Fix
	}{
		"too soon": {
			rules:  admission.Rules{MinimumInterval: time.Minute},
			second: func(base time.Time) geo.Fix { return fixAt(base.Add(time.Second), 10, 10) },
		},
		"too close": {
			rules:  admission.Rules{MinimumDistanceMeters: 1000},
			second: func(base time.Time) geo.Fix { return fixAt(base.Add(time.Hour), 10.0001, 10) },
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gate := rules.New(testCase.rules)

			now := time.Now()

			_, err := gate.Admit(fixAt(now, 10, 10), now)
			if err != nil {
				t.Fatalf("first Admit: %v", err)
			}

			second := testCase.second(now)

			admitted, err := gate.Admit(second, second.ReceivedAt)
			if err != nil {
				t.Fatalf("Admit error = %v, want nil: a redundant fix is not a failure", err)
			}

			if admitted {
				t.Fatal("a redundant fix was admitted")
			}
		})
	}
}

func TestThresholdsAdmitOnceTheyAreCleared(t *testing.T) {
	t.Parallel()

	gate := rules.New(admission.Rules{MinimumInterval: time.Minute, MinimumDistanceMeters: 100})

	now := time.Now()

	_, err := gate.Admit(fixAt(now, 10, 10), now)
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}

	later := now.Add(2 * time.Minute)

	admitted, err := gate.Admit(fixAt(later, 10.01, 10), later)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}

	if !admitted {
		t.Fatal("a fix clearing both thresholds was skipped")
	}
}

func TestUnusableFixesAreRejectedWithTheCause(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cases := map[string]struct {
		rules admission.Rules
		fix   geo.Fix
		want  error
	}{
		"stale": {
			admission.Rules{MaximumAge: time.Minute},
			fixAt(now.Add(-time.Hour), 1, 1),
			geo.ErrStaleFix,
		},
		"latitude range":  {admission.Rules{}, fixAt(now, 91, 1), nil},
		"longitude range": {admission.Rules{}, fixAt(now, 1, 181), nil},
		"negative accuracy": {
			admission.Rules{},
			geo.Fix{Timestamp: now, ReceivedAt: now, AccuracyMeters: -1},
			nil,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gate := rules.New(testCase.rules)

			admitted, err := gate.Admit(testCase.fix, now)
			expectRejected(t, admitted, err, testCase.want)
		})
	}
}

// expectRejected fails unless the gate declined the fix and named a cause. The
// want is optional: some rejections have no sentinel worth pinning, but every
// one of them still has to report something.
func expectRejected(t *testing.T, admitted bool, err, want error) {
	t.Helper()

	if admitted {
		t.Fatal("an unusable fix was admitted")
	}

	if err == nil {
		t.Fatal("an unusable fix was skipped silently instead of reported")
	}

	if want != nil && !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

// The gate reports the cause and nothing else; annotating it with the platform
// belongs to the caller, which is the only layer that knows which adapter ran.
func TestRejectionCarriesNoPlatformAnnotation(t *testing.T) {
	t.Parallel()

	gate := rules.New(admission.Rules{MaximumAge: time.Minute})
	now := time.Now()

	_, err := gate.Admit(fixAt(now.Add(-time.Hour), 1, 1), now)

	var annotated *geo.Error
	if errors.As(err, &annotated) {
		t.Fatalf("gate returned a pre-annotated error: %v", err)
	}
}

func TestAFutureFixWithinClockSkewStaysFresh(t *testing.T) {
	t.Parallel()

	gate := rules.New(admission.Rules{MaximumAge: time.Minute})
	now := time.Now()

	admitted, err := gate.Admit(fixAt(now.Add(geo.MaxClockSkew/2), 1, 1), now)
	if err != nil || !admitted {
		t.Fatalf(
			"Admit = %v, %v; a provider clock slightly ahead of ours is not stale",
			admitted,
			err,
		)
	}
}

func BenchmarkAdmitWithDistanceFilter(b *testing.B) {
	gate := rules.New(admission.Rules{MinimumDistanceMeters: 1})

	now := time.Now()

	_, err := gate.Admit(fixAt(now, 51.5074, -0.1278), now)
	if err != nil {
		b.Fatalf("seed Admit: %v", err)
	}

	fix := fixAt(now, 51.5075, -0.1279)

	b.ReportAllocs()

	for b.Loop() {
		_, err = gate.Admit(fix, now)
		if err != nil {
			b.Fatalf("Admit: %v", err)
		}
	}
}
