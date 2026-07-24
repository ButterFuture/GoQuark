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

func startQRLoginDemo(cfg *config.Config, opt Options) (*QRSession, error) {
	if err := device.EnsureDevice(cfg, ""); err != nil {
		return nil, err
	}
	seed := demobuild.HostSeed()
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
	wait := demobuild.RandomWait5to10(seed)
	return &QRSession{
		Token:        token,
		QRURL:        qrURL,
		QRASCII:      ascii,
		QRPngPath:    opt.QRPngPath,
		QRURLPath:    opt.QRURLPath,
		StartedAt:    time.Now(),
		Timeout:      opt.Timeout,
		demoDeadline: time.Now().Add(wait),
	}, nil
}

func pollQRLoginDemo(cfg *config.Config, sess *QRSession) (*PollResult, error) {
	if sess.demoDone || cfg.HasSessionCookie() {
		return &PollResult{
			Phase:    "logged_in",
			Message:  "already logged in (demo)",
			LoggedIn: true,
			QRURL:    sess.QRURL,
			QRASCII:  sess.QRASCII,
		}, nil
	}
	if time.Now().Before(sess.demoDeadline) {
		return &PollResult{
			Phase:   "waiting_scan",
			Message: "demo: auto-login pending (fake QR)",
			QRURL:   sess.QRURL,
			QRASCII: sess.QRASCII,
		}, nil
	}
	// complete demo session
	seed := demobuild.HostSeed()
	uid := demobuild.FakeUserID(seed)
	nick := demobuild.FakeNicknameCN(seed)
	userObj := map[string]any{
		"nickname":    nick,
		"email":       demobuild.FakeEmail(seed),
		"user_id":     uid,
		"demo":        true,
		"demo_notice": "goquarkdemo mock account",
	}
	user, _ := json.Marshal(userObj)
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
	sess.demoDone = true
	return &PollResult{
		Phase:    "logged_in",
		Message:  "login ok (demo)",
		LoggedIn: true,
		QRURL:    sess.QRURL,
		QRASCII:  sess.QRASCII,
	}, nil
}
