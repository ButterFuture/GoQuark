package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// keyHint is one shortcut: the key as typed and what it does.
type keyHint struct{ key, desc string }

// keyGroup is a titled block of the full shortcut list.
type keyGroup struct {
	title string
	keys  []keyHint
}

// Footer hints, most useful first: hintLine drops from the end when the
// terminal is narrow, so the order is the priority.
var (
	browseHints = []keyHint{
		{"空格", "多选"}, {"d", "下载"}, {"t", "下载中心"}, {"右键/m", "菜单"}, {"q", "退出"},
		{"enter", "打开"}, {"a", "全选"}, {"c", "清除"}, {"i", "信息"}, {"u", "检查更新"}, {"o", "退出登录"},
	}
	downloadHints = []keyHint{
		{"enter", "打开文件夹"}, {"p", "暂停"}, {"r", "恢复"}, {"x", "取消"}, {"c", "清除已完成"},
		{"右键/m", "菜单"}, {"u", "检查更新"}, {"q", "退出"},
	}
)

// Everything the ? panel lists; keep in step with handleKey.
var (
	browseKeys = []keyGroup{
		{"选择", []keyHint{{"单击", "选中"}, {"空格", "多选"}, {"a", "全选"}, {"c", "清除选择"}}},
		{"浏览", []keyHint{{"enter/双击", "打开"}, {"←/h", "上一级"}, {"↑↓/jk", "移动"}, {"PgUp/PgDn", "翻页"}, {"g/e", "首行/末行"}, {"r", "刷新"}}},
		{"下载与账号", []keyHint{{"d", "下载所选"}, {"t", "下载中心"}, {"右键/m", "菜单"}, {"i", "个人信息"}, {"o", "退出登录"}, {"u", "检查更新"}}},
		{"其他", []keyHint{{"?", "快捷键"}, {"q", "退出"}}},
	}
	downloadKeys = []keyGroup{
		{"浏览", []keyHint{{"↑↓/jk", "移动"}, {"PgUp/PgDn", "翻页"}, {"g/e", "首行/末行"}, {"enter/双击", "打开文件夹"}, {"esc/b", "返回"}}},
		{"任务", []keyHint{{"p", "全部暂停"}, {"r", "全部恢复"}, {"x", "全部取消"}, {"c", "清除已完成"}}},
		{"其他", []keyHint{{"右键/m", "菜单"}, {"u", "检查更新"}, {"?", "快捷键"}, {"q", "退出"}}},
	}
)

const moreHint = "?全部快捷键"

// hintLine fits as many hints as width allows, in order, and always ends with
// moreHint so the rest stays one key away.
func hintLine(hints []keyHint, width int) string {
	const sep = " · "
	line := ""
	for _, h := range hints {
		next := h.key + h.desc
		if line != "" {
			next = line + sep + next
		}
		if lipgloss.Width(next+sep+moreHint) > width {
			break
		}
		line = next
	}
	if line == "" {
		return truncate(moreHint, width)
	}
	return line + sep + moreHint
}

// Shortcut panel layout: two columns of key + description inside the 44-cell
// body of okBox.
const (
	keyColW  = 11
	cellW    = 21
	cellGapW = 2
)

// shortcutsBody renders groups as the ? panel's text.
func shortcutsBody(groups []keyGroup) string {
	var lines []string
	for i, g := range groups {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, notesHeadSt.Render(g.title))
		for j := 0; j < len(g.keys); j += 2 {
			row := padCells(keyCell(g.keys[j]), cellW)
			if j+1 < len(g.keys) {
				row += strings.Repeat(" ", cellGapW) + keyCell(g.keys[j+1])
			}
			lines = append(lines, strings.TrimRight(row, " "))
		}
	}
	return strings.Join(lines, "\n")
}

func keyCell(h keyHint) string { return padCells(h.key, keyColW) + h.desc }

func padCells(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s + " "
}

// showShortcuts opens the ? panel for the current view.
func (m model) showShortcuts() model {
	groups := browseKeys
	if m.mode == modeDownloads {
		groups = downloadKeys
	}
	return m.showModal("快捷键", shortcutsBody(groups), "help", m.mode)
}
