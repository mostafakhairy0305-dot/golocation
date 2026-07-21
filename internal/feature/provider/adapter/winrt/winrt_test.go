//go:build windows && (amd64 || arm64)

package winrt

import (
	"testing"
	"time"

	"github.com/deploymenttheory/go-bindings-winrt/bindings/winrt/devices/geolocation"
	"github.com/deploymenttheory/go-bindings-winrt/bindings/winrt/foundation"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// Source is the one field a caller uses to judge how much to trust a fix, and
// the two enumerations are numbered independently — so this mapping is exactly
// the kind that looks right and is off by one.
func TestMapWindowsSourceCoversEveryPositionSource(t *testing.T) {
	cases := map[string]struct {
		source geolocation.PositionSource
		want   geo.Source
	}{
		"cellular":   {source: geolocation.PositionSourceCellular, want: geo.SourceCellular},
		"satellite":  {source: geolocation.PositionSourceSatellite, want: geo.SourceSatellite},
		"wifi":       {source: geolocation.PositionSourceWiFi, want: geo.SourceWiFi},
		"ip address": {source: geolocation.PositionSourceIPAddress, want: geo.SourceIP},
		"default":    {source: geolocation.PositionSourceDefault, want: geo.SourceDefault},
		"obfuscated": {source: geolocation.PositionSourceObfuscated, want: geo.SourceObfuscated},
		"a source Windows added since": {
			source: geolocation.PositionSource(99),
			want:   geo.SourceUnknown,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mapWindowsSource(tc.source); got != tc.want {
				t.Fatalf("mapWindowsSource(%d) = %v, want %v", tc.source, got, tc.want)
			}
		})
	}
}

// WinRT counts in 100 ns ticks. Passing a Go nanosecond duration straight
// through would ask for a report interval 100 times shorter than requested,
// which the OS would honour.
func TestDurationToWinRTConvertsToHundredNanosecondTicks(t *testing.T) {
	cases := map[string]struct {
		duration time.Duration
		want     int64
	}{
		"a second":            {duration: time.Second, want: 10_000_000},
		"a millisecond":       {duration: time.Millisecond, want: 10_000},
		"one tick":            {duration: 100 * time.Nanosecond, want: 1},
		"below one tick":      {duration: 99 * time.Nanosecond, want: 0},
		"zero":                {duration: 0, want: 0},
		"a negative duration": {duration: -time.Second, want: -10_000_000},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := durationToWinRT(tc.duration); got != tc.want {
				t.Fatalf("durationToWinRT(%v) = %d, want %d", tc.duration, got, tc.want)
			}
		})
	}
}

// A WinRT DateTime counts from 1601, not 1970. Skipping the epoch shift would
// stamp every fix in the seventeenth century, and the freshness rules would
// then reject them all as stale rather than report a conversion bug.
func TestWinRTDateTimeShiftsFromThe1601Epoch(t *testing.T) {
	const ticksBetween1601And1970 = int64(116444736000000000)

	cases := map[string]struct {
		ticks int64
		want  time.Time
	}{
		"the unix epoch": {
			ticks: ticksBetween1601And1970,
			want:  time.Unix(0, 0).UTC(),
		},
		"one second after the unix epoch": {
			ticks: ticksBetween1601And1970 + 10_000_000,
			want:  time.Unix(1, 0).UTC(),
		},
		"a realistic timestamp": {
			ticks: ticksBetween1601And1970 + 1784030400*10_000_000,
			want:  time.Unix(1784030400, 0).UTC(),
		},
		"before the unix epoch": {
			ticks: ticksBetween1601And1970 - 10_000_000,
			want:  time.Unix(-1, 0).UTC(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := winRTDateTime(foundation.DateTime{UniversalTime: tc.ticks})
			if !got.Equal(tc.want) {
				t.Fatalf("winRTDateTime = %v, want %v", got, tc.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("Location = %v, want UTC", got.Location())
			}
		})
	}
}
