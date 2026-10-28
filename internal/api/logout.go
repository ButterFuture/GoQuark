package api

import (
	"context"
	"fmt"
	"time"

	"github.com/ButterFuture/GoQuark/internal/client"
)

// Logout ends the session on the server, as the PC client's 退出登录 does
// before it clears anything locally: GET /desktop/account/logout with the
// session cookies, 10s per try, 3 tries 200ms apart. The endpoint is
// idempotent, and a session the server already rejects counts as logged out.
// Callers clear the local session whatever this returns.
func Logout(c *client.Client) error {
	if DemoMode {
		return nil
	}
	var err error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var out map[string]any
		out, err = c.DoJSONCtx(ctx, "GET", client.PanDomain+"/desktop/account/logout", nil, nil)
		cancel()
		if client.IsAuthError(err) {
			return nil
		}
		if err == nil {
			if ok, _ := out["success"].(bool); ok {
				return nil
			}
			err = fmt.Errorf("logout: code=%v %v", out["code"], out["message"])
		}
	}
	return err
}
