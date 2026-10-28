package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/ButterFuture/GoQuark/internal/auth"
	"github.com/ButterFuture/GoQuark/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

func loginModel() model {
	return model{
		width: 100, height: 40, ready: true, mode: modeLogin,
		loginMethod: auth.MethodQuark, loginCtl: &loginControl{},
	}
}

// Tab flips the scan method and starts a new login, which supersedes the old
// one: its pump stops and its cancellation error is not shown as a failure.
func TestLoginTabSwitchesMethod(t *testing.T) {
	m := loginModel()
	if !strings.Contains(m.viewLogin(), "夸克扫码") || !strings.Contains(m.viewLogin(), "Tab") {
		t.Fatal("login screen lacks the method tabs / Tab hint")
	}
	_, oldGen := m.loginCtl.begin()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.loginMethod != auth.MethodWeChat || cmd == nil {
		t.Fatalf("after Tab: method %q, cmd nil=%v; want wechat and a new login", m.loginMethod, cmd == nil)
	}
	if m.loginCtl.current(oldGen) {
		t.Fatal("Tab did not supersede the running login")
	}
	if got := m.viewLogin(); !strings.Contains(got, auth.MethodWeChat.ScanHint()) && !strings.Contains(got, "正在获取二维码") {
		t.Errorf("WeChat login screen:\n%s", got)
	}

	done := make(chan error, 1)
	done <- errors.New("context canceled")
	next, cmd = m.Update(loginPumpTick{loginPump{gen: oldGen, doneCh: done}})
	if cmd != nil || next.(model).mode != modeLogin {
		t.Error("a stale login pump still acted on the model")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if next.(model).loginMethod != auth.MethodQuark {
		t.Error("second Tab did not switch back to Quark")
	}
}

// 退出登录 waits for the server call, then clears the session whatever it
// returned, and a second request meanwhile is ignored.
func TestLogoutServerThenLocal(t *testing.T) {
	cfg, err := config.Load(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetSession(map[string]string{"__pus": "p"}, "st", nil, "scan")
	m := loginModel()
	m.mode, m.cfg = modeBrowse, cfg

	next, cmd := m.doLogout()
	m = next.(model)
	if cmd == nil || !m.loggingOut || !cfg.HasSessionCookie() {
		t.Fatal("logout must call the server before clearing the session")
	}
	if _, again := m.doLogout(); again != nil {
		t.Error("a second logout started while one was running")
	}
	next, _ = m.Update(logoutDoneMsg{err: errors.New("timeout"), paused: 2})
	m = next.(model)
	if cfg.HasSessionCookie() || m.mode != modeLogin || m.loggingOut {
		t.Error("local session not cleared after a failed server logout")
	}
	if !strings.Contains(m.loginStatus, "服务端未确认") || !strings.Contains(m.loginStatus, "已暂停 2 个下载") {
		t.Errorf("status %q should report the paused downloads and the unconfirmed server logout", m.loginStatus)
	}
}
