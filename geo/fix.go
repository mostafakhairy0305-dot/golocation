// Package geo holds the domain values shared by every layer of the locator:
// location samples, service status, and the error vocabulary. It depends only
// on the standard library and carries no build tags, so it is identical on
// every platform.
package geo

import "time"

// Field reports which optional Fix fields contain provider data.
type Field uint32

const (
	FieldAltitude Field = 1 << iota
	FieldVerticalAccuracy
	FieldSpeed
	FieldHeading
)

// Source is the provider category reported by the operating system.
type Source uint8

const (
	SourceUnknown Source = iota
	SourceSystem
	SourceSatellite
	SourceWiFi
	SourceCellular
	SourceIP
	SourceDefault
	SourceManual
	SourceRemote
	SourceObfuscated
)

// Fix is one location sample. Latitude, Longitude, AccuracyMeters,
// Timestamp, and ReceivedAt are always populated for accepted fixes.
type Fix struct {
	Timestamp  time.Time
	ReceivedAt time.Time

	Latitude  float64
	Longitude float64

	AccuracyMeters         float64
	AltitudeMeters         float64
	VerticalAccuracyMeters float64
	SpeedMetersPerSecond   float64
	HeadingDegrees         float64

	Source Source
	Fields Field
}

// Has reports whether an optional field is valid.
func (f Fix) Has(field Field) bool { return f.Fields&field != 0 }

// Age returns the provider timestamp age relative to now.
func (f Fix) Age(now time.Time) time.Duration { return now.Sub(f.Timestamp) }
