package tui

import (
	"strings"

	"github.com/ButterFuture/GoQuark/internal/dlsvc"
	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

// What quitting does to running downloads (config tui_quit_downloads).
const (
	quitPause      = "pause"
	quitBackground = "background" // the download service finishes them
)

const quitRememberLabel = "下次不再询问"

// quitPref is the remembered quit choice, "" to ask. Downloads that run in
// this process (no download service) die with it, so those always pause.
func (m model) quitPref() string {
	if dlsvc.InProcess() {
		return quitPause
	}
	if m.cfg != nil {
		switch m.cfg.TUIQuitDownloads {
		case quitPause, quitBackground:
			return m.cfg.TUIQuitDownloads
		}
	}
	return ""
}

// askQuit opens the quit dialog: the first time downloads are running, the
// choice between pausing them and letting them finish in the background;
// otherwise the plain confirmation, saying what happens to them.
func (m model) askQuit() model {
	active := m.dlMgr != nil && m.dlMgr.ActiveCount() > 0
	if active && m.quitPref() == "" {
		m.quitRemember = true
		m = m.showConfirm("退出程序", "", "quit_choice")
		m.modalBody = m.quitChoiceBody()
		return m
	}
	body := "确定要退出 GoQuark 吗？"
	if active {
		if m.quitPref() == quitBackground {
			body += "\n进行中的下载会在后台继续。"
		} else {
			body += "\n进行中的下载会暂停，下次打开可继续。"
		}
	}
	return m.showConfirm("退出程序", body, "quit")
}

func (m model) quitChoiceBody() string {
	box := "[ ]"
	if m.quitRemember {
		box = "[✓]"
	}
	return "还有下载在进行，退出后怎么处理它们？\n\n" + box + " " + quitRememberLabel
}

func (m model) toggleQuitRemember() model {
	m.quitRemember = !m.quitRemember
	m.modalBody = m.quitChoiceBody()
	return m
}

// chooseQuit quits with pref, saving it first when 下次不再询问 is checked.
func (m model) chooseQuit(pref string) (tea.Model, tea.Cmd) {
	if m.quitRemember && m.cfg != nil {
		m.cfg.TUIQuitDownloads = pref
		_ = m.cfg.Save()
	}
	if pref == quitBackground {
		m.stopScan()
		return m, tea.Quit
	}
	return m.beginQuit()
}

// quitRememberHit reports whether (x, y) is on the 下次不再询问 checkbox.
func (m model) quitRememberHit(x, y int) bool {
	if m.mode != modeModal || m.modalKind != "confirm" || m.confirmAction != "quit_choice" {
		return false
	}
	ox, oy, _, _ := m.overlayOrigin()
	for i, line := range strings.Split(m.renderModal(), "\n") {
		plain := xansi.Strip(line)
		j := strings.Index(plain, quitRememberLabel)
		if j < 0 || y != oy+i {
			continue
		}
		// the box sits four cells before the label: "[✓] "
		x0 := ox + xansi.StringWidth(plain[:j]) - 4
		x1 := ox + xansi.StringWidth(plain[:j]) + xansi.StringWidth(quitRememberLabel)
		return x >= x0 && x <= x1
	}
	return false
}
