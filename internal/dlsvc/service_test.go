package dlsvc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ButterFuture/GoQuark/internal/download"
)

// fileServer serves size bytes with Range support; each part takes delay.
func fileServer(t *testing.T, size int64, delay time.Duration) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int64 = 0, size - 1
		if rg := strings.TrimPrefix(r.Header.Get("Range"), "bytes="); rg != "" {
			a, b, _ := strings.Cut(rg, "-")
			start, _ = strconv.ParseInt(a, 10, 64)
			if b != "" {
				end, _ = strconv.ParseInt(b, 10, 64)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			if end > start {
				time.Sleep(delay)
			}
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		}
		_, _ = w.Write(make([]byte, end-start+1))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type svc struct {
	cfgPath, exe string
	exited       chan struct{}
	err          error
}

// startService runs Serve on a fresh config dir. The dir is short because a
// unix socket path is limited to ~104 bytes on macOS.
func startService(t *testing.T) *svc {
	t.Helper()
	dir, err := os.MkdirTemp("", "gq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &svc{cfgPath: filepath.Join(dir, "config.json"), exe: filepath.Join(dir, "goquark"), exited: make(chan struct{})}
	if err := os.WriteFile(s.exe, []byte("v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldTick, oldIdle, oldExe := tickEvery, idleExit, executable
	tickEvery, idleExit = 50*time.Millisecond, 400*time.Millisecond
	executable = func() (string, error) { return s.exe, nil }
	// Cleanups run last-in first-out: the wait below runs first, so the
	// service is gone before the tunables go back.
	t.Cleanup(func() { tickEvery, idleExit, executable = oldTick, oldIdle, oldExe })
	t.Cleanup(func() {
		select {
		case <-s.exited:
		case <-time.After(5 * time.Second):
			t.Error("service still running at the end of the test")
		}
	})
	go func() { s.err = Serve(s.cfgPath, "test"); close(s.exited) }()
	c := newClient(s.cfgPath, false)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := c.Info(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not start")
		}
		c.failedAt = time.Time{}
		time.Sleep(20 * time.Millisecond)
	}
	c.Close()
	return s
}

func (s *svc) waitExit(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.exited:
		if s.err != nil {
			t.Fatal(s.err)
		}
	case <-time.After(within):
		t.Fatal("service did not exit")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func status(c *Client, id string) (download.Snapshot, bool) {
	c.listMu.Lock()
	c.listAt = time.Time{}
	c.listMu.Unlock()
	for _, s := range c.List() {
		if s.ID == id {
			return s, true
		}
	}
	return download.Snapshot{}, false
}

// Two processes (two clients) see and drive one queue, and only the service
// writes downloads.json.
func TestTwoClientsShareOneQueue(t *testing.T) {
	s := startService(t)
	srv := fileServer(t, 6<<20, 0)
	a, b := newClient(s.cfgPath, false), newClient(s.cfgPath, false)
	defer a.Close()
	defer b.Close()

	dest := filepath.Join(filepath.Dir(s.cfgPath), "f.bin")
	id, err := a.Add(download.EnqueueOpts{Name: "f.bin", URL: srv.URL, Dest: dest, FID: "fid1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "download done, seen from the other client", func() bool {
		snap, ok := status(b, id)
		return ok && snap.Status == download.StatusDone
	})
	if fi, err := os.Stat(dest); err != nil || fi.Size() != 6<<20 {
		t.Fatalf("file: %v %v", fi, err)
	}
	if got := b.FindAtDest("fid1", dest); got == nil || got.ID != id {
		t.Errorf("FindAtDest over the service = %v", got)
	}
	b.RemoveID(id)
	if _, ok := status(a, id); ok {
		t.Error("removal by one client not seen by the other")
	}
}

// A new binary does not interrupt a download: the service restarts only once
// nothing is downloading.
func TestRestartWaitsForDownloads(t *testing.T) {
	s := startService(t)
	srv := fileServer(t, 8<<20, 600*time.Millisecond) // parts take 0.6s
	c := newClient(s.cfgPath, false)
	defer c.Close()
	id, err := c.Add(download.EnqueueOpts{Name: "big", URL: srv.URL, Dest: filepath.Join(filepath.Dir(s.cfgPath), "big")})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "download running", func() bool { return c.ActiveCount() > 0 })

	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(s.exe, future, future); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restart pending", func() bool { in, _ := c.Info(); return in.RestartPending })
	if snap, _ := status(c, id); snap.Status == download.StatusDone {
		t.Fatal("download finished before the restart could be checked; slow the server")
	}
	select {
	case <-s.exited:
		t.Fatal("service restarted while a download was running")
	default:
	}
	s.waitExit(t, 15*time.Second)
	// The download it waited for completed.
	st, _ := os.ReadFile(statePath(filepath.Dir(s.cfgPath)))
	if !strings.Contains(string(st), `"status": "done"`) {
		t.Errorf("state after restart: %s", st)
	}
}

// With nothing to download the update applies at once, and an idle service
// with no client leaves on its own.
func TestRestartAtOnceWhenIdleAndIdleExit(t *testing.T) {
	s := startService(t)
	c := newClient(s.cfgPath, false)
	if _, err := c.Info(); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(s.exe, future, future)
	s.waitExit(t, 2*time.Second) // a connected client does not hold off an update
	c.Close()

	s2 := startService(t)
	s2.waitExit(t, 3*time.Second) // nobody connected, nothing running
}

// A config dir too deep for a socket path still gets a usable socket.
func TestLongConfigDirSocket(t *testing.T) {
	long := filepath.Join(os.TempDir(), strings.Repeat("d", 120))
	p := sockPath(long)
	if len(p) > maxSockPath {
		t.Fatalf("socket path %d bytes: %s", len(p), p)
	}
	if p != sockPath(long) || p == sockPath(long+"x") {
		t.Error("socket path must be stable per config dir and differ between dirs")
	}
}
