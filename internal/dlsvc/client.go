package dlsvc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/ButterFuture/GoQuark/internal/download"
)

// Tunables; tests shorten them.
var (
	callTimeout  = 5 * time.Second
	waitTimeout  = 3 * time.Minute // PauseAllWait: in-flight parts finish writing
	startTimeout = 5 * time.Second // a spawned service to start listening
	retryAfter   = 2 * time.Second // after a failed (re)connect, fail fast this long
	listTTL      = 150 * time.Millisecond
)

// Client is a download.Controller backed by the download service. It starts
// the service when none is running and reconnects, starting a new one, when
// the service goes away (it restarts itself on an update). Calls that fail
// return zero values; the TUI keeps drawing and the next call tries again.
type Client struct {
	dir, cfgPath, exe string
	spawn             bool // start the service if it is not running

	mu       sync.Mutex
	conn     net.Conn
	rd       *bufio.Reader
	seq      uint64
	failedAt time.Time
	lastErr  error

	listMu  sync.Mutex
	listAt  time.Time
	listVal []download.Snapshot
}

var _ download.Controller = (*Client)(nil)

func newClient(cfgPath string, spawn bool) *Client {
	exe, _ := os.Executable()
	return &Client{dir: filepath.Dir(cfgPath), cfgPath: cfgPath, exe: exe, spawn: spawn}
}

// Err is the last error talking to the service, nil after a good call.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// Info reports the running service, starting it if needed.
func (c *Client) Info() (Info, error) {
	var in Info
	err := c.call("Info", params{}, &in)
	return in, err
}

func (c *Client) dial() (net.Conn, error) {
	return net.DialTimeout("unix", sockPath(c.dir), time.Second)
}

// connectLocked returns the shared connection, dialling (and if allowed
// starting the service) when there is none.
func (c *Client) connectLocked() error {
	if c.conn != nil {
		return nil
	}
	if !c.failedAt.IsZero() && time.Since(c.failedAt) < retryAfter {
		return c.lastErr
	}
	conn, err := c.dial()
	if err != nil && c.spawn {
		if err = c.startService(); err == nil {
			conn, err = c.waitDial()
		}
	}
	if err != nil {
		c.failedAt = time.Now()
		c.lastErr = fmt.Errorf("download service: %w", err)
		return c.lastErr
	}
	c.failedAt = time.Time{}
	c.conn, c.rd = conn, bufio.NewReaderSize(conn, 256<<10)
	return nil
}

func (c *Client) waitDial() (net.Conn, error) {
	deadline := time.Now().Add(startTimeout)
	for {
		conn, err := c.dial()
		if err == nil || time.Now().After(deadline) {
			return conn, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// startService launches `goquark download-service` detached, logging next to
// the config. A second one started by a racing client finds the lock held and
// exits at once.
func (c *Client) startService() error {
	if c.exe == "" {
		return errors.New("cannot find the goquark executable")
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath(c.dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	if fi, err := logf.Stat(); err == nil && fi.Size() > 1<<20 {
		_ = logf.Truncate(0)
	}
	cmd := exec.Command(c.exe, "download-service", "--config", c.cfgPath)
	cmd.Stdout, cmd.Stderr = logf, logf
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func (c *Client) dropLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn, c.rd = nil, nil
}

// exchange sends one request on conn and reads its response.
func exchange(conn net.Conn, rd *bufio.Reader, req request, timeout time.Duration) (response, bool, error) {
	var resp response
	_ = conn.SetDeadline(time.Now().Add(timeout))
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return resp, false, err
	}
	line, err := rd.ReadBytes('\n')
	if err != nil {
		return resp, true, err
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return resp, true, err
	}
	return resp, true, nil
}

// idempotent methods may be resent after the service went away mid-call.
var idempotent = map[string]bool{
	"Info": true, "List": true, "LiveCount": true, "ActiveCount": true, "IncompleteCount": true,
	"DestDir": true, "SetDestDir": true, "SetMaxLive": true, "FindAtDest": true, "BusyAtDest": true,
	"PauseAll": true, "PauseID": true, "PauseIDs": true, "ReloadSession": true,
}

func (c *Client) call(method string, p params, out any) error {
	if method == "PauseAllWait" {
		return c.callAlone(method, p, out)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if method != "List" {
		c.listMu.Lock()
		c.listAt = time.Time{}
		c.listMu.Unlock()
	}
	var resp response
	var err error
	for try := 0; try < 2; try++ {
		if err = c.connectLocked(); err != nil {
			return err
		}
		c.seq++
		var sent bool
		resp, sent, err = exchange(c.conn, c.rd, request{ID: c.seq, Method: method, Params: p}, callTimeout)
		if err == nil {
			break
		}
		// The service restarted (update) or died: reconnect, which starts a
		// new one, and resend unless it may already have acted.
		c.dropLocked()
		c.failedAt = time.Time{}
		if sent && !idempotent[method] {
			break
		}
	}
	if err != nil {
		c.lastErr = fmt.Errorf("download service: %w", err)
		return c.lastErr
	}
	c.lastErr = nil
	return decode(resp, out)
}

// callAlone runs a long call on a connection of its own, so the shared one
// stays free for the UI's polling meanwhile.
func (c *Client) callAlone(method string, p params, out any) error {
	conn, err := c.dial()
	if err != nil {
		c.mu.Lock()
		err = c.connectLocked() // starts the service if needed
		c.mu.Unlock()
		if err != nil {
			return err
		}
		if conn, err = c.dial(); err != nil {
			return err
		}
	}
	defer conn.Close()
	resp, _, err := exchange(conn, bufio.NewReader(conn), request{ID: 1, Method: method, Params: p}, waitTimeout)
	if err != nil {
		return fmt.Errorf("download service: %w", err)
	}
	return decode(resp, out)
}

func decode(resp response, out any) error {
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

// Close drops the connection; the service exits once idle.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked()
}

// --- download.Controller ---

func (c *Client) List() []download.Snapshot {
	// The TUI asks several times per frame; one answer serves a frame.
	c.listMu.Lock()
	if !c.listAt.IsZero() && time.Since(c.listAt) < listTTL {
		v := c.listVal
		c.listMu.Unlock()
		return v
	}
	c.listMu.Unlock()
	var v []download.Snapshot
	if c.call("List", params{}, &v) != nil {
		return nil
	}
	c.listMu.Lock()
	c.listAt, c.listVal = time.Now(), v
	c.listMu.Unlock()
	return v
}

func (c *Client) LiveCount() (live, queued, limit int) {
	var v [3]int
	_ = c.call("LiveCount", params{}, &v)
	return v[0], v[1], v[2]
}

func (c *Client) intCall(method string, p params) int {
	var n int
	_ = c.call(method, p, &n)
	return n
}

func (c *Client) boolCall(method string, p params) bool {
	var ok bool
	_ = c.call(method, p, &ok)
	return ok
}

func (c *Client) snapCall(method string, p params) *download.Snapshot {
	var s *download.Snapshot
	_ = c.call(method, p, &s)
	return s
}

func (c *Client) ActiveCount() int     { return c.intCall("ActiveCount", params{}) }
func (c *Client) IncompleteCount() int { return c.intCall("IncompleteCount", params{}) }
func (c *Client) DestDir() string {
	var s string
	_ = c.call("DestDir", params{}, &s)
	return s
}
func (c *Client) SetDestDir(dir string) { _ = c.call("SetDestDir", params{Dir: dir}, nil) }
func (c *Client) SetMaxLive(n int) int  { return c.intCall("SetMaxLive", params{N: n}) }
func (c *Client) Add(opt download.EnqueueOpts) (string, error) {
	var id string
	err := c.call("Add", params{Opt: &opt}, &id)
	return id, err
}
func (c *Client) PauseAll() int              { return c.intCall("PauseAll", params{}) }
func (c *Client) PauseAllWait() int          { return c.intCall("PauseAllWait", params{}) }
func (c *Client) PauseID(id string) bool     { return c.boolCall("PauseID", params{ID: id}) }
func (c *Client) PauseIDs(ids []string) int  { return c.intCall("PauseIDs", params{IDs: ids}) }
func (c *Client) CancelID(id string) bool    { return c.boolCall("CancelID", params{ID: id}) }
func (c *Client) CancelIDs(ids []string) int { return c.intCall("CancelIDs", params{IDs: ids}) }
func (c *Client) CancelAll() int             { return c.intCall("CancelAll", params{}) }
func (c *Client) ResumeID(id string) bool    { return c.boolCall("ResumeID", params{ID: id}) }
func (c *Client) ResumeIDs(ids []string) int { return c.intCall("ResumeIDs", params{IDs: ids}) }
func (c *Client) ResumePaused() int          { return c.intCall("ResumePaused", params{}) }
func (c *Client) ResumeQuitPaused() int      { return c.intCall("ResumeQuitPaused", params{}) }
func (c *Client) RemoveID(id string) bool    { return c.boolCall("RemoveID", params{ID: id}) }
func (c *Client) ClearFinished() int         { return c.intCall("ClearFinished", params{}) }
func (c *Client) ClearStaleDone() int        { return c.intCall("ClearStaleDone", params{}) }
func (c *Client) ClearQuitResumeFlags()      { _ = c.call("ClearQuitResumeFlags", params{}, nil) }
func (c *Client) FindAtDest(fid, dest string) *download.Snapshot {
	return c.snapCall("FindAtDest", params{FID: fid, Dest: dest})
}
func (c *Client) BusyAtDest(dest string) *download.Snapshot {
	return c.snapCall("BusyAtDest", params{Dest: dest})
}
func (c *Client) ReloadSession() { _ = c.call("ReloadSession", params{}, nil) }
