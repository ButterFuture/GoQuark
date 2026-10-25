package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ButterFuture/GoQuark/internal/api"
	"github.com/ButterFuture/GoQuark/internal/client"
	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/download"
)

func browseModel(rows []api.FileEntry, ticked []int, cursor int) model {
	m := model{cursor: cursor, selected: map[int]bool{}}
	for _, e := range rows {
		m.rows = append(m.rows, row{entry: e})
	}
	for _, i := range ticked {
		m.selected[i] = true
	}
	return m
}

func entryNames(entries []api.FileEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func TestSelectedEntriesIncludesFolders(t *testing.T) {
	rows := []api.FileEntry{
		{Name: "..", Dir: true},
		{FID: "d1", Name: "资料", Dir: true},
		{FID: "f1", Name: "a.bin"},
		{FID: "f2", Name: "b.bin"},
	}
	cases := []struct {
		name   string
		ticked []int
		cursor int
		want   []string
	}{
		{name: "勾选文件夹和文件", ticked: []int{1, 2}, cursor: 3, want: []string{"资料", "a.bin"}},
		{name: "没勾选就用光标项", ticked: nil, cursor: 1, want: []string{"资料"}},
		{name: "光标停在返回上级时为空", ticked: nil, cursor: 0, want: nil},
		{name: "勾选里的返回上级被剔除", ticked: []int{0, 3}, cursor: 2, want: []string{"b.bin"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := entryNames(browseModel(rows, tc.ticked, tc.cursor).selectedEntries())
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("选中的是 %q，想要 %q", got, tc.want)
			}
		})
	}
}

func planTestManager(t *testing.T, destDir string) *download.Manager {
	t.Helper()
	m := download.NewManager(client.New(&config.Config{}), destDir)
	m.SetStatePath(filepath.Join(t.TempDir(), "state.json"))
	return m
}

// A queued job carries a FID and no link. Resolving at plan time is what made
// links expire while a long queue drained, so the plan must not do it.
func TestPlanDownloadsLeavesTheLinkToTheQueue(t *testing.T) {
	destDir := t.TempDir()
	mgr := planTestManager(t, destDir)
	sel := []api.FileEntry{{FID: "f1", Name: "片子.mkv", Size: 10}}

	msg := planDownloads(context.Background(), client.New(&config.Config{}), mgr, destDir, sel)
	if msg.err != nil {
		t.Fatalf("planDownloads: %v", msg.err)
	}
	if len(msg.jobs) != 1 {
		t.Fatalf("排了 %d 个任务，想要 1 个", len(msg.jobs))
	}
	j := msg.jobs[0]
	if j.url != "" {
		t.Fatalf("任务带上了链接 %q，应当留到取得名额时再取", j.url)
	}
	if j.fid != "f1" {
		t.Fatalf("任务的 FID 是 %q，想要 f1", j.fid)
	}
	if want := filepath.Join(destDir, "片子.mkv"); j.dest != want {
		t.Fatalf("目标是 %q，想要 %q", j.dest, want)
	}
}

// The name is built from whatever the drive sent, so a symlink sitting at the
// destination must stop the download rather than redirect it.
func TestPlanDownloadsRefusesNonRegularDest(t *testing.T) {
	destDir := t.TempDir()
	mgr := planTestManager(t, destDir)
	occupied := filepath.Join(destDir, "片子.mkv")
	if err := os.Symlink(filepath.Join(t.TempDir(), "别处"), occupied); err != nil {
		t.Skipf("这个系统建不了符号链接：%v", err)
	}
	sel := []api.FileEntry{{FID: "f1", Name: "片子.mkv", Size: 10}}

	msg := planDownloads(context.Background(), client.New(&config.Config{}), mgr, destDir, sel)
	if msg.err == nil {
		t.Fatal("目标是符号链接，应当拒绝")
	}
	if !strings.Contains(msg.err.Error(), "普通文件") {
		t.Fatalf("错误是 %q，想要说明目标不是普通文件", msg.err)
	}
}

func TestPlanDownloadsStopsOnCancel(t *testing.T) {
	destDir := t.TempDir()
	mgr := planTestManager(t, destDir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	msg := planDownloads(ctx, client.New(&config.Config{}), mgr, destDir, []api.FileEntry{{FID: "f1", Name: "a.bin"}})
	if msg.err == nil {
		t.Fatal("已取消的扫描应当返回错误")
	}
}

// A result from a scan the user already walked away from describes a session
// that is gone, so the model has to drop it.
func TestStaleScanResultIsDropped(t *testing.T) {
	m := model{mode: modeBrowse, selected: map[int]bool{}}
	m.scan.busy = true
	m.scan.gen = 7
	got, _ := m.Update(dlEnqueueMsg{scanGen: 6, status: "迟到的结果"})
	if mm, ok := got.(model); !ok || mm.mode != modeBrowse {
		t.Fatalf("过期结果不该改变界面状态，现在 mode=%v", got.(model).mode)
	}
}

func TestStopScanClearsState(t *testing.T) {
	m := model{}
	called := false
	m.scan.busy = true
	m.scan.cancel = func() { called = true }
	m.stopScan()
	if !called {
		t.Fatal("stopScan 没有取消正在跑的扫描")
	}
	if m.scan.busy || m.scan.cancel != nil {
		t.Fatal("stopScan 之后状态没清干净")
	}
}
