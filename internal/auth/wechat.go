package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/demobuild"
	"github.com/ButterFuture/GoQuark/internal/device"
	"github.com/skip2/go-qrcode"
)

// WeChat login mirrors https://pan.quark.cn/wechat_qrcode?need_bind_mobile=1:
// WeChat web QR connect → wx code → /account/third_party_info → service ticket
// → panLogin (same tail as the Quark-app QR flow).
const (
	wxAppID         = "wxcde89ee65c11b66a"
	wxConnectURL    = "https://open.weixin.qq.com/connect/qrconnect"
	wxConfirmURL    = "https://open.weixin.qq.com/connect/confirm"
	wxLongPollURL   = "https://lp.open.weixin.qq.com/connect/l/qrconnect"
	wxQuarkPage     = "https://pan.quark.cn/wechat_qrcode?need_bind_mobile=1"
	wxThirdPartyURL = "https://pan.quark.cn/account/third_party_info?pr=ucpro"
	wxGetAuthCode   = "https://uop.quark.cn/cas/thirdparty/getAuthCode"
	// third_party_info is only called by the web page, which always sends the web client id.
	wxWebClientID = "532"
)

var (
	wxUUIDRe    = regexp.MustCompile(`/connect/qrcode/([A-Za-z0-9_-]+)`)
	wxErrcodeRe = regexp.MustCompile(`wx_errcode\s*=\s*(\d+)`)
	wxCodeRe    = regexp.MustCompile(`wx_code\s*=\s*'([^']*)'`)
)

// LoginWeChat runs the WeChat QR login flow and persists session into cfg.
func LoginWeChat(cfg *config.Config, opt Options) (*LoginResult, error) {
	if demobuild.Enabled {
		return nil, fmt.Errorf("wechat login not available in demo build")
	}
	defaultOpts(&opt, cfg)
	if err := device.EnsureDevice(cfg, ""); err != nil {
		return nil, err
	}

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Timeout: 40 * time.Second, Jar: jar}

	// The wechat_qrcode page sets the ctoken cookie that third_party_info wants as CSRF token.
	opt.OnStatus("wechat: init")
	if err := wxGet(hc, cfg, wxQuarkPage, nil); err != nil {
		return nil, fmt.Errorf("load %s: %w", wxQuarkPage, err)
	}

	opt.OnStatus("wechat: qrconnect")
	uuid, err := wxNewUUID(hc, cfg)
	if err != nil {
		return nil, err
	}
	qrURL := wxConfirmURL + "?uuid=" + uuid
	if err := writeQRFiles(qrURL, opt.QRPngPath, opt.QRURLPath); err != nil {
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
		fmt.Println("扫码: 微信 → 扫一扫")
		fmt.Println(qrURL)
	}
	if opt.TokenOnly {
		return &LoginResult{Token: uuid, QRURL: qrURL}, nil
	}

	wxCode, err := wxWaitCode(hc, cfg, uuid, opt)
	if err != nil {
		return nil, err
	}

	opt.OnStatus("wechat: third_party_info")
	st, err := wxServiceTicket(hc, cfg, wxCode)
	if err != nil {
		return nil, err
	}

	user, cookies, err := panLogin(hc, cfg, st)
	if err != nil {
		return nil, err
	}
	cfg.SetSession(cookies, st, user, "wechat")
	if !cfg.HasSessionCookie() {
		return nil, fmt.Errorf("wechat login: no session cookies after panLogin: %s", truncate(string(user), 300))
	}
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	return &LoginResult{
		ServiceTicket: st,
		Cookies:       cookies,
		UserInfo:      user,
		QRURL:         qrURL,
		Token:         uuid,
	}, nil
}

// wxNewUUID opens WeChat's qrconnect page and extracts the QR uuid.
func wxNewUUID(hc *http.Client, cfg *config.Config) (string, error) {
	// Same redirect_uri the web page builds; WeChat validates its domain against the appid.
	ru := url.Values{}
	ru.Set("client_id", wxWebClientID)
	ru.Set("display", "pc")
	ru.Set("change_uid", "0")
	ru.Set("t", strconv.FormatInt(time.Now().UnixMilli(), 10))
	ru.Set("third_party_name", "wechat")
	ru.Set("la", "zh")
	ru.Set("redirect_uri", wxQuarkPage)

	q := url.Values{}
	q.Set("login_type", "jssdk")
	q.Set("self_redirect", "true")
	q.Set("appid", wxAppID)
	q.Set("scope", "snsapi_login")
	q.Set("fast_login", "0")
	q.Set("redirect_uri", wxGetAuthCode+"?"+ru.Encode())

	var body []byte
	if err := wxGet(hc, cfg, wxConnectURL+"?"+q.Encode(), &body); err != nil {
		return "", fmt.Errorf("qrconnect: %w", err)
	}
	m := wxUUIDRe.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("qrconnect: uuid not found: %s", truncate(string(body), 300))
	}
	return string(m[1]), nil
}

// wxWaitCode long-polls WeChat until the user confirms and returns the wx code.
// errcodes: 408 waiting, 404 scanned, 403 cancelled, 402 expired, 405 confirmed.
func wxWaitCode(hc *http.Client, cfg *config.Config, uuid string, opt Options) (string, error) {
	deadline := time.Now().Add(opt.Timeout)
	last := ""
	for time.Now().Before(deadline) {
		u := wxLongPollURL + "?uuid=" + url.QueryEscape(uuid)
		if last != "" {
			u += "&last=" + last
		}
		u += "&_=" + strconv.FormatInt(time.Now().UnixMilli(), 10)
		var body []byte
		if err := wxGet(hc, cfg, u, &body); err != nil {
			opt.OnStatus("poll err: " + err.Error())
			time.Sleep(opt.Interval)
			continue
		}
		m := wxErrcodeRe.FindSubmatch(body)
		if m == nil {
			opt.OnStatus("poll: unexpected " + truncate(string(body), 120))
			time.Sleep(opt.Interval)
			continue
		}
		switch code := string(m[1]); code {
		case "405":
			c := wxCodeRe.FindSubmatch(body)
			if c == nil || len(c[1]) == 0 {
				return "", fmt.Errorf("wechat confirmed but wx_code missing")
			}
			opt.OnStatus("scan confirmed")
			return string(c[1]), nil
		case "408":
			opt.OnStatus("waiting_scan")
		case "404":
			opt.OnStatus("scanned, confirm in WeChat")
			last = code
		case "403":
			opt.OnStatus("cancelled in WeChat, waiting")
			last = code
			time.Sleep(opt.Interval)
		case "402":
			return "", fmt.Errorf("qr expired")
		default:
			opt.OnStatus("wx_errcode=" + code)
			time.Sleep(opt.Interval)
		}
	}
	return "", fmt.Errorf("login timeout")
}

// wxServiceTicket exchanges the WeChat code for a Quark service ticket.
func wxServiceTicket(hc *http.Client, cfg *config.Config, wxCode string) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"third_party_token": wxCode,
		"third_party_name":  "wechat",
		"need_bind_mobile":  true,
		"token_type":        2,
		"client_id":         wxWebClientID,
	})
	req, err := http.NewRequest(http.MethodPost, wxThirdPartyURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	setHeaders(req, cfg)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Referer", wxQuarkPage)
	if pu, _ := url.Parse(wxQuarkPage); pu != nil {
		for _, c := range hc.Jar.Cookies(pu) {
			if c.Name == "ctoken" {
				req.Header.Set("X-Csrf-Token", c.Value)
			}
		}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	// code/msg types vary between success and error replies, so keep them raw.
	var out struct {
		Success bool            `json:"success"`
		Code    json.RawMessage `json:"code"`
		Msg     json.RawMessage `json:"msg"`
		Message json.RawMessage `json:"message"`
		Data    struct {
			ServiceTicket string `json:"service_ticket"`
		} `json:"data"`
	}
	// Never echo the body: on success it carries the service ticket.
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("third_party_info: http %d: %v", resp.StatusCode, err)
	}
	if !out.Success {
		code := strings.Trim(string(out.Code), `"`)
		msg := string(out.Msg)
		if len(out.Msg) == 0 {
			msg = string(out.Message)
		}
		if code == "50022" {
			return "", fmt.Errorf("this WeChat account is not bound to a Quark account with a phone number; log in once via the Quark app or pan.quark.cn to bind it (code=50022 %s)", msg)
		}
		return "", fmt.Errorf("third_party_info: code=%s %s", code, msg)
	}
	if out.Data.ServiceTicket == "" {
		return "", fmt.Errorf("third_party_info: no service_ticket")
	}
	return out.Data.ServiceTicket, nil
}

func wxGet(hc *http.Client, cfg *config.Config, u string, body *[]byte) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", device.UA(cfg.Device))
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	if body != nil {
		*body = b
	}
	return nil
}
