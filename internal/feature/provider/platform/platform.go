// Package platform provides the provider.Factory bound to the target
// operating system. It is the only place in the module, outside the adapter
// packages themselves, that carries OS build tags — every other package
// compiles identically everywhere.
package platform

import provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"

// Factory is the provider.Factory for the operating system this binary was
// built for. The per-platform files supply newProvider; this is the one
// definition they all share.
type Factory struct{}

var _ provider.Factory = Factory{}

func (Factory) New(opts provider.Options, sink provider.Sink) (provider.Provider, error) {
	return newProvider(opts, sink)
}
