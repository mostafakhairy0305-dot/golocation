//go:build windows && (amd64 || arm64)

// Package winrt adapts the Windows.Devices.Geolocation WinRT API to
// provider.Provider.
package winrt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/deploymenttheory/go-bindings-win32/bindings/runtime/win32"
	syswinrt "github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/winrt"
	wruntime "github.com/deploymenttheory/go-bindings-winrt/bindings/runtime/winrt"
	"github.com/deploymenttheory/go-bindings-winrt/bindings/winrt/devices/geolocation"
	"github.com/deploymenttheory/go-bindings-winrt/bindings/winrt/foundation"
	"github.com/mostafakhairy0305-dot/golocation/geo"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
	"github.com/mostafakhairy0305-dot/singleton"
)

const platform = "windows"

// Options are the WinRT-specific knobs. The core translates the public config
// into this; nothing outside this package needs to know these names.
// An unset knob means "leave the WinRT default alone".
type Options struct {
	Accuracy              provider.Accuracy       `exhaustruct:"optional"`
	DesiredAccuracyMeters uint32                  `exhaustruct:"optional"`
	MinimumInterval       time.Duration           `exhaustruct:"optional"`
	MaximumAge            time.Duration           `exhaustruct:"optional"`
	StartTimeout          time.Duration           `exhaustruct:"optional"`
	Permission            provider.PermissionMode `exhaustruct:"optional"`
}

// Backend is the WinRT provider.Provider. New builds one; the core drives it
// through the Provider methods.
type Backend struct {
	opts Options `exhaustruct:"optional"`
	sink provider.Sink

	mu sync.Mutex `exhaustruct:"optional"`

	// Owned by the Geolocator; populated on Start.
	locator       *geolocation.Geolocator                                               `exhaustruct:"optional"`
	positionToken syswinrt.EventRegistrationToken                                       `exhaustruct:"optional"`
	statusToken   syswinrt.EventRegistrationToken                                       `exhaustruct:"optional"`
	positionEvent *geolocation.TypedEventHandlerOfGeolocatorAndPositionChangedEventArgs `exhaustruct:"optional"`
	statusEvent   *geolocation.TypedEventHandlerOfGeolocatorAndStatusChangedEventArgs   `exhaustruct:"optional"`

	// stopGuard runs the teardown exactly once and carries the joined stop
	// error as its value, so the library never sees a failure to retry and
	// every caller of Stop is handed the same outcome.
	stopGuard *singleton.Provider[error] `exhaustruct:"optional"`
}

// New prepares a Backend. It does not start the provider; Start does.
func New(opts Options, sink provider.Sink) (*Backend, error) {
	backend := &Backend{opts: opts, sink: sink}
	// The teardown must never retry, back off, or time out, so the guard runs
	// at most one attempt with no deadline, and returns the stop error as its
	// value rather than as a failure the library would retry.
	backend.stopGuard = singleton.MustNew(
		func(context.Context) (error, error) { return backend.teardown(), nil },
		singleton.WithMaxAttempts(1),
		singleton.WithInitializationTimeout(0),
	)

	return backend, nil
}

var _ provider.Provider = (*Backend)(nil)

// Platform names this adapter for error annotation.
func (b *Backend) Platform() string { return platform }

// Capabilities reports the optional Fix fields the Windows Geolocator can supply.
func (b *Backend) Capabilities() geo.Capabilities {
	return geo.Capabilities{
		Altitude:         true,
		VerticalAccuracy: true,
		Speed:            true,
		Heading:          true,
		Source:           true,
	}
}

// Start brings the Geolocator up and returns once it is delivering or has
// failed.
func (b *Backend) Start(ctx context.Context) error {
	if b.opts.Permission != provider.PermissionDoNotRequest {
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateStarting,
				Permission: geo.PermissionPromptRequired,
				Message:    "requesting Windows location access",
			},
		)
		if err := b.requestAccess(ctx); err != nil {
			return err
		}
	}

	locator, err := geolocation.NewGeolocator()
	if err != nil {
		return geo.Wrap(platform, "create Geolocator", err, false)
	}

	accuracy := geolocation.PositionAccuracyDefault
	if b.opts.Accuracy == provider.AccuracyHigh || b.opts.Accuracy == provider.AccuracyNavigation ||
		b.opts.DesiredAccuracyMeters > 0 {
		accuracy = geolocation.PositionAccuracyHigh
	}
	if err := locator.SetDesiredAccuracy(accuracy); err != nil {
		locator.Release()
		return geo.Wrap(platform, "set accuracy", err, false)
	}
	if b.opts.DesiredAccuracyMeters > 0 {
		if err := setWindowsDesiredAccuracyMeters(
			locator,
			b.opts.DesiredAccuracyMeters,
		); err != nil {
			locator.Release()
			return geo.Wrap(platform, "set desired accuracy in meters", err, false)
		}
	}
	if b.opts.MinimumInterval > 0 {
		milliseconds := b.opts.MinimumInterval.Milliseconds()
		if milliseconds < 1 {
			milliseconds = 1
		}
		if milliseconds > int64(^uint32(0)) {
			milliseconds = int64(^uint32(0))
		}
		if err := locator.SetReportInterval(uint32(milliseconds)); err != nil {
			locator.Release()
			return geo.Wrap(platform, "set report interval", err, false)
		}
	}

	positionEvent, err := geolocation.NewTypedEventHandlerOfGeolocatorAndPositionChangedEventArgs(
		func(_ *geolocation.IGeolocator, args *geolocation.IPositionChangedEventArgs) {
			if args == nil {
				return
			}
			position, eventErr := args.Position()
			if eventErr != nil {
				b.sink.PublishError(geo.Wrap(platform, "read position event", eventErr, true))
				return
			}
			if position == nil {
				b.sink.PublishError(
					geo.Wrap(platform, "read position event", geo.ErrPositionUnavailable, true),
				)
				return
			}
			defer position.Release()
			b.publishPosition(position)
		},
	)
	if err != nil {
		locator.Release()
		return geo.Wrap(platform, "create position handler", err, false)
	}

	statusEvent, err := geolocation.NewTypedEventHandlerOfGeolocatorAndStatusChangedEventArgs(
		func(_ *geolocation.IGeolocator, args *geolocation.IStatusChangedEventArgs) {
			if args == nil {
				return
			}
			status, eventErr := args.Status()
			if eventErr != nil {
				b.sink.PublishError(geo.Wrap(platform, "read status event", eventErr, true))
				return
			}
			b.publishWindowsStatus(status)
		},
	)
	if err != nil {
		positionEvent.Close()
		locator.Release()
		return geo.Wrap(platform, "create status handler", err, false)
	}

	positionToken, err := locator.AddPositionChanged(positionEvent)
	if err != nil {
		statusEvent.Close()
		positionEvent.Close()
		locator.Release()
		return geo.Wrap(platform, "subscribe position", err, false)
	}
	statusToken, err := locator.AddStatusChanged(statusEvent)
	if err != nil {
		_ = locator.RemovePositionChanged(positionToken)
		statusEvent.Close()
		positionEvent.Close()
		locator.Release()
		return geo.Wrap(platform, "subscribe status", err, false)
	}

	b.mu.Lock()
	b.locator = locator
	b.positionToken = positionToken
	b.statusToken = statusToken
	b.positionEvent = positionEvent
	b.statusEvent = statusEvent
	b.mu.Unlock()

	if status, statusErr := locator.LocationStatus(); statusErr == nil {
		b.publishWindowsStatus(status)
	} else {
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateStarting,
				Permission: geo.PermissionGranted,
				Message:    "Windows Geolocator started",
			},
		)
	}

	b.startInitialRead(locator)
	return nil
}

// Stop ends the session. It is safe before or after Start: the stop guard runs
// the teardown once and hands the same joined stop error to every later call.
func (b *Backend) Stop() error {
	// The value is teardown's own joined-and-wrapped stop error; the second
	// result is the guard's initialization error, which this guard never emits.
	stopErr, _ := b.stopGuard.Get(context.Background())

	return stopErr //nolint:wrapcheck // the value is teardown's own wrapped stop error
}

// teardown is the stop guard's factory: it detaches the handlers and releases
// the Geolocator, returning whatever the removals reported.
func (b *Backend) teardown() error {
	b.mu.Lock()
	locator := b.locator
	positionToken := b.positionToken
	statusToken := b.statusToken
	positionEvent := b.positionEvent
	statusEvent := b.statusEvent
	b.locator = nil
	b.positionEvent = nil
	b.statusEvent = nil
	b.mu.Unlock()

	var stopErr error
	if locator != nil {
		if err := locator.RemovePositionChanged(positionToken); err != nil {
			stopErr = errors.Join(stopErr, err)
		}
		if err := locator.RemoveStatusChanged(statusToken); err != nil {
			stopErr = errors.Join(stopErr, err)
		}
	}
	if positionEvent != nil {
		positionEvent.Close()
	}
	if statusEvent != nil {
		statusEvent.Close()
	}
	if locator != nil {
		locator.Release()
	}
	return geo.Wrap(platform, "stop", stopErr, true)
}

func (b *Backend) requestAccess(ctx context.Context) error {
	statics, err := geolocation.GeolocatorStatics()
	if err != nil {
		return geo.Wrap(platform, "get Geolocator statics", err, false)
	}
	defer statics.Release()

	op, err := statics.RequestAccessAsync()
	if err != nil {
		return geo.Wrap(platform, "request access", err, false)
	}

	type result struct {
		status geolocation.GeolocationAccessStatus
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		defer op.Release()
		status, awaitErr := op.Await()
		resultCh <- result{status: status, err: awaitErr}
	}()

	select {
	case <-ctx.Done():
		return geo.Wrap(platform, "request access", ctx.Err(), true)
	case result := <-resultCh:
		if result.err != nil {
			return geo.Wrap(platform, "request access", result.err, false)
		}
		switch result.status {
		case geolocation.GeolocationAccessStatusAllowed:
			b.sink.PublishStatus(
				geo.Status{
					State:      geo.StateStarting,
					Permission: geo.PermissionGranted,
					Message:    "Windows location access granted",
				},
			)
			return nil
		case geolocation.GeolocationAccessStatusDenied:
			b.sink.PublishStatus(
				geo.Status{
					State:      geo.StateDisabled,
					Permission: geo.PermissionDenied,
					Message:    "Windows location access denied",
				},
			)
			return geo.Wrap(platform, "request access", geo.ErrPermissionDenied, false)
		default:
			return geo.Wrap(platform, "request access", geo.ErrPermissionNeeded, false)
		}
	}
}

func (b *Backend) startInitialRead(locator *geolocation.Geolocator) {
	maximumAge := b.opts.MaximumAge
	if maximumAge == 0 {
		maximumAge = 24 * time.Hour
	}
	timeout := b.opts.StartTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	op, err := locator.GetGeopositionAsyncWithAgeAndTimeout(
		foundation.TimeSpan{Duration: durationToWinRT(maximumAge)},
		foundation.TimeSpan{Duration: durationToWinRT(timeout)},
	)
	if err != nil {
		b.sink.PublishError(geo.Wrap(platform, "start initial position request", err, true))
		return
	}

	go func() {
		defer op.Release()
		position, awaitErr := op.Await()
		if awaitErr != nil {
			b.sink.PublishError(geo.Wrap(platform, "initial position request", awaitErr, true))
			return
		}
		if position == nil {
			b.sink.PublishError(
				geo.Wrap(platform, "initial position request", geo.ErrPositionUnavailable, true),
			)
			return
		}
		defer position.Release()
		b.publishPosition(position)
	}()
}

func (b *Backend) publishPosition(position *geolocation.IGeoposition) {
	coordinate, err := position.Coordinate()
	if err != nil {
		b.sink.PublishError(geo.Wrap(platform, "get coordinate", err, true))
		return
	}
	if coordinate == nil {
		b.sink.PublishError(geo.Wrap(platform, "get coordinate", geo.ErrPositionUnavailable, true))
		return
	}
	defer coordinate.Release()

	latitude, err := winRTDouble(coordinate.LpVtbl, unsafe.Pointer(coordinate), 6)
	if err != nil {
		b.sink.PublishError(geo.Wrap(platform, "get latitude", err, true))
		return
	}
	longitude, err := winRTDouble(coordinate.LpVtbl, unsafe.Pointer(coordinate), 7)
	if err != nil {
		b.sink.PublishError(geo.Wrap(platform, "get longitude", err, true))
		return
	}
	accuracy, err := winRTDouble(coordinate.LpVtbl, unsafe.Pointer(coordinate), 9)
	if err != nil {
		b.sink.PublishError(geo.Wrap(platform, "get accuracy", err, true))
		return
	}
	timestamp, err := coordinate.Timestamp()
	if err != nil {
		b.sink.PublishError(geo.Wrap(platform, "get timestamp", err, true))
		return
	}

	fix := geo.Fix{
		Timestamp:      winRTDateTime(timestamp),
		ReceivedAt:     time.Now().UTC(),
		Latitude:       latitude,
		Longitude:      longitude,
		AccuracyMeters: accuracy,
		Source:         geo.SourceUnknown,
	}

	if value, ok := optionalWinRTDouble(coordinate.Altitude); ok {
		fix.AltitudeMeters = value
		fix.Fields |= geo.FieldAltitude
	}
	if value, ok := optionalWinRTDouble(coordinate.AltitudeAccuracy); ok {
		fix.VerticalAccuracyMeters = value
		fix.Fields |= geo.FieldVerticalAccuracy
	}
	if value, ok := optionalWinRTDouble(coordinate.Speed); ok && value >= 0 {
		fix.SpeedMetersPerSecond = value
		fix.Fields |= geo.FieldSpeed
	}
	if value, ok := optionalWinRTDouble(coordinate.Heading); ok && value >= 0 {
		fix.HeadingDegrees = value
		fix.Fields |= geo.FieldHeading
	}

	if positionData, queryErr := wruntime.QueryInterface[geolocation.IGeocoordinateWithPositionData](
		unsafe.Pointer(coordinate),
		&geolocation.IID_IGeocoordinateWithPositionData,
	); queryErr == nil {
		if source, sourceErr := positionData.PositionSource(); sourceErr == nil {
			fix.Source = mapWindowsSource(source)
		}
		positionData.Release()
	}
	if remoteData, queryErr := wruntime.QueryInterface[geolocation.IGeocoordinateWithRemoteSource](
		unsafe.Pointer(coordinate),
		&geolocation.IID_IGeocoordinateWithRemoteSource,
	); queryErr == nil {
		if remote, remoteErr := remoteData.IsRemoteSource(); remoteErr == nil && remote {
			fix.Source = geo.SourceRemote
		}
		remoteData.Release()
	}

	b.sink.PublishFix(fix)
}

func (b *Backend) publishWindowsStatus(status geolocation.PositionStatus) {
	permission := geo.PermissionGranted
	switch status {
	case geolocation.PositionStatusReady:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateReady,
				Permission: permission,
				Message:    "Windows location ready",
			},
		)
	case geolocation.PositionStatusInitializing:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateStarting,
				Permission: permission,
				Message:    "Windows location initializing",
			},
		)
	case geolocation.PositionStatusNoData:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateUnavailable,
				Permission: permission,
				Message:    "Windows location has no data",
			},
		)
	case geolocation.PositionStatusDisabled:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateDisabled,
				Permission: geo.PermissionDenied,
				Message:    "Windows location disabled",
			},
		)
		b.sink.PublishError(geo.Wrap(platform, "status", geo.ErrServiceDisabled, false))
	case geolocation.PositionStatusNotInitialized:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateStarting,
				Permission: geo.PermissionUnknown,
				Message:    "Windows location not initialized",
			},
		)
	case geolocation.PositionStatusNotAvailable:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateUnavailable,
				Permission: permission,
				Message:    "Windows location unavailable",
			},
		)
		b.sink.PublishError(geo.Wrap(platform, "status", geo.ErrServiceUnavailable, true))
	default:
		b.sink.PublishStatus(
			geo.Status{
				State:      geo.StateUnavailable,
				Permission: geo.PermissionUnknown,
				Message:    fmt.Sprintf("unknown Windows location status %d", status),
			},
		)
	}
}

func setWindowsDesiredAccuracyMeters(locator *geolocation.Geolocator, meters uint32) error {
	scalar, err := locator.AsGeolocatorWithScalarAccuracy()
	if err != nil {
		return err
	}
	defer scalar.Release()

	propertyValues, err := foundation.PropertyValueStatics()
	if err != nil {
		return err
	}
	defer propertyValues.Release()

	boxed, err := propertyValues.CreateUInt32(meters)
	if err != nil {
		return err
	}
	defer boxed.Release()

	reference, err := wruntime.QueryInterface[geolocation.IReferenceOfUInt32](
		unsafe.Pointer(boxed),
		&geolocation.IID_IReferenceOfUInt32,
	)
	if err != nil {
		return err
	}
	defer reference.Release()

	return scalar.SetDesiredAccuracyInMeters(reference)
}

func optionalWinRTDouble(getter func() (*geolocation.IReferenceOfDouble, error)) (float64, bool) {
	ref, err := getter()
	if err != nil || ref == nil {
		return 0, false
	}
	defer ref.Release()
	value, err := winRTDouble(ref.LpVtbl, unsafe.Pointer(ref), 6)
	return value, err == nil
}

func winRTDouble(vtable *[1024]uintptr, self unsafe.Pointer, slot int) (float64, error) {
	if self == nil || vtable == nil || slot < 0 || slot >= len(vtable) {
		return 0, geo.ErrPositionUnavailable
	}
	result := new(float64)
	r1, _, _ := syscall.SyscallN(
		vtable[slot],
		uintptr(self),
		uintptr(wruntime.OutParam(unsafe.Pointer(result))),
	)
	return *result, win32.ErrIfFailed(int32(r1))
}

func mapWindowsSource(source geolocation.PositionSource) geo.Source {
	switch source {
	case geolocation.PositionSourceCellular:
		return geo.SourceCellular
	case geolocation.PositionSourceSatellite:
		return geo.SourceSatellite
	case geolocation.PositionSourceWiFi:
		return geo.SourceWiFi
	case geolocation.PositionSourceIPAddress:
		return geo.SourceIP
	case geolocation.PositionSourceDefault:
		return geo.SourceDefault
	case geolocation.PositionSourceObfuscated:
		return geo.SourceObfuscated
	default:
		return geo.SourceUnknown
	}
}

func durationToWinRT(duration time.Duration) int64 {
	return int64(duration / (100 * time.Nanosecond))
}

func winRTDateTime(value foundation.DateTime) time.Time {
	const ticksBetween1601And1970 = int64(116444736000000000)
	ticks := value.UniversalTime - ticksBetween1601And1970
	return time.Unix(0, ticks*100).UTC()
}
