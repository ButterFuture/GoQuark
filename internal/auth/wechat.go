package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
)

// Method is what the user scans the login QR with.
type Method string

const (
	MethodQuark  Method = "quark"  // Quark app (default)
	MethodWeChat Method = "wechat" // WeChat
)

// ParseMethod accepts "" / "quark" / "scan" (Quark app) and "wechat".
func ParseMethod(s string) (Method, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "quark", "scan":
		return MethodQuark, nil
	case "wechat", "weixin", "wx":
		return MethodWeChat, nil
	}
	return "", fmt.Errorf("unknown login method %q (quark | wechat)", s)
}

// Label is the short Chinese name shown in the TUI.
func (m Method) Label() string {
	if m == MethodWeChat {
		return "微信扫码"
	}
	return "夸克扫码"
}

// ScanHint tells the user how to scan.
func (m Method) ScanHint() string {
	if m == MethodWeChat {
		return "微信 → 扫一扫"
	}
	return "夸克 APP → 搜索框相机 → 扫码"
}

// Login runs the blocking QR login for method and persists the session.
func Login(cfg *config.Config, m Method, opt Options) (*LoginResult, error) {
	if m == MethodWeChat {
		return LoginWeChat(cfg, opt)
	}
	return LoginQR(cfg, opt)
}

// WeChat login follows the PC client: its login window loads
// pan.quark.cn/wechat_qrcode?desktop=1, which embeds WeChat's web QR connect
// with a UOP authorize redirect for the desktop client id; once the user
// confirms, UOP answers with a 302 carrying service_ticket, and the client
// finishes with desktop/account/login exactly as after a Quark-app scan.
const (
	wxAppID    = "wxcde89ee65c11b66a"
	wxClientID = "386" // desktop client (532 is the web page's)
	// UA of the client's login window.
	wxUA         = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/112.0.5615.165 Electron/24.1.3.8 Safari/537.36 Channel/pckk_information_ch"
	wxAcceptLang = "zh-CN,zh;q=0.9"
	// WeChatQRLifetime is how long WeChat keeps a login QR valid; it is the
	// default wait so the login never gives up on a QR that still works.
	WeChatQRLifetime = 5 * time.Minute
)

// wxEndpoints is a variable so tests can point the flow at a local server.
var wxEndpoints = struct {
	LoginPage, QRConnect, QRImage, LongPoll, Authorize, Confirm string
}{
	LoginPage: "https://pan.quark.cn/wechat_qrcode?desktop=1",
	QRConnect: "https://open.weixin.qq.com/connect/qrconnect",
	QRImage:   "https://open.weixin.qq.com/connect/qrcode/",
	LongPoll:  "https://lp.open.weixin.qq.com/connect/l/qrconnect",
	Authorize: "https://uop.quark.cn/cas/thirdparty/authorize",
	Confirm:   "https://open.weixin.qq.com/connect/confirm?uuid=",
}

var (
	// The login page passes this stylesheet to WeChat as href.
	wxCSSRe     = regexp.MustCompile("(?s)function displayQrCode.*?encodeURIComponent\\(`(.*?)`\\)")
	wxUUIDRe    = regexp.MustCompile(`/connect/qrcode/([A-Za-z0-9_-]+)`)
	wxErrcodeRe = regexp.MustCompile(`wx_errcode\s*=\s*(\d+)`)
	wxCodeRe    = regexp.MustCompile(`wx_code\s*=\s*'([^']*)'`)

	errWxExpired = errors.New("qr expired")
	errWxTimeout = errors.New("login timeout")
)

// wxFlow is one WeChat login attempt. All requests share one cookie jar and
// never follow redirects, so the authorize step can read its Location.
type wxFlow struct {
	cfg  *config.Config
	jar  http.CookieJar
	hc   *http.Client
	uuid string
	uop  string // authorize URL embedded in qrconnect (its t must not change)
	qc   string // full qrconnect URL
}

func newWxFlow(cfg *config.Config) *wxFlow {
	jar, _ := cookiejar.New(nil)
	return &wxFlow{
		cfg: cfg,
		jar: jar,
		hc: &http.Client{
			Timeout:       40 * time.Second, // WeChat holds a long poll ~25s
			Jar:           jar,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (f *wxFlow) get(ctx context.Context, u, referer, accept string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", wxUA)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", wxAcceptLang)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, b, err
}

// start loads the login page, opens qrconnect and fetches the QR image, in the
// order the login window does. It returns the string the QR encodes.
func (f *wxFlow) start(ctx context.Context) (string, error) {
	ep := wxEndpoints
	resp, page, err := f.get(ctx, ep.LoginPage, "", acceptHTML)
	if err != nil {
		return "", fmt.Errorf("wechat login page: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("wechat login page: http %d", resp.StatusCode)
	}

	t := strconv.FormatInt(time.Now().UnixMilli(), 10)
	f.uop = ep.Authorize + "?" + orderedQuery(
		"client_id", wxClientID, "display", "pc", "change_uid", "0", "t", t,
		"third_party_name", "wechat", "la", "zh", "redirect_uri", ep.LoginPage)
	kv := []string{"login_type", "jssdk", "self_redirect", "true", "appid", wxAppID,
		"scope", "snsapi_login", "fast_login", "0"}
	if m := wxCSSRe.FindSubmatch(page); m != nil {
		kv = append(kv, "href", "data:text/css;base64,"+base64.StdEncoding.EncodeToString(m[1]))
	}
	f.qc = ep.QRConnect + "?" + orderedQuery(append(kv, "redirect_uri", f.uop)...)

	resp, body, err := f.get(ctx, f.qc, origin(ep.LoginPage), acceptHTML)
	if err != nil {
		return "", fmt.Errorf("wechat qrconnect: %w", err)
	}
	m := wxUUIDRe.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("wechat qrconnect: http %d, no QR uuid", resp.StatusCode)
	}
	f.uuid = string(m[1])
	// The page shows WeChat's own image; we render the same content ourselves.
	_, _, _ = f.get(ctx, ep.QRImage+f.uuid, f.qc, "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	return ep.Confirm + f.uuid, nil
}

// waitCode long-polls until the user confirms in WeChat and returns the code,
// with the branches and delays of WeChat's own page script.
func (f *wxFlow) waitCode(ctx context.Context, deadline time.Time, onStatus func(string)) (string, error) {
	ref := origin(wxEndpoints.QRConnect)
	last := ""
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", errWxTimeout
		}
		u := wxEndpoints.LongPoll + "?uuid=" + url.QueryEscape(f.uuid)
		if last != "" {
			u += "&last=" + last
		}
		u += "&_=" + strconv.FormatInt(time.Now().UnixMilli(), 10)
		_, body, err := f.get(ctx, u, ref, "*/*")
		wait := 2 * time.Second
		switch m := wxErrcodeRe.FindSubmatch(body); {
		case err != nil:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			onStatus("poll err: " + err.Error())
		case m == nil:
			onStatus("poll err: unexpected reply")
		default:
			switch code := string(m[1]); code {
			case "405":
				c := wxCodeRe.FindSubmatch(body)
				if c == nil || len(c[1]) == 0 {
					return "", fmt.Errorf("wechat confirmed but no code returned")
				}
				onStatus("scan confirmed")
				return string(c[1]), nil
			case "404":
				onStatus("scanned")
				last, wait = code, 100*time.Millisecond
			case "403":
				onStatus("cancelled")
				last = code
			case "402", "500":
				return "", errWxExpired
			default: // 408: this long poll ended, keep the same QR
				onStatus("waiting_scan")
			}
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return "", err
		}
	}
}

// ticket follows authorize?…&code= until a redirect carries service_ticket.
func (f *wxFlow) ticket(ctx context.Context, code string) (string, error) {
	next := f.uop + "&code=" + url.QueryEscape(code) + "&state="
	ref := origin(wxEndpoints.QRConnect)
	for hop := 0; hop < 5; hop++ {
		resp, _, err := f.get(ctx, next, ref, acceptHTML)
		if err != nil {
			return "", fmt.Errorf("wechat authorize: %w", err)
		}
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", fmt.Errorf("wechat authorize: http %d without redirect (WeChat account not linked to Quark?)", resp.StatusCode)
		}
		cur, _ := url.Parse(next)
		lu, err := cur.Parse(loc)
		if err != nil {
			return "", fmt.Errorf("wechat authorize: bad redirect: %w", err)
		}
		if st := lu.Query().Get("service_ticket"); st != "" {
			return st, nil
		}
		ref, next = next, lu.String()
	}
	return "", fmt.Errorf("wechat authorize: no service_ticket after redirects")
}

// finish turns a service ticket into a saved session, like the Quark-app flow.
func (f *wxFlow) finish(st string) (json.RawMessage, map[string]string, error) {
	hc := &http.Client{Timeout: 20 * time.Second, Jar: f.jar}
	user, cookies, err := panLogin(hc, f.cfg, st)
	if err != nil {
		return nil, nil, err
	}
	f.cfg.SetSession(cookies, st, user, string(MethodWeChat))
	if err := f.cfg.Save(); err != nil {
		return nil, nil, err
	}
	return user, cookies, nil
}

// LoginWeChat runs the WeChat QR login and persists the session into cfg.
func LoginWeChat(cfg *config.Config, opt Options) (*LoginResult, error) {
	if demobuild.Enabled {
		return loginQRDemo(cfg, opt)
	}
	if opt.Timeout <= 0 {
		opt.Timeout = WeChatQRLifetime
	}
	defaultOpts(&opt, cfg)
	if err := device.EnsureDevice(cfg, ""); err != nil {
		return nil, err
	}
	f := newWxFlow(cfg)
	qrURL, err := f.start(opt.Ctx)
	if err != nil {
		return nil, err
	}
	if _, err := presentQR(qrURL, "扫码: "+MethodWeChat.ScanHint(), opt); err != nil {
		return nil, err
	}
	if opt.TokenOnly {
		return &LoginResult{Token: f.uuid, QRURL: qrURL}, nil
	}
	code, err := f.waitCode(opt.Ctx, time.Now().Add(opt.Timeout), opt.OnStatus)
	if err != nil {
		return nil, err
	}
	st, err := f.ticket(opt.Ctx, code)
	if err != nil {
		return nil, err
	}
	user, cookies, err := f.finish(st)
	if err != nil {
		return nil, err
	}
	return &LoginResult{ServiceTicket: st, Cookies: cookies, UserInfo: user, QRURL: qrURL, Token: f.uuid}, nil
}

const acceptHTML = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"

// orderedQuery encodes key/value pairs in order, as URLSearchParams.append does.
func orderedQuery(kv ...string) string {
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, url.QueryEscape(kv[i])+"="+url.QueryEscape(kv[i+1]))
	}
	return strings.Join(parts, "&")
}

// origin is what Chromium sends as Referer cross-origin: scheme://host/.
func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/"
}
