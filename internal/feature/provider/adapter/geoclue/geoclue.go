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
)

const platform = "linux"

const (
	geoClueService       = "org.freedesktop.GeoClue2"
	geoClueManagerPath   = dbus.ObjectPath("/org/freedesktop/GeoClue2/Manager")
	geoClueManagerIFace  = "org.freedesktop.GeoClue2.Manager"
	geoClueClientIFace   = "org.freedesktop.GeoClue2.Client"
	geoClueLocationIFace = "org.freedesktop.GeoClue2.Location"
	propertiesIFace      = "org.freedesktop.DBus.Properties"
)

// Options are the GeoClue-specific knobs, including the reconnect policy and
// the desktop ID GeoClue uses to look up this application's permissions.
type Options struct {
	Accuracy              provider.Accuracy
	DesiredAccuracyMeters uint32
	MinimumInterval       time.Duration
	MinimumDistanceMeters float64

	DesktopID    string
	Reconnect    bool
	ReconnectMin time.Duration
	ReconnectMax time.Duration
}

type backend struct {
	opts Options
	sink provider.Sink

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.Mutex
	conn       *dbus.Conn
	clientPath dbus.ObjectPath
}

type session struct {
	backend    *backend
	conn       *dbus.Conn
	clientPath dbus.ObjectPath
	signals    chan *dbus.Signal
}

func New(opts Options, sink provider.Sink) (provider.Provider, error) {
	return &backend{opts: opts, sink: sink}, nil
}

func (b *backend) Platform() string { return platform }

func (b *backend) Capabilities() geo.Capabilities {
	return geo.Capabilities{
		Altitude:         true,
		VerticalAccuracy: false,
		Speed:            true,
		Heading:          true,
		Source:           false,
	}
}

func (b *backend) Start(startCtx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(context.Background())
	ready := make(chan error, 1)
	b.wg.Add(1)
	go b.run(ready)

	select {
	case err := <-ready:
		return err
	case <-startCtx.Done():
		b.cancel()
		b.wg.Wait()
		return geo.Wrap(platform, "start GeoClue", startCtx.Err(), true)
	}
}

func (b *backend) Stop() error {
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

func (b *backend) run(ready chan<- error) {
	defer b.wg.Done()

	backoff := b.opts.ReconnectMin
	var (
		current *session
		err     error
	)
	for {
		current, err = b.connect(b.ctx)
		if err == nil {
			break
		}
		if !b.opts.Reconnect || errors.Is(err, geo.ErrPermissionDenied) {
			ready <- err
			return
		}
		b.sink.PublishStatus(geo.Status{State: geo.StateReconnecting, Permission: geo.PermissionUnknown, Message: "waiting for GeoClue"})
		if !sleepContext(b.ctx, backoff) {
			return
		}
		backoff *= 2
		if backoff > b.opts.ReconnectMax {
			backoff = b.opts.ReconnectMax
		}
	}
	ready <- nil
	backoff = b.opts.ReconnectMin

	for {
		err = current.run(b.ctx)
		current.close()
		if b.ctx.Err() != nil {
			return
		}
		b.sink.PublishError(geo.Wrap(platform, "GeoClue connection", err, true))
		if !b.opts.Reconnect {
			b.sink.PublishStatus(geo.Status{State: geo.StateUnavailable, Permission: geo.PermissionUnknown, Message: "GeoClue connection lost"})
			return
		}

		b.sink.PublishStatus(geo.Status{State: geo.StateReconnecting, Permission: geo.PermissionUnknown, Message: "reconnecting to GeoClue"})
		for {
			if !sleepContext(b.ctx, backoff) {
				return
			}
			current, err = b.connect(b.ctx)
			if err == nil {
				backoff = b.opts.ReconnectMin
				break
			}
			if errors.Is(err, geo.ErrPermissionDenied) {
				b.sink.PublishStatus(geo.Status{State: geo.StateDisabled, Permission: geo.PermissionDenied, Message: "GeoClue access denied"})
				b.sink.PublishError(err)
				return
			}
			b.sink.PublishError(err)
			backoff *= 2
			if backoff > b.opts.ReconnectMax {
				backoff = b.opts.ReconnectMax
			}
		}
	}
}

func (b *backend) connect(ctx context.Context) (*session, error) {
	conn, err := dbus.SystemBusPrivate()
	if err != nil {
		return nil, geo.Wrap(platform, "connect system bus", errors.Join(geo.ErrServiceUnavailable, err), true)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = conn.Close()
		}
	}()

	if err := conn.Auth(nil); err != nil {
		return nil, geo.Wrap(platform, "authenticate system bus", err, false)
	}
	if err := conn.Hello(); err != nil {
		return nil, geo.Wrap(platform, "hello system bus", err, false)
	}

	manager := conn.Object(geoClueService, geoClueManagerPath)
	var clientPath dbus.ObjectPath
	if err := manager.CallWithContext(ctx, geoClueManagerIFace+".GetClient", 0).Store(&clientPath); err != nil {
		return nil, mapGeoClueError("get client", err)
	}
	if !clientPath.IsValid() {
		return nil, geo.Wrap(platform, "get client", geo.ErrServiceUnavailable, true)
	}

	client := conn.Object(geoClueService, clientPath)
	if err := setDBusProperty(ctx, client, geoClueClientIFace, "DesktopId", b.opts.DesktopID); err != nil {
		return nil, mapGeoClueError("set desktop ID", err)
	}
	if err := setDBusProperty(ctx, client, geoClueClientIFace, "RequestedAccuracyLevel", geoClueAccuracy(b.opts)); err != nil {
		return nil, mapGeoClueError("set accuracy", err)
	}
	if b.opts.MinimumDistanceMeters > 0 {
		distance := uint32(math.Ceil(b.opts.MinimumDistanceMeters))
		if err := setDBusProperty(ctx, client, geoClueClientIFace, "DistanceThreshold", distance); err != nil {
			return nil, mapGeoClueError("set distance threshold", err)
		}
	}
	if b.opts.MinimumInterval > 0 {
		seconds := uint32(math.Ceil(b.opts.MinimumInterval.Seconds()))
		if seconds == 0 {
			seconds = 1
		}
		if err := setDBusProperty(ctx, client, geoClueClientIFace, "TimeThreshold", seconds); err != nil {
			return nil, mapGeoClueError("set time threshold", err)
		}
	}

	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(clientPath),
		dbus.WithMatchInterface(geoClueClientIFace),
		dbus.WithMatchMember("LocationUpdated"),
	); err != nil {
		conn.RemoveSignal(signals)
		return nil, geo.Wrap(platform, "subscribe GeoClue", err, true)
	}

	if err := client.CallWithContext(ctx, geoClueClientIFace+".Start", 0).Err; err != nil {
		conn.RemoveSignal(signals)
		return nil, mapGeoClueError("start client", err)
	}

	current := &session{backend: b, conn: conn, clientPath: clientPath, signals: signals}
	b.mu.Lock()
	b.conn = conn
	b.clientPath = clientPath
	b.mu.Unlock()
	closeOnError = false

	b.sink.PublishStatus(geo.Status{State: geo.StateStarting, Permission: geo.PermissionGranted, Message: "GeoClue started"})
	if variant, err := client.GetProperty(geoClueClientIFace + ".Location"); err == nil {
		if locationPath, ok := variant.Value().(dbus.ObjectPath); ok && locationPath.IsValid() && locationPath != "/" {
			if fix, readErr := current.readLocation(ctx, locationPath); readErr == nil {
				b.sink.PublishFix(fix)
			}
		}
	}
	return current, nil
}

func (s *session) run(ctx context.Context) error {
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case signal, ok := <-s.signals:
			if !ok {
				return geo.ErrServiceUnavailable
			}
			if signal == nil || signal.Name != geoClueClientIFace+".LocationUpdated" || len(signal.Body) < 2 {
				continue
			}
			path, ok := signal.Body[1].(dbus.ObjectPath)
			if !ok || !path.IsValid() || path == "/" {
				continue
			}
			fix, err := s.readLocation(ctx, path)
			if err != nil {
				s.backend.sink.PublishError(err)
				continue
			}
			s.backend.sink.PublishFix(fix)
		case <-ping.C:
			peer := s.conn.Object(geoClueService, s.clientPath)
			if err := peer.CallWithContext(ctx, "org.freedesktop.DBus.Peer.Ping", 0).Err; err != nil {
				return err
			}
		}
	}
}

func (s *session) readLocation(ctx context.Context, path dbus.ObjectPath) (geo.Fix, error) {
	var props map[string]dbus.Variant
	obj := s.conn.Object(geoClueService, path)
	if err := obj.CallWithContext(ctx, propertiesIFace+".GetAll", 0, geoClueLocationIFace).Store(&props); err != nil {
		return geo.Fix{}, mapGeoClueError("read location", err)
	}

	latitude, okLat := variantFloat64(props["Latitude"])
	longitude, okLon := variantFloat64(props["Longitude"])
	accuracy, okAccuracy := variantFloat64(props["Accuracy"])
	if !okLat || !okLon || !okAccuracy {
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

	if altitude, ok := variantFloat64(props["Altitude"]); ok && altitude > -math.MaxFloat64/2 {
		fix.AltitudeMeters = altitude
		fix.Fields |= geo.FieldAltitude
	}
	if speed, ok := variantFloat64(props["Speed"]); ok && speed >= 0 {
		fix.SpeedMetersPerSecond = speed
		fix.Fields |= geo.FieldSpeed
	}
	if heading, ok := variantFloat64(props["Heading"]); ok && heading >= 0 {
		fix.HeadingDegrees = heading
		fix.Fields |= geo.FieldHeading
	}
	return fix, nil
}

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

func setDBusProperty(ctx context.Context, object dbus.BusObject, iface, name string, value any) error {
	return object.CallWithContext(ctx, propertiesIFace+".Set", 0, iface, name, dbus.MakeVariant(value)).Err
}

func geoClueAccuracy(opts Options) uint32 {
	if opts.DesiredAccuracyMeters > 0 {
		switch {
		case opts.DesiredAccuracyMeters <= 100:
			return 8 // exact
		case opts.DesiredAccuracyMeters <= 1000:
			return 6 // street
		case opts.DesiredAccuracyMeters <= 10000:
			return 5 // neighborhood
		default:
			return 4 // city
		}
	}
	switch opts.Accuracy {
	case provider.AccuracyHigh, provider.AccuracyNavigation:
		return 8
	default:
		return 6
	}
}

func variantFloat64(v dbus.Variant) (float64, bool) {
	if v.Value() == nil {
		return 0, false
	}
	value, ok := v.Value().(float64)
	return value, ok && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func geoClueTimestamp(v dbus.Variant) time.Time {
	if v.Value() == nil {
		return time.Time{}
	}
	var decoded struct {
		Seconds      uint64
		Microseconds uint64
	}
	if err := v.Store(&decoded); err == nil {
		return time.Unix(int64(decoded.Seconds), int64(decoded.Microseconds)*1000).UTC()
	}
	switch value := v.Value().(type) {
	case []any:
		if len(value) >= 2 {
			seconds, okSeconds := asUint64(value[0])
			micros, okMicros := asUint64(value[1])
			if okSeconds && okMicros {
				return time.Unix(int64(seconds), int64(micros)*1000).UTC()
			}
		}
	case []uint64:
		if len(value) >= 2 {
			return time.Unix(int64(value[0]), int64(value[1])*1000).UTC()
		}
	case struct{ Seconds, Microseconds uint64 }:
		return time.Unix(int64(value.Seconds), int64(value.Microseconds)*1000).UTC()
	}
	return time.Time{}
}

func asUint64(value any) (uint64, bool) {
	switch v := value.(type) {
	case uint64:
		return v, true
	case uint32:
		return uint64(v), true
	case int64:
		return uint64(v), v >= 0
	default:
		return 0, false
	}
}

func mapGeoClueError(op string, err error) error {
	var dbusErr *dbus.Error
	if errors.As(err, &dbusErr) {
		switch dbusErr.Name {
		case "org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.GeoClue2.Error.NotAuthorized":
			return geo.Wrap(platform, op, errors.Join(geo.ErrPermissionDenied, err), false)
		case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner":
			return geo.Wrap(platform, op, errors.Join(geo.ErrServiceUnavailable, err), true)
		}
	}
	return geo.Wrap(platform, op, err, true)
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		duration = time.Second
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
