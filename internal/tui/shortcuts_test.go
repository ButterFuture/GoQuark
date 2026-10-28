package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

// The footer keeps whole hints only and always ends with the way to the rest.
func TestHintLineFitsAndKeepsMore(t *testing.T) {
	for _, w := range []int{20, 40, 60, 80, 100, 200} {
		got := hintLine(browseHints, w)
		if lipgloss.Width(got) > w {
			t.Errorf("width %d: %q is %d cells", w, got, lipgloss.Width(got))
		}
		if !strings.HasSuffix(got, moreHint) {
			t.Errorf("width %d: %q lacks %q", w, got, moreHint)
		}
		if strings.Contains(got, "…") && w >= lipgloss.Width(moreHint) {
			t.Errorf("width %d: %q cuts a hint in half", w, got)
		}
	}
	if got := hintLine(browseHints, 200); !strings.Contains(got, "o退出登录") {
		t.Errorf("wide footer drops hints: %q", got)
	}
}

// Every shortcut handled in browse mode is listed in the ? panel.
func TestShortcutPanel(t *testing.T) {
	m := model{width: 100, height: 30, ready: true, mode: modeBrowse}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = next.(model)
	if m.mode != modeModal || m.modalKind != "help" {
		t.Fatalf("? opened mode %v kind %q", m.mode, m.modalKind)
	}
	panel := m.renderModal()
	plain := xansi.Strip(panel)
	for _, want := range []string{"空格", "全选", "上一级", "下载中心", "个人信息", "退出登录", "检查更新", "刷新"} {
		if !strings.Contains(plain, want) {
			t.Errorf("panel lacks %q", want)
		}
	}
	if h := lipgloss.Height(panel); h > 24 {
		t.Errorf("panel is %d lines, taller than a 24-line terminal", h)
	}
	if w := lipgloss.Width(panel); w > 50 {
		t.Errorf("panel is %d cells wide, want the 48-cell box plus border", w)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if next.(model).mode != modeBrowse {
		t.Error("second ? did not close the panel")
	}
	t.Log("\n" + plain)
}

// With a Chinese input method ? arrives as the full-width ？.
func TestKeyLetterFoldsFullWidth(t *testing.T) {
	for in, want := range map[string]string{"？": "?", "?": "?", "ｄ": "d", "Ｄ": "d", "D": "d", "　": " ", "enter": "enter", "中": "中"} {
		if got := keyLetter(in); got != want {
			t.Errorf("keyLetter(%q) = %q, want %q", in, got, want)
		}
	}
	m := model{width: 100, height: 30, ready: true, mode: modeBrowse}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'？'}})
	if next.(model).modalKind != "help" {
		t.Error("full-width ？ did not open the shortcut list")
	}
}

// A key pressed on the ? panel closes it and does what the panel says.
func TestShortcutPanelRunsKey(t *testing.T) {
	m := model{width: 100, height: 30, ready: true, mode: modeBrowse}
	m = m.showShortcuts()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if got := next.(model).mode; got != modeDownloads {
		t.Fatalf("t on the panel: mode %v, want the download center", got)
	}
	m = next.(model).showShortcuts()
	if m.modalBack != modeDownloads {
		t.Fatal("panel opened from the download center does not return there")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := next.(model).mode; got != modeDownloads {
		t.Errorf("Esc on the panel: mode %v, want back in the download center", got)
	}
}
