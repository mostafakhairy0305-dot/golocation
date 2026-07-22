package retry

import (
	"time"

	"github.com/cenkalti/backoff/v7"
)

// The values a zero option set resolves to. initial and maxInterval bound the
// exponential curve, multiplier is its growth per attempt, and randomization is
// the jitter fraction. maxElapsed and maxTries stay unset so the caller's
// context is the only bound until an option narrows it.
const (
	defaultInitialInterval = 500 * time.Millisecond
	defaultMaxInterval     = 30 * time.Second
	defaultMultiplier      = 1.5
	defaultRandomization   = 0.5
)

// config is the resolved retry configuration an Option mutates.
type config struct {
	initial       time.Duration
	maxInterval   time.Duration
	multiplier    float64
	randomization float64
	maxElapsed    time.Duration
	maxTries      uint
	notify        backoff.Notify
}

func defaults() config {
	return config{
		initial:       defaultInitialInterval,
		maxInterval:   defaultMaxInterval,
		multiplier:    defaultMultiplier,
		randomization: defaultRandomization,
		maxElapsed:    0,
		maxTries:      0,
		notify:        nil,
	}
}

// Option configures [Do].
type Option func(*config)

// WithInitialInterval sets the first backoff interval.
func WithInitialInterval(d time.Duration) Option {
	return func(c *config) { c.initial = d }
}

// WithMaxInterval caps how large a single backoff interval can grow.
func WithMaxInterval(d time.Duration) Option {
	return func(c *config) { c.maxInterval = d }
}

// WithMultiplier sets the factor the interval grows by after each attempt.
func WithMultiplier(m float64) Option {
	return func(c *config) { c.multiplier = m }
}

// WithRandomizationFactor sets the jitter fraction applied to each interval.
// Zero removes jitter, which suits tests that assert on timing.
func WithRandomizationFactor(f float64) Option {
	return func(c *config) { c.randomization = f }
}

// WithMaxElapsedTime bounds the total wall-clock time spent retrying. Zero, the
// default, leaves the caller's context as the only bound.
func WithMaxElapsedTime(d time.Duration) Option {
	return func(c *config) { c.maxElapsed = d }
}

// WithMaxTries caps the number of attempts. WithMaxTries(1) runs op once and
// never retries; zero, the default, means no attempt limit.
func WithMaxTries(n uint) Option {
	return func(c *config) { c.maxTries = n }
}

// WithNotify registers a function called after each failed attempt that will be
// retried, with the error and the delay before the next attempt.
func WithNotify(n backoff.Notify) Option {
	return func(c *config) { c.notify = n }
}
