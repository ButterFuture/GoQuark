//go:build !demo

package auth

import (
	"fmt"

	"github.com/ButterFuture/GoQuark/internal/config"
)

func startQRLoginDemo(cfg *config.Config, opt Options) (*QRSession, error) {
	_ = cfg
	_ = opt
	return nil, fmt.Errorf("demo login not available")
}

func pollQRLoginDemo(cfg *config.Config, sess *QRSession) (*PollResult, error) {
	_ = cfg
	_ = sess
	return nil, fmt.Errorf("demo login not available")
}
