package tui

import (
	"strings"
	"testing"

	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/download"
	tea "github.com/charmbracelet/bubbletea"
)

// busyDownloads is a download controller with active tasks; anything the
// quit path should not call panics through the nil interface.
type busyDownloads struct {
	download.Controller
	paused int
}

func (b *busyDownloads) ActiveCount() int  { return 2 }
func (b *busyDownloads) PauseAllWait() int { b.paused++; return 2 }

func quitModel(t *testing.T) (model, *busyDownloads) {
	cfg, err := config.Load(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	dl := &busyDownloads{}
	return model{width: 100, height: 30, ready: true, mode: modeBrowse, cfg: cfg, dlMgr: dl}, dl
}

func key(m model, k tea.KeyMsg) (model, tea.Cmd) {
	next, cmd := m.Update(k)
	return next.(model), cmd
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// The first quit with downloads running asks pause or background, with
// 下次不再询问 checked; the answer is saved and the next quit just confirms.
func TestQuitChoiceRemembered(t *testing.T) {
	m, dl := quitModel(t)
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if m.confirmAction != "quit_choice" || !strings.Contains(m.modalBody, "[✓] 下次不再询问") {
		t.Fatalf("first quit: action %q body %q", m.confirmAction, m.modalBody)
	}
	if ok, no := m.confirmLabels(); ok != "暂停下载并退出" || no != "后台继续下载" {
		t.Errorf("buttons %q / %q", ok, no)
	}

	// Esc leaves the dialog without quitting or pausing.
	if m2, cmd := key(m, tea.KeyMsg{Type: tea.KeyEsc}); m2.mode != modeBrowse || cmd != nil || dl.paused != 0 {
		t.Fatal("Esc on the quit choice must just close it")
	}

	m, _ = key(m, tea.KeyMsg{Type: tea.KeyRight}) // focus 后台继续下载
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !isQuit(cmd) || dl.paused != 0 {
		t.Fatalf("background: quit=%v paused=%d", isQuit(cmd), dl.paused)
	}
	if m.cfg.TUIQuitDownloads != quitBackground {
		t.Errorf("choice not remembered: %q", m.cfg.TUIQuitDownloads)
	}

	m.mode = modeBrowse
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if m.confirmAction != "quit" || !strings.Contains(m.modalBody, "后台继续") {
		t.Errorf("second quit should only confirm and say downloads go on: %q %q", m.confirmAction, m.modalBody)
	}
}

// Unchecking 下次不再询问 quits as chosen but asks again next time; pausing
// waits for the parts in flight.
func TestQuitChoiceNotRemembered(t *testing.T) {
	m, _ := quitModel(t)
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m, _ = key(m, tea.KeyMsg{Type: tea.KeySpace})
	if m.quitRemember || !strings.Contains(m.modalBody, "[ ] 下次不再询问") {
		t.Fatalf("space did not uncheck: %q", m.modalBody)
	}
	m, cmd := key(m, tea.KeyMsg{Type: tea.KeyEnter}) // 暂停下载并退出
	if !m.quitting || cmd == nil {
		t.Fatal("pause choice did not start the safe pause")
	}
	if m.cfg.TUIQuitDownloads != "" {
		t.Errorf("unchecked choice was saved: %q", m.cfg.TUIQuitDownloads)
	}
}
