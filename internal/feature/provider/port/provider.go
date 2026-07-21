// Package port declares the provider feature's contracts for the native
// location source. The operating system is an adapter like any other: Provider
// is the port it implements, Sink is the port it publishes through, Host is
// what it gets bound to, Factory is the port that chooses between
// implementations, and Options is the neutral request a caller fills in.
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

// The accuracy preferences, cheapest first. Each adapter maps them onto
// whatever its operating system actually offers.
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
	PublishFix(fix geo.Fix)
	PublishStatus(status geo.Status)
	PublishError(err error)
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

// Host is what a Factory hands the Provider it built to: the Sink that provider
// will publish through, plus the one call that binds the two together. Binding
// is what keeps a Factory from having to give a Provider back to its caller,
// which would make every build-tagged constructor in the chain return an
// interface. The core implements it; a test implements it in a few lines.
type Host interface {
	Sink
	// Attach binds native as the provider whose output this host publishes. A
	// Factory calls it at most once, and only on success.
	Attach(native Provider)
}

// Factory builds the Provider for the environment it is asked in and attaches
// it to host. The choice of operating system is substitutable through it: the
// composition root passes the build-tagged one, and a test passes a fake to
// exercise start-up without a real location service.
type Factory func(opts Options, host Host) error

// LinuxOptions carries the GeoClue knobs across the factory boundary without
// the neutral layer having to import the GeoClue adapter — which would not
// compile off Linux.
type LinuxOptions struct {
	DesktopID string
	Reconnect bool
	// Only consulted when Reconnect is set; zero means the adapter default.
	ReconnectMin time.Duration `exhaustruct:"optional"`
	ReconnectMax time.Duration `exhaustruct:"optional"`
}

// Options is the neutral request. Each build-tagged Factory maps the subset
// its adapter cares about into that adapter's own Options type.
// A field the requesting platform has no equivalent for is simply left unset,
// so none of them can be required.
type Options struct {
	Accuracy              Accuracy       `exhaustruct:"optional"`
	DesiredAccuracyMeters uint32         `exhaustruct:"optional"`
	MinimumInterval       time.Duration  `exhaustruct:"optional"`
	MinimumDistanceMeters float64        `exhaustruct:"optional"`
	MaximumAge            time.Duration  `exhaustruct:"optional"`
	StartTimeout          time.Duration  `exhaustruct:"optional"`
	Permission            PermissionMode `exhaustruct:"optional"`

	Linux LinuxOptions `exhaustruct:"optional"`
}
