// Package retry runs fallible operations under exponential backoff with jitter,
// retrying only the failures the geo layer marked temporary.
//
// It is a thin, opinionated wrapper over github.com/cenkalti/backoff/v7. The
// value it adds is the retry/stop decision: an operation that fails with a
// temporary *geo.Error is worth another attempt, while a permission,
// configuration, or otherwise permanent failure is not and must never be
// looped. Do encodes that rule once so every call site inherits it, which is
// what makes it safe to wrap even an operation that can only ever fail
// permanently — it simply runs once.
package retry

import (
	"context"
	"errors"

	"github.com/cenkalti/backoff/v7"
	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// Do runs op under exponential backoff with jitter and returns its result.
//
// op is retried only while it fails with a retryable error (see [Retryable]):
// a *geo.Error whose Temporary flag is set. Any other failure — a permanent
// geo.Error, a permission or configuration sentinel, or an error the geo layer
// never classified — stops Do at once, so op runs exactly once. op always runs
// at least once.
//
// Retrying is bounded by ctx: its cancellation or deadline stops further
// attempts and interrupts the wait between them. op captures ctx itself, so a
// cancellation aborts an in-flight attempt only if op observes it. On failure
// Do returns the last error op returned, not the backoff bookkeeping error, so
// a caller sees the same error it would without the wrapper and errors.Is and
// errors.As keep matching the geo sentinels.
func Do[T any](ctx context.Context, operation func() (T, error), opts ...Option) (T, error) {
	cfg := defaults()
	for _, apply := range opts {
		apply(&cfg)
	}

	// lastErr captures the operation's own final error. backoff.Retry calls the
	// operation synchronously, so this is not racy, and it lets Do return the
	// error the caller expects rather than the "backoff: ... (last error: ...)"
	// bookkeeping wrapper.
	var lastErr error

	value, err := backoff.Retry(ctx, func() (T, error) {
		value, opErr := operation()
		lastErr = opErr

		if opErr != nil && !Retryable(opErr) {
			return value, backoff.Permanent(opErr)
		}

		return value, opErr
	}, cfg.retryOptions()...)
	if err != nil {
		return value, lastErr
	}

	return value, nil
}

// retryOptions turns the resolved config into the backoff.RetryOption set Do
// hands to backoff.Retry.
func (c config) retryOptions() []backoff.RetryOption {
	policy := backoff.NewExponentialBackOff()
	policy.InitialInterval = c.initial
	policy.MaxInterval = c.maxInterval
	policy.Multiplier = c.multiplier
	policy.RandomizationFactor = c.randomization

	opts := []backoff.RetryOption{
		backoff.WithBackOff(policy),
		// Zero disables the library's 15-minute default, leaving ctx as the only
		// wall-clock bound unless a caller narrows it with WithMaxElapsedTime.
		backoff.WithMaxElapsedTime(c.maxElapsed),
	}

	if c.maxTries > 0 {
		opts = append(opts, backoff.WithMaxTries(c.maxTries))
	}

	if c.notify != nil {
		opts = append(opts, backoff.WithNotify(c.notify))
	}

	return opts
}

// DoErr is [Do] for an operation that returns only an error.
func DoErr(ctx context.Context, operation func() error, opts ...Option) error {
	_, err := Do(ctx, func() (struct{}, error) {
		return struct{}{}, operation()
	}, opts...)

	return err
}

// Retryable reports whether err is worth another attempt. Only a *geo.Error
// with Temporary set is: the geo layer marks an error temporary exactly when a
// later attempt might succeed. A permission, configuration, unsupported, or
// closed sentinel is never retryable even if some layer wrapped it as
// temporary, and an error the geo layer never classified is treated as
// permanent so an unknown failure cannot become a busy loop.
func Retryable(err error) bool {
	switch {
	case err == nil,
		errors.Is(err, geo.ErrPermissionDenied),
		errors.Is(err, geo.ErrPermissionNeeded),
		errors.Is(err, geo.ErrInvalidConfig),
		errors.Is(err, geo.ErrUnsupported),
		errors.Is(err, geo.ErrClosed):
		return false
	}

	var e *geo.Error
	if errors.As(err, &e) {
		return e.Temporary
	}

	return false
}
