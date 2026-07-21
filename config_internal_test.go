package location

import (
	"errors"
	"testing"
	"time"
)

// The failure os.Executable reports when the platform cannot name the running
// binary, which is what sends desktopIDFrom to its fallback.
var errNoExecutablePath = errors.New("readlink /proc/self/exe: no such file")

// Setting one field used to skip the defaults for every other field, because
// MinimumInterval and MaximumAge were only filled in on the all-zero shortcut
// path. A zero MaximumAge disables the staleness check entirely, so this
// regressed Current into serving arbitrarily old fixes.
func TestNormalizeConfigDefaultsEveryFieldIndependently(t *testing.T) {
	t.Parallel()

	defaults := DefaultConfig()

	cases := []struct {
		name string
		in   Config
	}{
		{"zero value", Config{}},
		{"one unrelated field set", Config{Accuracy: AccuracyHigh}},
		{"buffer set", Config{DefaultChannelBuffer: 4}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := normalizeConfig(tc.in)
			if err != nil {
				t.Fatalf("normalizeConfig: %v", err)
			}

			expectDefaults(t, out, defaults)
		})
	}
}

// expectDefaults fails for every field normalizeConfig should have filled in,
// reporting all of them rather than stopping at the first: which fields a
// half-applied default missed is the whole diagnosis.
func expectDefaults(t *testing.T, out, defaults Config) {
	t.Helper()

	type durations struct{ got, want time.Duration }

	for name, field := range map[string]durations{
		"MinimumInterval": {out.MinimumInterval, defaults.MinimumInterval},
		"MaximumAge":      {out.MaximumAge, defaults.MaximumAge},
		"StartTimeout":    {out.StartTimeout, defaults.StartTimeout},
	} {
		if field.got != field.want {
			t.Errorf("%s = %v, want %v", name, field.got, field.want)
		}
	}

	if out.DefaultDropPolicy != defaults.DefaultDropPolicy {
		t.Errorf(
			"DefaultDropPolicy = %v, want %v",
			out.DefaultDropPolicy,
			defaults.DefaultDropPolicy,
		)
	}

	if out.Linux.DesktopID == "" {
		t.Error("Linux.DesktopID is empty")
	}
}

func TestNormalizeConfigKeepsExplicitValues(t *testing.T) {
	t.Parallel()

	explicit := Config{
		MinimumInterval: 5 * time.Second,
		MaximumAge:      time.Minute,
		StartTimeout:    2 * time.Second,
	}

	out, err := normalizeConfig(explicit)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}

	if out.MinimumInterval != 5*time.Second {
		t.Errorf("MinimumInterval = %v, want 5s", out.MinimumInterval)
	}

	if out.MaximumAge != time.Minute {
		t.Errorf("MaximumAge = %v, want 1m", out.MaximumAge)
	}

	if out.StartTimeout != 2*time.Second {
		t.Errorf("StartTimeout = %v, want 2s", out.StartTimeout)
	}
}

func TestNormalizeConfigRejectsInvalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   Config
	}{
		{"negative duration", Config{MinimumInterval: -time.Second}},
		{"unknown accuracy", Config{Accuracy: AccuracyNavigation + 1}},
		{"unknown permission mode", Config{Permission: PermissionDoNotRequest + 1}},
		{"unknown drop policy", Config{DefaultDropPolicy: DropNewest + 1}},
		{"negative distance", Config{MinimumDistanceMeters: -1}},
		{"negative buffer", Config{DefaultChannelBuffer: -1}},
		{"blank desktop ID", Config{Linux: LinuxConfig{DesktopID: "   "}}},
		{
			"inverted reconnect range",
			Config{Linux: LinuxConfig{ReconnectMin: time.Minute, ReconnectMax: time.Second}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := normalizeConfig(tc.in)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

// The desktop ID is what GeoClue looks this application up by, so an empty one
// is not a cosmetic problem: normalizeConfig rejects a blank ID outright, and
// a derivation that returned one would make Open fail on Linux for reasons the
// caller never configured. Both fallbacks exist to keep that from happening.
func TestDesktopIDAlwaysDerivesANonEmptyName(t *testing.T) {
	t.Parallel()

	const appName = "myapp"

	cases := map[string]struct {
		executable string `exhaustruct:"optional"`
		err        error  `exhaustruct:"optional"`
		want       string
	}{
		"a plain name": {executable: "/usr/local/bin/" + appName, want: appName},
		"an extension is dropped": {
			executable: "/usr/local/bin/" + appName + ".exe",
			want:       appName,
		},
		"no directory": {executable: appName, want: appName},
		"the executable is unknown": {
			err:  errNoExecutablePath,
			want: fallbackDesktopID,
		},
		// filepath.Ext of a dotfile is the whole name, so trimming it leaves
		// nothing behind — the one input that reaches the second fallback.
		"a dotfile leaves nothing behind": {
			executable: "/usr/local/bin/." + appName,
			want:       fallbackDesktopID,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := desktopIDFrom(tc.executable, tc.err); got != tc.want {
				t.Fatalf("desktopIDFrom(%q, %v) = %q, want %q", tc.executable, tc.err, got, tc.want)
			}
		})
	}
}

// Whatever the real executable is called, the derived ID has to survive
// normalizeConfig's own blank check — which is the only consumer that matters.
func TestTheDerivedDesktopIDIsAcceptedByNormalizeConfig(t *testing.T) {
	t.Parallel()

	out, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}

	if out.Linux.DesktopID == "" {
		t.Fatal("the defaulted desktop ID is empty")
	}

	if out.Linux.DesktopID != defaultDesktopID() {
		t.Fatalf("DesktopID = %q, want the derived %q", out.Linux.DesktopID, defaultDesktopID())
	}
}
