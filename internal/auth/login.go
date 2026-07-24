package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/demobuild"
	"github.com/ButterFuture/GoQuark/internal/device"
	"github.com/skip2/go-qrcode"
)

const (
	clientID      = "386"
	clientVer     = "1.2"
	appVersion    = "3.24.0"
	channel       = "pckk@information_ch"
	scanLoginPage = "https://su.quark.cn/4_eMHBJ"
	uopBase       = "https://uop.quark.cn/cas/ajax"
	panLoginURL   = "https://pan.quark.cn/desktop/account/login"
)

// LoginResult is returned after a successful QR login.
type LoginResult struct {
	ServiceTicket string
	Cookies       map[string]string
	UserInfo      json.RawMessage
	QRURL         string
	Token         string
}

// Options controls QR login behavior.
type Options struct {
	QRPngPath  string
	QRURLPath  string
	Timeout    time.Duration
	Interval   time.Duration
	TokenOnly  bool
	PrintASCII bool // print QR to stdout (CLI only; TUI should set false)
	Quiet      bool // suppress stdout URL / scan hints
	OnStatus   func(msg string)
	// OnQR is called once QR content is ready (url + terminal ASCII art).
	OnQR func(qrURL, ascii string)
}

func defaultOpts(o *Options) {
	if o.QRPngPath == "" {
		o.QRPngPath = "qrcode.png"
	}
	if o.QRURLPath == "" {
		o.QRURLPath = "qrcode.url.txt"
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Minute
	}
	if o.Interval <= 0 {
		o.Interval = 2 * time.Second
	}
	if o.OnStatus == nil {
		o.OnStatus = func(string) {}
	}
}

// LoginQR runs official PC QR login flow and persists session into cfg.
// Demo builds (-tags demo / goquarkdemo) use a full mock path (fake QR, auto success).
func LoginQR(cfg *config.Config, opt Options) (*LoginResult, error) {
	if demobuild.Enabled {
		return loginQRDemo(cfg, opt)
	}
	defaultOpts(&opt)
	if err := device.EnsureDevice(cfg, ""); err != nil {
		return nil, err
	}

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Timeout: 20 * time.Second, Jar: jar}

	opt.OnStatus("getTokenForQrcodeLogin")
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
		// ToSmallString is compact half-block QR suitable for terminals.
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
		fmt.Println("扫码: 夸克APP → 搜索框相机 → 扫码")
		fmt.Println(qrURL)
	}

	if opt.TokenOnly {
		return &LoginResult{Token: token, QRURL: qrURL}, nil
	}

	deadline := time.Now().Add(opt.Timeout)
	var st string
	for time.Now().Before(deadline) {
		ticket, status, msg, err := pollTicket(hc, cfg, token)
		if err != nil {
			opt.OnStatus("poll err: " + err.Error())
			time.Sleep(opt.Interval)
			continue
		}
		switch status {
		case 2000000:
			st = ticket
			opt.OnStatus("scan confirmed")
			goto done
		case 50004001:
			// Official often returns "Query result is empty" while waiting.
			opt.OnStatus("waiting_scan")
			_ = msg
		case 50004002:
			return nil, fmt.Errorf("qr expired")
		default:
			opt.OnStatus(fmt.Sprintf("status=%d %s", status, msg))
		}
		time.Sleep(opt.Interval)
	}
	return nil, fmt.Errorf("login timeout")

done:
	user, cookies, err := panLogin(hc, cfg, st)
	if err != nil {
		return nil, err
	}
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

func baseParams(cfg *config.Config) url.Values {
	v := url.Values{}
	v.Set("pr", "ucpro")
	v.Set("fr", "pc")
	// Mac client: always darwin
	v.Set("sys", "darwin")
	v.Set("client_id", clientID)
	v.Set("v", clientVer)
	v.Set("request_id", fmt.Sprintf("%d", time.Now().UnixNano()))
	v.Set("uc_param_str", "utprfr")
	// Official encrypts utdid; plain machine_id works for token API.
	v.Set("ut", cfg.Device.MachineID)
	// CRITICAL: device display name goes to mi.
	// Official interceptor: if mi empty → window.user.deviceName; if still empty
	// the server falls back to User-Agent (shows "Mozilla/5.0 (Macintosh...").
	if cfg.Device.DeviceName != "" {
		v.Set("mi", cfg.Device.DeviceName)
	}
	return v
}

func setHeaders(req *http.Request, cfg *config.Config) {
	req.Header.Set("User-Agent", device.UA(cfg.Device))
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://pan.quark.cn/")
	req.Header.Set("Origin", "https://pan.quark.cn")
}

func getToken(hc *http.Client, cfg *config.Config) (string, error) {
	u := uopBase + "/getTokenForQrcodeLogin?" + baseParams(cfg).Encode()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	setHeaders(req, cfg)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
		Data    struct {
			Members struct {
				Token string `json:"token"`
			} `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	if out.Status != 2000000 || out.Data.Members.Token == "" {
		return "", fmt.Errorf("getToken status=%d msg=%s body=%s", out.Status, out.Message, string(b))
	}
	return out.Data.Members.Token, nil
}

func buildScanURL(token string) string {
	q := url.Values{}
	q.Set("token", token)
	q.Set("client_id", clientID)
	q.Set("sch", channel)
	q.Set("sve", appVersion)
	q.Set("ssb", "weblogin")
	q.Set("uc_param_str", "")
	q.Set("uc_biz_str", "S:custom|OPT:SAREA@0|OPT:IMMERSIVE@1|OPT:BACK_BTN_STYLE@0")
	return scanLoginPage + "?" + q.Encode()
}

func pollTicket(hc *http.Client, cfg *config.Config, token string) (ticket string, status int, msg string, err error) {
	p := baseParams(cfg)
	p.Set("token", token)
	u := uopBase + "/getServiceTicketByQrcodeToken?" + p.Encode()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", 0, "", err
	}
	setHeaders(req, cfg)
	resp, err := hc.Do(req)
	if err != nil {
		return "", 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
		Data    struct {
			Members struct {
				ServiceTicket string `json:"service_ticket"`
			} `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", 0, "", err
	}
	return out.Data.Members.ServiceTicket, out.Status, out.Message, nil
}

func panLogin(hc *http.Client, cfg *config.Config, st string) (json.RawMessage, map[string]string, error) {
	q := url.Values{}
	q.Set("st", st)
	q.Set("fr", "pc")
	q.Set("pr", "ucpro")
	// Register device name at login (same mi field as drive requests).
	if cfg.Device.DeviceName != "" {
		q.Set("mi", cfg.Device.DeviceName)
	}
	if cfg.Device.MachineID != "" {
		q.Set("ut", cfg.Device.MachineID)
	}
	u := panLoginURL + "?" + q.Encode()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	setHeaders(req, cfg)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	cookies := map[string]string{}
	// collect from jar + response
	for _, host := range []string{
		"https://pan.quark.cn",
		"https://drive-pc.quark.cn",
		"https://uop.quark.cn",
		"https://quark.cn",
	} {
		uu, _ := url.Parse(host)
		for _, c := range hc.Jar.Cookies(uu) {
			cookies[c.Name] = c.Value
		}
	}
	for _, c := range resp.Cookies() {
		cookies[c.Name] = c.Value
	}

	// parse body JSON if possible
	var asMap map[string]any
	if json.Unmarshal(b, &asMap) == nil {
		return json.RawMessage(b), cookies, nil
	}
	if len(cookies) == 0 {
		return nil, cookies, fmt.Errorf("login failed: status=%d body=%s", resp.StatusCode, truncate(string(b), 300))
	}
	raw, _ := json.Marshal(map[string]any{"http_status": resp.StatusCode, "body": string(b)})
	return raw, cookies, nil
}

// CookieString builds Cookie header from map.
func CookieString(m map[string]string) string {
	var b strings.Builder
	first := true
	for k, v := range m {
		if k == "" || v == "" {
			continue
		}
		if !first {
			b.WriteString("; ")
		}
		first = false
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
