package unsupported_test

import (
	"errors"
	"testing"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/unsupported"
)

// Returning nothing but the refusal is deliberate — there is no provider to
// start — so the contract worth pinning is that the refusal names the platform,
// the operation, and whether retrying could ever help.
func TestNewFailsWithTheUnsupportedSentinel(t *testing.T) {
	t.Parallel()

	err := unsupported.New("plan9")

	if !errors.Is(err, geo.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}

	var annotated *geo.Error
	if !errors.As(err, &annotated) {
		t.Fatalf("error = %v, want a *geo.Error", err)
	}

	expectAnnotation(t, annotated)
}

// expectAnnotation fails for every piece of context the refusal should carry.
func expectAnnotation(t *testing.T, annotated *geo.Error) {
	t.Helper()

	if annotated.Platform != "plan9" {
		t.Errorf("Platform = %q, want %q", annotated.Platform, "plan9")
	}

	if annotated.Op != "open" {
		t.Errorf("Op = %q, want %q", annotated.Op, "open")
	}
	// Retrying will not make the platform supported, so a caller backing off
	// on Temporary must not loop here forever.
	if annotated.Temporary {
		t.Errorf("Temporary = true, want false")
	}
}
