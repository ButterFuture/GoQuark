//go:build !demo

package device

import "github.com/ButterFuture/GoQuark/internal/config"

func ensureDemoDevice(cfg *config.Config, nameOverride string) error {
	_ = cfg
	_ = nameOverride
	panic("ensureDemoDevice without -tags demo")
}
