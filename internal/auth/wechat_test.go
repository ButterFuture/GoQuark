package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ButterFuture/GoQuark/internal/config"
)

// fakeWx serves the login page, WeChat and UOP from one local server. polls
// lists the wx_errcode replies in order; the last one repeats.
type fakeWx struct {
	t     *testing.T
	srv   *httptest.Server
	polls []string
	mu    sync.Mutex
	n     int
	lasts []string // last= of each poll
	auth  url.Values
	refs  map[string]string // path → Referer
}

func newFakeWx(t *testing.T, polls ...string) *fakeWx {
	f := &fakeWx{t: t, polls: polls, refs: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	old := wxEndpoints
	t.Cleanup(func() { wxEndpoints = old })
	base := f.srv.URL
	wxEndpoints.LoginPage = base + "/wechat_qrcode?desktop=1"
	wxEndpoints.QRConnect = base + "/connect/qrconnect"
	wxEndpoints.QRImage = base + "/connect/qrcode/"
	wxEndpoints.LongPoll = base + "/connect/l/qrconnect"
	wxEndpoints.Authorize = base + "/cas/thirdparty/authorize"
	return f
}

func (f *fakeWx) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs[r.URL.Path] = r.Referer()
	if ua := r.UserAgent(); ua != wxUA {
		f.t.Errorf("%s: User-Agent %q, want the login-window UA", r.URL.Path, ua)
	}
	switch r.URL.Path {
	case "/wechat_qrcode":
		fmt.Fprint(w, "<script>function displayQrCode() { btoa(unescape(encodeURIComponent(`.x{}`))) }</script>")
	case "/connect/qrconnect":
		fmt.Fprint(w, `<img class="qrcode" src="/connect/qrcode/UUID-1">`)
	case "/connect/qrcode/UUID-1":
		w.Header().Set("Content-Type", "image/jpeg")
	case "/connect/l/qrconnect":
		f.lasts = append(f.lasts, r.URL.Query().Get("last"))
		code := f.polls[min(f.n, len(f.polls)-1)]
		f.n++
		if code == "405" {
			fmt.Fprint(w, "window.wx_errcode=405;window.wx_code='WXCODE';")
			return
		}
		fmt.Fprintf(w, "window.wx_errcode=%s;window.wx_code='';", code)
	case "/cas/thirdparty/authorize":
		f.auth = r.URL.Query()
		http.Redirect(w, r, f.srv.URL+"/wechat_qrcode?desktop=1&service_ticket=ST-1", http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func TestWeChatFlowDesktopClient(t *testing.T) {
	f := newFakeWx(t, "408", "404", "405")
	cfg, _ := config.Load(t.TempDir() + "/config.json")
	flow := newWxFlow(cfg)
	ctx := context.Background()

	qr, err := flow.start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := wxEndpoints.Confirm + "UUID-1"; qr != want {
		t.Errorf("qr = %q, want %q", qr, want)
	}
	qc, _ := url.Parse(flow.qc)
	if got := qc.Query().Get("href"); !strings.HasPrefix(got, "data:text/css;base64,") {
		t.Errorf("qrconnect href = %q, want the login page's stylesheet", got)
	}

	var statuses []string
	code, err := flow.waitCode(ctx, time.Now().Add(time.Minute), func(s string) { statuses = append(statuses, s) })
	if err != nil || code != "WXCODE" {
		t.Fatalf("waitCode = %q, %v", code, err)
	}
	if got := strings.Join(f.lasts, ","); got != ",,404" {
		t.Errorf("poll last= sequence %q, want \",,404\" (last only after a scan)", got)
	}
	if got := strings.Join(statuses, ","); got != "waiting_scan,scanned,scan confirmed" {
		t.Errorf("statuses %q", got)
	}

	st, err := flow.ticket(ctx, code)
	if err != nil || st != "ST-1" {
		t.Fatalf("ticket = %q, %v", st, err)
	}
	for k, want := range map[string]string{
		"client_id": "386", "code": "WXCODE", "third_party_name": "wechat",
		"redirect_uri": wxEndpoints.LoginPage,
	} {
		if got := f.auth.Get(k); got != want {
			t.Errorf("authorize %s = %q, want %q", k, got, want)
		}
	}
	if _, ok := f.auth["state"]; !ok {
		t.Error("authorize lacks state=, which WeChat's redirect always adds")
	}
	// Referers as the login window's navigation sends them.
	srvOrigin := f.srv.URL + "/"
	for path, want := range map[string]string{
		"/wechat_qrcode":         "",
		"/connect/qrconnect":     srvOrigin,
		"/connect/qrcode/UUID-1": flow.qc,
		"/connect/l/qrconnect":   srvOrigin,
	} {
		if got := f.refs[path]; got != want {
			t.Errorf("Referer for %s = %q, want %q", path, got, want)
		}
	}
}

func TestWeChatExpiredAndCancel(t *testing.T) {
	newFakeWx(t, "402")
	cfg, _ := config.Load(t.TempDir() + "/config.json")
	flow := newWxFlow(cfg)
	if _, err := flow.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.waitCode(context.Background(), time.Now().Add(time.Minute), func(string) {}); !errors.Is(err, errWxExpired) {
		t.Errorf("402: err = %v, want errWxExpired", err)
	}

	newFakeWx(t, "408")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	begin := time.Now()
	if _, err := flow.waitCode(ctx, time.Now().Add(time.Minute), func(string) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: err = %v, want context.Canceled", err)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Errorf("cancel took %s, want prompt return", d)
	}
}

func TestParseMethod(t *testing.T) {
	for in, want := range map[string]Method{"": MethodQuark, "quark": MethodQuark, "scan": MethodQuark, "WeChat": MethodWeChat} {
		if got, err := ParseMethod(in); err != nil || got != want {
			t.Errorf("ParseMethod(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseMethod("qq"); err == nil {
		t.Error("ParseMethod(qq) accepted")
	}
}
