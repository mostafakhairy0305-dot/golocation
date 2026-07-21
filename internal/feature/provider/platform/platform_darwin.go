//go:build darwin && (amd64 || arm64)

package platform

import (
	"fmt"

	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/corelocation"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// darwinConstructor is corelocation.New, named so a test can pass one that
// fails. Loading CoreLocation only fails on a machine where the framework is
// missing or the delegate class will not register, which is not a state a test
// running on a working mac can arrange.
type darwinConstructor func(corelocation.Options, provider.Sink) (*corelocation.Backend, error)

func newProvider(opts provider.Options, host provider.Host) error {
	return newProviderWith(corelocation.New, opts, host)
}

func newProviderWith(build darwinConstructor, opts provider.Options, host provider.Host) error {
	native, err := build(corelocation.Options{
		Accuracy:              opts.Accuracy,
		DesiredAccuracyMeters: opts.DesiredAccuracyMeters,
		MinimumDistanceMeters: opts.MinimumDistanceMeters,
		Permission:            opts.Permission,
	}, host)
	if err != nil {
		return fmt.Errorf("select the darwin provider: %w", err)
	}

	host.Attach(native)

	return nil
}
