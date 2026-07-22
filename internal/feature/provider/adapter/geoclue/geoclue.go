//go:build linux

// Package geoclue adapts the freedesktop GeoClue2 D-Bus service to
// provider.Provider.
package geoclue

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/mostafakhairy0305-dot/golocation/geo"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
	"github.com/mostafakhairy0305-dot/golocation/internal/shared/retry"
)

const platform = "linux"

const (
	geoClueService       = "org.freedesktop.GeoClue2"
	geoClueManagerPath   = dbus.ObjectPath("/org/freedesktop/GeoClue2/Manager")
	geoClueManagerIFace  = "org.freedesktop.GeoClue2.Manager"
	geoClueClientIFace   = "org.freedesktop.GeoClue2.Client"
	geoClueLocationIFace = "org.freedesktop.GeoClue2.Location"
	propertiesIFace      = "org.freedesktop.DBus.Properties"
	peerIFace            = "org.freedesktop.DBus.Peer"
)

// GeoClue takes an accuracy *level*, not metres. These are the levels it
// defines; the negative-sounding ones do not exist, a smaller number is simply
// a coarser answer.
const (
	accuracyCity         uint32 = 4
	accuracyNeighborhood uint32 = 5
	accuracyStreet       uint32 = 6
	accuracyExact        uint32 = 8
)

// The metre boundaries a requested accuracy is bucketed into. They are the
// widths the GeoClue levels above are documented to mean.
const (
	exactMeters        uint32 = 100
	streetMeters       uint32 = 1000
	neighborhoodMeters uint32 = 10000
)

// signalBuffer is how many LocationUpdated signals may queue while the session
// is busy reading the last one. Deep enough that a slow property read does not
// make libdbus drop the connection, shallow enough that a stalled session does
// not accumulate stale positions.
const signalBuffer = 32

// pingInterval is how often the session pokes the client object. A D-Bus
// connection that has died reports nothing on its own, so this is the only
// thing that turns a silent connection into a reconnect.
const pingInterval = 30 * time.Second

// defaultBackoff stands in for a reconnect interval that was left at zero, so
// that a misconfigured policy cannot turn the reconnect loop into a busy loop
// against the bus.
const defaultBackoff = time.Second

// backoffFactor is the exponential growth of the reconnect interval.
const backoffFactor = 2

// A GeoClue timestamp is a (seconds, microseconds) pair, and the LocationUpdated
// body is an (old, new) pair. Both are read positionally, so a shorter one is
// not something to decode.
const pairLength = 2

// altitudeFloor separates a real altitude from the -DBL_MAX GeoClue reports for
// one it does not have. Nothing on Earth is below half of DBL_MAX.
const altitudeFloor = -math.MaxFloat64 / 2

// presentFloor is the boundary for the fields GeoClue reports as absent by
// giving them a negative value.
const presentFloor = 0

// Options are the GeoClue-specific knobs, including the reconnect policy and
// the desktop ID GeoClue uses to look up this application's permissions.
// An unset knob means "leave the GeoClue default alone".
type Options struct {
	Accuracy              provider.Accuracy `exhaustruct:"optional"`
	DesiredAccuracyMeters uint32            `exhaustruct:"optional"`
	MinimumInterval       time.Duration     `exhaustruct:"optional"`
	MinimumDistanceMeters float64           `exhaustruct:"optional"`

	DesktopID    string        `exhaustruct:"optional"`
	Reconnect    bool          `exhaustruct:"optional"`
	ReconnectMin time.Duration `exhaustruct:"optional"`
	ReconnectMax time.Duration `exhaustruct:"optional"`
}

// Backend is the GeoClue provider.Provider. New builds one; the core drives it
// through the Provider methods.
type Backend struct {
	opts Options `exhaustruct:"optional"`
	sink provider.Sink

	// Established by Start. The run's context is not held here — it is passed
	// down the call chain — so only the handle Stop needs is.
	cancel context.CancelFunc `exhaustruct:"optional"`
	wg     sync.WaitGroup     `exhaustruct:"optional"`

	mu         sync.Mutex      `exhaustruct:"optional"`
	conn       *dbus.Conn      `exhaustruct:"optional"`
	clientPath dbus.ObjectPath `exhaustruct:"optional"`
}

// session is one live GeoClue client: the bus connection, the client object it
// was handed, and the signals that client emits.
type session struct {
	backend    *Backend
	conn       *dbus.Conn
	clientPath dbus.ObjectPath
	signals    chan *dbus.Signal
}

// New prepares a Backend. It does not start the provider; Start does.
func New(opts Options, sink provider.Sink) (*Backend, error) {
	return &Backend{opts: opts, sink: sink}, nil
}

var _ provider.Provider = (*Backend)(nil)

// Platform names this adapter for error annotation.
func (b *Backend) Platform() string { return platform }

// Capabilities reports the optional Fix fields GeoClue can supply.
func (b *Backend) Capabilities() geo.Capabilities {
	return geo.Capabilities{
		Altitude:         true,
		VerticalAccuracy: false,
		Speed:            true,
		Heading:          true,
		Source:           false,
	}
}

// Start brings the GeoClue session up and returns once it is delivering or has
// failed. startCtx bounds start-up only; the session outlives it.
func (b *Backend) Start(startCtx context.Context) error {
	// The session outlives startCtx, so it inherits that context's values
	// without inheriting its deadline.
	ctx, cancel := context.WithCancel(context.WithoutCancel(startCtx))
	b.cancel = cancel

	ready := make(chan error, 1)

	b.wg.Add(1)

	go b.run(ctx, ready)

	select {
	case err := <-ready:
		return err
	case <-startCtx.Done():
		cancel()
		b.wg.Wait()

		return geo.Wrap(platform, "start GeoClue", startCtx.Err(), true)
	}
}

// Stop ends the session. It is idempotent and safe before or after Start.
func (b *Backend) Stop() error {
	if b.cancel != nil {
		b.cancel()
	}

	b.mu.Lock()
	conn := b.conn
	clientPath := b.clientPath
	b.mu.Unlock()

	if conn != nil && clientPath.IsValid() {
		_ = conn.Object(geoClueService, clientPath).Call(geoClueClientIFace+".Stop", 0).Err
	}

	b.wg.Wait()

	return nil
}

// run owns the whole life of the provider: the first connection, whose outcome
// is what Start returns, and then delivery for as long as the policy allows.
func (b *Backend) run(ctx context.Context, ready chan<- error) {
	defer b.wg.Done()

	current, err := b.dialUntilReady(ctx)

	ready <- err

	if err != nil {
		return
	}

	b.deliver(ctx, current)
}

// dialUntilReady connects, retrying under exponential backoff until it
// succeeds, the policy gives up, or the run is stopped. Only failures the geo
// layer marked temporary are retried, so a permission denial ends it straight
// away: no amount of retrying changes a decision only the user can make. When
// reconnection is disabled the connect is attempted exactly once.
func (b *Backend) dialUntilReady(ctx context.Context) (*session, error) {
	opts := append(
		b.reconnectBackoff(),
		retry.WithNotify(func(error, time.Duration) {
			b.publishReconnecting("waiting for GeoClue")
		}),
	)
	if !b.opts.Reconnect {
		opts = append(opts, retry.WithMaxTries(1))
	}

	return retry.Do(ctx, func() (*session, error) {
		return b.connect(ctx)
	}, opts...)
}

// deliver runs the session, and rebuilds it for as long as the reconnect policy
// allows. It returns once the run is stopped or the policy gives up.
func (b *Backend) deliver(ctx context.Context, current *session) {
	for current != nil {
		err := current.run(ctx)

		current.close()

		if ctx.Err() != nil {
			return
		}

		b.sink.PublishError(geo.Wrap(platform, "GeoClue connection", err, true))

		if !b.opts.Reconnect {
			b.publishLost()

			return
		}

		b.publishReconnecting("reconnecting to GeoClue")

		current = b.redial(ctx)
	}
}

// redial rebuilds the session after a connection is lost, retrying under
// exponential backoff and publishing each failed attempt so subscribers see the
// reconnect in progress. A nil result means the run is over: stopped, or denied
// a permission it will not be granted by asking again.
func (b *Backend) redial(ctx context.Context) *session {
	opts := append(
		b.reconnectBackoff(),
		retry.WithNotify(func(err error, _ time.Duration) {
			b.sink.PublishError(err)
		}),
	)

	current, err := retry.Do(ctx, func() (*session, error) {
		return b.connect(ctx)
	}, opts...)
	if err != nil {
		// A cancelled run is a stop, not a failure to report.
		if ctx.Err() != nil {
			return nil
		}

		if errors.Is(err, geo.ErrPermissionDenied) {
			b.publishDenied()
		}

		b.sink.PublishError(err)

		return nil
	}

	return current
}

// reconnectBackoff builds the exponential policy shared by both reconnect
// paths: a jittered curve between ReconnectMin and ReconnectMax, growing by
// backoffFactor and bounded only by the run's context. The floors guard a
// misconfigured policy from turning the loop into a busy loop against the bus.
func (b *Backend) reconnectBackoff() []retry.Option {
	minInterval := b.opts.ReconnectMin
	if minInterval <= 0 {
		minInterval = defaultBackoff
	}

	maxInterval := max(b.opts.ReconnectMax, minInterval)

	return []retry.Option{
		retry.WithInitialInterval(minInterval),
		retry.WithMaxInterval(maxInterval),
		retry.WithMultiplier(backoffFactor),
		retry.WithMaxElapsedTime(0),
	}
}

// connect builds a live session: a private bus connection, a configured GeoClue
// client, and a subscription to the signals it emits.
func (b *Backend) connect(ctx context.Context) (*session, error) {
	conn, err := openSystemBus()
	if err != nil {
		return nil, err
	}

	current, err := b.openSession(ctx, conn)
	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	b.adopt(current)
	b.publishStarted()
	current.publishInitialFix(ctx)

	return current, nil
}

// openSystemBus opens a private connection, which unlike the shared one can be
// closed when this session ends without affecting anything else in the process.
func openSystemBus() (*dbus.Conn, error) {
	conn, err := dbus.SystemBusPrivate()
	if err != nil {
		return nil, geo.Wrap(
			platform,
			"connect system bus",
			errors.Join(geo.ErrServiceUnavailable, err),
			true,
		)
	}

	err = conn.Auth(nil)
	if err != nil {
		_ = conn.Close()

		return nil, geo.Wrap(platform, "authenticate system bus", err, false)
	}

	err = conn.Hello()
	if err != nil {
		_ = conn.Close()

		return nil, geo.Wrap(platform, "hello system bus", err, false)
	}

	return conn, nil
}

// openSession asks the manager for a client, configures it, and subscribes.
func (b *Backend) openSession(ctx context.Context, conn *dbus.Conn) (*session, error) {
	clientPath, err := newClient(ctx, conn)
	if err != nil {
		return nil, err
	}

	client := conn.Object(geoClueService, clientPath)

	err = b.configure(ctx, client)
	if err != nil {
		return nil, err
	}

	signals, err := subscribe(ctx, conn, client, clientPath)
	if err != nil {
		return nil, err
	}

	return &session{backend: b, conn: conn, clientPath: clientPath, signals: signals}, nil
}

// newClient asks the manager for this connection's client object.
func newClient(ctx context.Context, conn *dbus.Conn) (dbus.ObjectPath, error) {
	manager := conn.Object(geoClueService, geoClueManagerPath)

	var clientPath dbus.ObjectPath

	err := manager.CallWithContext(ctx, geoClueManagerIFace+".GetClient", 0).Store(&clientPath)
	if err != nil {
		return "", mapGeoClueError("get client", err)
	}

	if !clientPath.IsValid() {
		return "", geo.Wrap(platform, "get client", geo.ErrServiceUnavailable, true)
	}

	return clientPath, nil
}

// configure applies the caller's knobs to the client. The desktop ID comes
// first because it is what GeoClue looks this application's permissions up by.
func (b *Backend) configure(ctx context.Context, client dbus.BusObject) error {
	err := setClientProperty(ctx, client, "DesktopId", b.opts.DesktopID)
	if err != nil {
		return mapGeoClueError("set desktop ID", err)
	}

	err = setClientProperty(ctx, client, "RequestedAccuracyLevel", geoClueAccuracy(b.opts))
	if err != nil {
		return mapGeoClueError("set accuracy", err)
	}

	return b.configureThresholds(ctx, client)
}

// configureThresholds applies the rate limits. GeoClue takes both as whole
// units, and leaving one at zero means "no limit", so anything the caller asked
// for rounds up rather than down.
func (b *Backend) configureThresholds(ctx context.Context, client dbus.BusObject) error {
	if b.opts.MinimumDistanceMeters > 0 {
		distance := distanceMeters(b.opts.MinimumDistanceMeters)

		err := setClientProperty(ctx, client, "DistanceThreshold", distance)
		if err != nil {
			return mapGeoClueError("set distance threshold", err)
		}
	}

	if b.opts.MinimumInterval <= 0 {
		return nil
	}

	seconds := intervalSeconds(b.opts.MinimumInterval)

	err := setClientProperty(ctx, client, "TimeThreshold", seconds)
	if err != nil {
		return mapGeoClueError("set time threshold", err)
	}

	return nil
}

// subscribe starts the client and routes its LocationUpdated signals to a
// channel of this session's own. The match rule goes on before Start, so a fix
// GeoClue has ready is not emitted into a connection nobody is listening to.
func subscribe(
	ctx context.Context,
	conn *dbus.Conn,
	client dbus.BusObject,
	clientPath dbus.ObjectPath,
) (chan *dbus.Signal, error) {
	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)

	err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(clientPath),
		dbus.WithMatchInterface(geoClueClientIFace),
		dbus.WithMatchMember("LocationUpdated"),
	)
	if err != nil {
		conn.RemoveSignal(signals)

		return nil, geo.Wrap(platform, "subscribe GeoClue", err, true)
	}

	err = client.CallWithContext(ctx, geoClueClientIFace+".Start", 0).Err
	if err != nil {
		conn.RemoveSignal(signals)

		return nil, mapGeoClueError("start client", err)
	}

	return signals, nil
}

// adopt records the live connection, which is what lets Stop tell GeoClue to
// stop from whatever goroutine calls it.
func (b *Backend) adopt(current *session) {
	b.mu.Lock()
	b.conn = current.conn
	b.clientPath = current.clientPath
	b.mu.Unlock()
}

func (b *Backend) publishStarted() {
	b.sink.PublishStatus(
		geo.Status{
			State:      geo.StateStarting,
			Permission: geo.PermissionGranted,
			Message:    "GeoClue started",
		},
	)
}

func (b *Backend) publishReconnecting(message string) {
	b.sink.PublishStatus(
		geo.Status{
			State:      geo.StateReconnecting,
			Permission: geo.PermissionUnknown,
			Message:    message,
		},
	)
}

func (b *Backend) publishLost() {
	b.sink.PublishStatus(
		geo.Status{
			State:      geo.StateUnavailable,
			Permission: geo.PermissionUnknown,
			Message:    "GeoClue connection lost",
		},
	)
}

func (b *Backend) publishDenied() {
	b.sink.PublishStatus(
		geo.Status{
			State:      geo.StateDisabled,
			Permission: geo.PermissionDenied,
			Message:    "GeoClue access denied",
		},
	)
}

// run delivers fixes until the session ends. The error it returns is why.
func (s *session) run(ctx context.Context) error {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		err := s.step(ctx, ping.C)
		if err != nil {
			return err
		}
	}
}

// step waits for the next thing to happen on the session: a location, a
// heartbeat, or the run ending. A nil error means the session continues.
func (s *session) step(ctx context.Context, ping <-chan time.Time) error {
	select {
	case <-ctx.Done():
		return geo.Wrap(platform, "GeoClue session", ctx.Err(), true)
	case signal, ok := <-s.signals:
		if !ok {
			return geo.ErrServiceUnavailable
		}

		s.publishSignal(ctx, signal)

		return nil
	case <-ping:
		return s.ping(ctx)
	}
}

// ping asks the peer whether the connection is still there. A dead connection
// reports nothing on its own, so without this a silent bus would look like a
// place where nobody is moving.
func (s *session) ping(ctx context.Context) error {
	peer := s.conn.Object(geoClueService, s.clientPath)

	err := peer.CallWithContext(ctx, peerIFace+".Ping", 0).Err
	if err != nil {
		return geo.Wrap(platform, "ping GeoClue", err, true)
	}

	return nil
}

// publishSignal turns a LocationUpdated into a fix. Anything else that arrives
// on the connection is not this session's business, and is dropped.
func (s *session) publishSignal(ctx context.Context, signal *dbus.Signal) {
	path, ok := updatedLocation(signal)
	if !ok {
		return
	}

	fix, err := s.readLocation(ctx, path)
	if err != nil {
		s.backend.sink.PublishError(err)

		return
	}

	s.backend.sink.PublishFix(fix)
}

// publishInitialFix delivers the position GeoClue already had, so that a caller
// waiting on a first fix does not have to wait for the next movement. There
// often is none, and that is not a failure.
func (s *session) publishInitialFix(ctx context.Context) {
	client := s.conn.Object(geoClueService, s.clientPath)

	variant, err := client.GetProperty(geoClueClientIFace + ".Location")
	if err != nil {
		return
	}

	path, ok := locationPath(variant.Value())
	if !ok {
		return
	}

	fix, err := s.readLocation(ctx, path)
	if err != nil {
		return
	}

	s.backend.sink.PublishFix(fix)
}

// readLocation reads one GeoClue Location object.
func (s *session) readLocation(ctx context.Context, path dbus.ObjectPath) (geo.Fix, error) {
	var props map[string]dbus.Variant

	object := s.conn.Object(geoClueService, path)

	err := object.CallWithContext(ctx, propertiesIFace+".GetAll", 0, geoClueLocationIFace).
		Store(&props)
	if err != nil {
		return geo.Fix{}, mapGeoClueError("read location", err)
	}

	return locationFix(props)
}

// close ends the session and gives the connection back. It is what makes a
// reconnect start from nothing rather than from half a dead session.
func (s *session) close() {
	_ = s.conn.Object(geoClueService, s.clientPath).Call(geoClueClientIFace+".Stop", 0).Err
	s.conn.RemoveSignal(s.signals)
	_ = s.conn.Close()

	s.backend.mu.Lock()

	if s.backend.conn == s.conn {
		s.backend.conn = nil
		s.backend.clientPath = ""
	}

	s.backend.mu.Unlock()
}

// updatedLocation reads the new location's path out of a LocationUpdated
// signal, whose body is an (old, new) pair.
func updatedLocation(signal *dbus.Signal) (dbus.ObjectPath, bool) {
	if signal == nil || signal.Name != geoClueClientIFace+".LocationUpdated" ||
		len(signal.Body) < pairLength {
		return "", false
	}

	return locationPath(signal.Body[1])
}

// locationPath reads a D-Bus object path, rejecting the "/" GeoClue uses to
// mean "no location yet".
func locationPath(value any) (dbus.ObjectPath, bool) {
	path, ok := value.(dbus.ObjectPath)
	if !ok || !path.IsValid() || path == "/" {
		return "", false
	}

	return path, true
}

// locationFix maps the Location properties onto a Fix. A reply missing any of
// the three required ones is unusable: the zero values would otherwise reach
// geo.Validate far from the reply that produced them.
func locationFix(props map[string]dbus.Variant) (geo.Fix, error) {
	latitude, okLatitude := variantFloat64(props["Latitude"])
	longitude, okLongitude := variantFloat64(props["Longitude"])
	accuracy, okAccuracy := variantFloat64(props["Accuracy"])

	if !okLatitude || !okLongitude || !okAccuracy {
		return geo.Fix{}, geo.Wrap(platform, "decode location", geo.ErrPositionUnavailable, true)
	}

	fix := geo.Fix{
		Timestamp:      geoClueTimestamp(props["Timestamp"]),
		ReceivedAt:     time.Now().UTC(),
		Latitude:       latitude,
		Longitude:      longitude,
		AccuracyMeters: accuracy,
		Source:         geo.SourceSystem,
	}

	return withOptionalFields(fix, props), nil
}

// withOptionalFields copies the fields GeoClue reports as absent by giving them
// an out-of-range value, and records in Fields which ones were real. Copying
// them unconditionally would report an altitude of -1.8e308 m and a heading of
// due north for every sample taken indoors.
func withOptionalFields(fix geo.Fix, props map[string]dbus.Variant) geo.Fix {
	altitude, ok := optionalFloat(props["Altitude"], altitudeFloor)
	if ok {
		fix.AltitudeMeters = altitude
		fix.Fields |= geo.FieldAltitude
	}

	speed, ok := optionalFloat(props["Speed"], presentFloor)
	if ok {
		fix.SpeedMetersPerSecond = speed
		fix.Fields |= geo.FieldSpeed
	}

	heading, ok := optionalFloat(props["Heading"], presentFloor)
	if ok {
		fix.HeadingDegrees = heading
		fix.Fields |= geo.FieldHeading
	}

	return fix
}

// optionalFloat reads a property that is only present when it is at or above
// floor.
func optionalFloat(variant dbus.Variant, floor float64) (float64, bool) {
	value, ok := variantFloat64(variant)

	return value, ok && value >= floor
}

// setClientProperty writes one property on the GeoClue client. Every knob this
// adapter has is set this way, because the client interface exposes them as
// properties rather than as methods.
func setClientProperty(
	ctx context.Context,
	client dbus.BusObject,
	name string,
	value any,
) error {
	return client.CallWithContext(
		ctx,
		propertiesIFace+".Set",
		0,
		geoClueClientIFace,
		name,
		dbus.MakeVariant(value),
	).Err
}

// geoClueAccuracy maps the request onto a GeoClue accuracy level. Anything
// unrecognised falls back to the street level, which is what GeoClue itself
// treats as the ordinary case.
func geoClueAccuracy(opts Options) uint32 {
	if opts.DesiredAccuracyMeters > 0 {
		return accuracyForMeters(opts.DesiredAccuracyMeters)
	}

	levels := map[provider.Accuracy]uint32{
		provider.AccuracyNavigation: accuracyExact,
		provider.AccuracyHigh:       accuracyExact,
		provider.AccuracyBalanced:   accuracyStreet,
	}

	level, ok := levels[opts.Accuracy]
	if !ok {
		return accuracyStreet
	}

	return level
}

// accuracyForMeters buckets a distance the caller can express onto a level
// GeoClue understands. The bucketing is lossy and the caller cannot see it, so
// a boundary off by one asks for city precision where a street was wanted.
func accuracyForMeters(meters uint32) uint32 {
	switch {
	case meters <= exactMeters:
		return accuracyExact
	case meters <= streetMeters:
		return accuracyStreet
	case meters <= neighborhoodMeters:
		return accuracyNeighborhood
	default:
		return accuracyCity
	}
}

// distanceMeters rounds a distance up to the whole metres GeoClue takes, and
// clamps it to what the property can hold.
func distanceMeters(meters float64) uint32 {
	rounded := math.Ceil(meters)
	if rounded >= math.MaxUint32 {
		return math.MaxUint32
	}

	return uint32(rounded)
}

// intervalSeconds rounds an interval up to the whole seconds GeoClue takes.
// A sub-second interval becomes one second rather than zero, which GeoClue
// reads as no limit at all.
func intervalSeconds(interval time.Duration) uint32 {
	seconds := math.Ceil(interval.Seconds())
	if seconds < 1 {
		return 1
	}

	if seconds >= math.MaxUint32 {
		return math.MaxUint32
	}

	return uint32(seconds)
}

// variantFloat64 reads a D-Bus double. D-Bus is dynamically typed, so a
// property can arrive as the wrong type or as a NaN, and both have to read as
// absent rather than reach geo.Validate as a coordinate.
func variantFloat64(variant dbus.Variant) (float64, bool) {
	if variant.Value() == nil {
		return 0, false
	}

	value, ok := variant.Value().(float64)

	return value, ok && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// geoClueTimestamp decodes the fix's own stamp. A shape that does not decode
// leaves the timestamp zero, which makes the sample look infinitely stale, so
// every shape the pair arrives in has to be handled.
func geoClueTimestamp(variant dbus.Variant) time.Time {
	if variant.Value() == nil {
		return time.Time{}
	}

	seconds, microseconds, ok := timestampPair(variant)
	if !ok {
		return time.Time{}
	}

	stamp, ok := unixTime(seconds, microseconds)
	if !ok {
		return time.Time{}
	}

	return stamp
}

// timestampPair pulls the (seconds, microseconds) pair out of a variant. Which
// concrete Go shape it arrives in depends on the dbus version and on how the
// variant was built, so the struct decode is tried first and the shapes it
// cannot handle are read positionally.
func timestampPair(variant dbus.Variant) (uint64, uint64, bool) {
	var decoded struct {
		Seconds      uint64
		Microseconds uint64
	}

	err := variant.Store(&decoded)
	if err == nil {
		return decoded.Seconds, decoded.Microseconds, true
	}

	return timestampPairOf(variant.Value())
}

func timestampPairOf(value any) (uint64, uint64, bool) {
	switch pair := value.(type) {
	case []any:
		return anyPair(pair)
	case []uint64:
		if len(pair) < pairLength {
			return 0, 0, false
		}

		return pair[0], pair[1], true
	case struct{ Seconds, Microseconds uint64 }:
		return pair.Seconds, pair.Microseconds, true
	}

	return 0, 0, false
}

func anyPair(values []any) (uint64, uint64, bool) {
	if len(values) < pairLength {
		return 0, 0, false
	}

	seconds, okSeconds := asUint64(values[0])
	microseconds, okMicroseconds := asUint64(values[1])

	return seconds, microseconds, okSeconds && okMicroseconds
}

// unixTime builds the fix's timestamp, rejecting a pair too large for the
// signed seconds time.Unix takes rather than wrapping it into the past.
func unixTime(seconds, microseconds uint64) (time.Time, bool) {
	if seconds > math.MaxInt64 || microseconds > math.MaxInt64 {
		return time.Time{}, false
	}

	nanoseconds := int64(microseconds) * int64(time.Microsecond)

	return time.Unix(int64(seconds), nanoseconds).UTC(), true
}

// asUint64 widens the integer shapes a D-Bus pair member can arrive in. A
// negative int64 is rejected rather than widened, because as a timestamp it
// would become a date roughly 580 billion years out — merely far-future rather
// than obviously invalid.
func asUint64(value any) (uint64, bool) {
	switch typed := value.(type) {
	case uint64:
		return typed, true
	case uint32:
		return uint64(typed), true
	case int64:
		if typed < 0 {
			return 0, false
		}

		return uint64(typed), true
	}

	return 0, false
}

// mapGeoClueError translates a D-Bus error name into the sentinel a caller
// switches on, and into the retry hint the reconnect loop reads. Marking a
// permission denial temporary would spin that loop forever against a decision
// only the user can change.
func mapGeoClueError(operation string, err error) error {
	var dbusErr *dbus.Error

	if errors.As(err, &dbusErr) {
		switch dbusErr.Name {
		case "org.freedesktop.DBus.Error.AccessDenied",
			"org.freedesktop.GeoClue2.Error.NotAuthorized":
			return geo.Wrap(
				platform,
				operation,
				errors.Join(geo.ErrPermissionDenied, err),
				false,
			)
		case "org.freedesktop.DBus.Error.ServiceUnknown",
			"org.freedesktop.DBus.Error.NameHasNoOwner":
			return geo.Wrap(
				platform,
				operation,
				errors.Join(geo.ErrServiceUnavailable, err),
				true,
			)
		}
	}

	return geo.Wrap(platform, operation, err, true)
}
