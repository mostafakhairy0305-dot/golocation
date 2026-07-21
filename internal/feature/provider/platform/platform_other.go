//go:build !linux && !windows && !darwin

package platform

import (
	"runtime"

	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/unsupported"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

func newProvider(_ provider.Options, _ provider.Sink) (provider.Provider, error) {
	return unsupported.New(runtime.GOOS)
}
