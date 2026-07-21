package geo

import (
	"errors"
	"fmt"
	"testing"
)

// The nil receiver case is not defensive padding: a *Error travels as an
// error interface, and a typed-nil that reaches Error() would otherwise panic
// inside whatever was only trying to log it.
func TestErrorFormatsWithAndWithoutAPlatform(t *testing.T) {
	cases := map[string]struct {
		err  *Error
		want string
	}{
		"nil receiver": {
			err:  nil,
			want: "<nil>",
		},
		"no platform": {
			err:  &Error{Op: "open", Err: ErrServiceDisabled},
			want: "location open: location service disabled",
		},
		"with platform": {
			err:  &Error{Op: "start", Platform: "darwin", Err: ErrPermissionDenied},
			want: "location start (darwin): location permission denied",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Preserving errors.Is through the annotation is the whole reason the type
// exists: callers switch on the sentinel, not on the wrapper.
func TestWrapKeepsTheSentinelReachable(t *testing.T) {
	err := Wrap("darwin", "start", ErrPermissionDenied, false)

	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("errors.Is(%v, ErrPermissionDenied) = false, want true", err)
	}

	if errors.Is(err, ErrServiceDisabled) {
		t.Fatalf("errors.Is(%v, ErrServiceDisabled) = true, want false", err)
	}

	var annotated *Error
	if !errors.As(err, &annotated) {
		t.Fatalf("errors.As(%v, *geo.Error) = false, want true", err)
	}

	got := annotated.Unwrap()
	if !errors.Is(got, ErrPermissionDenied) {
		t.Fatalf("Unwrap() = %v, want ErrPermissionDenied", got)
	}
}

// Wrap sits at the end of paths that may or may not have failed, so returning
// nil for nil is what lets callers write `return geo.Wrap(...)` unguarded.
func TestWrapOfNilStaysNil(t *testing.T) {
	err := Wrap("darwin", "stop", nil, true)
	if err != nil {
		t.Fatalf("Wrap(nil) = %v, want nil", err)
	}
}

func TestWrapCarriesTheAnnotationThrough(t *testing.T) {
	cause := fmt.Errorf("underlying: %w", ErrPositionUnavailable)
	err := Wrap("linux", "fix", cause, true)

	var annotated *Error
	if !errors.As(err, &annotated) {
		t.Fatalf("errors.As(%v, *geo.Error) = false, want true", err)
	}

	if annotated.Op != "fix" {
		t.Errorf("Op = %q, want %q", annotated.Op, "fix")
	}

	if annotated.Platform != "linux" {
		t.Errorf("Platform = %q, want %q", annotated.Platform, "linux")
	}

	if !annotated.Temporary {
		t.Errorf("Temporary = false, want true")
	}

	if !errors.Is(err, ErrPositionUnavailable) {
		t.Errorf("errors.Is(%v, ErrPositionUnavailable) = false, want true", err)
	}
}
