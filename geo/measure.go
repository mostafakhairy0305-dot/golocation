package geo

import (
	"fmt"
	"math"
	"time"
)

// MaxClockSkew is how far a provider timestamp may run ahead of the local
// clock and still count as fresh. Native providers stamp fixes with their own
// clock, which can drift ahead of ours between NTP corrections, so a fix from
// the immediate future is treated as current rather than discarded.
const MaxClockSkew = time.Minute

// earthRadius is the IUGG mean radius, in metres.
const earthRadius = 6371008.8

const degreesToRadians = math.Pi / 180

// Validate reports whether a fix carries usable coordinates. Optional fields
// are not checked: their presence is advertised through Fix.Fields.
//
// Each check is a single bounded-range test rather than a separate NaN, Inf,
// and range test, because the range test already subsumes the other two: every
// comparison against NaN is false, and an infinity always falls outside a
// finite bound. Negating the "is in range" form is what makes NaN reject —
// writing it as `v < low || v > high` would let NaN through.
func Validate(fix Fix) error {
	if !(fix.Latitude >= -90 && fix.Latitude <= 90) {
		return fmt.Errorf("invalid latitude %v", fix.Latitude)
	}

	if !(fix.Longitude >= -180 && fix.Longitude <= 180) {
		return fmt.Errorf("invalid longitude %v", fix.Longitude)
	}

	if !(fix.AccuracyMeters >= 0 && fix.AccuracyMeters <= math.MaxFloat64) {
		return fmt.Errorf("invalid horizontal accuracy %v", fix.AccuracyMeters)
	}

	return nil
}

// Distance returns the great-circle distance between two fixes, in metres.
// Every distance-filtered fix goes through it, so each half-angle sine is
// computed once and squared rather than recomputed.
//
// Both deltas are taken in degrees and converted afterwards, never by
// subtracting two already-converted radian values. Converting first loses the
// cancellation: two equal latitudes then differ in the last bit or two, and
// what should be a flat zero comes back as a fraction of a nanometre.
func Distance(a, b Fix) float64 {
	lat1 := a.Latitude * degreesToRadians
	lat2 := b.Latitude * degreesToRadians
	halfLat := math.Sin((b.Latitude - a.Latitude) * degreesToRadians / 2)
	halfLon := math.Sin((b.Longitude - a.Longitude) * degreesToRadians / 2)
	h := halfLat*halfLat + math.Cos(lat1)*math.Cos(lat2)*halfLon*halfLon

	return 2 * earthRadius * math.Asin(math.Sqrt(h))
}

// IsFresh reports whether a fix is recent enough to serve. A maxAge of zero
// disables the check. Fixes timestamped up to MaxClockSkew in the future are
// still considered fresh.
func IsFresh(fix Fix, maxAge time.Duration, now time.Time) bool {
	if maxAge == 0 {
		return true
	}

	age := now.Sub(fix.Timestamp)

	return age <= maxAge && age >= -MaxClockSkew
}
