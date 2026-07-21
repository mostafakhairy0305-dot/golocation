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

// fakeSink records what the delegate callbacks publish. Every test here drives
// the callbacks synchronously on its own goroutine, so it needs no locking of
// its own — a real Sink does, which is why provider.Sink says so.
type fakeSink struct {
	fixes    []geo.Fix
	statuses []geo.Status
	errs     []error
}

func (s *fakeSink) PublishFix(fix geo.Fix)          { s.fixes = append(s.fixes, fix) }
func (s *fakeSink) PublishStatus(status geo.Status) { s.statuses = append(s.statuses, status) }
func (s *fakeSink) PublishError(err error)          { s.errs = append(s.errs, err) }

var _ provider.Sink = (*fakeSink)(nil)

var (
	selAlloc                     = objc.RegisterName("alloc")
	selStringWithUTF8String      = objc.RegisterName("stringWithUTF8String:")
	selErrorWithDomain           = objc.RegisterName("errorWithDomain:code:userInfo:")
	selArrayWithObject           = objc.RegisterName("arrayWithObject:")
	selArray                     = objc.RegisterName("array")
	selDateWithTimeInterval1970  = objc.RegisterName("dateWithTimeIntervalSince1970:")
	selInitWithLatitudeLongitude = objc.RegisterName("initWithLatitude:longitude:")
	selInitWithCoordinateFull    = objc.RegisterName("initWithCoordinate:altitude:horizontalAccuracy:verticalAccuracy:course:speed:timestamp:")
)

// withLoadedCoreLocation prepares the ObjC side the same way New does, and
// gives the test an autorelease pool so the framework's autoreleased
// temporaries do not warn about having nowhere to go.
func withLoadedCoreLocation(t *testing.T) {
	t.Helper()
	if err := loadCoreLocation(); err != nil {
		t.Fatalf("loadCoreLocation: %v", err)
	}
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(selNew)
	if pool != 0 {
		t.Cleanup(func() { pool.Send(selDrain) })
	}
}

// registerDelegate hands back an object registered as a live delegate, which
// is the precondition every callback checks before it publishes anything.
func registerDelegate(t *testing.T, b *backend) objc.ID {
	t.Helper()
	self := objc.ID(objc.GetClass("NSObject")).Send(selNew)
	if self == 0 {
		t.Fatal("could not allocate an NSObject to stand in for the delegate")
	}
	coreLocationDelegates.Store(self, b)
	t.Cleanup(func() {
		coreLocationDelegates.Delete(self)
		self.Send(selRelease)
	})
	return self
}

func nsString(t *testing.T, s string) objc.ID {
	t.Helper()
	value := objc.ID(objc.GetClass("NSString")).Send(selStringWithUTF8String, s)
	if value == 0 {
		t.Fatalf("could not build an NSString from %q", s)
	}
	return value
}

// The kCL sentinels are negative magic numbers whose meaning is the opposite of
// what their sign suggests: -2 is the *most* accurate setting, not a disabled
// one. Transposing them would silently downgrade every fix.
func TestCoreLocationAccuracyMapsEveryPreference(t *testing.T) {
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
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := coreLocationAccuracy(tc.opts); got != tc.want {
				t.Fatalf("coreLocationAccuracy = %v, want %v", got, tc.want)
			}
		})
	}
}

// New only loads system frameworks and registers the delegate class, so it
// must succeed — and ask for nothing — on any Mac. Stop before Start is the
// path a caller takes when Open succeeds but the caller gives up, and it must
// not wait on a run loop that was never entered.
func TestNewPreparesAProviderWithoutStartingOrPrompting(t *testing.T) {
	sink := &fakeSink{}
	native, err := New(Options{Permission: provider.PermissionDoNotRequest}, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := native.Platform(); got != "darwin" {
		t.Errorf("Platform = %q, want %q", got, "darwin")
	}
	want := geo.Capabilities{Altitude: true, VerticalAccuracy: true, Speed: true, Heading: true}
	if got := native.Capabilities(); got != want {
		t.Errorf("Capabilities = %+v, want %+v", got, want)
	}

	if err := native.Stop(); err != nil {
		t.Fatalf("Stop before Start: %v", err)
	}
	if err := native.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}

	// Preparing a provider must not publish anything: nothing has started, so
	// a status here would tell a subscriber the service exists when it does not.
	if len(sink.fixes)+len(sink.statuses)+len(sink.errs) != 0 {
		t.Fatalf("New published %d fixes, %d statuses, %d errors; want none",
			len(sink.fixes), len(sink.statuses), len(sink.errs))
	}
}

// This is the package's whole permission policy in one switch, and it is the
// difference between a locator that reports "denied" and one that looks like
// it is merely slow. Every CLAuthorizationStatus the OS can hand back is
// pinned here, including one it does not define yet.
func TestPublishAuthorizationMapsEveryStatus(t *testing.T) {
	cases := map[string]struct {
		status         int64
		wantState      geo.State
		wantPermission geo.PermissionState
		wantErr        bool
	}{
		"not determined":          {status: 0, wantState: geo.StateStarting, wantPermission: geo.PermissionPromptRequired},
		"restricted":              {status: 1, wantState: geo.StateDisabled, wantPermission: geo.PermissionRestricted, wantErr: true},
		"denied":                  {status: 2, wantState: geo.StateDisabled, wantPermission: geo.PermissionDenied, wantErr: true},
		"authorized always":       {status: 3, wantState: geo.StateStarting, wantPermission: geo.PermissionGranted},
		"authorized when in use":  {status: 4, wantState: geo.StateStarting, wantPermission: geo.PermissionGranted},
		"a status we do not know": {status: 99, wantState: geo.StateUnavailable, wantPermission: geo.PermissionUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sink := &fakeSink{}
			b := &backend{sink: sink}

			b.publishAuthorization(tc.status)

			if len(sink.statuses) != 1 {
				t.Fatalf("published %d statuses, want 1", len(sink.statuses))
			}
			got := sink.statuses[0]
			if got.State != tc.wantState {
				t.Errorf("state = %v, want %v", got.State, tc.wantState)
			}
			if got.Permission != tc.wantPermission {
				t.Errorf("permission = %v, want %v", got.Permission, tc.wantPermission)
			}
			if got.Message == "" {
				t.Error("status carries no message")
			}

			switch {
			case tc.wantErr && len(sink.errs) != 1:
				t.Fatalf("published %d errors, want 1", len(sink.errs))
			case tc.wantErr && !errors.Is(sink.errs[0], geo.ErrPermissionDenied):
				t.Errorf("error = %v, want ErrPermissionDenied", sink.errs[0])
			case !tc.wantErr && len(sink.errs) != 0:
				t.Fatalf("published %d errors, want 0", len(sink.errs))
			}
		})
	}
}

// A delegate outlives the run that created it only if teardown went wrong, but
// CoreLocation can still deliver one last callback on a delegate we have
// already forgotten. Looking the backend up rather than assuming it is what
// keeps that from dereferencing nil on an OS callback thread.
func TestCallbacksIgnoreADelegateThatIsNoLongerRegistered(t *testing.T) {
	withLoadedCoreLocation(t)
	sink := &fakeSink{}
	// Registered, then dropped — exactly the state a torn-down run leaves.
	self := registerDelegate(t, &backend{sink: sink})
	coreLocationDelegates.Delete(self)

	coreLocationDidUpdateLocations(self, 0, 0, 0)
	coreLocationDidFail(self, 0, 0, 0)
	coreLocationDidChangeAuthorization(self, 0, 0)

	if len(sink.fixes)+len(sink.statuses)+len(sink.errs) != 0 {
		t.Fatalf("an unregistered delegate published %d fixes, %d statuses, %d errors; want none",
			len(sink.fixes), len(sink.statuses), len(sink.errs))
	}
}

// The joined sentinel is what a caller switches on, and the native description
// is what a human reads; losing either leaves one of the two audiences with
// nothing. kCLErrorDenied additionally has to move the reported permission,
// because a run that keeps saying "starting" after a denial never terminates
// from the caller's point of view.
func TestDidFailMapsTheCoreLocationErrorCode(t *testing.T) {
	withLoadedCoreLocation(t)

	cases := map[string]struct {
		code       int64
		wantErr    error
		wantStatus bool
	}{
		"location unknown": {code: 0, wantErr: geo.ErrPositionUnavailable},
		"denied":           {code: 1, wantErr: geo.ErrPermissionDenied, wantStatus: true},
		"anything else":    {code: 7, wantErr: nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sink := &fakeSink{}
			self := registerDelegate(t, &backend{sink: sink})

			nativeError := objc.ID(objc.GetClass("NSError")).Send(
				selErrorWithDomain, nsString(t, "kCLErrorDomain"), tc.code, objc.ID(0))
			if nativeError == 0 {
				t.Fatal("could not build an NSError")
			}

			coreLocationDidFail(self, 0, 0, nativeError)

			if len(sink.errs) != 1 {
				t.Fatalf("published %d errors, want 1", len(sink.errs))
			}
			err := sink.errs[0]
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want %v", err, tc.wantErr)
			}
			// The localized description travels through as the human half of
			// the joined error.
			if !strings.Contains(err.Error(), "kCLErrorDomain") {
				t.Errorf("error = %q, want it to carry the native description", err)
			}

			var annotated *geo.Error
			if !errors.As(err, &annotated) {
				t.Fatalf("error = %v, want a *geo.Error", err)
			}
			if annotated.Platform != platform {
				t.Errorf("platform = %q, want %q", annotated.Platform, platform)
			}
			// A denial is permanent; anything else is worth retrying.
			if wantTemporary := tc.code != 1; annotated.Temporary != wantTemporary {
				t.Errorf("Temporary = %v, want %v", annotated.Temporary, wantTemporary)
			}

			switch {
			case tc.wantStatus && len(sink.statuses) != 1:
				t.Fatalf("published %d statuses, want 1", len(sink.statuses))
			case tc.wantStatus && sink.statuses[0].Permission != geo.PermissionDenied:
				t.Errorf("permission = %v, want PermissionDenied", sink.statuses[0].Permission)
			case !tc.wantStatus && len(sink.statuses) != 0:
				t.Fatalf("published %d statuses, want 0", len(sink.statuses))
			}
		})
	}
}

// CoreLocation reports an absent optional field as a negative accuracy, speed,
// or course rather than by omitting it, so a fix that copied the values
// unconditionally would report an altitude of 0 m and a heading of due north
// for every sample taken indoors. Fields is what tells the caller apart.
func TestDidUpdateLocationsMapsTheOptionalFields(t *testing.T) {
	withLoadedCoreLocation(t)

	stamp := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	date := objc.ID(objc.GetClass("NSDate")).Send(selDateWithTimeInterval1970, float64(stamp.Unix()))
	if date == 0 {
		t.Fatal("could not build an NSDate")
	}

	cases := map[string]struct {
		location func(t *testing.T) objc.ID
		want     geo.Fix
	}{
		"every optional field present": {
			location: func(t *testing.T) objc.ID {
				return objc.ID(objc.GetClass("CLLocation")).Send(selAlloc).Send(
					selInitWithCoordinateFull,
					clCoordinate{Latitude: 51.5, Longitude: -0.12},
					float64(35),  // altitude
					float64(10),  // horizontal accuracy
					float64(4),   // vertical accuracy
					float64(180), // course
					float64(3),   // speed
					date,
				)
			},
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
				Fields:                 geo.FieldAltitude | geo.FieldVerticalAccuracy | geo.FieldSpeed | geo.FieldHeading,
			},
		},
		// initWithLatitude:longitude: leaves vertical accuracy, speed, and
		// course at -1, which is how CoreLocation says "no data".
		"every optional field absent": {
			location: func(t *testing.T) objc.ID {
				return objc.ID(objc.GetClass("CLLocation")).Send(selAlloc).Send(
					selInitWithLatitudeLongitude, float64(48.85), float64(2.35))
			},
			want: geo.Fix{
				Latitude:  48.85,
				Longitude: 2.35,
				Source:    geo.SourceSystem,
				Fields:    0,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sink := &fakeSink{}
			self := registerDelegate(t, &backend{sink: sink})

			location := tc.location(t)
			if location == 0 {
				t.Fatal("could not build a CLLocation")
			}
			defer location.Send(selRelease)
			locations := objc.ID(objc.GetClass("NSArray")).Send(selArrayWithObject, location)

			coreLocationDidUpdateLocations(self, 0, 0, locations)

			if len(sink.fixes) != 1 {
				t.Fatalf("published %d fixes, want 1", len(sink.fixes))
			}
			got := sink.fixes[0]

			if got.Latitude != tc.want.Latitude || got.Longitude != tc.want.Longitude {
				t.Errorf("coordinate = %v,%v want %v,%v", got.Latitude, got.Longitude, tc.want.Latitude, tc.want.Longitude)
			}
			if got.Fields != tc.want.Fields {
				t.Errorf("Fields = %b, want %b", got.Fields, tc.want.Fields)
			}
			if got.Source != geo.SourceSystem {
				t.Errorf("Source = %v, want SourceSystem", got.Source)
			}
			if got.ReceivedAt.IsZero() {
				t.Error("ReceivedAt was not stamped")
			}
			if got.Timestamp.IsZero() {
				t.Error("Timestamp was not stamped")
			}
			if tc.want.Fields != 0 {
				if got.AccuracyMeters != tc.want.AccuracyMeters {
					t.Errorf("AccuracyMeters = %v, want %v", got.AccuracyMeters, tc.want.AccuracyMeters)
				}
				if got.AltitudeMeters != tc.want.AltitudeMeters {
					t.Errorf("AltitudeMeters = %v, want %v", got.AltitudeMeters, tc.want.AltitudeMeters)
				}
				if got.VerticalAccuracyMeters != tc.want.VerticalAccuracyMeters {
					t.Errorf("VerticalAccuracyMeters = %v, want %v", got.VerticalAccuracyMeters, tc.want.VerticalAccuracyMeters)
				}
				if got.SpeedMetersPerSecond != tc.want.SpeedMetersPerSecond {
					t.Errorf("SpeedMetersPerSecond = %v, want %v", got.SpeedMetersPerSecond, tc.want.SpeedMetersPerSecond)
				}
				if got.HeadingDegrees != tc.want.HeadingDegrees {
					t.Errorf("HeadingDegrees = %v, want %v", got.HeadingDegrees, tc.want.HeadingDegrees)
				}
				if !got.Timestamp.Equal(tc.want.Timestamp) {
					t.Errorf("Timestamp = %v, want the CLLocation's %v", got.Timestamp, tc.want.Timestamp)
				}
			} else {
				// An absent field must be left at zero, not filled with the
				// -1 sentinel CoreLocation reported.
				if got.AltitudeMeters != 0 || got.SpeedMetersPerSecond != 0 || got.HeadingDegrees != 0 {
					t.Errorf("absent fields carried values: %+v", got)
				}
			}
		})
	}
}

// An update carrying no locations is not an error — CoreLocation can deliver
// one — so it must return quietly rather than publish a fix at 0,0, which
// validates cleanly and would be cached as a real position off West Africa.
func TestDidUpdateLocationsIgnoresAnEmptyArray(t *testing.T) {
	withLoadedCoreLocation(t)
	sink := &fakeSink{}
	self := registerDelegate(t, &backend{sink: sink})

	coreLocationDidUpdateLocations(self, 0, 0, objc.ID(objc.GetClass("NSArray")).Send(selArray))

	if len(sink.fixes) != 0 {
		t.Fatalf("published %d fixes for an empty update, want 0", len(sink.fixes))
	}
}
