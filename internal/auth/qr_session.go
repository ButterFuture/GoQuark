package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"

	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/demobuild"
	"github.com/ButterFuture/GoQuark/internal/device"
	"github.com/skip2/go-qrcode"
)

// QRSession is a multi-step login session for agents (MCP).
// Create with StartQRLogin → show QRURL / QRASCII to user → PollQRLogin until done.
type QRSession struct {
	Token     string
	QRURL     string
	QRASCII   string
	QRPngPath string
	QRURLPath string
	StartedAt time.Time
	Timeout   time.Duration
	// demo-only fields (ignored when demobuild.Enabled is false)
	demoDeadline time.Time
	demoDone     bool
}

// StartQRLogin fetches a QR token and returns scan content without blocking.
// Does not wait for the user to scan — call PollQRLogin afterwards.
func StartQRLogin(cfg *config.Config, opt Options) (*QRSession, error) {
	defaultOpts(&opt)
	if err := device.EnsureDevice(cfg, ""); err != nil {
		return nil, err
	}

	if demobuild.Enabled {
		return startQRLoginDemo(cfg, opt)
	}

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Timeout: 20 * time.Second, Jar: jar}
	token, err := getToken(hc, cfg)
	if err != nil {
		return nil, err
	}
	qrURL := buildScanURL(token)
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
	return &QRSession{
		Token:     token,
		QRURL:     qrURL,
		QRASCII:   ascii,
		QRPngPath: opt.QRPngPath,
		QRURLPath: opt.QRURLPath,
		StartedAt: time.Now(),
		Timeout:   opt.Timeout,
	}, nil
}

// PollResult is one poll of the QR login state.
type PollResult struct {
	// Phase: waiting_scan | scan_confirmed | logged_in | expired | timeout | error
	Phase   string `json:"phase"`
	Message string `json:"message,omitempty"`
	// LoggedIn true only when session cookies are persisted.
	LoggedIn bool `json:"logged_in"`
	// QR fields echoed so agents can re-display without re-starting
	QRURL   string `json:"qr_url,omitempty"`
	QRASCII string `json:"qr_ascii,omitempty"`
}

// PollQRLogin checks whether the QR was scanned and completes pan login if so.
// Safe to call repeatedly. On success, writes session into cfg and saves.
func PollQRLogin(cfg *config.Config, sess *QRSession) (*PollResult, error) {
	if sess == nil || sess.Token == "" {
		return nil, fmt.Errorf("no QR session; call StartQRLogin first")
	}
	if demobuild.Enabled {
		return pollQRLoginDemo(cfg, sess)
	}
	if sess.Timeout > 0 && time.Since(sess.StartedAt) > sess.Timeout {
		return &PollResult{
			Phase:   "timeout",
			Message: "login timeout",
			QRURL:   sess.QRURL,
			QRASCII: sess.QRASCII,
		}, nil
	}

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Timeout: 20 * time.Second, Jar: jar}
	ticket, status, msg, err := pollTicket(hc, cfg, sess.Token)
	if err != nil {
		return &PollResult{
			Phase:   "error",
			Message: err.Error(),
			QRURL:   sess.QRURL,
			QRASCII: sess.QRASCII,
		}, nil
	}
	switch status {
	case 2000000:
		// scanned — finish pan login
		user, cookies, err := panLogin(hc, cfg, ticket)
		if err != nil {
			return &PollResult{
				Phase:   "error",
				Message: "pan login: " + err.Error(),
				QRURL:   sess.QRURL,
				QRASCII: sess.QRASCII,
			}, nil
		}
		cfg.SetSession(cookies, ticket, user, "scan")
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return &PollResult{
			Phase:    "logged_in",
			Message:  "login ok",
			LoggedIn: true,
			QRURL:    sess.QRURL,
			QRASCII:  sess.QRASCII,
		}, nil
	case 50004001:
		return &PollResult{
			Phase:   "waiting_scan",
			Message: "waiting for Quark APP scan",
			QRURL:   sess.QRURL,
			QRASCII: sess.QRASCII,
		}, nil
	case 50004002:
		return &PollResult{
			Phase:   "expired",
			Message: "qr expired; start a new login",
			QRURL:   sess.QRURL,
			QRASCII: sess.QRASCII,
		}, nil
	default:
		_ = msg
		return &PollResult{
			Phase:   "waiting_scan",
			Message: fmt.Sprintf("status=%d %s", status, msg),
			QRURL:   sess.QRURL,
			QRASCII: sess.QRASCII,
		}, nil
	}
}

// UserInfoFromSession returns a small JSON-friendly map from stored session user blob.
func UserInfoFromSession(cfg *config.Config) map[string]any {
	if cfg == nil || len(cfg.Session.UserInfo) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(cfg.Session.UserInfo, &m) != nil {
		return map[string]any{"raw": string(cfg.Session.UserInfo)}
	}
	return m
}
