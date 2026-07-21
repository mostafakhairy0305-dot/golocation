//go:build darwin && (amd64 || arm64)

package corelocation

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"
	"github.com/mostafakhairy0305-dot/golocation/geo"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// fakeSink records what the run loop publishes. Every test here drives the
// callbacks and the drain synchronously on its own goroutine, so it needs no
// locking of its own — a real Sink does, which is why provider.Sink says so.
type fakeSink struct {
	fixes    []geo.Fix    `exhaustruct:"optional"`
	statuses []geo.Status `exhaustruct:"optional"`
	errs     []error      `exhaustruct:"optional"`
}

func (s *fakeSink) PublishFix(fix geo.Fix)          { s.fixes = append(s.fixes, fix) }
func (s *fakeSink) PublishStatus(status geo.Status) { s.statuses = append(s.statuses, status) }
func (s *fakeSink) PublishError(err error)          { s.errs = append(s.errs, err) }

var _ provider.Sink = (*fakeSink)(nil)

// testSelectorSet is the selectors only the tests send — the ones used to
// build the CLLocation, NSError, and NSArray fixtures a real CoreLocation
// callback would have been handed.
type testSelectorSet struct {
	alloc                     objc.SEL
	stringWithUTF8String      objc.SEL
	errorWithDomain           objc.SEL
	arrayWithObject           objc.SEL
	array                     objc.SEL
	dateWithTimeInterval1970  objc.SEL
	initWithLatitudeLongitude objc.SEL
	initWithCoordinateFull    objc.SEL
}

// testSelectors mirrors selectors: a function rather than a package-level
// variable, resolving against the ObjC runtime's own table on every call.
func testSelectors() *testSelectorSet {
	return &testSelectorSet{
		alloc:                     objc.RegisterName("alloc"),
		stringWithUTF8String:      objc.RegisterName("stringWithUTF8String:"),
		errorWithDomain:           objc.RegisterName("errorWithDomain:code:userInfo:"),
		arrayWithObject:           objc.RegisterName("arrayWithObject:"),
		array:                     objc.RegisterName("array"),
		dateWithTimeInterval1970:  objc.RegisterName("dateWithTimeIntervalSince1970:"),
		initWithLatitudeLongitude: objc.RegisterName("initWithLatitude:longitude:"),
		initWithCoordinateFull: objc.RegisterName(
			"initWithCoordinate:altitude:horizontalAccuracy:verticalAccuracy:course:speed:timestamp:",
		),
	}
}

// newTestBackend builds the Backend a delegate callback expects to find behind
// it: New is what resolves the selector set, the delegate class, and the ivars,
// and without them a drain would have nothing to read.
func newTestBackend(t *testing.T, sink provider.Sink) *Backend {
	t.Helper()

	native, err := New(Options{Permission: provider.PermissionDoNotRequest}, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() { _ = native.Stop() })

	return native
}

// withAutoreleasePool gives the test somewhere for the framework's autoreleased
// temporaries to go, which is what the run loop's per-turn pool does in
// production.
func withAutoreleasePool(t *testing.T, native *Backend) {
	t.Helper()

	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(native.sel.newObject)
	if pool != 0 {
		t.Cleanup(func() { pool.Send(native.sel.drain) })
	}
}

// newDelegate hands back a live delegate: an instance of the class New
// registered, carrying the queues its callbacks append to. Opening the queues
// is the precondition every callback checks before it records anything.
func newDelegate(t *testing.T, native *Backend) objc.ID {
	t.Helper()

	delegate := objc.ID(native.class).Send(native.sel.newObject)
	if delegate == 0 {
		t.Fatal("could not allocate a delegate")
	}

	if !native.openQueues(delegate) {
		t.Fatal("could not give the delegate its queues")
	}

	t.Cleanup(func() {
		native.closeQueues(delegate)
		delegate.Send(native.sel.release)
	})

	return delegate
}

func nsString(t *testing.T, value string) objc.ID {
	t.Helper()

	object := objc.ID(objc.GetClass("NSString")).Send(testSelectors().stringWithUTF8String, value)
	if object == 0 {
		t.Fatalf("could not build an NSString from %q", value)
	}

	return object
}

// The kCL sentinels are negative magic numbers whose meaning is the opposite of
// what their sign suggests: -2 is the *most* accurate setting, not a disabled
// one. Transposing them would silently downgrade every fix.
func TestCoreLocationAccuracyMapsEveryPreference(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		opts Options
		want float64
	}{
		"explicit metres win over the preference": {
			opts: Options{DesiredAccuracyMeters: 25, Accuracy: provider.AccuracyNavigation},
			want: 25,
		},
		"navigation": {opts: Options{Accuracy: provider.AccuracyNavigation}, want: -2},
		"high":       {opts: Options{Accuracy: provider.AccuracyHigh}, want: -1},
		"balanced":   {opts: Options{Accuracy: provider.AccuracyBalanced}, want: 100},
		"unknown preference falls back to balanced": {
			opts: Options{Accuracy: provider.AccuracyNavigation + 1},
			want: 100,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := coreLocationAccuracy(testCase.opts); got != testCase.want {
				t.Fatalf("coreLocationAccuracy = %v, want %v", got, testCase.want)
			}
		})
	}
}

// New only loads system frameworks and registers the delegate class, so it
// must succeed — and ask for nothing — on any Mac. Stop before Start is the
// path a caller takes when Open succeeds but the caller gives up, and it must
// not wait on a run loop that was never entered.
func TestNewPreparesAProviderWithoutStartingOrPrompting(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	native := newTestBackend(t, sink)

	if got := native.Platform(); got != platform {
		t.Errorf("Platform = %q, want %q", got, platform)
	}

	want := geo.Capabilities{Altitude: true, VerticalAccuracy: true, Speed: true, Heading: true}
	if got := native.Capabilities(); got != want {
		t.Errorf("Capabilities = %+v, want %+v", got, want)
	}

	expectStopsTwice(t, native)

	// Preparing a provider must not publish anything: nothing has started, so
	// a status here would tell a subscriber the service exists when it does not.
	expectPublishedNothing(t, "New", sink)
}

// expectStopsTwice fails unless Stop succeeds before Start and stays idempotent
// — the path a caller takes when Open succeeds but it then gives up.
func expectStopsTwice(t *testing.T, native provider.Provider) {
	t.Helper()

	err := native.Stop()
	if err != nil {
		t.Fatalf("Stop before Start: %v", err)
	}

	err = native.Stop()
	if err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// expectPublishedNothing fails if the sink was reached at all.
func expectPublishedNothing(t *testing.T, who string, sink *fakeSink) {
	t.Helper()

	if len(sink.fixes)+len(sink.statuses)+len(sink.errs) != 0 {
		t.Fatalf("%s published %d fixes, %d statuses, %d errors; want none",
			who, len(sink.fixes), len(sink.statuses), len(sink.errs))
	}
}

// This is the package's whole permission policy in one switch, and it is the
// difference between a locator that reports "denied" and one that looks like
// it is merely slow. Every CLAuthorizationStatus the OS can hand back is
// pinned here, including one it does not define yet.
func TestPublishAuthorizationMapsEveryStatus(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		status         int64
		wantState      geo.State
		wantPermission geo.PermissionState
		wantErr        bool `exhaustruct:"optional"`
	}{
		"not determined": {
			status:         0,
			wantState:      geo.StateStarting,
			wantPermission: geo.PermissionPromptRequired,
		},
		"restricted": {
			status:         1,
			wantState:      geo.StateDisabled,
			wantPermission: geo.PermissionRestricted,
			wantErr:        true,
		},
		"denied": {
			status:         2,
			wantState:      geo.StateDisabled,
			wantPermission: geo.PermissionDenied,
			wantErr:        true,
		},
		"authorized always": {
			status:         3,
			wantState:      geo.StateStarting,
			wantPermission: geo.PermissionGranted,
		},
		"authorized when in use": {
			status:         4,
			wantState:      geo.StateStarting,
			wantPermission: geo.PermissionGranted,
		},
		"a status we do not know": {
			status:         99,
			wantState:      geo.StateUnavailable,
			wantPermission: geo.PermissionUnknown,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &fakeSink{}
			native := newTestBackend(t, sink)

			native.publishAuthorization(testCase.status)

			if len(sink.statuses) != 1 {
				t.Fatalf("published %d statuses, want 1", len(sink.statuses))
			}

			expectAuthorization(t, sink.statuses[0], testCase.wantState, testCase.wantPermission)
			expectPermissionError(t, sink.errs, testCase.wantErr)
		})
	}
}

// expectAuthorization fails for every part of the status the mapping should
// have set, including the message: a status a human cannot read is half-mapped.
func expectAuthorization(
	t *testing.T,
	got geo.Status,
	wantState geo.State,
	wantPermission geo.PermissionState,
) {
	t.Helper()

	if got.State != wantState {
		t.Errorf("state = %v, want %v", got.State, wantState)
	}

	if got.Permission != wantPermission {
		t.Errorf("permission = %v, want %v", got.Permission, wantPermission)
	}

	if got.Message == "" {
		t.Error("status carries no message")
	}
}

// expectPermissionError fails unless a refusal — and only a refusal — published
// the sentinel a caller switches on.
func expectPermissionError(t *testing.T, errs []error, want bool) {
	t.Helper()

	if !want {
		if len(errs) != 0 {
			t.Fatalf("published %d errors, want 0", len(errs))
		}

		return
	}

	if len(errs) != 1 {
		t.Fatalf("published %d errors, want 1", len(errs))
	}

	if !errors.Is(errs[0], geo.ErrPermissionDenied) {
		t.Errorf("error = %v, want ErrPermissionDenied", errs[0])
	}
}

// A delegate outlives the run that created it only if teardown went wrong, but
// CoreLocation can still deliver one last callback on a delegate whose session
// is already gone. Taking the queues away in closeSession is what makes that
// harmless: with nowhere to append, the callback records nothing.
func TestCallbacksIgnoreADelegateWithNoQueues(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	native := newTestBackend(t, sink)

	withAutoreleasePool(t, native)

	// Opened, then taken away — exactly the state a torn-down run leaves.
	delegate := newDelegate(t, native)
	native.closeQueues(delegate)

	coreLocationDidUpdateLocations(delegate, 0, 0, 0)
	coreLocationDidFail(delegate, 0, 0, 0)
	coreLocationDidChangeAuthorization(delegate, 0, 0)

	native.publishQueued(delegate)

	expectPublishedNothing(t, "a delegate with no queues", sink)
}

// The joined sentinel is what a caller switches on, and the native description
// is what a human reads; losing either leaves one of the two audiences with
// nothing. kCLErrorDenied additionally has to move the reported permission,
// because a run that keeps saying "starting" after a denial never terminates
// from the caller's point of view.
func TestDidFailMapsTheCoreLocationErrorCode(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		code       int64
		wantErr    error
		wantStatus bool `exhaustruct:"optional"`
	}{
		"location unknown": {code: 0, wantErr: geo.ErrPositionUnavailable},
		"denied":           {code: 1, wantErr: geo.ErrPermissionDenied, wantStatus: true},
		"anything else":    {code: 7, wantErr: nil},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &fakeSink{}
			native := newTestBackend(t, sink)

			withAutoreleasePool(t, native)

			delegate := newDelegate(t, native)

			nativeError := objc.ID(objc.GetClass("NSError")).Send(
				testSelectors().errorWithDomain,
				nsString(t, "kCLErrorDomain"),
				testCase.code,
				objc.ID(0),
			)
			if nativeError == 0 {
				t.Fatal("could not build an NSError")
			}

			coreLocationDidFail(delegate, 0, 0, nativeError)
			native.publishQueued(delegate)

			if len(sink.errs) != 1 {
				t.Fatalf("published %d errors, want 1", len(sink.errs))
			}

			err := sink.errs[0]
			expectBothHalves(t, err, testCase.wantErr)
			// A denial is permanent; anything else is worth retrying.
			expectAnnotation(t, err, testCase.code != 1)
			expectDenialStatus(t, sink.statuses, testCase.wantStatus)
		})
	}
}

// expectBothHalves fails unless the failure came through carrying both of its
// audiences: the sentinel a caller switches on, and the native description a
// human reads. A wantErr of nil is a code with no sentinel of its own.
func expectBothHalves(t *testing.T, err, wantErr error) {
	t.Helper()

	if wantErr != nil && !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}

	if !strings.Contains(err.Error(), "kCLErrorDomain") {
		t.Errorf("error = %q, want it to carry the native description", err)
	}
}

// expectAnnotation fails unless the error carries this adapter's attribution
// and the retry hint the code deserves.
func expectAnnotation(t *testing.T, err error, wantTemporary bool) {
	t.Helper()

	var annotated *geo.Error
	if !errors.As(err, &annotated) {
		t.Fatalf("error = %v, want a *geo.Error", err)
	}

	if annotated.Platform != platform {
		t.Errorf("platform = %q, want %q", annotated.Platform, platform)
	}

	if annotated.Temporary != wantTemporary {
		t.Errorf("Temporary = %v, want %v", annotated.Temporary, wantTemporary)
	}
}

// expectDenialStatus fails unless a denial — and only a denial — also moved the
// reported permission.
func expectDenialStatus(t *testing.T, statuses []geo.Status, want bool) {
	t.Helper()

	if !want {
		if len(statuses) != 0 {
			t.Fatalf("published %d statuses, want 0", len(statuses))
		}

		return
	}

	if len(statuses) != 1 {
		t.Fatalf("published %d statuses, want 1", len(statuses))
	}

	if statuses[0].Permission != geo.PermissionDenied {
		t.Errorf("permission = %v, want PermissionDenied", statuses[0].Permission)
	}
}

// CoreLocation reports an absent optional field as a negative accuracy, speed,
// or course rather than by omitting it, so a fix that copied the values
// unconditionally would report an altitude of 0 m and a heading of due north
// for every sample taken indoors. Fields is what tells the caller apart.
func TestDidUpdateLocationsMapsTheOptionalFields(t *testing.T) {
	t.Parallel()

	stamp := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		location func(t *testing.T) objc.ID
		want     geo.Fix
	}{
		"every optional field present": {
			location: fullLocation(stamp),
			want: geo.Fix{
				Timestamp:              stamp,
				Latitude:               51.5,
				Longitude:              -0.12,
				AccuracyMeters:         10,
				AltitudeMeters:         35,
				VerticalAccuracyMeters: 4,
				SpeedMetersPerSecond:   3,
				HeadingDegrees:         180,
				Source:                 geo.SourceSystem,
				Fields: geo.FieldAltitude | geo.FieldVerticalAccuracy |
					geo.FieldSpeed | geo.FieldHeading,
			},
		},
		// initWithLatitude:longitude: leaves vertical accuracy, speed, and
		// course at -1, which is how CoreLocation says "no data".
		"every optional field absent": {
			location: bareLocation,
			want: geo.Fix{
				Latitude:  48.85,
				Longitude: 2.35,
				Source:    geo.SourceSystem,
				Fields:    0,
			},
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			expectFix(t, publishOneLocation(t, testCase.location), testCase.want)
		})
	}
}

// publishOneLocation drives one CLLocation through the delegate callback and
// the drain the run loop performs, and returns the single fix that came out.
func publishOneLocation(t *testing.T, build func(t *testing.T) objc.ID) geo.Fix {
	t.Helper()

	sink := &fakeSink{}
	native := newTestBackend(t, sink)

	withAutoreleasePool(t, native)

	delegate := newDelegate(t, native)

	location := build(t)
	if location == 0 {
		t.Fatal("could not build a CLLocation")
	}

	defer location.Send(native.sel.release)

	locations := objc.ID(objc.GetClass("NSArray")).
		Send(testSelectors().arrayWithObject, location)

	coreLocationDidUpdateLocations(delegate, 0, 0, locations)
	native.publishQueued(delegate)

	if len(sink.fixes) != 1 {
		t.Fatalf("published %d fixes, want 1", len(sink.fixes))
	}

	return sink.fixes[0]
}

// fullLocation builds a CLLocation carrying every optional field.
func fullLocation(stamp time.Time) func(t *testing.T) objc.ID {
	return func(t *testing.T) objc.ID {
		t.Helper()

		sel := testSelectors()

		date := objc.ID(objc.GetClass("NSDate")).
			Send(sel.dateWithTimeInterval1970, float64(stamp.Unix()))
		if date == 0 {
			t.Fatal("could not build an NSDate")
		}

		return objc.ID(objc.GetClass("CLLocation")).Send(sel.alloc).Send(
			sel.initWithCoordinateFull,
			clCoordinate{Latitude: 51.5, Longitude: -0.12},
			float64(35),  // altitude
			float64(10),  // horizontal accuracy
			float64(4),   // vertical accuracy
			float64(180), // course
			float64(3),   // speed
			date,
		)
	}
}

// bareLocation builds a CLLocation carrying nothing but a coordinate.
func bareLocation(t *testing.T) objc.ID {
	t.Helper()

	sel := testSelectors()

	return objc.ID(objc.GetClass("CLLocation")).Send(sel.alloc).Send(
		sel.initWithLatitudeLongitude, float64(48.85), float64(2.35))
}

// expectFix fails for every part of the mapping that did not survive. The
// present-field assertions are split out so that each function states one rule:
// what every fix must carry, and what an absent field must not.
func expectFix(t *testing.T, got, want geo.Fix) {
	t.Helper()

	expectCoordinate(t, got, want)
	expectStamped(t, got)

	if want.Fields == 0 {
		expectAbsentFieldsLeftAtZero(t, got)

		return
	}

	expectOptionalFields(t, got, want)
}

// expectCoordinate fails for the three things every mapped fix carries,
// whichever optional fields the sample had.
func expectCoordinate(t *testing.T, got, want geo.Fix) {
	t.Helper()

	if got.Latitude != want.Latitude || got.Longitude != want.Longitude {
		t.Errorf(
			"coordinate = %v,%v want %v,%v",
			got.Latitude, got.Longitude, want.Latitude, want.Longitude,
		)
	}

	if got.Fields != want.Fields {
		t.Errorf("Fields = %b, want %b", got.Fields, want.Fields)
	}

	if got.Source != geo.SourceSystem {
		t.Errorf("Source = %v, want SourceSystem", got.Source)
	}
}

// expectStamped fails unless both times were filled in — a fix with neither is
// one the staleness rules cannot judge.
func expectStamped(t *testing.T, got geo.Fix) {
	t.Helper()

	if got.ReceivedAt.IsZero() {
		t.Error("ReceivedAt was not stamped")
	}

	if got.Timestamp.IsZero() {
		t.Error("Timestamp was not stamped")
	}
}

// expectOptionalFields fails for every optional value that did not come
// through, including the provider's own timestamp.
func expectOptionalFields(t *testing.T, got, want geo.Fix) {
	t.Helper()

	for name, pair := range map[string]struct{ got, want float64 }{
		"AccuracyMeters":         {got.AccuracyMeters, want.AccuracyMeters},
		"AltitudeMeters":         {got.AltitudeMeters, want.AltitudeMeters},
		"VerticalAccuracyMeters": {got.VerticalAccuracyMeters, want.VerticalAccuracyMeters},
		"SpeedMetersPerSecond":   {got.SpeedMetersPerSecond, want.SpeedMetersPerSecond},
		"HeadingDegrees":         {got.HeadingDegrees, want.HeadingDegrees},
	} {
		if pair.got != pair.want {
			t.Errorf("%s = %v, want %v", name, pair.got, pair.want)
		}
	}

	if !got.Timestamp.Equal(want.Timestamp) {
		t.Errorf("Timestamp = %v, want the CLLocation's %v", got.Timestamp, want.Timestamp)
	}
}

// expectAbsentFieldsLeftAtZero fails if an absent field carried the -1 sentinel
// CoreLocation reported rather than being left alone.
func expectAbsentFieldsLeftAtZero(t *testing.T, got geo.Fix) {
	t.Helper()

	if got.AltitudeMeters != 0 || got.SpeedMetersPerSecond != 0 || got.HeadingDegrees != 0 {
		t.Errorf("absent fields carried values: %+v", got)
	}
}

// An update carrying no locations is not an error — CoreLocation can deliver
// one — so it must be dropped rather than published as a fix at 0,0, which
// validates cleanly and would be cached as a real position off West Africa.
func TestDidUpdateLocationsIgnoresAnEmptyArray(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	native := newTestBackend(t, sink)

	withAutoreleasePool(t, native)

	delegate := newDelegate(t, native)

	coreLocationDidUpdateLocations(
		delegate, 0, 0, objc.ID(objc.GetClass("NSArray")).Send(testSelectors().array),
	)
	native.publishQueued(delegate)

	if len(sink.fixes) != 0 {
		t.Fatalf("published %d fixes for an empty update, want 0", len(sink.fixes))
	}
}
