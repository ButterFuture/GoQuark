//go:build demo

package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/demobuild"
	"github.com/ButterFuture/GoQuark/internal/device"
	"github.com/skip2/go-qrcode"
)

// loginQRDemo is the full mock login path for goquarkdemo.
// Fake QR → wait 5–10s → success session (no network).
func loginQRDemo(cfg *config.Config, opt Options) (*LoginResult, error) {
	defaultOpts(&opt)

	// Demo device identity (host-stable, distinct from product)
	if err := device.EnsureDevice(cfg, ""); err != nil {
		return nil, err
	}

	seed := demobuild.HostSeed()
	// Fake QR payload — never a real Quark login URL
	token := fmt.Sprintf("demo-token-%s", demobuild.FakeMachineID(seed)[:12])
	qrURL := "https://example.invalid/goquark-demo/scan?token=" + token

	_ = os.WriteFile(opt.QRURLPath, []byte(qrURL+"\n"), 0o644)
	if err := qrcode.WriteFile(qrURL, qrcode.Medium, 320, opt.QRPngPath); err != nil {
		return nil, err
	}
	ascii := ""
	if art, err := qrcode.New(qrURL, qrcode.Medium); err == nil {
		ascii = art.ToSmallString(false)
	}
	if opt.OnQR != nil {
		opt.OnQR(qrURL, ascii)
	}
	if opt.PrintASCII && ascii != "" {
		fmt.Println(ascii)
	}
	opt.OnStatus("qr ready: " + opt.QRPngPath)
	if !opt.Quiet {
		fmt.Println("【演示模式】假二维码 — 无需真实扫码，将自动登录")
		fmt.Println(qrURL)
	}

	if opt.TokenOnly {
		return &LoginResult{Token: token, QRURL: qrURL}, nil
	}

	// Status sequence similar to real flow
	opt.OnStatus("waiting_scan")
	wait := demobuild.RandomWait5to10(seed)
	// emit mid status once
	half := wait / 2
	if half < time.Second {
		half = time.Second
	}
	time.Sleep(half)
	opt.OnStatus("scan confirmed")
	time.Sleep(wait - half)
	opt.OnStatus("login ok (demo)")

	uid := demobuild.FakeUserID(seed)
	nick := demobuild.FakeNicknameCN(seed)
	userObj := map[string]any{
		"nickname":    nick,
		"mobile":      "",
		"email":       demobuild.FakeEmail(seed),
		"kps_wg":      uid,
		"user_id":     uid,
		"avatar_url":  "",
		"demo":        true,
		"demo_notice": "goquarkdemo mock account",
	}
	user, _ := json.Marshal(userObj)

	// Fake session cookies (only used by HasSessionCookie / demo paths)
	cookies := map[string]string{
		"__pus":  "demo_pus_" + demobuild.FakeMachineID(seed)[:16],
		"__uid":  uid,
		"__kps":  "demo_kps_" + demobuild.FakeMachineID(seed^1)[:16],
		"__puus": "demo_puus",
	}
	st := "demo_service_ticket_" + demobuild.FakeMachineID(seed^2)[:12]
	cfg.SetSession(cookies, st, user, "scan")
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	return &LoginResult{
		ServiceTicket: st,
		Cookies:       cookies,
		UserInfo:      user,
		QRURL:         qrURL,
		Token:         token,
	}, nil
}
