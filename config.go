package location

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// LinuxConfig configures the GeoClue backend. The knobs are public because
// callers need to reach them, but the adapter that consumes them owns its own
// options type — nothing under internal/adapter imports this package.
//
// Every field is optional: normalizeConfig fills each unset one from
// DefaultConfig individually, so a caller overrides one knob and inherits the
// rest.
type LinuxConfig struct {
	DesktopID string `exhaustruct:"optional"`

	Reconnect    bool          `exhaustruct:"optional"`
	ReconnectMin time.Duration `exhaustruct:"optional"`
	ReconnectMax time.Duration `exhaustruct:"optional"`
}

// Config configures the cross-platform locator. Every field is optional —
// normalizeConfig defaults each unset one — so the zero Config is valid and a
// caller sets only what it cares about.
type Config struct {
	Accuracy              Accuracy `exhaustruct:"optional"`
	DesiredAccuracyMeters uint32   `exhaustruct:"optional"`

	// Both filters are enforced by the common layer. Native backends also receive
	// the values where the operating system exposes equivalent controls.
	MinimumInterval       time.Duration `exhaustruct:"optional"`
	MinimumDistanceMeters float64       `exhaustruct:"optional"`

	// MaximumAge rejects provider samples older than this duration. Zero means
	// "use the default" rather than "no limit": normalization always resolves
	// it to a positive value, so there is no way to disable the age check.
	MaximumAge time.Duration `exhaustruct:"optional"`

	// StartTimeout bounds native service startup and an optional permission prompt.
	StartTimeout time.Duration `exhaustruct:"optional"`

	Permission PermissionMode `exhaustruct:"optional"`

	DefaultChannelBuffer int        `exhaustruct:"optional"`
	DefaultDropPolicy    DropPolicy `exhaustruct:"optional"`

	Linux LinuxConfig `exhaustruct:"optional"`
}

// The defaults DefaultConfig hands out, named so that the trade-off behind each
// one has somewhere to be written down.
const (
	// defaultMaximumAge is how old a sample may be and still be served from the
	// cache. Two minutes is long enough to cover a provider that has gone quiet
	// indoors, and short enough that nobody is handed a stale city block.
	defaultMaximumAge = 2 * time.Minute
	// defaultStartTimeout bounds native startup, which on first run includes a
	// permission prompt a human has to answer.
	defaultStartTimeout = 30 * time.Second
	// defaultReconnectMax caps the GeoClue backoff, so a desktop that suspends
	// for an hour still reconnects within half a minute of waking.
	defaultReconnectMax = 30 * time.Second
)

// DefaultConfig returns production-oriented defaults.
func DefaultConfig() Config {
	return Config{
		Accuracy:             AccuracyBalanced,
		MinimumInterval:      time.Second,
		MaximumAge:           defaultMaximumAge,
		StartTimeout:         defaultStartTimeout,
		Permission:           PermissionAuto,
		DefaultChannelBuffer: 1,
		DefaultDropPolicy:    DropOldest,
		Linux: LinuxConfig{
			DesktopID:    defaultDesktopID(),
			Reconnect:    true,
			ReconnectMin: time.Second,
			ReconnectMax: defaultReconnectMax,
		},
	}
}

// normalizeConfig fills unset fields from DefaultConfig and rejects values the
// locator cannot honour. Every defaultable field is defaulted individually, so
// setting one field never silently clears another.
// The defaulting and validation steps are separate functions so that each one
// states a single rule. Defaulting runs first and in full, because a validator
// must judge the value the locator will actually use, not the one the caller
// happened to leave unset.
func normalizeConfig(in Config) (Config, error) {
	defaults := DefaultConfig()

	out := withTimingDefaults(in, defaults)
	out = withDeliveryDefaults(out, defaults)
	out = withLinuxDefaults(out, defaults)

	checks := []func(Config) error{
		validateEnums,
		validateDurations,
		validateDistance,
		validateBuffer,
		validateLinuxReconnect,
		validateLinuxIdentity,
	}

	for _, check := range checks {
		err := check(out)
		if err != nil {
			return Config{}, err
		}
	}

	return out, nil
}

func withTimingDefaults(out, defaults Config) Config {
	if out.MinimumInterval == 0 {
		out.MinimumInterval = defaults.MinimumInterval
	}

	if out.MaximumAge == 0 {
		out.MaximumAge = defaults.MaximumAge
	}

	if out.StartTimeout == 0 {
		out.StartTimeout = defaults.StartTimeout
	}

	return out
}

func withDeliveryDefaults(out, defaults Config) Config {
	if out.DefaultChannelBuffer == 0 {
		out.DefaultChannelBuffer = defaults.DefaultChannelBuffer
	}

	if out.DefaultDropPolicy == DropDefault {
		out.DefaultDropPolicy = defaults.DefaultDropPolicy
	}

	return out
}

func withLinuxDefaults(out, defaults Config) Config {
	if out.Linux.DesktopID == "" {
		out.Linux.DesktopID = defaults.Linux.DesktopID
	}

	if out.Linux.ReconnectMin == 0 {
		out.Linux.ReconnectMin = defaults.Linux.ReconnectMin
	}

	if out.Linux.ReconnectMax == 0 {
		out.Linux.ReconnectMax = defaults.Linux.ReconnectMax
	}

	return out
}

// validateEnums rejects values past the last name each enum defines. They are
// checked after defaulting, so a zero that meant "use the default" has already
// become a real value.
func validateEnums(out Config) error {
	if out.Accuracy > AccuracyNavigation {
		return fmt.Errorf("%w: unknown accuracy value %d", geo.ErrInvalidConfig, out.Accuracy)
	}

	if out.Permission > PermissionDoNotRequest {
		return fmt.Errorf("%w: unknown permission mode %d", geo.ErrInvalidConfig, out.Permission)
	}

	if out.DefaultDropPolicy > DropNewest {
		return fmt.Errorf(
			"%w: unknown drop policy %d",
			geo.ErrInvalidConfig,
			out.DefaultDropPolicy,
		)
	}

	return nil
}

func validateDurations(out Config) error {
	if out.MinimumInterval < 0 || out.MaximumAge < 0 || out.StartTimeout < 0 {
		return fmt.Errorf("%w: durations cannot be negative", geo.ErrInvalidConfig)
	}

	return nil
}

func validateDistance(out Config) error {
	if math.IsNaN(out.MinimumDistanceMeters) || math.IsInf(out.MinimumDistanceMeters, 0) ||
		out.MinimumDistanceMeters < 0 {
		return fmt.Errorf(
			"%w: minimum distance must be finite and non-negative",
			geo.ErrInvalidConfig,
		)
	}

	return nil
}

func validateBuffer(out Config) error {
	if out.DefaultChannelBuffer < 1 {
		return fmt.Errorf("%w: default channel buffer must be at least 1", geo.ErrInvalidConfig)
	}

	return nil
}

func validateLinuxReconnect(out Config) error {
	if out.Linux.ReconnectMin < 0 || out.Linux.ReconnectMax < 0 ||
		out.Linux.ReconnectMax < out.Linux.ReconnectMin {
		return fmt.Errorf("%w: invalid Linux reconnect range", geo.ErrInvalidConfig)
	}

	return nil
}

func validateLinuxIdentity(out Config) error {
	if strings.TrimSpace(out.Linux.DesktopID) == "" {
		return fmt.Errorf("%w: Linux desktop ID cannot be empty", geo.ErrInvalidConfig)
	}

	return nil
}

// fallbackDesktopID is the GeoClue identity used when the running executable
// cannot supply one. GeoClue rejects an empty ID, so there must always be a
// name to fall back to.
const fallbackDesktopID = "golocation"

func defaultDesktopID() string { return desktopIDFrom(os.Executable()) }

// desktopIDFrom derives the GeoClue desktop ID from the running executable.
// It takes os.Executable's pair rather than calling it, because both fallbacks
// below depend on how the process was started — a deleted or unnamed binary —
// and neither is reachable from a test that has to run as a normal test
// binary.
func desktopIDFrom(executable string, err error) string {
	if err != nil {
		return fallbackDesktopID
	}

	name := filepath.Base(executable)

	name = strings.TrimSuffix(name, filepath.Ext(name))
	if name == "" {
		return fallbackDesktopID
	}

	return name
}
