package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/device"
	utls "github.com/refraction-networking/utls"
)

const (
	PanDomain   = "https://pan.quark.cn"
	DriveDomain = "https://drive-pc.quark.cn"
)

// AuthError means the session is no longer accepted by the server.
// Official codes: 31001/31004 ("请重新登录"), pan NO_AUTH / AUTH_ERROR*.
// There is NO OAuth-style refresh_token in the PC client — re-scan QR is required.
type AuthError struct {
	Code    string
	Message string
	HTTP    int
	URL     string
}

func (e *AuthError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("auth expired (%s): %s — run goquark login", e.Code, e.Message)
	}
	return fmt.Sprintf("auth expired (http %d): %s — run goquark login", e.HTTP, e.Message)
}

func IsAuthError(err error) bool {
	var ae *AuthError
	return errors.As(err, &ae)
}

// Client is the Quark HTTP API client.
type Client struct {
	cfg *config.Config
	api *http.Client // standard TLS for JSON APIs
	dl  *http.Client // utls Chrome fingerprint for CDN download
}

func New(cfg *config.Config) *Client {
	c := &Client{cfg: cfg}
	c.api = &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2: true,
			MaxIdleConns:      64,
			IdleConnTimeout:   90 * time.Second,
		},
		// Official Electron session auto-persists Set-Cookie; we do it in DoJSON.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	c.dl = newDownloadClient()
	return c
}

func newDownloadClient() *http.Client {
	// IMPORTANT: CDN hosts (dl-pc-*.drive.quark.cn) negotiate HTTP/2.
	// Using utls DialTLSContext WITHOUT wiring http2.Transport caused:
	//   malformed HTTP response "\x00\x00\x12\x04..."  (raw h2 SETTINGS frame)
	// because net/http expected HTTP/1.1 on that conn.
	//
	// Fix: standard TLS with ALPN h2/http1.1 + ForceAttemptHTTP2.
	// CDN URLs already carry auth_key tokens; Chrome JA3 is less critical
	// here than for the JSON API (which still uses the default transport).
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   128,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
		ReadBufferSize:        256 << 10,
		WriteBufferSize:       64 << 10,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2", "http/1.1"},
		},
	}
	return &http.Client{Transport: tr, Timeout: 0}
}

// newDownloadClientUTLS is kept as an optional HTTP/1.1-only path if needed.
// Callers should prefer newDownloadClient() which supports h2.
func newDownloadClientUTLS() *http.Client {
	dialTLS := func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		raw, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if tc, ok := raw.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
			_ = tc.SetKeepAlive(true)
			_ = tc.SetReadBuffer(2 << 20)
		}
		host, _, _ := net.SplitHostPort(addr)
		// HelloCustom + explicit ALPN http/1.1 only — never speak h2 on this conn
		// because this transport is HTTP/1.1-only.
		uconn := utls.UClient(raw, &utls.Config{
			ServerName: host,
			NextProtos: []string{"http/1.1"},
		}, utls.HelloChrome_Auto)
		// Re-apply ALPN after fingerprint preset (presets may override NextProtos)
		if err := uconn.BuildHandshakeState(); err == nil {
			// BuildHandshakeState already done by Handshake; set via config only.
		}
		if err := uconn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		// If server still picked h2 (ignored our ALPN), connection is unusable
		// with HTTP/1.1 transport — caller should fall back to newDownloadClient.
		return uconn, nil
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 128,
		IdleConnTimeout:     120 * time.Second,
		DisableCompression:  true,
		ReadBufferSize:      256 << 10,
		DialTLSContext:      dialTLS,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	return &http.Client{Transport: tr, Timeout: 0}
}

func (c *Client) DownloadHTTP() *http.Client { return c.dl }
func (c *Client) Config() *config.Config     { return c.cfg }

func (c *Client) setCommon(req *http.Request) {
	req.Header.Set("User-Agent", device.UA(c.cfg.Device))
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://pan.quark.cn/")
	req.Header.Set("Origin", "https://pan.quark.cn")
	if ck := c.cfg.CookieHeader(); ck != "" {
		req.Header.Set("Cookie", ck)
	}
}

// DoJSON performs API request and returns parsed JSON object.
// Side effects:
//   - merges Set-Cookie into config session (official Electron does this automatically)
//   - returns *AuthError on 31001/31004/NO_AUTH/AUTH_ERROR/HTTP 401/403
func (c *Client) DoJSON(method, rawURL string, query url.Values, body any) (map[string]any, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if q.Get("pr") == "" {
		q.Set("pr", "ucpro")
	}
	if q.Get("fr") == "" {
		q.Set("fr", "pc")
	}
	// Official Mac client also sends sys=darwin on drive/pan params.
	if q.Get("sys") == "" {
		sys := c.cfg.Device.Platform
		if sys == "" {
			sys = "darwin"
		}
		q.Set("sys", sys)
	}
	if q.Get("mi") == "" && c.cfg.Device.DeviceName != "" {
		q.Set("mi", c.cfg.Device.DeviceName)
	}
	for k, vs := range query {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	if q.Get("mi") == "" && c.cfg.Device.DeviceName != "" {
		q.Set("mi", c.cfg.Device.DeviceName)
	}
	u.RawQuery = q.Encode()

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, u.String(), rdr)
	if err != nil {
		return nil, err
	}
	c.setCommon(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.api.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Persist any Set-Cookie (session rotation) — critical for long sessions.
	c.ingestCookies(resp)

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// HTTP-level auth failures
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, &AuthError{
			HTTP:    resp.StatusCode,
			Message: truncate(string(b), 200),
			URL:     rawURL,
		}
	}

	// HTML body often means WAF / login page
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(strings.ToLower(ct), "text/html") {
		return nil, &AuthError{
			HTTP:    resp.StatusCode,
			Code:    "EXCEPTION_BODY",
			Message: "server returned HTML (session likely invalid)",
			URL:     rawURL,
		}
	}

	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		// non-JSON 403-ish
		if resp.StatusCode >= 400 {
			return nil, &AuthError{HTTP: resp.StatusCode, Message: truncate(string(b), 200), URL: rawURL}
		}
		return nil, fmt.Errorf("json: %w body=%s", err, truncate(string(b), 200))
	}

	if ae := detectAuthPayload(out, resp.StatusCode, rawURL); ae != nil {
		return out, ae
	}
	return out, nil
}

func (c *Client) ingestCookies(resp *http.Response) {
	if resp == nil {
		return
	}
	updated := false
	for _, ck := range resp.Cookies() {
		if ck.Name == "" {
			continue
		}
		// empty value = delete
		if ck.Value == "" {
			if c.cfg.Session.Cookies != nil {
				if _, ok := c.cfg.Session.Cookies[ck.Name]; ok {
					delete(c.cfg.Session.Cookies, ck.Name)
					updated = true
				}
			}
			continue
		}
		c.cfg.UpsertCookie(ck.Name, ck.Value)
		updated = true
	}
	if updated {
		_ = c.cfg.Save()
	}
}

func detectAuthPayload(out map[string]any, httpStatus int, rawURL string) *AuthError {
	if out == nil {
		return nil
	}
	// Drive API: code is number; 31001/31004 = re-login
	if code, ok := asInt(out["code"]); ok {
		if code == 31001 || code == 31004 {
			msg, _ := out["message"].(string)
			if msg == "" {
				msg = "请重新登录"
			}
			return &AuthError{Code: fmt.Sprintf("%d", code), Message: msg, HTTP: httpStatus, URL: rawURL}
		}
		// some paths use status field
	}
	// string code
	if sc, ok := out["code"].(string); ok {
		if isAuthCode(sc) {
			msg, _ := out["message"].(string)
			if msg == "" {
				msg = sc
			}
			return &AuthError{Code: sc, Message: msg, HTTP: httpStatus, URL: rawURL}
		}
	}
	// pan API: success=false + code NO_AUTH / AUTH_ERROR*
	if success, ok := out["success"].(bool); ok && !success {
		if sc, ok := out["code"].(string); ok && isAuthCode(sc) {
			msg, _ := out["message"].(string)
			return &AuthError{Code: sc, Message: msg, HTTP: httpStatus, URL: rawURL}
		}
	}
	// message hints
	if msg, ok := out["message"].(string); ok {
		if strings.Contains(msg, "重新登录") || strings.Contains(strings.ToLower(msg), "not login") {
			code := fmt.Sprintf("%v", out["code"])
			return &AuthError{Code: code, Message: msg, HTTP: httpStatus, URL: rawURL}
		}
	}
	return nil
}

func isAuthCode(sc string) bool {
	switch sc {
	case "NO_AUTH", "AUTH_ERROR", "AUTH_ERROR:50051", "AUTH_ERROR:50052", "31001", "31004":
		return true
	default:
		return strings.HasPrefix(sc, "AUTH_ERROR")
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case json.Number:
		n, err := t.Int64()
		return int(n), err == nil
	case string:
		var n int
		_, err := fmt.Sscanf(t, "%d", &n)
		return n, err == nil
	default:
		return 0, false
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
