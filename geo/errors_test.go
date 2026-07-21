package geo_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// The nil receiver case is not defensive padding: a *Error travels as an
// error interface, and a typed-nil that reaches Error() would otherwise panic
// inside whatever was only trying to log it.
func TestErrorFormatsWithAndWithoutAPlatform(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  *geo.Error
		want string
	}{
		"nil receiver": {
			err:  nil,
			want: "<nil>",
		},
		"no platform": {
			err:  &geo.Error{Op: "open", Err: geo.ErrServiceDisabled},
			want: "location open: location service disabled",
		},
		"with platform": {
			err:  &geo.Error{Op: "start", Platform: "darwin", Err: geo.ErrPermissionDenied},
			want: "location start (darwin): location permission denied",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.err.Error(); got != testCase.want {
				t.Fatalf("Error() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Preserving errors.Is through the annotation is the whole reason the type
// exists: callers switch on the sentinel, not on the wrapper.
func TestWrapKeepsTheSentinelReachable(t *testing.T) {
	t.Parallel()

	err := geo.Wrap("darwin", "start", geo.ErrPermissionDenied, false)

	if !errors.Is(err, geo.ErrPermissionDenied) {
		t.Fatalf("errors.Is(%v, ErrPermissionDenied) = false, want true", err)
	}

	if errors.Is(err, geo.ErrServiceDisabled) {
		t.Fatalf("errors.Is(%v, ErrServiceDisabled) = true, want false", err)
	}

	var annotated *geo.Error
	if !errors.As(err, &annotated) {
		t.Fatalf("errors.As(%v, *geo.Error) = false, want true", err)
	}

	got := annotated.Unwrap()
	if !errors.Is(got, geo.ErrPermissionDenied) {
		t.Fatalf("Unwrap() = %v, want ErrPermissionDenied", got)
	}
}

// Wrap sits at the end of paths that may or may not have failed, so returning
// nil for nil is what lets callers write `return geo.Wrap(...)` unguarded.
func TestWrapOfNilStaysNil(t *testing.T) {
	t.Parallel()

	err := geo.Wrap("darwin", "stop", nil, true)
	if err != nil {
		t.Fatalf("Wrap(nil) = %v, want nil", err)
	}
}

func TestWrapCarriesTheAnnotationThrough(t *testing.T) {
	t.Parallel()

	cause := fmt.Errorf("underlying: %w", geo.ErrPositionUnavailable)
	err := geo.Wrap("linux", "fix", cause, true)

	var annotated *geo.Error
	if !errors.As(err, &annotated) {
		t.Fatalf("errors.As(%v, *geo.Error) = false, want true", err)
	}

	expectAnnotation(t, annotated, "fix", "linux")

	if !errors.Is(err, geo.ErrPositionUnavailable) {
		t.Errorf("errors.Is(%v, ErrPositionUnavailable) = false, want true", err)
	}
}

// expectAnnotation fails for every piece of context Wrap should have attached.
func expectAnnotation(t *testing.T, got *geo.Error, wantOp, wantPlatform string) {
	t.Helper()

	if got.Op != wantOp {
		t.Errorf("Op = %q, want %q", got.Op, wantOp)
	}

	if got.Platform != wantPlatform {
		t.Errorf("Platform = %q, want %q", got.Platform, wantPlatform)
	}

	if !got.Temporary {
		t.Errorf("Temporary = false, want true")
	}
}
