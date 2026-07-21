package location

import (
	"github.com/mostafakhairy0305-dot/golocation/geo"
	fanout "github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/port"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// The names below are defined by the feature that owns each concept — the
// domain kernel for values, the fan-out feature for how a subscription
// behaves, the provider feature for what to ask a platform for. They are
// re-exported here so callers still need only this one package, and so that
// moving a concept between features never breaks the public API.
//
// Every name carries a summary here, because godoc cannot follow an alias into
// internal/ to find the original. The ones aliasing geo point at it for the
// documentation in full.

// Domain values, owned by package geo.
type (
	// Fix is one location sample. See [geo.Fix].
	Fix = geo.Fix
	// Field reports which optional Fix fields carry data. See [geo.Field].
	Field = geo.Field
	// Source is the provider category behind a Fix. See [geo.Source].
	Source = geo.Source
	// State is the native location service state. See [geo.State].
	State = geo.State
	// PermissionState is the current location permission.
	// See [geo.PermissionState].
	PermissionState = geo.PermissionState
	// Status is a snapshot of service and permission state. See [geo.Status].
	Status = geo.Status
	// Capabilities reports the fields the active backend can supply.
	// See [geo.Capabilities].
	Capabilities = geo.Capabilities
	// Error carries the operation, platform, and retry hint behind a failure.
	// See [geo.Error].
	Error = geo.Error
)

// Subscription contains independent channels for fixes, native errors, and
// status changes:
//
//	Locations <-chan Fix
//	Errors    <-chan error
//	Statuses  <-chan Status
//
// All three close when the subscription's context is done or the Locator
// closes, so ranging over any of them ends at shutdown.
type Subscription = fanout.Subscription

// SubscriptionConfig configures one independent real-time subscription:
//
//	Buffer       int         // per-channel capacity; 0 takes Config.DefaultChannelBuffer
//	DropPolicy   // behavior when a channel is full
//	ReplayLatest bool        // immediately send the most recent accepted fix, when present
type SubscriptionConfig = fanout.SubscriptionConfig

// DropPolicy defines subscriber behavior when its channel is full. A
// subscription never blocks the provider: one value is always lost, and the
// policy chooses which. See DropDefault, DropOldest, and DropNewest.
type DropPolicy = fanout.DropPolicy

// Accuracy controls the native provider's power/precision preference. See
// AccuracyBalanced, AccuracyHigh, and AccuracyNavigation.
type Accuracy = provider.Accuracy

// PermissionMode controls whether Open asks the operating system for access.
// See PermissionAuto, PermissionRequest, and PermissionDoNotRequest.
type PermissionMode = provider.PermissionMode

// Optional Fix fields.
const (
	FieldAltitude         = geo.FieldAltitude
	FieldVerticalAccuracy = geo.FieldVerticalAccuracy
	FieldSpeed            = geo.FieldSpeed
	FieldHeading          = geo.FieldHeading
)

// Provider categories.
const (
	SourceUnknown    = geo.SourceUnknown
	SourceSystem     = geo.SourceSystem
	SourceSatellite  = geo.SourceSatellite
	SourceWiFi       = geo.SourceWiFi
	SourceCellular   = geo.SourceCellular
	SourceIP         = geo.SourceIP
	SourceDefault    = geo.SourceDefault
	SourceManual     = geo.SourceManual
	SourceRemote     = geo.SourceRemote
	SourceObfuscated = geo.SourceObfuscated
)

// Service states.
const (
	StateStarting     = geo.StateStarting
	StateReady        = geo.StateReady
	StateReconnecting = geo.StateReconnecting
	StateUnavailable  = geo.StateUnavailable
	StateDisabled     = geo.StateDisabled
	StateClosed       = geo.StateClosed
)

// Permission states, as reported in Status.
const (
	PermissionUnknown        = geo.PermissionUnknown
	PermissionPromptRequired = geo.PermissionPromptRequired
	PermissionGranted        = geo.PermissionGranted
	PermissionDenied         = geo.PermissionDenied
	PermissionRestricted     = geo.PermissionRestricted
)

// Accuracy preferences.
const (
	AccuracyBalanced   = provider.AccuracyBalanced
	AccuracyHigh       = provider.AccuracyHigh
	AccuracyNavigation = provider.AccuracyNavigation
)

// Permission request modes, as configured on Config.
const (
	PermissionAuto         = provider.PermissionAuto
	PermissionRequest      = provider.PermissionRequest
	PermissionDoNotRequest = provider.PermissionDoNotRequest
)

// Subscriber backpressure policies.
const (
	DropDefault = fanout.DropDefault
	DropOldest  = fanout.DropOldest
	DropNewest  = fanout.DropNewest
)

// MaxClockSkew is how far a provider timestamp may run ahead of the local
// clock and still count as fresh.
const MaxClockSkew = geo.MaxClockSkew

// Sentinel errors. Match them with errors.Is; the concrete error is an *Error
// carrying the operation and platform.
var (
	ErrInvalidConfig       = geo.ErrInvalidConfig
	ErrPermissionDenied    = geo.ErrPermissionDenied
	ErrPermissionNeeded    = geo.ErrPermissionNeeded
	ErrServiceDisabled     = geo.ErrServiceDisabled
	ErrServiceUnavailable  = geo.ErrServiceUnavailable
	ErrPositionUnavailable = geo.ErrPositionUnavailable
	ErrStaleFix            = geo.ErrStaleFix
	ErrClosed              = geo.ErrClosed
	ErrUnsupported         = geo.ErrUnsupported
)
