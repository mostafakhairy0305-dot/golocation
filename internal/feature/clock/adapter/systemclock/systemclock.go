// Package systemclock implements clock.Clock with the machine's wall clock.
package systemclock

import (
	"time"

	clock "github.com/mostafakhairy0305-dot/golocation/internal/feature/clock/port"
)

// Clock is the real clock. The zero value is ready to use, and it carries no
// state, so one value can be shared by every feature that needs the time.
type Clock struct{}

var _ clock.Clock = Clock{}

func (Clock) Now() time.Time { return time.Now().UTC() }
