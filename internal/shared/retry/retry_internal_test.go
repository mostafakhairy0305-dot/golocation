package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// Static errors the tests hand to geo.Wrap: err113 forbids inline errors.New.
var (
	errTransient    = errors.New("transient")
	errPermanentOp  = errors.New("permanent")
	errUnclassified = errors.New("unclassified")
)

// fastOpts keep the tests quick and deterministic: a millisecond curve with no
// jitter, so retries neither wait long nor vary in timing.
func fastOpts() []Option {
	return []Option{
		WithInitialInterval(time.Millisecond),
		WithMaxInterval(2 * time.Millisecond),
		WithRandomizationFactor(0),
	}
}

func TestDoRetriesTemporaryThenSucceeds(t *testing.T) {
	t.Parallel()

	calls := 0

	got, err := Do(context.Background(), func() (int, error) {
		calls++
		if calls < 3 {
			return 0, geo.Wrap("test", "op", errTransient, true)
		}

		return 42, nil
	}, fastOpts()...)
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}

	if got != 42 {
		t.Fatalf("Do = %d, want 42", got)
	}

	if calls != 3 {
		t.Fatalf("op called %d times, want 3", calls)
	}
}

func TestDoStopsOnPermanentError(t *testing.T) {
	t.Parallel()

	sentinel := errPermanentOp
	calls := 0

	_, err := Do(context.Background(), func() (int, error) {
		calls++

		return 0, geo.Wrap("test", "op", sentinel, false)
	}, fastOpts()...)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Do error = %v, want it to wrap the sentinel", err)
	}

	if calls != 1 {
		t.Fatalf("op called %d times, want 1", calls)
	}
}

func TestDoDoesNotRetryPermissionDenied(t *testing.T) {
	t.Parallel()

	calls := 0

	// The error is flagged temporary, but a permission denial must still stop
	// at once: no amount of retrying changes a decision only the user can make.
	_, err := Do(context.Background(), func() (int, error) {
		calls++

		return 0, geo.Wrap("test", "op", geo.ErrPermissionDenied, true)
	}, fastOpts()...)
	if !errors.Is(err, geo.ErrPermissionDenied) {
		t.Fatalf("Do error = %v, want ErrPermissionDenied", err)
	}

	if calls != 1 {
		t.Fatalf("op called %d times, want 1", calls)
	}
}

func TestDoStopsWhenContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	_, err := Do(ctx, func() (int, error) {
		calls++

		cancel() // cancel after the first attempt

		return 0, geo.Wrap("test", "op", errTransient, true)
	}, fastOpts()...)
	if err == nil {
		t.Fatal("Do returned nil error, want the cancellation to stop it")
	}

	if calls != 1 {
		t.Fatalf("op called %d times, want 1 (no retry after cancel)", calls)
	}
}

func TestDoErrRunsOnceOnSuccess(t *testing.T) {
	t.Parallel()

	calls := 0

	err := DoErr(context.Background(), func() error {
		calls++

		return nil
	}, fastOpts()...)
	if err != nil {
		t.Fatalf("DoErr error: %v", err)
	}

	if calls != 1 {
		t.Fatalf("op called %d times, want 1", calls)
	}
}

func TestDoInvokesNotifyOnRetry(t *testing.T) {
	t.Parallel()

	notifications := 0
	calls := 0

	got, err := Do(context.Background(), func() (int, error) {
		calls++
		if calls < 2 {
			return 0, geo.Wrap("test", "op", errTransient, true)
		}

		return 7, nil
	},
		WithInitialInterval(time.Millisecond),
		WithMaxInterval(2*time.Millisecond),
		WithMultiplier(2),
		WithRandomizationFactor(0),
		WithMaxElapsedTime(time.Minute),
		WithNotify(func(error, time.Duration) { notifications++ }),
	)
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}

	if got != 7 {
		t.Fatalf("Do = %d, want 7", got)
	}

	if notifications != 1 {
		t.Fatalf("notify called %d times, want 1", notifications)
	}
}

func TestDoRespectsMaxTries(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := Do(context.Background(), func() (int, error) {
		calls++

		return 0, geo.Wrap("test", "op", errTransient, true)
	}, WithInitialInterval(time.Millisecond),
		WithMaxInterval(2*time.Millisecond),
		WithRandomizationFactor(0),
		WithMaxTries(2))
	if err == nil {
		t.Fatal("Do returned nil error, want exhaustion after the try budget")
	}

	if calls != 2 {
		t.Fatalf("op called %d times, want 2", calls)
	}
}

func TestRetryable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"temporary geo error", geo.Wrap("p", "op", errUnclassified, true), true},
		{"permanent geo error", geo.Wrap("p", "op", errUnclassified, false), false},
		{
			"permission denied even if temporary",
			geo.Wrap("p", "op", geo.ErrPermissionDenied, true),
			false,
		},
		{"invalid config sentinel", geo.ErrInvalidConfig, false},
		{"unclassified error", errUnclassified, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := Retryable(tc.err); got != tc.want {
				t.Fatalf("Retryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
