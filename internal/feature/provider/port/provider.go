// Package port declares the provider feature's contracts for the native
// location source. The operating system is an adapter like any other: Provider
// is the port it implements, Sink is the port it publishes through, Factory is
// the port that chooses between implementations, and Options is the neutral
// request a caller fills in.
//
// The adapters live under ../adapter, one per operating system, and ../platform
// provides the Factory that picks between them at build time. That is why this
// package holds only contracts and imports no adapter: it keeps the dependency
// running one way and leaves the feature free of build tags.
package port

import (
	"context"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// Accuracy controls the native provider's power/precision preference.
type Accuracy uint8

const (
	AccuracyBalanced Accuracy = iota
	AccuracyHigh
	AccuracyNavigation
)

// PermissionMode controls whether starting a provider asks the operating
// system for access.
type PermissionMode uint8

const (
	// PermissionAuto asks for access when the platform supports an explicit request.
	PermissionAuto PermissionMode = iota
	// PermissionRequest always asks for access.
	PermissionRequest
	// PermissionDoNotRequest lets the host application manage permission first.
	PermissionDoNotRequest
)

// Sink receives everything a Provider produces. The core implements it; a test
// can implement it in a few lines to drive the core without an operating
// system. Implementations must be safe to call from any goroutine, including
// an OS callback thread, and must not block the caller.
type Sink interface {
	PublishFix(geo.Fix)
	PublishStatus(geo.Status)
	PublishError(error)
}

// Provider is one operating system's native location service. Stop must be
// idempotent and safe to call before or after a failed Start.
type Provider interface {
	Start(ctx context.Context) error
	Stop() error
	Capabilities() geo.Capabilities
	// Platform names the adapter for error annotation, e.g. "darwin".
	Platform() string
}

// Factory builds the Provider for the environment it is asked in. It is a port
// rather than a plain function so that the choice of operating system is
// itself substitutable: the composition root passes the build-tagged one, and
// a test passes a fake to exercise start-up without a real location service.
type Factory interface {
	New(opts Options, sink Sink) (Provider, error)
}

// FactoryFunc adapts a plain function to Factory.
type FactoryFunc func(Options, Sink) (Provider, error)

func (f FactoryFunc) New(opts Options, sink Sink) (Provider, error) { return f(opts, sink) }

// LinuxOptions carries the GeoClue knobs across the factory boundary without
// the neutral layer having to import the GeoClue adapter — which would not
// compile off Linux.
type LinuxOptions struct {
	DesktopID    string
	Reconnect    bool
	ReconnectMin time.Duration
	ReconnectMax time.Duration
}

// Options is the neutral request. Each build-tagged Factory maps the subset
// its adapter cares about into that adapter's own Options type.
type Options struct {
	Accuracy              Accuracy
	DesiredAccuracyMeters uint32
	MinimumInterval       time.Duration
	MinimumDistanceMeters float64
	MaximumAge            time.Duration
	StartTimeout          time.Duration
	Permission            PermissionMode

	Linux LinuxOptions
}
