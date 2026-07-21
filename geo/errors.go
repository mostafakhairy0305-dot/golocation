package geo

import (
	"errors"
	"fmt"
)

// The sentinel errors every layer reports through. A caller matches them with
// errors.Is; the concrete value is usually an *Error carrying the operation and
// the platform that raised it.
var (
	ErrInvalidConfig    = errors.New("invalid location configuration")
	ErrPermissionDenied = errors.New("location permission denied")
	ErrPermissionNeeded = errors.New(
		"location permission must be requested by the host application",
	)
	ErrServiceDisabled     = errors.New("location service disabled")
	ErrServiceUnavailable  = errors.New("location service unavailable")
	ErrPositionUnavailable = errors.New("position unavailable")
	ErrStaleFix            = errors.New("stale location fix")
	ErrClosed              = errors.New("locator closed")
	ErrUnsupported         = errors.New("platform or architecture unsupported")
	// ErrInvalidFix reports coordinates a provider cannot have meant: out of
	// range, NaN, or infinite. It is the sentinel behind every Validate
	// failure, so a caller can match the cause instead of the message.
	ErrInvalidFix = errors.New("invalid location fix")
)

// Error adds operation, platform, and retry metadata while preserving errors.Is.
type Error struct {
	Op string
	// Platform is empty for errors raised above the adapter layer, and a
	// non-retryable error is the common case, so both are optional.
	Platform  string `exhaustruct:"optional"`
	Temporary bool   `exhaustruct:"optional"`
	Err       error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	if e.Platform == "" {
		return fmt.Sprintf("location %s: %v", e.Op, e.Err)
	}

	return fmt.Sprintf("location %s (%s): %v", e.Op, e.Platform, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Wrap annotates err with the platform, operation, and retry hint. It returns
// nil when err is nil, so it is safe to use on a possibly-empty error value.
func Wrap(platform, op string, err error, temporary bool) error {
	if err == nil {
		return nil
	}

	return &Error{Op: op, Platform: platform, Temporary: temporary, Err: err}
}
