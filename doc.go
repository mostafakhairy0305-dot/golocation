// Package location exposes one cross-platform location API backed by the
// native provider on each operating system: CoreLocation on macOS, GeoClue2 on
// Linux, and Windows.Devices.Geolocation on Windows.
//
// Open starts a provider and returns a Locator. Current serves a cached fix
// when one is fresh enough and otherwise waits; Next always waits for a new
// one; Subscribe opens an independent stream. Close stops the provider and
// closes every subscription.
//
//	loc, err := location.Open(ctx, location.DefaultConfig())
//	if err != nil {
//		return err
//	}
//	defer loc.Close()
//
//	fix, err := loc.Current(ctx)
//
// # Layout
//
// This package is the façade and the composition root — it is the only place
// that knows both the public Config and the internals it configures. It also
// declares Locator, because a driving port belongs with the side that calls
// it, and callers are on this side. Behind it:
//
//   - geo holds the domain values (Fix, Status, the error vocabulary) and
//     depends only on the standard library.
//   - internal/core sequences the features and satisfies Locator.
//   - internal/feature/* is one package per capability.
//
// Every feature has the same shape — port/ declares the contract and nothing
// else, adapter/* holds the implementations:
//
//	internal/feature/<name>/port/       package port
//	internal/feature/<name>/adapter/*/  one package per implementation
//
// All six port packages are named port, so importers alias each to its feature
// name and call sites read admission.Gate, fanout.Broadcaster, and so on.
//
//	admission   which samples are publishable      → rules
//	fanout      delivery, backpressure, waiters    → chanhub
//	fixcache    the newest admitted fix            → atomiccache
//	lifecycle   service and permission state       → atomicstate
//	clock       the current time                   → systemclock, fixedclock
//	provider    the native location source         → corelocation, geoclue,
//	                                                 winrt, unsupported
//
// The operating system is an adapter like any other: it implements
// provider.Provider, and provider.Factory is the port that chooses between
// implementations. internal/feature/provider/platform provides the Factory
// bound at build time — the only build-tagged code outside the adapters.
//
// Nothing is shared between features except geo. A concept belongs to exactly
// one of them, which is why DropPolicy lives with fan-out and Accuracy lives
// with the provider rather than in a common package both would have to import.
//
// Callers need only this package; the types above are re-exported here.
//
// # Permissions
//
// Location access is gated by the operating system, and a denial is reported
// through Status rather than by failing Open. On macOS the process must be a
// signed bundle with NSLocationWhenInUseUsageDescription in its Info.plist to
// receive the authorization prompt at all — an unsigned binary typically stays
// in StateStarting with PermissionPromptRequired forever.
package location
