//go:build linux

package geoclue

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/mostafakhairy0305-dot/golocation/geo"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
	"github.com/mostafakhairy0305-dot/golocation/internal/shared/retry"
)

// errTransport stands for a failure that never reached GeoClue at all, which is
// the case mapGeoClueError has to fall through on.
var errTransport = errors.New("dial unix: connection refused")

// GeoClue takes an accuracy *level*, not metres, so the mapping is a lossy
// bucketing the caller cannot see. Getting a boundary wrong asks the service
// for city-level precision when the caller wanted a street address.
func TestGeoClueAccuracyBucketsTheRequest(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		opts Options
		want uint32
	}{
		"exact at the boundary":    {opts: Options{DesiredAccuracyMeters: 100}, want: 8},
		"street just past exact":   {opts: Options{DesiredAccuracyMeters: 101}, want: 6},
		"street at the boundary":   {opts: Options{DesiredAccuracyMeters: 1000}, want: 6},
		"neighborhood":             {opts: Options{DesiredAccuracyMeters: 10000}, want: 5},
		"city beyond every bucket": {opts: Options{DesiredAccuracyMeters: 10001}, want: 4},
		"metres win over a preference": {
			opts: Options{DesiredAccuracyMeters: 50, Accuracy: provider.AccuracyBalanced},
			want: 8,
		},
		"high": {opts: Options{Accuracy: provider.AccuracyHigh}, want: 8},
		"navigation": {
			opts: Options{Accuracy: provider.AccuracyNavigation},
			want: 8,
		},
		"balanced": {
			opts: Options{Accuracy: provider.AccuracyBalanced},
			want: 6,
		},
		"an unknown preference": {
			opts: Options{Accuracy: provider.AccuracyNavigation + 1},
			want: 6,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := geoClueAccuracy(testCase.opts); got != testCase.want {
				t.Fatalf("geoClueAccuracy = %d, want %d", got, testCase.want)
			}
		})
	}
}

// D-Bus is dynamically typed, so a property can arrive as the wrong type or as
// a NaN. Both have to read as "absent" rather than reach geo.Validate as a
// coordinate, which is where a NaN would otherwise be caught far from its
// cause.
func TestVariantFloat64RejectsAnythingUnusable(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		variant dbus.Variant
		want    float64
		wantOK  bool
	}{
		"a float":           {variant: dbus.MakeVariant(float64(51.5)), want: 51.5, wantOK: true},
		"a negative float":  {variant: dbus.MakeVariant(float64(-0.12)), want: -0.12, wantOK: true},
		"zero":              {variant: dbus.MakeVariant(float64(0)), want: 0, wantOK: true},
		"an empty variant":  {variant: dbus.Variant{}, want: 0, wantOK: false},
		"a string":          {variant: dbus.MakeVariant("51.5"), want: 0, wantOK: false},
		"an integer":        {variant: dbus.MakeVariant(int32(51)), want: 0, wantOK: false},
		"NaN":               {variant: dbus.MakeVariant(math.NaN()), want: 0, wantOK: false},
		"positive infinity": {variant: dbus.MakeVariant(math.Inf(1)), want: 0, wantOK: false},
		"negative infinity": {variant: dbus.MakeVariant(math.Inf(-1)), want: 0, wantOK: false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := variantFloat64(testCase.variant)
			if ok != testCase.wantOK {
				t.Fatalf("ok = %v, want %v", ok, testCase.wantOK)
			}

			if ok && got != testCase.want {
				t.Fatalf("value = %v, want %v", got, testCase.want)
			}
		})
	}
}

// The GeoClue timestamp is a (seconds, microseconds) pair, and which concrete
// Go shape it arrives in depends on the dbus version and how the variant was
// built. Every shape has to decode; anything else silently zeroes the fix
// timestamp and makes every sample look infinitely stale.
func TestGeoClueTimestampDecodesEveryShapeItArrivesIn(t *testing.T) {
	t.Parallel()

	want := time.Unix(1784030400, 250000*int64(time.Microsecond)).UTC()

	cases := map[string]struct {
		variant dbus.Variant
		want    time.Time
	}{
		"a struct": {
			variant: dbus.MakeVariant(struct{ Seconds, Microseconds uint64 }{1784030400, 250000}),
			want:    want,
		},
		"a uint64 slice": {
			variant: dbus.MakeVariant([]uint64{1784030400, 250000}),
			want:    want,
		},
		"an empty variant": {variant: dbus.Variant{}, want: time.Time{}},
		"the wrong type":   {variant: dbus.MakeVariant("1784030400"), want: time.Time{}},
		"a truncated pair": {variant: dbus.MakeVariant([]uint64{1784030400}), want: time.Time{}},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := geoClueTimestamp(testCase.variant); !got.Equal(testCase.want) {
				t.Fatalf("geoClueTimestamp = %v, want %v", got, testCase.want)
			}
		})
	}
}

// A negative int64 widened to uint64 would become a timestamp roughly 580
// billion years out, so the sign check is what keeps a malformed reply from
// looking merely far-future rather than invalid.
func TestAsUint64AcceptsOnlyNonNegativeIntegers(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		value  any
		want   uint64
		wantOK bool
	}{
		"uint64":           {value: uint64(42), want: 42, wantOK: true},
		"uint32":           {value: uint32(42), want: 42, wantOK: true},
		"a positive int64": {value: int64(42), want: 42, wantOK: true},
		"zero":             {value: int64(0), want: 0, wantOK: true},
		"a negative int64": {value: int64(-1), want: 0, wantOK: false},
		"a float":          {value: float64(42), want: 0, wantOK: false},
		"a string":         {value: "42", want: 0, wantOK: false},
		"nothing at all":   {value: nil, want: 0, wantOK: false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := asUint64(testCase.value)
			if ok != testCase.wantOK {
				t.Fatalf("ok = %v, want %v", ok, testCase.wantOK)
			}

			if ok && got != testCase.want {
				t.Fatalf("value = %d, want %d", got, testCase.want)
			}
		})
	}
}

// Translating the D-Bus error name into a sentinel is what decides whether the
// backend reconnects. Marking a permission denial temporary would spin the
// reconnect loop forever against a decision only the user can change.
func TestMapGeoClueErrorPicksTheSentinelAndTheRetryHint(t *testing.T) {
	t.Parallel()

	for name, testCase := range mapGeoClueErrorCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := mapGeoClueError("connect", testCase.err)

			if testCase.want != nil && !errors.Is(got, testCase.want) {
				t.Errorf("error = %v, want %v", got, testCase.want)
			}

			// The cause always survives, whichever sentinel was chosen.
			if !errors.Is(got, testCase.err) {
				t.Errorf("error = %v, want it to still wrap %v", got, testCase.err)
			}

			expectAnnotation(t, got, "connect", testCase.wantTemporary)
		})
	}
}

// mapGeoClueErrorCase is one D-Bus failure and the sentinel plus retry hint the
// caller should end up seeing for it.
type mapGeoClueErrorCase struct {
	err           error
	want          error
	wantTemporary bool
}

func mapGeoClueErrorCases() map[string]mapGeoClueErrorCase {
	return map[string]mapGeoClueErrorCase{
		"access denied": {
			err:           &dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied", Body: nil},
			want:          geo.ErrPermissionDenied,
			wantTemporary: false,
		},
		"not authorized": {
			err: &dbus.Error{
				Name: "org.freedesktop.GeoClue2.Error.NotAuthorized",
				Body: nil,
			},
			want:          geo.ErrPermissionDenied,
			wantTemporary: false,
		},
		"service unknown": {
			err: &dbus.Error{
				Name: "org.freedesktop.DBus.Error.ServiceUnknown",
				Body: nil,
			},
			want:          geo.ErrServiceUnavailable,
			wantTemporary: true,
		},
		"name has no owner": {
			err: &dbus.Error{
				Name: "org.freedesktop.DBus.Error.NameHasNoOwner",
				Body: nil,
			},
			want:          geo.ErrServiceUnavailable,
			wantTemporary: true,
		},
		"an unrecognised D-Bus error": {
			err:           &dbus.Error{Name: "org.freedesktop.DBus.Error.Failed", Body: nil},
			want:          nil,
			wantTemporary: true,
		},
		"not a D-Bus error at all": {
			err:           errTransport,
			want:          nil,
			wantTemporary: true,
		},
	}
}

// expectAnnotation fails for every piece of context mapGeoClueError should have
// attached, which is what a caller reads to tell one failure from another.
func expectAnnotation(t *testing.T, got error, wantOp string, wantTemporary bool) {
	t.Helper()

	var annotated *geo.Error

	if !errors.As(got, &annotated) {
		t.Fatalf("error = %v, want a *geo.Error", got)
	}

	if annotated.Platform != platform {
		t.Errorf("platform = %q, want %q", annotated.Platform, platform)
	}

	if annotated.Op != wantOp {
		t.Errorf("op = %q, want %q", annotated.Op, wantOp)
	}

	if annotated.Temporary != wantTemporary {
		t.Errorf("Temporary = %v, want %v", annotated.Temporary, wantTemporary)
	}
}

// TestConnectErrorsAreClassifiedForRetry pins the retry contract the reconnect
// loop now leans on: the permission denial the connect path produces is
// permanent (never retried) while an ordinary service failure is temporary
// (retried under backoff).
func TestConnectErrorsAreClassifiedForRetry(t *testing.T) {
	t.Parallel()

	denied := geo.Wrap(platform, "connect", geo.ErrPermissionDenied, false)
	if retry.Retryable(denied) {
		t.Errorf("Retryable(permission denied) = true, want false")
	}

	unavailable := geo.Wrap(platform, "connect", geo.ErrServiceUnavailable, true)
	if !retry.Retryable(unavailable) {
		t.Errorf("Retryable(service unavailable) = false, want true")
	}
}
