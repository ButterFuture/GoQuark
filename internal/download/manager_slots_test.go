package download

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ButterFuture/GoQuark/internal/client"
	"github.com/ButterFuture/GoQuark/internal/config"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(client.New(&config.Config{}), t.TempDir())
	m.SetStatePath(filepath.Join(t.TempDir(), "state.json"))
	// Registered after the TempDirs, so it runs before they are removed: a
	// task that winds down or a state write that lands during RemoveAll leaves
	// the directory non-empty and fails the test that owned it.
	t.Cleanup(func() { settle(m) })
	return m
}

// settle waits until every task's goroutine has returned and the state writes
// they queued are on disk. Tasks first: a task that is still finishing can
// start another write.
func settle(m *Manager) {
	m.mu.RLock()
	tasks := append([]*Task(nil), m.tasks...)
	m.mu.RUnlock()
	for _, task := range tasks {
		task.wg.Wait()
	}
	m.saves.Wait()
}

// waitFor polls until cond holds, so the tests never depend on a fixed sleep.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等 %s 超时", what)
}

// A task resolves its link only after it has taken a slot, so counting the
// resolvers in flight measures exactly how many downloads are live.
func TestManagerCapsLiveDownloads(t *testing.T) {
	m := newTestManager(t)
	var live, peak atomic.Int64
	gate := make(chan struct{})
	m.ResolveURL = func(fid string) (string, int64, error) {
		n := live.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-gate
		live.Add(-1)
		return "", 0, errors.New("这个测试不提供链接")
	}

	const total = defaultMaxLive * 2
	for i := 0; i < total; i++ {
		dest := filepath.Join(t.TempDir(), fmt.Sprintf("%d.bin", i))
		if _, err := m.EnqueueEx(EnqueueOpts{Name: fmt.Sprintf("f%d", i), FID: fmt.Sprintf("fid%d", i), Dest: dest}); err != nil {
			t.Fatalf("入队 %d：%v", i, err)
		}
	}

	waitFor(t, "名额被占满", func() bool { return live.Load() >= defaultMaxLive })
	close(gate)
	waitFor(t, "全部任务收尾", func() bool {
		for _, s := range m.List() {
			if holdsDest(s.Status) {
				return false
			}
		}
		return true
	})

	if got := peak.Load(); got != defaultMaxLive {
		t.Fatalf("同时在下的文件数峰值为 %d，想要正好 %d（%d 个任务排队）", got, defaultMaxLive, total)
	}
}

// max_downloads changes the cap for the queue that is already waiting: raising
// it starts more of the parked tasks at once.
func TestSetMaxLiveAppliesToTheWaitingQueue(t *testing.T) {
	m := newTestManager(t)
	if got := m.SetMaxLive(2); got != 2 {
		t.Fatalf("SetMaxLive(2) = %d", got)
	}
	var live atomic.Int64
	gate := make(chan struct{})
	m.ResolveURL = func(fid string) (string, int64, error) {
		live.Add(1)
		<-gate
		live.Add(-1)
		return "", 0, errors.New("这个测试不提供链接")
	}
	for i := 0; i < 6; i++ {
		dest := filepath.Join(t.TempDir(), fmt.Sprintf("%d.bin", i))
		if _, err := m.EnqueueEx(EnqueueOpts{Name: fmt.Sprintf("f%d", i), FID: fmt.Sprintf("fid%d", i), Dest: dest}); err != nil {
			t.Fatalf("入队 %d：%v", i, err)
		}
	}
	waitFor(t, "两个名额被占满", func() bool { return live.Load() == 2 })
	m.SetMaxLive(5)
	waitFor(t, "放宽到五个", func() bool { return live.Load() == 5 })
	if l, q, limit := m.LiveCount(); l != 5 || q != 1 || limit != 5 {
		t.Errorf("LiveCount = %d live, %d queued, limit %d; want 5, 1, 5", l, q, limit)
	}
	close(gate)
	waitFor(t, "全部任务收尾", func() bool {
		for _, s := range m.List() {
			if holdsDest(s.Status) {
				return false
			}
		}
		return true
	})
}

func TestSetMaxLiveBounds(t *testing.T) {
	m := newTestManager(t)
	for _, c := range []struct{ in, want int }{{0, defaultMaxLive}, {-1, defaultMaxLive}, {1, 1}, {10, 10}, {50, maxLiveCap}} {
		if got := m.SetMaxLive(c.in); got != c.want {
			t.Errorf("SetMaxLive(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// Two tasks on one file would interleave their parts and share a single
// .gqparts, and the loser would still report as complete.
func TestEnqueueRefusesBusyDest(t *testing.T) {
	m := newTestManager(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	m.ResolveURL = func(fid string) (string, int64, error) {
		<-block
		return "", 0, errors.New("停住")
	}
	dest := filepath.Join(t.TempDir(), "一个文件.bin")
	if _, err := m.EnqueueEx(EnqueueOpts{Name: "第一个", FID: "fid1", Dest: dest}); err != nil {
		t.Fatalf("第一次入队就失败了：%v", err)
	}
	_, err := m.EnqueueEx(EnqueueOpts{Name: "第二个", FID: "fid2", Dest: dest})
	if err == nil {
		t.Fatal("同一个目标路径第二次入队应当被拒绝")
	}
	if got := m.BusyAtDest(dest); got == nil {
		t.Fatal("BusyAtDest 没找到占着这个路径的任务")
	}
}

// One cloud file can legitimately be wanted at two paths in one recursive
// download, so dedupe has to look at the whole path and not just the folder.
func TestFindAtDestSeparatesPaths(t *testing.T) {
	m := newTestManager(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	m.ResolveURL = func(fid string) (string, int64, error) {
		<-block
		return "", 0, errors.New("停住")
	}
	root := t.TempDir()
	first := filepath.Join(root, "甲", "报告.pdf")
	second := filepath.Join(root, "乙", "报告.pdf")
	a, err := m.EnqueueEx(EnqueueOpts{Name: "甲/报告.pdf", FID: "same", Dest: first})
	if err != nil {
		t.Fatalf("入队 1：%v", err)
	}
	b, err := m.EnqueueEx(EnqueueOpts{Name: "乙/报告.pdf", FID: "same", Dest: second})
	if err != nil {
		t.Fatalf("入队 2 被拒了，但它是另一个路径：%v", err)
	}
	if a.ID == b.ID {
		t.Fatal("两个路径应当是两个任务")
	}
	got := m.FindAtDest("same", first)
	if got == nil || got.ID != a.ID {
		t.Fatalf("FindAtDest(%q) 返回 %v，想要 %s", first, got, a.ID)
	}
	got = m.FindAtDest("same", second)
	if got == nil || got.ID != b.ID {
		t.Fatalf("FindAtDest(%q) 返回 %v，想要 %s", second, got, b.ID)
	}
	if m.FindAtDest("same", filepath.Join(root, "丙", "报告.pdf")) != nil {
		t.Fatal("没下载到的路径不该匹配出任务")
	}
}

// Done is written by the part workers while the dispatcher and the periodic
// flush read it. Ranging over a map that is being written to is a hard runtime
// fault, so this has to hold under -race.
func TestPartMapSurvivesConcurrentUse(t *testing.T) {
	const parts = 64
	p := newPartMap(1024, parts*1024)
	// Two groups, not one: the readers only stop once stop is closed, and that
	// cannot happen until the writers have been waited for.
	var writers, readers sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < parts; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			p.mark(int64(i) * 1024)
		}(i)
	}
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := p.encode(); err != nil {
					t.Errorf("encode: %v", err)
					return
				}
				p.completedBytes()
				p.done(0)
			}
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()

	if got := p.completedBytes(); got != parts*1024 {
		t.Fatalf("completedBytes = %d，想要 %d", got, parts*1024)
	}
}
