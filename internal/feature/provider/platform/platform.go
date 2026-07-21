// Package platform provides the provider.Factory bound to the target
// operating system. It is the only place in the module, outside the adapter
// packages themselves, that carries OS build tags — every other package
// compiles identically everywhere.
package platform

import provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"

// New builds the provider for the operating system this binary was built for
// and attaches it to host, which is also the sink it publishes through. The
// per-platform files supply newProvider; this is the one definition they all
// share, and the one value the composition root passes as a provider.Factory.
func New(opts provider.Options, host provider.Host) error {
	return newProvider(opts, host)
}

var _ provider.Factory = New
