//go:build !demo

package auth

import "github.com/ButterFuture/GoQuark/internal/config"

func loginQRDemo(cfg *config.Config, opt Options) (*LoginResult, error) {
	_ = cfg
	_ = opt
	panic("loginQRDemo called without -tags demo")
}
