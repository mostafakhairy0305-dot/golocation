//go:build darwin && (amd64 || arm64)

// Package corelocation adapts Apple's CoreLocation framework to
// provider.Provider.
package corelocation

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/mostafakhairy0305-dot/golocation/geo"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

const platform = "darwin"

// Options are the CoreLocation-specific knobs. The core translates the public
// config into this; nothing outside this package needs to know these names.
type Options struct {
	Accuracy              provider.Accuracy
	DesiredAccuracyMeters uint32
	MinimumDistanceMeters float64
	Permission            provider.PermissionMode
}

type backend struct {
	opts Options
	sink provider.Sink

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup

	mu       sync.Mutex
	manager  objc.ID
	delegate objc.ID
}

type clCoordinate struct {
	Latitude  float64
	Longitude float64
}

var (
	coreLocationLoadOnce sync.Once
	coreLocationLoadErr  error

	coreLocationClassOnce sync.Once
	coreLocationClass     objc.Class
	coreLocationClassErr  error

	coreLocationDelegates sync.Map // map[objc.ID]*backend
)

var (
	selNew                           = objc.RegisterName("new")
	selRelease                       = objc.RegisterName("release")
	selDrain                         = objc.RegisterName("drain")
	selSetDelegate                   = objc.RegisterName("setDelegate:")
	selSetDesiredAccuracy            = objc.RegisterName("setDesiredAccuracy:")
	selSetDistanceFilter             = objc.RegisterName("setDistanceFilter:")
	selRequestWhenInUseAuthorization = objc.RegisterName("requestWhenInUseAuthorization")
	selStartUpdatingLocation         = objc.RegisterName("startUpdatingLocation")
	selStopUpdatingLocation          = objc.RegisterName("stopUpdatingLocation")
	selLocationServicesEnabled       = objc.RegisterName("locationServicesEnabled")
	selAuthorizationStatus           = objc.RegisterName("authorizationStatus")
	selLastObject                    = objc.RegisterName("lastObject")
	selCoordinate                    = objc.RegisterName("coordinate")
	selHorizontalAccuracy            = objc.RegisterName("horizontalAccuracy")
	selAltitude                      = objc.RegisterName("altitude")
	selVerticalAccuracy              = objc.RegisterName("verticalAccuracy")
	selSpeed                         = objc.RegisterName("speed")
	selCourse                        = objc.RegisterName("course")
	selTimestamp                     = objc.RegisterName("timestamp")
	selTimeIntervalSince1970         = objc.RegisterName("timeIntervalSince1970")
	selCode                          = objc.RegisterName("code")
	selLocalizedDescription          = objc.RegisterName("localizedDescription")
	selUTF8String                    = objc.RegisterName("UTF8String")
	selCurrentRunLoop                = objc.RegisterName("currentRunLoop")
	selDateWithTimeIntervalSinceNow  = objc.RegisterName("dateWithTimeIntervalSinceNow:")
	selRunUntilDate                  = objc.RegisterName("runUntilDate:")
	selDidUpdateLocations            = objc.RegisterName("locationManager:didUpdateLocations:")
	selDidFailWithError              = objc.RegisterName("locationManager:didFailWithError:")
	selDidChangeAuthorization        = objc.RegisterName("locationManagerDidChangeAuthorization:")
)

// New loads CoreLocation and prepares a backend. It does not start the
// provider; Start does.
func New(opts Options, sink provider.Sink) (provider.Provider, error) {
	err := loadCoreLocation()
	if err != nil {
		return nil, geo.Wrap(platform, "load CoreLocation", err, false)
	}

	err = registerCoreLocationDelegate()
	if err != nil {
		return nil, geo.Wrap(platform, "register CoreLocation delegate", err, false)
	}

	return &backend{opts: opts, sink: sink, stopCh: make(chan struct{})}, nil
}

func (b *backend) Platform() string { return platform }

func (b *backend) Capabilities() geo.Capabilities {
	return geo.Capabilities{
		Altitude:         true,
		VerticalAccuracy: true,
		Speed:            true,
		Heading:          true,
		Source:           false,
	}
}

func (b *backend) Start(ctx context.Context) error {
	ready := make(chan error, 1)

	b.wg.Add(1)
	go b.run(ready)

	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		b.stopOnce.Do(func() { close(b.stopCh) })
		b.wg.Wait()

		return geo.Wrap(platform, "start CoreLocation", ctx.Err(), true)
	}
}

func (b *backend) Stop() error {
	b.stopOnce.Do(func() { close(b.stopCh) })
	b.wg.Wait()

	return nil
}

func (b *backend) run(ready chan<- error) {
	defer b.wg.Done()

	runtime.LockOSThread()

	defer runtime.UnlockOSThread()

	// Setup pool. It holds only the objects created below, and drains once on
	// return — see the loop for why per-iteration objects need their own.
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(selNew)
	if pool != 0 {
		defer pool.Send(selDrain)
	}

	managerClass := objc.GetClass("CLLocationManager")
	if managerClass == 0 {
		ready <- geo.Wrap(platform, "find CLLocationManager", geo.ErrServiceUnavailable, false)

		return
	}

	if !objc.Send[bool](objc.ID(managerClass), selLocationServicesEnabled) {
		ready <- geo.Wrap(platform, "check location services", geo.ErrServiceDisabled, false)

		return
	}

	delegate := objc.ID(coreLocationClass).Send(selNew)

	manager := objc.ID(managerClass).Send(selNew)
	if delegate == 0 || manager == 0 {
		if delegate != 0 {
			delegate.Send(selRelease)
		}

		if manager != 0 {
			manager.Send(selRelease)
		}

		ready <- geo.Wrap(platform, "create CoreLocation objects", geo.ErrServiceUnavailable, false)

		return
	}

	b.mu.Lock()
	b.delegate = delegate
	b.manager = manager
	b.mu.Unlock()
	coreLocationDelegates.Store(delegate, b)

	defer func() {
		manager.Send(selStopUpdatingLocation)
		manager.Send(selSetDelegate, objc.ID(0))
		coreLocationDelegates.Delete(delegate)
		manager.Send(selRelease)
		delegate.Send(selRelease)
		b.mu.Lock()
		b.manager = 0
		b.delegate = 0
		b.mu.Unlock()
	}()

	manager.Send(selSetDelegate, delegate)
	manager.Send(selSetDesiredAccuracy, coreLocationAccuracy(b.opts))

	if b.opts.MinimumDistanceMeters > 0 {
		manager.Send(selSetDistanceFilter, b.opts.MinimumDistanceMeters)
	} else {
		manager.Send(selSetDistanceFilter, float64(-1)) // kCLDistanceFilterNone
	}

	authorization := objc.Send[int64](manager, selAuthorizationStatus)
	b.publishAuthorization(authorization)

	if authorization == 0 {
		if b.opts.Permission == provider.PermissionDoNotRequest {
			ready <- geo.Wrap(platform, "request permission", geo.ErrPermissionNeeded, false)

			return
		}

		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateStarting,
				Permission: geo.PermissionPromptRequired,
				Message:    "requesting macOS location access",
			},
		)
		manager.Send(selRequestWhenInUseAuthorization)
	}

	manager.Send(selStartUpdatingLocation)

	ready <- nil

	runLoop := objc.ID(objc.GetClass("NSRunLoop")).Send(selCurrentRunLoop)
	dateClass := objc.ID(objc.GetClass("NSDate"))
	poolClass := objc.ID(objc.GetClass("NSAutoreleasePool"))

	for {
		select {
		case <-b.stopCh:
			return
		default:
		}
		// Every turn autoreleases objects: the NSDate below, plus each
		// CLLocation, NSArray, and NSError description handed to the delegate
		// while the run loop spins. Without a pool scoped to the iteration
		// they all accumulate until run returns, which for a location service
		// is never. Draining here is safe because the delegate copies every
		// value it needs into Go memory before returning.
		iterationPool := poolClass.Send(selNew)
		until := dateClass.Send(selDateWithTimeIntervalSinceNow, float64(0.25))
		runLoop.Send(selRunUntilDate, until)

		if iterationPool != 0 {
			iterationPool.Send(selDrain)
		}
	}
}

func (b *backend) publishAuthorization(status int64) {
	switch status {
	case 0:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateStarting,
				Permission: geo.PermissionPromptRequired,
				Message:    "macOS location permission not determined",
			},
		)
	case 1:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateDisabled,
				Permission: geo.PermissionRestricted,
				Message:    "macOS location permission restricted",
			},
		)
		b.sink.PublishError(geo.Wrap(platform, "authorization", geo.ErrPermissionDenied, false))
	case 2:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateDisabled,
				Permission: geo.PermissionDenied,
				Message:    "macOS location permission denied",
			},
		)
		b.sink.PublishError(geo.Wrap(platform, "authorization", geo.ErrPermissionDenied, false))
	case 3, 4:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateStarting,
				Permission: geo.PermissionGranted,
				Message:    "macOS location access granted",
			},
		)
	default:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateUnavailable,
				Permission: geo.PermissionUnknown,
				Message:    fmt.Sprintf("unknown macOS authorization status %d", status),
			},
		)
	}
}

func loadCoreLocation() error {
	coreLocationLoadOnce.Do(func() {
		_, err := purego.Dlopen(
			"/System/Library/Frameworks/Foundation.framework/Foundation",
			purego.RTLD_NOW|purego.RTLD_GLOBAL,
		)
		if err != nil {
			coreLocationLoadErr = err

			return
		}

		_, err = purego.Dlopen(
			"/System/Library/Frameworks/CoreLocation.framework/CoreLocation",
			purego.RTLD_NOW|purego.RTLD_GLOBAL,
		)
		coreLocationLoadErr = err
	})

	return coreLocationLoadErr
}

func registerCoreLocationDelegate() error {
	coreLocationClassOnce.Do(func() {
		if existing := objc.GetClass("GoLocationCoreLocationDelegate"); existing != 0 {
			coreLocationClass = existing

			return
		}

		protocols := []*objc.Protocol{}
		if protocol := objc.GetProtocol("CLLocationManagerDelegate"); protocol != nil {
			protocols = append(protocols, protocol)
		}

		coreLocationClass, coreLocationClassErr = objc.RegisterClass(
			"GoLocationCoreLocationDelegate",
			objc.GetClass("NSObject"),
			protocols,
			nil,
			[]objc.MethodDef{
				{Cmd: selDidUpdateLocations, Fn: coreLocationDidUpdateLocations},
				{Cmd: selDidFailWithError, Fn: coreLocationDidFail},
				{Cmd: selDidChangeAuthorization, Fn: coreLocationDidChangeAuthorization},
			},
		)
	})

	return coreLocationClassErr
}

func coreLocationDidUpdateLocations(self objc.ID, _ objc.SEL, _ objc.ID, locations objc.ID) {
	value, ok := coreLocationDelegates.Load(self)
	if !ok {
		return
	}

	b := value.(*backend)

	location := locations.Send(selLastObject)
	if location == 0 {
		return
	}

	coordinate := objc.Send[clCoordinate](location, selCoordinate)
	accuracy := objc.Send[float64](location, selHorizontalAccuracy)
	timestampObject := location.Send(selTimestamp)

	timestamp := time.Now().UTC()
	if timestampObject != 0 {
		timestamp = time.Unix(0, int64(objc.Send[float64](timestampObject, selTimeIntervalSince1970)*float64(time.Second))).
			UTC()
	}

	fix := geo.Fix{
		Timestamp:      timestamp,
		ReceivedAt:     time.Now().UTC(),
		Latitude:       coordinate.Latitude,
		Longitude:      coordinate.Longitude,
		AccuracyMeters: accuracy,
		Source:         geo.SourceSystem,
	}

	altitude := objc.Send[float64](location, selAltitude)

	verticalAccuracy := objc.Send[float64](location, selVerticalAccuracy)
	if verticalAccuracy >= 0 {
		fix.AltitudeMeters = altitude
		fix.VerticalAccuracyMeters = verticalAccuracy
		fix.Fields |= geo.FieldAltitude | geo.FieldVerticalAccuracy
	}

	speed := objc.Send[float64](location, selSpeed)
	if speed >= 0 {
		fix.SpeedMetersPerSecond = speed
		fix.Fields |= geo.FieldSpeed
	}

	course := objc.Send[float64](location, selCourse)
	if course >= 0 {
		fix.HeadingDegrees = course
		fix.Fields |= geo.FieldHeading
	}

	b.sink.PublishFix(fix)
}

func coreLocationDidFail(self objc.ID, _ objc.SEL, _ objc.ID, nativeError objc.ID) {
	value, ok := coreLocationDelegates.Load(self)
	if !ok {
		return
	}

	b := value.(*backend)
	code := objc.Send[int64](nativeError, selCode)

	description := "CoreLocation error"
	if object := nativeError.Send(selLocalizedDescription); object != 0 {
		description = objc.Send[string](object, selUTF8String)
	}

	native := errors.New(description)

	switch code {
	case 0: // kCLErrorLocationUnknown
		b.sink.PublishError(
			geo.Wrap(
				platform,
				"location update",
				errors.Join(geo.ErrPositionUnavailable, native),
				true,
			),
		)
	case 1: // kCLErrorDenied
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateDisabled,
				Permission: geo.PermissionDenied,
				Message:    description,
			},
		)
		b.sink.PublishError(
			geo.Wrap(
				platform,
				"location update",
				errors.Join(geo.ErrPermissionDenied, native),
				false,
			),
		)
	default:
		b.sink.PublishError(geo.Wrap(platform, "location update", native, true))
	}
}

func coreLocationDidChangeAuthorization(self objc.ID, _ objc.SEL, manager objc.ID) {
	value, ok := coreLocationDelegates.Load(self)
	if !ok {
		return
	}

	b := value.(*backend)
	b.publishAuthorization(objc.Send[int64](manager, selAuthorizationStatus))
}

func coreLocationAccuracy(opts Options) float64 {
	if opts.DesiredAccuracyMeters > 0 {
		return float64(opts.DesiredAccuracyMeters)
	}

	switch opts.Accuracy {
	case provider.AccuracyNavigation:
		return -2 // kCLLocationAccuracyBestForNavigation
	case provider.AccuracyHigh:
		return -1 // kCLLocationAccuracyBest
	default:
		return 100 // kCLLocationAccuracyHundredMeters
	}
}
