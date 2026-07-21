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
type LinuxConfig struct {
	DesktopID string

	Reconnect    bool
	ReconnectMin time.Duration
	ReconnectMax time.Duration
}

// Config configures the cross-platform locator.
type Config struct {
	Accuracy              Accuracy
	DesiredAccuracyMeters uint32

	// Both filters are enforced by the common layer. Native backends also receive
	// the values where the operating system exposes equivalent controls.
	MinimumInterval       time.Duration
	MinimumDistanceMeters float64

	// MaximumAge rejects provider samples older than this duration. Zero means
	// "use the default" rather than "no limit": normalization always resolves
	// it to a positive value, so there is no way to disable the age check.
	MaximumAge time.Duration

	// StartTimeout bounds native service startup and an optional permission prompt.
	StartTimeout time.Duration

	Permission PermissionMode

	DefaultChannelBuffer int
	DefaultDropPolicy    DropPolicy

	Linux LinuxConfig
}

// DefaultConfig returns production-oriented defaults.
func DefaultConfig() Config {
	return Config{
		Accuracy:             AccuracyBalanced,
		MinimumInterval:      time.Second,
		MaximumAge:           2 * time.Minute,
		StartTimeout:         30 * time.Second,
		Permission:           PermissionAuto,
		DefaultChannelBuffer: 1,
		DefaultDropPolicy:    DropOldest,
		Linux: LinuxConfig{
			DesktopID:    defaultDesktopID(),
			Reconnect:    true,
			ReconnectMin: time.Second,
			ReconnectMax: 30 * time.Second,
		},
	}
}

// normalizeConfig fills unset fields from DefaultConfig and rejects values the
// locator cannot honour. Every defaultable field is defaulted individually, so
// setting one field never silently clears another.
func normalizeConfig(in Config) (Config, error) {
	defaults := DefaultConfig()
	out := in

	if out.MinimumInterval == 0 {
		out.MinimumInterval = defaults.MinimumInterval
	}
	if out.MaximumAge == 0 {
		out.MaximumAge = defaults.MaximumAge
	}
	if out.StartTimeout == 0 {
		out.StartTimeout = defaults.StartTimeout
	}
	if out.DefaultChannelBuffer == 0 {
		out.DefaultChannelBuffer = defaults.DefaultChannelBuffer
	}
	if out.Linux.DesktopID == "" {
		out.Linux.DesktopID = defaults.Linux.DesktopID
	}
	if out.Linux.ReconnectMin == 0 {
		out.Linux.ReconnectMin = defaults.Linux.ReconnectMin
	}
	if out.Linux.ReconnectMax == 0 {
		out.Linux.ReconnectMax = defaults.Linux.ReconnectMax
	}
	if out.Accuracy > AccuracyNavigation {
		return Config{}, fmt.Errorf("%w: unknown accuracy value %d", geo.ErrInvalidConfig, out.Accuracy)
	}
	if out.Permission > PermissionDoNotRequest {
		return Config{}, fmt.Errorf("%w: unknown permission mode %d", geo.ErrInvalidConfig, out.Permission)
	}
	if out.DefaultDropPolicy == DropDefault {
		out.DefaultDropPolicy = defaults.DefaultDropPolicy
	}
	if out.DefaultDropPolicy > DropNewest {
		return Config{}, fmt.Errorf("%w: unknown drop policy %d", geo.ErrInvalidConfig, out.DefaultDropPolicy)
	}
	if out.MinimumInterval < 0 || out.MaximumAge < 0 || out.StartTimeout < 0 {
		return Config{}, fmt.Errorf("%w: durations cannot be negative", geo.ErrInvalidConfig)
	}
	if math.IsNaN(out.MinimumDistanceMeters) || math.IsInf(out.MinimumDistanceMeters, 0) || out.MinimumDistanceMeters < 0 {
		return Config{}, fmt.Errorf("%w: minimum distance must be finite and non-negative", geo.ErrInvalidConfig)
	}
	if out.DefaultChannelBuffer < 1 {
		return Config{}, fmt.Errorf("%w: default channel buffer must be at least 1", geo.ErrInvalidConfig)
	}
	if out.Linux.ReconnectMin < 0 || out.Linux.ReconnectMax < 0 || out.Linux.ReconnectMax < out.Linux.ReconnectMin {
		return Config{}, fmt.Errorf("%w: invalid Linux reconnect range", geo.ErrInvalidConfig)
	}
	if strings.TrimSpace(out.Linux.DesktopID) == "" {
		return Config{}, fmt.Errorf("%w: Linux desktop ID cannot be empty", geo.ErrInvalidConfig)
	}

	return out, nil
}

func defaultDesktopID() string { return desktopIDFrom(os.Executable()) }

// desktopIDFrom derives the GeoClue desktop ID from the running executable.
// It takes os.Executable's pair rather than calling it, because both fallbacks
// below depend on how the process was started — a deleted or unnamed binary —
// and neither is reachable from a test that has to run as a normal test
// binary.
func desktopIDFrom(executable string, err error) string {
	if err != nil {
		return "golocation"
	}
	name := filepath.Base(executable)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if name == "" {
		return "golocation"
	}
	return name
}
