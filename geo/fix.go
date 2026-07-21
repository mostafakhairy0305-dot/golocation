// Package geo holds the domain values shared by every layer of the locator:
// location samples, service status, and the error vocabulary. It depends only
// on the standard library and carries no build tags, so it is identical on
// every platform.
package geo

import "time"

// Field reports which optional Fix fields contain provider data.
type Field uint32

// The optional fields, one bit each. A Fix reports the ones it carries through
// Has; the rest hold zero and mean nothing.
const (
	FieldAltitude Field = 1 << iota
	FieldVerticalAccuracy
	FieldSpeed
	FieldHeading
)

// Source is the provider category reported by the operating system.
type Source uint8

// The provider categories. SourceUnknown is the zero value, for a platform
// that does not say where a fix came from.
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
// Every field is tagged optional: a Fix is a value type that callers build
// incrementally and that adapters return zero on failure, so a partial literal
// is a normal thing to write. Validate, not the compiler, is what enforces the
// invariant above.
type Fix struct {
	Timestamp  time.Time `exhaustruct:"optional"`
	ReceivedAt time.Time `exhaustruct:"optional"`

	Latitude  float64 `exhaustruct:"optional"`
	Longitude float64 `exhaustruct:"optional"`

	AccuracyMeters         float64 `exhaustruct:"optional"`
	AltitudeMeters         float64 `exhaustruct:"optional"`
	VerticalAccuracyMeters float64 `exhaustruct:"optional"`
	SpeedMetersPerSecond   float64 `exhaustruct:"optional"`
	HeadingDegrees         float64 `exhaustruct:"optional"`

	Source Source `exhaustruct:"optional"`
	Fields Field  `exhaustruct:"optional"`
}

// Has reports whether an optional field is valid.
func (f Fix) Has(field Field) bool { return f.Fields&field != 0 }

// Age returns the provider timestamp age relative to now.
func (f Fix) Age(now time.Time) time.Duration { return now.Sub(f.Timestamp) }
