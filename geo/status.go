package geo

import "time"

// State describes the native location service state.
type State uint8

// The service states, in the order a healthy session moves through them.
// StateReconnecting, StateUnavailable, and StateDisabled are recoverable;
// StateClosed is terminal.
const (
	StateStarting State = iota
	StateReady
	StateReconnecting
	StateUnavailable
	StateDisabled
	StateClosed
)

// PermissionState describes the current location permission state.
type PermissionState uint8

// The permission states. PermissionUnknown is the zero value, meaning the
// platform has not told us yet — not that access was refused.
const (
	PermissionUnknown PermissionState = iota
	PermissionPromptRequired
	PermissionGranted
	PermissionDenied
	PermissionRestricted
)

// Status is a snapshot of service and permission state.
// State is the only required field: an unknown permission, an unset timestamp
// (which the lifecycle tracker stamps), and an absent message are all ordinary.
type Status struct {
	State      State
	Permission PermissionState `exhaustruct:"optional"`
	UpdatedAt  time.Time       `exhaustruct:"optional"`
	Message    string          `exhaustruct:"optional"`
}

// Capabilities reports fields that can be supplied by the active backend.
// An omitted field means "not supported", so every field is optional.
type Capabilities struct {
	Altitude         bool `exhaustruct:"optional"`
	VerticalAccuracy bool `exhaustruct:"optional"`
	Speed            bool `exhaustruct:"optional"`
	Heading          bool `exhaustruct:"optional"`
	Source           bool `exhaustruct:"optional"`
}
