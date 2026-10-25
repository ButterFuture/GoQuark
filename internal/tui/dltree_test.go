package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ButterFuture/GoQuark/internal/client"
	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/download"
	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

// snapAt is a task named by its path under root, the way a recursive
// download names its files.
func snapAt(root, rel string, st download.Status, total, done int64) download.Snapshot {
	rel = filepath.FromSlash(rel)
	return download.Snapshot{ID: rel, Name: rel, Dest: filepath.Join(root, rel), Status: st, Total: total, Done: done}
}

func rowNames(rows []dlRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.name
		if r.dir {
			out[i] += "/"
		}
	}
	return out
}

func TestDownloadLevelsFollowTheFolders(t *testing.T) {
	root := "/dl"
	list := []download.Snapshot{
		snapAt(root, "专辑/CD1/Track 10.flac", download.StatusRunning, 10, 5),
		snapAt(root, "单曲.mp3", download.StatusDone, 4, 4),
		snapAt(root, "专辑/CD1/Track 2.flac", download.StatusRunning, 10, 0),
		snapAt(root, "专辑/封面.jpg", download.StatusDone, 2, 2),
		snapAt(root, "专辑/CD2/Track 1.flac", download.StatusPaused, 10, 3),
	}

	top := dlLevel(list, nil, nil)
	if got, want := rowNames(top), []string{"专辑/", "单曲.mp3"}; !slices.Equal(got, want) {
		t.Fatalf("top level = %v, want %v", got, want)
	}
	album := top[0]
	if album.agg.files != 4 || album.agg.total != 32 || album.agg.done != 10 {
		t.Errorf("album sums = %d files, %d/%d bytes; want 4 files, 10/32", album.agg.files, album.agg.done, album.agg.total)
	}
	if album.agg.status != download.StatusRunning {
		t.Errorf("album status = %s, want running", album.agg.status)
	}

	path := []dlCrumb{{key: album.key, name: album.name}}
	inside := dlLevel(list, path, nil)
	if got, want := rowNames(inside), []string{"CD1/", "CD2/", "封面.jpg"}; !slices.Equal(got, want) {
		t.Fatalf("inside 专辑 = %v, want %v", got, want)
	}
	path = append(path, dlCrumb{key: inside[0].key, name: inside[0].name})
	cd1 := dlLevel(list, path, nil)
	if got, want := rowNames(cd1), []string{"Track 2.flac", "Track 10.flac"}; !slices.Equal(got, want) {
		t.Fatalf("inside CD1 = %v, want %v (same status: numbers in names sort as numbers)", got, want)
	}
}

// Every level: folders above files, and within each, downloading, failed,
// queued, paused, finished. Ties keep the newest first at the top level and
// go by name inside a folder.
func TestDownloadRowsSortFoldersFirstThenByStatus(t *testing.T) {
	root := "/dl"
	S := download.StatusRunning
	list := []download.Snapshot{ // Manager.List order: newest first
		snapAt(root, "done.bin", download.StatusDone, 1, 1),
		snapAt(root, "queued-new.bin", download.StatusQueued, 1, 0),
		snapAt(root, "排队的文件夹/a", download.StatusQueued, 1, 0),
		snapAt(root, "paused.bin", download.StatusPaused, 1, 0),
		snapAt(root, "failed.bin", download.StatusError, 1, 0),
		snapAt(root, "完成的文件夹/a", download.StatusDone, 1, 1),
		snapAt(root, "queued-old.bin", download.StatusQueued, 1, 0),
		snapAt(root, "running.bin", S, 1, 0),
		snapAt(root, "失败的文件夹/a", download.StatusError, 1, 0),
		snapAt(root, "下载中的文件夹/a", S, 1, 0),
		snapAt(root, "下载中的文件夹/b", download.StatusError, 1, 0),
	}
	want := []string{
		"下载中的文件夹/", "失败的文件夹/", "排队的文件夹/", "完成的文件夹/",
		"running.bin", "failed.bin", "queued-new.bin", "queued-old.bin", "paused.bin", "done.bin",
	}
	if got := rowNames(dlLevel(list, nil, nil)); !slices.Equal(got, want) {
		t.Errorf("top level =\n  %v\nwant\n  %v", got, want)
	}

	inside := []download.Snapshot{
		snapAt(root, "F/b.flac", download.StatusQueued, 1, 0),
		snapAt(root, "F/a.flac", download.StatusQueued, 1, 0),
		snapAt(root, "F/z.flac", S, 1, 0),
		snapAt(root, "F/sub/x", download.StatusDone, 1, 1),
		snapAt(root, "F/c.flac", download.StatusError, 1, 0),
	}
	top := dlLevel(inside, nil, nil)
	got := rowNames(dlLevel(inside, []dlCrumb{{key: top[0].key}}, nil))
	if want := []string{"sub/", "z.flac", "c.flac", "a.flac", "b.flac"}; !slices.Equal(got, want) {
		t.Errorf("inside F = %v, want %v", got, want)
	}
}

// The same folder name downloaded into two different roots is two folders.
func TestDownloadFoldersAreKeyedByLocalPath(t *testing.T) {
	list := []download.Snapshot{
		snapAt("/a", "资料/x.txt", download.StatusDone, 1, 1),
		snapAt("/b", "资料/x.txt", download.StatusDone, 1, 1),
	}
	if rows := dlLevel(list, nil, nil); len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
}

func TestAggregateStatus(t *testing.T) {
	r := download.StatusRunning
	cases := []struct {
		in   []download.Status
		want download.Status
	}{
		{[]download.Status{download.StatusDone, download.StatusPaused, r}, r},
		{[]download.Status{download.StatusDone, download.StatusQueued, download.StatusError}, download.StatusQueued},
		{[]download.Status{download.StatusDone, download.StatusPaused, download.StatusError}, download.StatusPaused},
		{[]download.Status{download.StatusDone, download.StatusError}, download.StatusError},
		{[]download.Status{download.StatusDone, download.StatusCancel}, download.StatusDone},
		{[]download.Status{download.StatusCancel, download.StatusCancel}, download.StatusCancel},
	}
	for _, c := range cases {
		var tasks []download.Snapshot
		for _, st := range c.in {
			tasks = append(tasks, download.Snapshot{Status: st, Total: 10})
		}
		if got := aggregate(tasks).status; got != c.want {
			t.Errorf("aggregate(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

// A cancelled file would hold a folder below 100% for ever if it counted.
func TestAggregateLeavesCancelledOutOfTheBytes(t *testing.T) {
	a := aggregate([]download.Snapshot{
		{Status: download.StatusDone, Total: 10, Done: 10},
		{Status: download.StatusCancel, Total: 90, Done: 5},
	})
	if a.percent() != 100 {
		t.Errorf("percent = %.1f, want 100", a.percent())
	}
}

func TestClampWindow(t *testing.T) {
	cases := []struct{ cursor, offset, n, h, wantC, wantO int }{
		{0, 0, 10, 4, 0, 0},
		{5, 0, 10, 4, 5, 2}, // scrolls down to show the cursor
		{1, 3, 10, 4, 1, 1}, // and back up
		{9, 0, 10, 4, 9, 6},
		{12, 0, 10, 4, 9, 6}, // cursor past the end lands on the last row
		{-3, 2, 10, 4, 0, 0}, // and before the start on the first
		{2, 8, 5, 4, 2, 1},   // list shrank: no empty space below the last row
		{0, 5, 0, 4, 0, 0},   // empty
		{3, 0, 10, 0, 3, 3},  // a zero-height window still shows the cursor
	}
	for _, c := range cases {
		gc, gotO := clampWindow(c.cursor, c.offset, c.n, c.h)
		if gc != c.wantC || gotO != c.wantO {
			t.Errorf("clampWindow(%d, %d, %d, %d) = %d, %d; want %d, %d",
				c.cursor, c.offset, c.n, c.h, gc, gotO, c.wantC, c.wantO)
		}
	}
}

// downloadsModel opens the download center on a manager holding tasks, as
// restored from history: nothing is downloading, so the list stays put.
func downloadsModel(t *testing.T, tasks []download.PersistTask, w, h int) model {
	t.Helper()
	// Not t.TempDir: the manager writes its state file in the background, and
	// a write landing during the cleanup would fail the test.
	dir, err := os.MkdirTemp("", "goquark-tui-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for i := range tasks {
		tasks[i].Dest = filepath.Join(dir, tasks[i].Name)
	}
	state := filepath.Join(dir, "downloads.json")
	b, err := json.Marshal(tasks)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, b, 0o600); err != nil {
		t.Fatal(err)
	}
	mgr := download.NewManager(client.New(&config.Config{}), dir)
	mgr.SetStatePath(state)
	m := model{
		cfg: &config.Config{DownloadDir: dir}, dlMgr: mgr, mode: modeDownloads,
		width: w, height: h, ready: true, selected: map[int]bool{},
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(model)
}

func pausedFiles(n int, name func(i int) string) []download.PersistTask {
	out := make([]download.PersistTask, n)
	for i := range out {
		out[i] = download.PersistTask{
			ID: fmt.Sprintf("t%d", i), Name: filepath.FromSlash(name(i)),
			Total: 100, Done: 40, Status: download.StatusPaused,
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		}
	}
	return out
}

func screen(m model) []string {
	return strings.Split(xansi.Strip(m.View()), "\n")
}

// focusedLine returns the first screen line of the highlighted row.
func focusedLine(t *testing.T, lines []string) string {
	t.Helper()
	for _, l := range lines {
		if strings.HasPrefix(l, "┃") {
			return l
		}
	}
	t.Fatal("no highlighted row on screen")
	return ""
}

// Issue #1: with more tasks than fit, moving down used to leave the
// highlighted row below the bottom of the terminal.
func TestDownloadCenterScrollsWithTheCursor(t *testing.T) {
	const height = 20
	m := downloadsModel(t, pausedFiles(30, func(i int) string { return fmt.Sprintf("file-%02d.bin", i) }), 100, height)
	for i := 0; i < 29; i++ {
		lines := screen(m)
		if len(lines) > height {
			t.Fatalf("after %d moves the view is %d lines, taller than the %d-line terminal", i, len(lines), height)
		}
		want := fmt.Sprintf("file-%02d.bin", i)
		if l := focusedLine(t, lines); !strings.Contains(l, want) {
			t.Fatalf("after %d moves the highlighted row is %q, want %s", i, l, want)
		}
		m = press(m, tea.KeyDown)
	}
	// pinned to the bottom of the screen, not pushed off it
	lines := screen(m)
	if !strings.Contains(lines[len(lines)-2], "第 30/30 项") {
		t.Errorf("summary line = %q, want it to show 第 30/30 项", lines[len(lines)-2])
	}
	for i := 0; i < 29; i++ {
		m = press(m, tea.KeyUp)
	}
	if l := focusedLine(t, screen(m)); !strings.Contains(l, "file-00.bin") {
		t.Errorf("back at the top the highlighted row is %q", l)
	}
	if m.dlOffset != 0 {
		t.Errorf("offset back at the top = %d, want 0", m.dlOffset)
	}
}

// Clicking used to pick the row above the one clicked, because the list was
// taken to start a line lower than it does.
func TestDownloadCenterClickPicksTheRowUnderTheMouse(t *testing.T) {
	m := downloadsModel(t, pausedFiles(30, func(i int) string { return fmt.Sprintf("file-%02d.bin", i) }), 100, 20)
	for i := 0; i < 10; i++ {
		m = press(m, tea.KeyDown)
	}
	lines := screen(m)
	clicked := 0
	for y, l := range lines {
		if !strings.Contains(l, "file-") || strings.Contains(l, "/") {
			continue // only the name line of each row
		}
		clicked++
		got := click(m, 5, y, tea.MouseActionRelease)
		name := strings.Fields(strings.TrimLeft(l, "┃ "))[1]
		if l := focusedLine(t, screen(got)); !strings.Contains(l, name) {
			t.Errorf("clicking line %d (%s) highlighted %q", y, name, l)
		}
	}
	if clicked != m.dlListH() {
		t.Errorf("clicked %d rows, want one per visible row (%d)", clicked, m.dlListH())
	}
}

func TestDownloadCenterOpensFoldersAndComesBack(t *testing.T) {
	tasks := pausedFiles(40, func(i int) string { return fmt.Sprintf("专辑/Track %d.flac", i+1) })
	tasks = append(tasks, pausedFiles(3, func(i int) string { return fmt.Sprintf("single-%d.bin", i) })...)
	for i := range tasks[40:] {
		tasks[40+i].ID = fmt.Sprintf("s%d", i)
		tasks[40+i].CreatedAt = time.Now().Add(time.Duration(i+1) * time.Minute)
	}
	m := downloadsModel(t, tasks, 100, 20)

	if rows := m.dlRows(); len(rows) != 4 {
		t.Fatalf("top level has %d rows, want the folder plus 3 files", len(rows))
	}
	m = press(m, tea.KeyDown)
	m = press(m, tea.KeyHome)
	if !m.dlRows()[m.dlCursor].dir {
		t.Fatal("expected the folder first at the top level")
	}
	m = press(m, tea.KeyEnter)
	if len(m.dlPath) != 1 || m.dlCursor != 0 {
		t.Fatalf("after enter: path %v, cursor %d", m.dlPath, m.dlCursor)
	}
	lines := screen(m)
	if !strings.Contains(lines[0], "› 专辑") {
		t.Errorf("title = %q, want the folder in it", lines[0])
	}
	if l := focusedLine(t, lines); !strings.Contains(l, "Track 1.flac") || strings.Contains(l, "专辑/") {
		t.Errorf("first row inside = %q, want Track 1.flac by its own name", l)
	}
	m = press(m, tea.KeyEnd)
	if l := focusedLine(t, screen(m)); !strings.Contains(l, "Track 40.flac") {
		t.Errorf("end of the folder = %q, want Track 40.flac", l)
	}
	m = press(m, tea.KeyEsc)
	if len(m.dlPath) != 0 || m.mode != modeDownloads || m.dlCursor != 0 {
		t.Fatalf("after esc: path %v, mode %v, cursor %d; want the top level on the folder", m.dlPath, m.mode, m.dlCursor)
	}
	m = press(m, tea.KeyEsc)
	if m.mode != modeBrowse {
		t.Errorf("esc at the top level should return to the drive")
	}
}

func TestDoubleClickOpensAFolder(t *testing.T) {
	m := downloadsModel(t, pausedFiles(3, func(i int) string { return fmt.Sprintf("资料/%d.txt", i) }), 100, 20)
	m = click(m, 5, dlHeaderLines, tea.MouseActionRelease)
	m = click(m, 5, dlHeaderLines, tea.MouseActionRelease)
	if len(m.dlPath) != 1 {
		t.Fatalf("double-click did not open the folder")
	}
}

// A folder that empties while it is open (here: its finished files cleared)
// closes, instead of leaving an empty page with no way to tell why.
func TestEmptiedFolderCloses(t *testing.T) {
	tasks := pausedFiles(3, func(i int) string { return fmt.Sprintf("资料/%d.txt", i) })
	for i := range tasks {
		tasks[i].Status = download.StatusDone
		tasks[i].Done = tasks[i].Total
	}
	tasks = append(tasks, pausedFiles(1, func(int) string { return "other.bin" })...)
	tasks[3].ID = "other"
	m := downloadsModel(t, tasks, 100, 20)
	m.dlCursor = dlRowOf(m.dlRows(), tasks[0].ID)
	m = press(m, tea.KeyEnter)
	if len(m.dlPath) != 1 {
		t.Fatal("folder did not open")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = next.(model)
	if len(m.dlPath) != 0 {
		t.Errorf("still inside the cleared folder: %v", m.dlPath)
	}
	if rows := m.dlRows(); len(rows) != 1 || rows[0].name != "other.bin" {
		t.Errorf("top level after clearing = %v", rowNames(rows))
	}
}

// Long names are cut to the width, so no row wraps and pushes the rest down.
func TestDownloadRowsNeverWrap(t *testing.T) {
	long := strings.Repeat("很长的文件名", 20) + ".bin"
	m := downloadsModel(t, pausedFiles(5, func(i int) string { return fmt.Sprintf("%s%d", long, i) }), 40, 20)
	for _, l := range screen(m) {
		if w := xansi.StringWidth(l); w > 40 {
			t.Errorf("line is %d cells wide on a 40-cell terminal: %q", w, l)
		}
	}
}

// Rows move as their status changes. The cursor stays on the row it was put
// on rather than on whatever slides into its place.
func TestCursorFollowsItsRowWhenTheOrderChanges(t *testing.T) {
	m := downloadsModel(t, pausedFiles(5, func(i int) string { return fmt.Sprintf("file-%d.bin", i) }), 100, 20)
	m = press(m, tea.KeyDown) // onto file-1
	if l := focusedLine(t, screen(m)); !strings.Contains(l, "file-1.bin") {
		t.Fatalf("highlighted %q, want file-1.bin", l)
	}
	now := time.Now()
	dlClock = func() time.Time { return now }
	t.Cleanup(func() { dlClock = time.Now })
	tick := func() {
		next, _ := m.Update(dlTickMsg{})
		m = next.(model)
	}

	m.dlMgr.CancelIDs([]string{"t1"}) // paused → cancelled: sorts to the bottom
	tick()
	if l := focusedLine(t, screen(m)); !strings.Contains(l, "file-1.bin") || !strings.Contains(l, "取消") || m.dlCursor != 1 {
		t.Errorf("right after the change: cursor %d on %q; want file-1, shown 取消, still in place", m.dlCursor, l)
	}
	now = now.Add(dlSettle)
	tick()
	if l := focusedLine(t, screen(m)); !strings.Contains(l, "file-1.bin") {
		t.Errorf("after file-1 moved the cursor is on %q", l)
	}
	if m.dlCursor != 4 {
		t.Errorf("cursor = %d, want 4 (file-1 now last)", m.dlCursor)
	}
}

func placedOrder(p dlPlaces, list []download.Snapshot) []string {
	return rowNames(dlLevel(list, nil, p))
}

// A row that changes status shows it where it is and moves only once the new
// status has held for dlSettle; one that flips back in time never moves. A
// paused row keeps the place it had before the pause.
func TestRowsSettleBeforeMoving(t *testing.T) {
	t0 := time.Now()
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	snap := func(name string, st download.Status, age int) download.Snapshot {
		s := snapAt("/dl", name, st, 10, 0)
		s.CreatedAt = t0.Add(-time.Duration(age) * time.Minute)
		return s
	}
	list := []download.Snapshot{
		snap("a", download.StatusRunning, 3),
		snap("b", download.StatusQueued, 2),
		snap("c", download.StatusQueued, 1),
	}
	p := dlPlaces{}
	p.settle(list, at(0))
	want := []string{"a", "c", "b"} // running, then the queued ones newest first
	if got := placedOrder(p, list); !slices.Equal(got, want) {
		t.Fatalf("start: %v, want %v", got, want)
	}

	list[1].Status = download.StatusError // b fails: belongs above the queued c
	p.settle(list, at(1*time.Second))
	p.settle(list, at(1*time.Second+dlSettle-time.Millisecond))
	if got := placedOrder(p, list); !slices.Equal(got, want) {
		t.Errorf("before dlSettle b moved: %v", got)
	}
	p.settle(list, at(1*time.Second+dlSettle))
	if got, want := placedOrder(p, list), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("after dlSettle: %v, want %v", got, want)
	}

	list[0].Status = download.StatusPaused // a paused: stays first
	for i := 0; i < 3; i++ {
		p.settle(list, at(10*time.Second+time.Duration(i)*dlSettle))
	}
	if got := placedOrder(p, list); got[0] != "a" {
		t.Errorf("paused a moved: %v", got)
	}

	list[2].Status = download.StatusRunning // c starts, then is back in the queue in time
	p.settle(list, at(30*time.Second))
	list[2].Status = download.StatusQueued
	p.settle(list, at(31*time.Second))
	p.settle(list, at(40*time.Second))
	if got, want := placedOrder(p, list), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("a status that flipped back moved the row: %v, want %v", got, want)
	}
}
