package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ButterFuture/GoQuark/internal/download"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

// The download center lists one folder level at a time. A recursive download
// queues one task per file and names it by its path under the download root
// ("专辑/CD1/01.flac"), so the folders can be rebuilt from the task names
// alone: a folder row stands for every file underneath it, and opening it
// lists what is directly inside.

const (
	dlHeaderLines = 2 // title, save path
	dlFooterLines = 3 // blank, summary, help
	dlRowLines    = 3 // name, progress, path
)

// dlRow is one row of the download center: a file task, or a folder.
type dlRow struct {
	// key is the folder's local path, or the task ID for a file.
	key  string
	name string
	dir  bool
	// tasks are the files the row stands for: its own task for a file row,
	// every file underneath for a folder row.
	tasks []download.Snapshot
	agg   dlAgg
}

// dlAgg sums a folder's files into what one row can show.
type dlAgg struct {
	status      download.Status
	total, done int64
	speed       float64
	eta         time.Duration
	hasETA      bool
	files       int
	doneN       int
	errN        int
}

// dlCrumb is one opened folder, outermost first in model.dlPath.
type dlCrumb struct {
	key, name string
	// cursor and offset in the level it was opened from, restored on the way
	// back out
	cursor, offset int
}

// taskParts splits a task name into its path components. Names are built with
// filepath.Join, and every component was sanitised of both separators first,
// so splitting on either is safe on any OS.
func taskParts(name string) []string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return []string{name}
	}
	return parts
}

// folderKeys returns the local path of every folder in a task's name,
// outermost first. They are taken from Dest rather than the name, so one
// folder name downloaded under two different roots stays two folders.
func folderKeys(s download.Snapshot, parts []string) []string {
	n := len(parts) - 1
	if n <= 0 {
		return nil
	}
	dir := filepath.Join(parts[:n]...)
	if s.Dest != "" {
		dir = filepath.Dir(s.Dest)
	}
	keys := make([]string, n)
	for i := n - 1; i >= 0; i-- {
		keys[i] = dir
		dir = filepath.Dir(dir)
	}
	return keys
}

// dlLevel returns the rows directly inside the opened folder path (empty =
// top level). Every level lists its folders first and its files below, and
// each of the two by status (dlRank): downloading, then failed, then queued,
// then paused, then finished. Ties go newest download first at the top level
// and by name inside a folder; neither changes with status, so a row moves
// only when its rank does. places, when not
// nil, holds where each row currently sits (see dlPlaces); a row it does not
// know is placed by its status.
func dlLevel(list []download.Snapshot, path []dlCrumb, places dlPlaces) []dlRow {
	depth := len(path)
	var rows []dlRow
	index := map[string]int{}
	for _, s := range list {
		parts := taskParts(s.Name)
		keys := folderKeys(s, parts)
		if depth > 0 && (len(keys) < depth || keys[depth-1] != path[depth-1].key) {
			continue
		}
		if len(keys) == depth {
			rows = append(rows, dlRow{key: s.ID, name: parts[len(parts)-1], tasks: []download.Snapshot{s}})
			continue
		}
		k := keys[depth]
		i, ok := index[k]
		if !ok {
			i = len(rows)
			index[k] = i
			rows = append(rows, dlRow{key: k, name: parts[depth], dir: true})
		}
		rows[i].tasks = append(rows[i].tasks, s)
	}
	for i := range rows {
		rows[i].agg = aggregate(rows[i].tasks)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].dir != rows[j].dir {
			return rows[i].dir
		}
		if ri, rj := places.rankOf(rows[i]), places.rankOf(rows[j]); ri != rj {
			return ri < rj
		}
		if depth == 0 {
			return rows[i].newest().After(rows[j].newest())
		}
		return naturalLess(rows[i].name, rows[j].name)
	})
	return rows
}

// dlRank places a status in the list: what is moving first, then what failed
// and needs a retry, then what waits its turn, then what was paused, and what
// is over last.
func dlRank(st download.Status) int {
	switch st {
	case download.StatusRunning, download.StatusFinalizing:
		return 0
	case download.StatusError:
		return 1
	case download.StatusQueued:
		return 2
	case download.StatusPausing, download.StatusPaused:
		return 3
	case download.StatusDone:
		return 4
	}
	return 5
}

// dlSettle is how long a row that changed status keeps its place before it
// moves to the new one. The new status shows at once, where the row is; the
// row jumping away the instant a file fails or finishes is hard to follow.
const dlSettle = 4 * time.Second

// dlClock is time.Now; tests swap it to step through dlSettle.
var dlClock = time.Now

// dlPlaces records the rank each row is sorted by, keyed like dlRow.key: task
// IDs for files, local paths for folders, at every level.
type dlPlaces map[string]*dlPlace

type dlPlace struct {
	rank int
	// pending is the rank the row is due to move to, -1 for none, first
	// seen at since
	pending int
	since   time.Time
}

func (p dlPlaces) rankOf(r dlRow) int {
	if pl, ok := p[r.key]; ok {
		return pl.rank
	}
	return dlRank(r.status())
}

// settle brings the places up to date with list at now. A row seen for the
// first time is placed by its status straight away. A changed status moves
// the row only once it has held for dlSettle, so a folder that is between two
// files for a moment does not move at all. A paused row never moves: it
// stays where it was when it was paused.
func (p dlPlaces) settle(list []download.Snapshot, now time.Time) {
	groups := map[string][]download.Snapshot{}
	for _, s := range list {
		groups[s.ID] = append(groups[s.ID], s)
		for _, k := range folderKeys(s, taskParts(s.Name)) {
			groups[k] = append(groups[k], s)
		}
	}
	for k := range p {
		if _, ok := groups[k]; !ok {
			delete(p, k)
		}
	}
	for k, tasks := range groups {
		st := tasks[0].Status
		if len(tasks) > 1 || tasks[0].ID != k {
			st = aggregate(tasks).status
		}
		r := dlRank(st)
		pl, ok := p[k]
		switch {
		case !ok:
			p[k] = &dlPlace{rank: r, pending: -1}
		case st == download.StatusPaused || st == download.StatusPausing || r == pl.rank:
			pl.pending = -1
		case pl.pending != r:
			pl.pending, pl.since = r, now
		case now.Sub(pl.since) >= dlSettle:
			pl.rank, pl.pending = r, -1
		}
	}
}

// newest is when the row's latest file was queued.
func (r dlRow) newest() time.Time {
	var t time.Time
	for _, s := range r.tasks {
		if s.CreatedAt.After(t) {
			t = s.CreatedAt
		}
	}
	return t
}

// status is the row's own status for a file, the combined one for a folder.
func (r dlRow) status() download.Status {
	if r.dir {
		return r.agg.status
	}
	return r.tasks[0].Status
}

// dlRowOf returns the index of the row holding task id, or -1.
func dlRowOf(rows []dlRow, id string) int {
	for i, r := range rows {
		for _, s := range r.tasks {
			if s.ID == id {
				return i
			}
		}
	}
	return -1
}

// ids returns the task IDs a row stands for.
func (r dlRow) ids() []string {
	out := make([]string, len(r.tasks))
	for i, s := range r.tasks {
		out[i] = s.ID
	}
	return out
}

// aggregate sums tasks into one status and one progress figure. The status is
// the most pressing one among the files: anything still moving beats paused,
// paused beats failed, and only a folder with nothing left to do is done.
// Cancelled files are left out of the byte count, or a folder with one
// cancelled file could never reach 100%.
func aggregate(tasks []download.Snapshot) dlAgg {
	a := dlAgg{files: len(tasks)}
	count := map[download.Status]int{}
	for _, s := range tasks {
		count[s.Status]++
	}
	a.doneN = count[download.StatusDone]
	a.errN = count[download.StatusError]
	allCancelled := count[download.StatusCancel] == len(tasks)
	var remain int64
	for _, s := range tasks {
		if s.Status == download.StatusCancel && !allCancelled {
			continue
		}
		// tui has its own int min/max, so int64 is clamped by hand
		done := s.Done
		if s.Total > 0 && done > s.Total {
			done = s.Total
		}
		a.total += s.Total
		a.done += done
		switch s.Status {
		case download.StatusRunning, download.StatusQueued, download.StatusFinalizing:
			a.speed += s.Speed
			if s.Total > done {
				remain += s.Total - done
			}
		}
	}
	switch {
	case count[download.StatusRunning]+count[download.StatusFinalizing] > 0:
		a.status = download.StatusRunning
	case count[download.StatusPausing] > 0:
		a.status = download.StatusPausing
	case count[download.StatusQueued] > 0:
		a.status = download.StatusQueued
	case count[download.StatusPaused] > 0:
		a.status = download.StatusPaused
	case count[download.StatusError] > 0:
		a.status = download.StatusError
	case count[download.StatusDone] > 0:
		a.status = download.StatusDone
	default:
		a.status = download.StatusCancel
	}
	if a.speed > 1 && remain > 0 {
		a.eta = time.Duration(float64(remain)/a.speed*float64(time.Second)) + time.Second/2
		if a.eta > 99*time.Hour {
			a.eta = 99 * time.Hour
		}
		a.hasETA = true
	}
	return a
}

// percent is the folder's progress by bytes, or by files when no sizes are
// known (records from before sizes were kept, or a folder of empty files).
func (a dlAgg) percent() float64 {
	if a.total > 0 {
		return float64(a.done) * 100 / float64(a.total)
	}
	if a.files > 0 {
		return float64(a.doneN) * 100 / float64(a.files)
	}
	return 0
}

// clampWindow keeps cursor on one of n rows and the h-row window starting at
// offset around it, without scrolling past the last row.
func clampWindow(cursor, offset, n, h int) (int, int) {
	if n <= 0 {
		return 0, 0
	}
	h = max(h, 1)
	cursor = min(max(cursor, 0), n-1)
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+h {
		offset = cursor - h + 1
	}
	offset = min(max(offset, 0), max(n-h, 0))
	return cursor, offset
}

// naturalLess orders names the way a file manager does: case-insensitive, with
// runs of digits compared as numbers, so "2.flac" comes before "10.flac".
func naturalLess(a, b string) bool {
	ar, br := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' }
	i, j := 0, 0
	for i < len(ar) && j < len(br) {
		if isDigit(ar[i]) && isDigit(br[j]) {
			si, sj := i, j
			for i < len(ar) && isDigit(ar[i]) {
				i++
			}
			for j < len(br) && isDigit(br[j]) {
				j++
			}
			na := strings.TrimLeft(string(ar[si:i]), "0")
			nb := strings.TrimLeft(string(br[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ar[i] != br[j] {
			return ar[i] < br[j]
		}
		i++
		j++
	}
	if rest := (len(ar) - i) - (len(br) - j); rest != 0 {
		return rest < 0
	}
	return a < b
}

// dlListH is how many rows fit between the header and the footer.
func (m model) dlListH() int {
	return max(1, (m.height-dlHeaderLines-dlFooterLines)/dlRowLines)
}

// dlRows returns the rows of the level currently open.
func (m model) dlRows() []dlRow {
	if m.dlMgr == nil {
		return nil
	}
	return dlLevel(m.dlMgr.List(), m.dlPath, m.dlPlaces)
}

// inDownloads reports whether the download center is on screen, on its own or
// under its menu or a dialog.
func (m model) inDownloads() bool {
	switch m.mode {
	case modeDownloads:
		return true
	case modeMenu:
		return m.menuFrom == modeDownloads
	case modeModal:
		return m.modalBack == modeDownloads
	}
	return false
}

// syncDownloads closes opened folders that have emptied (cleared, or every
// file removed) and keeps the cursor on a row inside the visible window. A
// cursor that was not moved since the last sync follows its row, which may
// have moved as its status changed.
// Update runs it after every message, so keys, ticks and resizes all land on a
// consistent view.
func (m *model) syncDownloads() {
	m.settleDownloads()
	rows := m.dlRows()
	for len(rows) == 0 && len(m.dlPath) > 0 {
		m.dlLeaveFolder()
		rows = m.dlRows()
	}
	if m.dlCursor == m.dlSyncedCursor && m.dlCursorKey != "" {
		for i, r := range rows {
			if r.key == m.dlCursorKey {
				m.dlCursor = i
				break
			}
		}
	}
	m.dlCursor, m.dlOffset = clampWindow(m.dlCursor, m.dlOffset, len(rows), m.dlListH())
	m.dlSyncedCursor, m.dlCursorKey = m.dlCursor, ""
	if m.dlCursor < len(rows) {
		m.dlCursorKey = rows[m.dlCursor].key
	}
}

// settleDownloads moves rows whose new status has held long enough. It also
// runs while the center is closed, so rows that changed out of sight are in
// their new places when it opens.
func (m *model) settleDownloads() {
	if m.dlMgr == nil {
		return
	}
	if m.dlPlaces == nil {
		m.dlPlaces = dlPlaces{}
	}
	m.dlPlaces.settle(m.dlMgr.List(), dlClock())
}

// dlOpenFolder descends into row r.
func (m *model) dlOpenFolder(r dlRow) {
	m.dlPath = append(m.dlPath, dlCrumb{key: r.key, name: r.name, cursor: m.dlCursor, offset: m.dlOffset})
	m.dlCursor, m.dlOffset = 0, 0
	m.dlCursorKey = ""
}

// dlLeaveFolder goes back up one level, onto the folder it came from.
func (m *model) dlLeaveFolder() {
	c := m.dlPath[len(m.dlPath)-1]
	m.dlPath = m.dlPath[:len(m.dlPath)-1]
	m.dlCursor, m.dlOffset = c.cursor, c.offset
	// back onto the folder, wherever it has moved to meanwhile
	m.dlCursorKey, m.dlSyncedCursor = c.key, c.cursor
}

// dlHome resets the center to its top level, first row.
func (m *model) dlHome() {
	m.dlPath = nil
	m.dlCursor, m.dlOffset = 0, 0
	m.dlCursorKey = ""
}

// dlRowAt maps a screen row to the list row drawn there.
func (m model) dlRowAt(y, n int) (int, bool) {
	rel := y - dlHeaderLines
	if rel < 0 {
		return 0, false
	}
	slot := rel / dlRowLines
	if slot >= m.dlListH() {
		return 0, false
	}
	idx := m.dlOffset + slot
	if idx >= n {
		return 0, false
	}
	return idx, true
}

// dlStatusLabel is how a status reads in the download center, and its color.
func dlStatusLabel(st download.Status) (label, fg string) {
	switch st {
	case download.StatusRunning:
		return "下载中", "39"
	case download.StatusFinalizing:
		return "收尾中", "213"
	case download.StatusPausing:
		return "暂停中", "214"
	case download.StatusQueued:
		return "排队", "117"
	case download.StatusPaused:
		return "已暂停", "214"
	case download.StatusDone:
		return "完成", "42"
	case download.StatusError:
		return "失败", "196"
	case download.StatusCancel:
		return "取消", "244"
	}
	return string(st), "252"
}

// dlBlock lays out one row's lines with the same left gutter whether focused
// or not; a focused row gets the accent bar and its background on every cell.
// Lines are cut to the width first, so a long name can never wrap and push the
// rows below it out of the window.
func dlBlock(lines []string, focused bool, w int) string {
	var out strings.Builder
	for i, line := range lines {
		line = xansi.Truncate(line, max(w-3, 1), "…")
		if focused {
			accent := lipgloss.NewStyle().
				Foreground(lipgloss.Color("135")).
				Background(lipgloss.Color("237")).
				Render("┃")
			padL := lipgloss.NewStyle().Background(lipgloss.Color("237")).Render("  ")
			fillN := max(w-3-lipgloss.Width(line), 0)
			padR := lipgloss.NewStyle().Background(lipgloss.Color("237")).Render(strings.Repeat(" ", fillN))
			out.WriteString(accent + padL + line + padR)
		} else {
			out.WriteString("   " + line)
		}
		if i < len(lines)-1 {
			out.WriteString("\n")
		}
	}
	return out.String()
}

// renderDownloadFolder draws a folder row: its combined status, a bar over the
// total size of every file underneath, and the local folder path.
func (m model) renderDownloadFolder(r dlRow, focused bool) string {
	w := max(24, m.width)
	hlBG := ""
	if focused {
		hlBG = "237"
	}
	a := r.agg

	barW := 24
	if w > 60 {
		barW = min(40, w/3)
	}
	pct := a.percent()
	filled := int(float64(barW) * pct / 100)
	if a.status == download.StatusDone {
		filled = barW
	}
	bar := progressBar(filled, barW, a.status, hlBG)

	label, fg := dlStatusLabel(a.status)
	stStyle := withBG(lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Bold(true), hlBG)
	prefix := stStyle.Render(label)
	if a.status == download.StatusPausing {
		prefix = stStyle.Render(m.spinner.View() + " " + label)
	}
	sp := withBG(lipgloss.NewStyle(), hlBG).Render("  ")
	nameLine := prefix + sp + withBG(nameDir, hlBG).Bold(true).Render("📁 "+r.name+"/")

	meta := fmt.Sprintf("  %.1f%%", pct)
	if a.speed > 0 {
		meta += fmt.Sprintf("  %.1f MiB/s", a.speed/1024/1024)
	}
	if a.total > 0 {
		meta += fmt.Sprintf("  %s / %s", humanSize(a.done), humanSize(a.total))
	}
	meta += fmt.Sprintf("  · %d/%d 个文件", a.doneN, a.files)
	if a.hasETA {
		meta += " · 剩余 " + download.FormatDuration(a.eta)
	}
	metaStyle := descDim
	if focused {
		metaStyle = descOnSel
	}
	brStyle := withBG(lipgloss.NewStyle().Foreground(lipgloss.Color("252")), hlBG)
	subLine := brStyle.Render("[") + bar + brStyle.Render("]") + withBG(metaStyle, hlBG).Render(meta)
	if a.errN > 0 {
		subLine += withBG(lipgloss.NewStyle().Foreground(lipgloss.Color("196")), hlBG).
			Render(fmt.Sprintf(" · %d 个失败", a.errN))
	}

	pathSt := pathStyle
	if focused {
		pathSt = descOnSel
	}
	pathLine := withBG(pathSt, hlBG).Render(r.key)

	return dlBlock([]string{nameLine, subLine, pathLine}, focused, w)
}

// renderDownloadRow draws row r of the open level.
func (m model) renderDownloadRow(r dlRow, focused bool, dlRoot string) string {
	if r.dir {
		return m.renderDownloadFolder(r, focused)
	}
	// inside a folder the row shows the file's own name, not its whole path
	s := r.tasks[0]
	s.Name = r.name
	return m.renderDownloadItem(s, focused, dlRoot)
}
