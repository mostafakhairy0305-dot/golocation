package atomicstate

import (
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/clock/adapter/fixedclock"
)

func newTracker() *Tracker {
	return New(
		geo.Status{State: geo.StateStarting, Permission: geo.PermissionUnknown},
		fixedclock.New(time.Now()),
	)
}

func TestRestatingAStatusIsNotAChange(t *testing.T) {
	tracker := newTracker()
	status := geo.Status{State: geo.StateReady, Permission: geo.PermissionGranted, Message: "ready"}

	if _, changed := tracker.Set(status); !changed {
		t.Fatal("the first Set reported no change")
	}
	// A different UpdatedAt must not count: a restated status says nothing new.
	status.UpdatedAt = time.Now().Add(time.Hour)
	if _, changed := tracker.Set(status); changed {
		t.Fatal("restating the same status reported a change")
	}
}

func TestUnknownPermissionInheritsWhatWeAlreadyKnow(t *testing.T) {
	tracker := newTracker()
	if _, changed := tracker.Set(
		geo.Status{State: geo.StateReady, Permission: geo.PermissionGranted},
	); !changed {
		t.Fatal("Set reported no change")
	}

	// A backend reporting only a state change leaves permission unknown.
	recorded, _ := tracker.Set(geo.Status{State: geo.StateReconnecting})
	if recorded.Permission != geo.PermissionGranted {
		t.Fatalf("permission = %v, want the inherited PermissionGranted", recorded.Permission)
	}
}

func TestMarkReadySettlesThePermissionQuestion(t *testing.T) {
	for _, before := range []geo.PermissionState{geo.PermissionUnknown, geo.PermissionPromptRequired} {
		tracker := New(
			geo.Status{State: geo.StateStarting, Permission: before},
			fixedclock.New(time.Now()),
		)

		recorded, changed := tracker.MarkReady("receiving locations")
		if !changed {
			t.Fatalf("MarkReady from %v reported no change", before)
		}

		if recorded.State != geo.StateReady {
			t.Fatalf("state = %v, want StateReady", recorded.State)
		}

		if recorded.Permission != geo.PermissionGranted {
			t.Fatalf(
				"permission = %v, want PermissionGranted: a fix cannot arrive without access",
				recorded.Permission,
			)
		}
	}
}

func TestMarkReadyLeavesADeniedPermissionAlone(t *testing.T) {
	tracker := New(
		geo.Status{State: geo.StateDisabled, Permission: geo.PermissionRestricted},
		fixedclock.New(time.Now()),
	)

	recorded, _ := tracker.MarkReady("receiving locations")
	if recorded.Permission != geo.PermissionRestricted {
		t.Fatalf("permission = %v, want the reported PermissionRestricted", recorded.Permission)
	}
}

func TestMarkReadyTwiceReportsOneChange(t *testing.T) {
	tracker := newTracker()
	if _, changed := tracker.MarkReady("receiving locations"); !changed {
		t.Fatal("the first MarkReady reported no change")
	}

	if _, changed := tracker.MarkReady("receiving locations"); changed {
		t.Fatal("MarkReady reported a change on the second call")
	}
}

func TestStateMatchesTheRecordedStatus(t *testing.T) {
	tracker := newTracker()
	if tracker.State() != geo.StateStarting {
		t.Fatalf("initial state = %v, want StateStarting", tracker.State())
	}

	tracker.Set(geo.Status{State: geo.StateUnavailable})

	if tracker.State() != tracker.Get().State {
		t.Fatalf("State() = %v but Get().State = %v", tracker.State(), tracker.Get().State)
	}
}

func TestNewStampsAnUnsetTime(t *testing.T) {
	if got := New(
		geo.Status{State: geo.StateStarting},
		fixedclock.New(time.Now()),
	).Get().
		UpdatedAt; got.IsZero() {
		t.Fatal("UpdatedAt was left zero")
	}

	if got, _ := newTracker().Set(geo.Status{State: geo.StateReady}); got.UpdatedAt.IsZero() {
		t.Fatal("Set left UpdatedAt zero")
	}
}
