package geo

import "time"

// State describes the native location service state.
type State uint8

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

const (
	PermissionUnknown PermissionState = iota
	PermissionPromptRequired
	PermissionGranted
	PermissionDenied
	PermissionRestricted
)

// Status is a snapshot of service and permission state.
type Status struct {
	State      State
	Permission PermissionState
	UpdatedAt  time.Time
	Message    string
}

// Capabilities reports fields that can be supplied by the active backend.
type Capabilities struct {
	Altitude         bool
	VerticalAccuracy bool
	Speed            bool
	Heading          bool
	Source           bool
}
