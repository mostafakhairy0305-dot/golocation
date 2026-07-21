//go:build darwin && (amd64 || arm64)

// Package corelocation adapts Apple's CoreLocation framework to
// provider.Provider.
package corelocation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/mostafakhairy0305-dot/golocation/geo"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

const platform = "darwin"

// delegateClassName is the CLLocationManagerDelegate subclass this package
// registers with the ObjC runtime. The runtime's own class table is what
// memoizes the registration, so the name is the only thing this package has to
// remember between Backends.
const delegateClassName = "GoLocationCoreLocationDelegate"

// The delegate's instance variables. Each holds an NSMutableArray that the
// callbacks append to and that the run loop drains — see delegateQueues.
const (
	ivarLocations      = "locations"
	ivarFailures       = "failures"
	ivarAuthorizations = "authorizations"
)

// runLoopTurn is how long spin lets CoreLocation have the thread before it
// drains what the delegate queued, in seconds. It bounds how late a fix can be
// published, so it is short enough that nobody notices and long enough that the
// thread is not spending its life waking up.
const runLoopTurn = 0.1

// errCoreLocation stands behind every error the framework reports, whose own
// text is a localized description rather than anything a caller can match on.
var errCoreLocation = errors.New("CoreLocation")

// Options are the CoreLocation-specific knobs. The core translates the public
// config into this; nothing outside this package needs to know these names.
// An unset knob means "leave the CoreLocation default alone".
type Options struct {
	Accuracy              provider.Accuracy       `exhaustruct:"optional"`
	DesiredAccuracyMeters uint32                  `exhaustruct:"optional"`
	MinimumDistanceMeters float64                 `exhaustruct:"optional"`
	Permission            provider.PermissionMode `exhaustruct:"optional"`
}

// Backend is the CoreLocation provider.Provider. New builds one; the core
// drives it through the Provider methods.
type Backend struct {
	opts Options `exhaustruct:"optional"`
	sink provider.Sink
	// sel is resolved once in New and read from the run loop, which runs on a
	// native thread where resolving 35 selectors per turn would be pure
	// overhead.
	sel *selectorSet
	// class and queues are the delegate's ObjC shape, resolved once in New so
	// that nothing on the delivery path has to look either of them up.
	class  objc.Class
	queues delegateQueues

	stopOnce sync.Once      `exhaustruct:"optional"`
	stopCh   chan struct{}  `exhaustruct:"optional"`
	wg       sync.WaitGroup `exhaustruct:"optional"`

	// Owned by the CoreLocation manager; populated on Start.
	mu       sync.Mutex `exhaustruct:"optional"`
	manager  objc.ID    `exhaustruct:"optional"`
	delegate objc.ID    `exhaustruct:"optional"`
}

var _ provider.Provider = (*Backend)(nil)

type clCoordinate struct {
	Latitude  float64
	Longitude float64
}

// delegateQueues locates the three arrays a delegate carries. Keeping the
// queues on the ObjC object — rather than in a Go map from delegate to backend
// — is what lets the callbacks be plain functions that close over nothing, and
// so lets this package hold no mutable state of its own.
type delegateQueues struct {
	locations      objc.Ivar
	failures       objc.Ivar
	authorizations objc.Ivar
}

// selectorSet is every ObjC selector this package sends, resolved once and
// carried as one value rather than as package-level variables.
type selectorSet struct {
	newObject                     objc.SEL
	release                       objc.SEL
	drain                         objc.SEL
	setDelegate                   objc.SEL
	setDesiredAccuracy            objc.SEL
	setDistanceFilter             objc.SEL
	requestWhenInUseAuthorization objc.SEL
	startUpdatingLocation         objc.SEL
	stopUpdatingLocation          objc.SEL
	locationServicesEnabled       objc.SEL
	authorizationStatus           objc.SEL
	count                         objc.SEL
	objectAtIndex                 objc.SEL
	removeAllObjects              objc.SEL
	longLongValue                 objc.SEL
	coordinate                    objc.SEL
	horizontalAccuracy            objc.SEL
	altitude                      objc.SEL
	verticalAccuracy              objc.SEL
	speed                         objc.SEL
	course                        objc.SEL
	timestamp                     objc.SEL
	timeIntervalSince1970         objc.SEL
	code                          objc.SEL
	localizedDescription          objc.SEL
	utf8String                    objc.SEL
	currentRunLoop                objc.SEL
	dateWithTimeIntervalSinceNow  objc.SEL
	runUntilDate                  objc.SEL
	didUpdateLocations            objc.SEL
	didFailWithError              objc.SEL
	didChangeAuthorization        objc.SEL
}

// selectors returns the package's selector set. It is a function rather than a
// package-level variable so the package carries no mutable global state; the
// values are identical on every call because objc.RegisterName is a lookup in
// the ObjC runtime's own table, which is where the memoization already lives.
// Callers that send in a hot path hold the returned pointer — Backend does —
// rather than calling this per message.
func selectors() *selectorSet {
	return &selectorSet{
		newObject:                     objc.RegisterName("new"),
		release:                       objc.RegisterName("release"),
		drain:                         objc.RegisterName("drain"),
		setDelegate:                   objc.RegisterName("setDelegate:"),
		setDesiredAccuracy:            objc.RegisterName("setDesiredAccuracy:"),
		setDistanceFilter:             objc.RegisterName("setDistanceFilter:"),
		requestWhenInUseAuthorization: objc.RegisterName("requestWhenInUseAuthorization"),
		startUpdatingLocation:         objc.RegisterName("startUpdatingLocation"),
		stopUpdatingLocation:          objc.RegisterName("stopUpdatingLocation"),
		locationServicesEnabled:       objc.RegisterName("locationServicesEnabled"),
		authorizationStatus:           objc.RegisterName("authorizationStatus"),
		count:                         objc.RegisterName("count"),
		objectAtIndex:                 objc.RegisterName("objectAtIndex:"),
		removeAllObjects:              objc.RegisterName("removeAllObjects"),
		longLongValue:                 objc.RegisterName("longLongValue"),
		coordinate:                    objc.RegisterName("coordinate"),
		horizontalAccuracy:            objc.RegisterName("horizontalAccuracy"),
		altitude:                      objc.RegisterName("altitude"),
		verticalAccuracy:              objc.RegisterName("verticalAccuracy"),
		speed:                         objc.RegisterName("speed"),
		course:                        objc.RegisterName("course"),
		timestamp:                     objc.RegisterName("timestamp"),
		timeIntervalSince1970:         objc.RegisterName("timeIntervalSince1970"),
		code:                          objc.RegisterName("code"),
		localizedDescription:          objc.RegisterName("localizedDescription"),
		utf8String:                    objc.RegisterName("UTF8String"),
		currentRunLoop:                objc.RegisterName("currentRunLoop"),
		dateWithTimeIntervalSinceNow:  objc.RegisterName("dateWithTimeIntervalSinceNow:"),
		runUntilDate:                  objc.RegisterName("runUntilDate:"),
		didUpdateLocations:            objc.RegisterName("locationManager:didUpdateLocations:"),
		didFailWithError:              objc.RegisterName("locationManager:didFailWithError:"),
		didChangeAuthorization:        objc.RegisterName("locationManagerDidChangeAuthorization:"),
	}
}

// callbackSelectorSet is the handful of selectors the delegate callbacks send.
// They resolve their own rather than reading the Backend's cached set, because
// a callback has no Backend to read one from — which is the point of queueing
// on the delegate. objc.RegisterName is a lookup in the runtime's own table,
// and a callback arrives about once a second.
type callbackSelectorSet struct {
	addObject           objc.SEL
	lastObject          objc.SEL
	authorizationStatus objc.SEL
	numberWithLongLong  objc.SEL
}

func callbackSelectors() callbackSelectorSet {
	return callbackSelectorSet{
		addObject:           objc.RegisterName("addObject:"),
		lastObject:          objc.RegisterName("lastObject"),
		authorizationStatus: objc.RegisterName("authorizationStatus"),
		numberWithLongLong:  objc.RegisterName("numberWithLongLong:"),
	}
}

// New loads CoreLocation and prepares a Backend. It does not start the
// provider; Start does.
func New(opts Options, sink provider.Sink) (*Backend, error) {
	err := loadCoreLocation()
	if err != nil {
		return nil, geo.Wrap(platform, "load CoreLocation", err, false)
	}

	class, err := delegateClass()
	if err != nil {
		return nil, geo.Wrap(platform, "register CoreLocation delegate", err, false)
	}

	return &Backend{
		opts:   opts,
		sink:   sink,
		sel:    selectors(),
		class:  class,
		queues: queuesOf(class),
		stopCh: make(chan struct{}),
	}, nil
}

// Platform names this adapter for error annotation.
func (b *Backend) Platform() string { return platform }

// Capabilities reports the optional Fix fields CoreLocation can supply.
func (b *Backend) Capabilities() geo.Capabilities {
	return geo.Capabilities{
		Altitude:         true,
		VerticalAccuracy: true,
		Speed:            true,
		Heading:          true,
		Source:           false,
	}
}

// Start brings the CoreLocation session up on a thread of its own, and returns
// once it is delivering or has failed.
func (b *Backend) Start(ctx context.Context) error {
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

// Stop ends the session. It is idempotent and safe before or after Start.
func (b *Backend) Stop() error {
	b.stopOnce.Do(func() { close(b.stopCh) })
	b.wg.Wait()

	return nil
}

func (b *Backend) run(ready chan<- error) {
	defer b.wg.Done()

	runtime.LockOSThread()

	defer runtime.UnlockOSThread()

	// Setup pool. It holds only the objects created below, and drains once on
	// return — see the loop for why per-iteration objects need their own.
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(b.sel.newObject)
	if pool != 0 {
		defer pool.Send(b.sel.drain)
	}

	manager, delegate, err := b.openSession()
	if err != nil {
		ready <- err

		return
	}

	defer b.closeSession(manager, delegate)

	err = b.startSession(manager, delegate)
	if err != nil {
		ready <- err

		return
	}

	ready <- nil

	b.spin(delegate)
}

// openSession creates the manager and the delegate, and gives the delegate the
// queues its callbacks append to. Both objects are owned by the caller from
// here on, which is why a partial failure releases what it made.
func (b *Backend) openSession() (objc.ID, objc.ID, error) {
	managerClass, err := locationManagerClass(b.sel)
	if err != nil {
		return 0, 0, err
	}

	manager, delegate, err := b.createSessionObjects(managerClass)
	if err != nil {
		return 0, 0, err
	}

	b.mu.Lock()
	b.delegate = delegate
	b.manager = manager
	b.mu.Unlock()

	return manager, delegate, nil
}

// createSessionObjects makes the manager and the delegate, and gives the
// delegate its queues. A partial failure releases what it made, because from
// here on the caller owns both objects.
func (b *Backend) createSessionObjects(managerClass objc.Class) (objc.ID, objc.ID, error) {
	delegate := objc.ID(b.class).Send(b.sel.newObject)

	manager := objc.ID(managerClass).Send(b.sel.newObject)
	if delegate == 0 || manager == 0 || !b.openQueues(delegate) {
		b.closeQueues(delegate)
		b.release(delegate, manager)

		return 0, 0, geo.Wrap(
			platform,
			"create CoreLocation objects",
			geo.ErrServiceUnavailable,
			false,
		)
	}

	return manager, delegate, nil
}

// openQueues gives the delegate the three arrays its callbacks append to, and
// reports whether every one of them was created.
func (b *Backend) openQueues(delegate objc.ID) bool {
	if delegate == 0 {
		return false
	}

	arrayClass := objc.ID(objc.GetClass("NSMutableArray"))

	for _, ivar := range b.queues.all() {
		queue := arrayClass.Send(b.sel.newObject)
		if queue == 0 {
			return false
		}

		delegate.SetIvar(ivar, queue)
	}

	return true
}

// closeQueues releases the delegate's queues and clears them, so that a
// callback arriving after teardown finds nothing to append to and returns.
func (b *Backend) closeQueues(delegate objc.ID) {
	if delegate == 0 {
		return
	}

	for _, ivar := range b.queues.all() {
		queue := delegate.GetIvar(ivar)

		delegate.SetIvar(ivar, objc.ID(0))

		if queue != 0 {
			queue.Send(b.sel.release)
		}
	}
}

// release drops every non-nil object handed to it.
func (b *Backend) release(ids ...objc.ID) {
	for _, id := range ids {
		if id != 0 {
			id.Send(b.sel.release)
		}
	}
}

// closeSession unwinds openSession, and takes the delegate's queues away before
// the objects go away so a late callback finds nothing rather than freed memory.
func (b *Backend) closeSession(manager, delegate objc.ID) {
	manager.Send(b.sel.stopUpdatingLocation)
	manager.Send(b.sel.setDelegate, objc.ID(0))
	b.closeQueues(delegate)
	manager.Send(b.sel.release)
	delegate.Send(b.sel.release)

	b.mu.Lock()
	b.manager = 0
	b.delegate = 0
	b.mu.Unlock()
}

// startSession configures the manager, settles the permission question, and
// starts delivery.
func (b *Backend) startSession(manager, delegate objc.ID) error {
	manager.Send(b.sel.setDelegate, delegate)
	manager.Send(b.sel.setDesiredAccuracy, coreLocationAccuracy(b.opts))
	manager.Send(b.sel.setDistanceFilter, distanceFilter(b.opts))

	authorization := objc.Send[int64](manager, b.sel.authorizationStatus)
	b.publishAuthorization(authorization)

	if authorization == 0 {
		err := b.requestAuthorization(manager)
		if err != nil {
			return err
		}
	}

	manager.Send(b.sel.startUpdatingLocation)

	return nil
}

// requestAuthorization puts the system prompt on screen, unless the caller
// asked never to be prompted — in which case the run cannot proceed.
func (b *Backend) requestAuthorization(manager objc.ID) error {
	if b.opts.Permission == provider.PermissionDoNotRequest {
		return geo.Wrap(platform, "request permission", geo.ErrPermissionNeeded, false)
	}

	b.sink.PublishStatus(
		geo.Status{
			State:      geo.StateStarting,
			Permission: geo.PermissionPromptRequired,
			Message:    "requesting macOS location access",
		},
	)
	manager.Send(b.sel.requestWhenInUseAuthorization)

	return nil
}

// spin turns the run loop until Stop closes stopCh, publishing whatever the
// delegate queued at the end of every turn. It must stay on the thread run
// locked, because that is the thread the delegate callbacks arrive on.
func (b *Backend) spin(delegate objc.ID) {
	runLoop := objc.ID(objc.GetClass("NSRunLoop")).Send(b.sel.currentRunLoop)
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
		// is never.
		iterationPool := poolClass.Send(b.sel.newObject)
		until := dateClass.Send(b.sel.dateWithTimeIntervalSinceNow, float64(runLoopTurn))
		runLoop.Send(b.sel.runUntilDate, until)
		// Publishing comes before the pool drains, and copies every value it
		// needs into Go memory, so nothing queued is read after it is freed.
		b.publishQueued(delegate)

		if iterationPool != 0 {
			iterationPool.Send(b.sel.drain)
		}
	}
}

// publishQueued hands the sink everything the delegate collected during the
// run-loop turn that just ended, and empties the queues. It runs on the thread
// the callbacks arrive on, so it never races them.
func (b *Backend) publishQueued(delegate objc.ID) {
	b.publishLocations(delegate)
	b.publishFailures(delegate)
	b.publishAuthorizations(delegate)
}

// publishLocations drains the queued CLLocations.
func (b *Backend) publishLocations(delegate objc.ID) {
	queue := b.queueOf(delegate, b.queues.locations)
	if queue == 0 {
		return
	}

	for index := range objc.Send[int](queue, b.sel.count) {
		b.publishLocation(queue.Send(b.sel.objectAtIndex, index))
	}

	queue.Send(b.sel.removeAllObjects)
}

// publishFailures drains the queued NSErrors.
func (b *Backend) publishFailures(delegate objc.ID) {
	queue := b.queueOf(delegate, b.queues.failures)
	if queue == 0 {
		return
	}

	for index := range objc.Send[int](queue, b.sel.count) {
		b.publishFailure(queue.Send(b.sel.objectAtIndex, index))
	}

	queue.Send(b.sel.removeAllObjects)
}

// publishAuthorizations drains the queued CLAuthorizationStatus values.
func (b *Backend) publishAuthorizations(delegate objc.ID) {
	queue := b.queueOf(delegate, b.queues.authorizations)
	if queue == 0 {
		return
	}

	for index := range objc.Send[int](queue, b.sel.count) {
		boxed := queue.Send(b.sel.objectAtIndex, index)
		b.publishAuthorization(objc.Send[int64](boxed, b.sel.longLongValue))
	}

	queue.Send(b.sel.removeAllObjects)
}

// queueOf reads one of the delegate's queues. A zero result means the delegate
// has none — it was torn down, or never opened — and there is nothing to drain.
func (b *Backend) queueOf(delegate objc.ID, ivar objc.Ivar) objc.ID {
	if delegate == 0 || ivar == 0 {
		return 0
	}

	return delegate.GetIvar(ivar)
}

// publishLocation maps one queued CLLocation onto a Fix and publishes it.
func (b *Backend) publishLocation(location objc.ID) {
	if location == 0 {
		return
	}

	coordinate := objc.Send[clCoordinate](location, b.sel.coordinate)

	fix := geo.Fix{
		Timestamp:      fixTimestamp(location, b.sel),
		ReceivedAt:     time.Now().UTC(),
		Latitude:       coordinate.Latitude,
		Longitude:      coordinate.Longitude,
		AccuracyMeters: objc.Send[float64](location, b.sel.horizontalAccuracy),
		Source:         geo.SourceSystem,
	}

	b.sink.PublishFix(withOptionalFields(fix, location, b.sel))
}

// publishFailure maps one queued NSError onto the sentinel a caller switches
// on, carrying the native description a human reads.
func (b *Backend) publishFailure(nativeError objc.ID) {
	if nativeError == 0 {
		return
	}

	code := objc.Send[int64](nativeError, b.sel.code)
	description := errorDescription(nativeError, b.sel)

	// The framework hands back a localized sentence, not a value we can match
	// on, so it rides along behind a sentinel that callers can.
	native := fmt.Errorf("%w: %s", errCoreLocation, description)

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
		b.publishDenial(description, native)
	default:
		b.sink.PublishError(geo.Wrap(platform, "location update", native, true))
	}
}

// publishDenial reports kCLErrorDenied, which has to move the reported
// permission as well as raise an error: a run that keeps saying "starting"
// after a denial never terminates from the caller's point of view.
func (b *Backend) publishDenial(description string, native error) {
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
}

// publishAuthorization reports what a CLAuthorizationStatus means. A status
// the caller cannot act on is an error as well as a status: it is what tells
// a locator waiting on a first fix to stop waiting.
func (b *Backend) publishAuthorization(status int64) {
	meaning := authorizationMeaning(status)

	b.sink.PublishStatus(meaning)

	if meaning.Permission == geo.PermissionDenied ||
		meaning.Permission == geo.PermissionRestricted {
		b.sink.PublishError(geo.Wrap(platform, "authorization", geo.ErrPermissionDenied, false))
	}
}

// all returns the queues in a fixed order, for the operations that treat them
// alike: opening them, and taking them away again.
func (q delegateQueues) all() []objc.Ivar {
	return []objc.Ivar{q.locations, q.failures, q.authorizations}
}

// queuesOf locates the delegate class's instance variables once, so that
// nothing on the delivery path has to look them up by name.
func queuesOf(class objc.Class) delegateQueues {
	return delegateQueues{
		locations:      class.InstanceVariable(ivarLocations),
		failures:       class.InstanceVariable(ivarFailures),
		authorizations: class.InstanceVariable(ivarAuthorizations),
	}
}

// locationManagerClass finds CLLocationManager and rejects the two conditions
// that make a session pointless before it is built: no such class, and location
// services switched off machine-wide.
func locationManagerClass(sel *selectorSet) (objc.Class, error) {
	managerClass := objc.GetClass("CLLocationManager")
	if managerClass == 0 {
		return 0, geo.Wrap(
			platform,
			"find CLLocationManager",
			geo.ErrServiceUnavailable,
			false,
		)
	}

	if !objc.Send[bool](objc.ID(managerClass), sel.locationServicesEnabled) {
		return 0, geo.Wrap(platform, "check location services", geo.ErrServiceDisabled, false)
	}

	return managerClass, nil
}

// distanceFilter maps the configured minimum distance onto CoreLocation's
// filter, where a negative value is kCLDistanceFilterNone.
func distanceFilter(opts Options) float64 {
	if opts.MinimumDistanceMeters > 0 {
		return opts.MinimumDistanceMeters
	}

	return -1
}

// authorizationMeaning maps every CLAuthorizationStatus the OS defines, plus
// the ones it does not yet, onto a status. The table is built per call rather
// than kept in a package variable, which is a lookup either way.
func authorizationMeaning(status int64) geo.Status {
	known := map[int64]geo.Status{
		0: {
			State:      geo.StateStarting,
			Permission: geo.PermissionPromptRequired,
			Message:    "macOS location permission not determined",
		},
		1: {
			State:      geo.StateDisabled,
			Permission: geo.PermissionRestricted,
			Message:    "macOS location permission restricted",
		},
		2: {
			State:      geo.StateDisabled,
			Permission: geo.PermissionDenied,
			Message:    "macOS location permission denied",
		},
		3: {
			State:      geo.StateStarting,
			Permission: geo.PermissionGranted,
			Message:    "macOS location access granted",
		},
		4: {
			State:      geo.StateStarting,
			Permission: geo.PermissionGranted,
			Message:    "macOS location access granted",
		},
	}

	meaning, ok := known[status]
	if !ok {
		return geo.Status{
			State:      geo.StateUnavailable,
			Permission: geo.PermissionUnknown,
			Message:    fmt.Sprintf("unknown macOS authorization status %d", status),
		}
	}

	return meaning
}

// loadCoreLocation brings the two frameworks into the process. dlopen is
// idempotent and refcounted by the dynamic linker, so every call after the
// first is a lookup — which is why this needs no memoization of its own, and
// therefore no package-level state to keep one in.
func loadCoreLocation() error {
	frameworks := []string{
		"/System/Library/Frameworks/Foundation.framework/Foundation",
		"/System/Library/Frameworks/CoreLocation.framework/CoreLocation",
	}

	for _, framework := range frameworks {
		_, err := purego.Dlopen(framework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return fmt.Errorf("dlopen %s: %w", framework, err)
		}
	}

	return nil
}

// delegateClass returns the delegate class, registering it the first time. The
// ObjC runtime's class table is the memo: a class an earlier Backend registered
// is found here by name, which is why this needs no sync.Once and no package
// variable to cache the result in.
func delegateClass() (objc.Class, error) {
	if existing := objc.GetClass(delegateClassName); existing != 0 {
		return existing, nil
	}

	protocols := []*objc.Protocol{}
	if protocol := objc.GetProtocol("CLLocationManagerDelegate"); protocol != nil {
		protocols = append(protocols, protocol)
	}

	sel := selectors()

	class, err := objc.RegisterClass(
		delegateClassName,
		objc.GetClass("NSObject"),
		protocols,
		queueFields(),
		[]objc.MethodDef{
			{Cmd: sel.didUpdateLocations, Fn: coreLocationDidUpdateLocations},
			{Cmd: sel.didFailWithError, Fn: coreLocationDidFail},
			{Cmd: sel.didChangeAuthorization, Fn: coreLocationDidChangeAuthorization},
		},
	)
	if err != nil {
		// Two Backends racing to be the first is the only way to get here, and
		// the loser wants the winner's class rather than a failed Open.
		if existing := objc.GetClass(delegateClassName); existing != 0 {
			return existing, nil
		}

		return 0, fmt.Errorf("register %s: %w", delegateClassName, err)
	}

	return class, nil
}

// queueFields declares the delegate's instance variables. They are ReadOnly
// because the generated getter is the cheapest attribute the runtime will
// accept — this package reads and writes them through GetIvar and SetIvar.
func queueFields() []objc.FieldDef {
	names := []string{ivarLocations, ivarFailures, ivarAuthorizations}

	fields := make([]objc.FieldDef, 0, len(names))
	for _, name := range names {
		fields = append(fields, objc.FieldDef{
			Name:      name,
			Type:      reflect.TypeFor[objc.ID](),
			Attribute: objc.ReadOnly,
		})
	}

	return fields
}

// coreLocationDidUpdateLocations queues the newest location for the run loop to
// publish. It runs on a native thread and reaches no Go state at all: the queue
// belongs to the delegate, so there is nothing here to look up or to lock, and
// a panic on this thread would take the host process down with it.
func coreLocationDidUpdateLocations(self objc.ID, _ objc.SEL, _ objc.ID, locations objc.ID) {
	sel := callbackSelectors()

	enqueue(self, ivarLocations, locations.Send(sel.lastObject), sel)
}

// coreLocationDidFail queues a native failure for the run loop to map.
func coreLocationDidFail(self objc.ID, _ objc.SEL, _ objc.ID, nativeError objc.ID) {
	enqueue(self, ivarFailures, nativeError, callbackSelectors())
}

// coreLocationDidChangeAuthorization queues the manager's new authorization
// status. The value is boxed here rather than read at drain time because the
// manager may have moved on by then.
func coreLocationDidChangeAuthorization(self objc.ID, _ objc.SEL, manager objc.ID) {
	sel := callbackSelectors()

	status := objc.Send[int64](manager, sel.authorizationStatus)
	boxed := objc.ID(objc.GetClass("NSNumber")).Send(sel.numberWithLongLong, status)

	enqueue(self, ivarAuthorizations, boxed, sel)
}

// enqueue appends value to one of the delegate's queues. A delegate that has no
// queue — because it was never opened, or has been torn down — drops the value,
// which is what makes a callback arriving outside a session harmless.
func enqueue(self objc.ID, name string, value objc.ID, sel callbackSelectorSet) {
	if self == 0 || value == 0 {
		return
	}

	queue := queueNamed(self, name)
	if queue == 0 {
		return
	}

	queue.Send(sel.addObject, value)
}

// queueNamed locates one of the delegate's queues by name. It is the callback
// path's counterpart to Backend.queueOf, which reads an Ivar resolved once in
// New — a callback has no Backend to have resolved one.
func queueNamed(self objc.ID, name string) objc.ID {
	class := objc.GetClass(delegateClassName)
	if class == 0 {
		return 0
	}

	ivar := class.InstanceVariable(name)
	if ivar == 0 {
		return 0
	}

	return self.GetIvar(ivar)
}

// fixTimestamp reads the provider's own stamp, falling back to now for a
// CLLocation that carries none.
func fixTimestamp(location objc.ID, sel *selectorSet) time.Time {
	object := location.Send(sel.timestamp)
	if object == 0 {
		return time.Now().UTC()
	}

	seconds := objc.Send[float64](object, sel.timeIntervalSince1970)

	return time.Unix(0, int64(seconds*float64(time.Second))).UTC()
}

// withOptionalFields copies the fields CoreLocation reports as absent by
// giving them a negative value, and records in Fields which ones were real.
// Copying unconditionally would report an altitude of 0 m and a heading of due
// north for every sample taken indoors.
func withOptionalFields(fix geo.Fix, location objc.ID, sel *selectorSet) geo.Fix {
	verticalAccuracy := objc.Send[float64](location, sel.verticalAccuracy)
	if verticalAccuracy >= 0 {
		fix.AltitudeMeters = objc.Send[float64](location, sel.altitude)
		fix.VerticalAccuracyMeters = verticalAccuracy
		fix.Fields |= geo.FieldAltitude | geo.FieldVerticalAccuracy
	}

	speed := objc.Send[float64](location, sel.speed)
	if speed >= 0 {
		fix.SpeedMetersPerSecond = speed
		fix.Fields |= geo.FieldSpeed
	}

	course := objc.Send[float64](location, sel.course)
	if course >= 0 {
		fix.HeadingDegrees = course
		fix.Fields |= geo.FieldHeading
	}

	return fix
}

// errorDescription reads NSError's localized description, which is the only
// human-readable half of a CoreLocation failure.
func errorDescription(nativeError objc.ID, sel *selectorSet) string {
	object := nativeError.Send(sel.localizedDescription)
	if object == 0 {
		return "CoreLocation error"
	}

	return objc.Send[string](object, sel.utf8String)
}

// coreLocationAccuracy maps the requested preference onto a
// kCLLocationAccuracy sentinel. The negative ones are the *most* accurate
// settings, not disabled ones, so transposing them would silently downgrade
// every fix. Anything unrecognised falls back to the balanced setting.
func coreLocationAccuracy(opts Options) float64 {
	const hundredMeters = 100 // kCLLocationAccuracyHundredMeters

	if opts.DesiredAccuracyMeters > 0 {
		return float64(opts.DesiredAccuracyMeters)
	}

	sentinels := map[provider.Accuracy]float64{
		provider.AccuracyNavigation: -2, // kCLLocationAccuracyBestForNavigation
		provider.AccuracyHigh:       -1, // kCLLocationAccuracyBest
		provider.AccuracyBalanced:   hundredMeters,
	}

	sentinel, ok := sentinels[opts.Accuracy]
	if !ok {
		return hundredMeters
	}

	return sentinel
}
