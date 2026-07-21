package unsupported

import (
	"errors"
	"testing"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// Returning a nil Provider alongside the error is deliberate — there is
// nothing to start — so the contract worth pinning is that a caller can never
// be handed a Provider it would then try to Start.
func TestNewFailsWithTheUnsupportedSentinelAndNoProvider(t *testing.T) {
	provider, err := New("plan9")

	if provider != nil {
		t.Fatalf("Provider = %v, want nil", provider)
	}

	if !errors.Is(err, geo.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}

	var annotated *geo.Error
	if !errors.As(err, &annotated) {
		t.Fatalf("error = %v, want a *geo.Error", err)
	}

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
