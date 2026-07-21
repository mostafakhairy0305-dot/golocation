//go:build darwin && !(amd64 || arm64)

package platform

import (
	"runtime"

	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/unsupported"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

func newProvider(_ provider.Options, _ provider.Host) error {
	return unsupported.New("darwin/" + runtime.GOARCH)
}
