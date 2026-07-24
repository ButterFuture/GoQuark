package download

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ButterFuture/GoQuark/internal/client"
)

// Status of a download task.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusRunning    Status = "running"
	StatusFinalizing Status = "finalizing" // bytes complete; flushing/closing file
	StatusPausing    Status = "pausing"    // pause requested; waiting for workers (non-blocking UI)
	StatusPaused     Status = "paused"
	StatusDone       Status = "done"
	StatusError      Status = "error"
	StatusCancel     Status = "cancelled"
)

// Task is a tracked download job.
type Task struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Dest      string    `json:"dest"`
	FID       string    `json:"fid,omitempty"`
	URL       string    `json:"-"`
	Total     int64     `json:"total"`
	Done      int64     `json:"done"`
	Speed     float64   `json:"speed"`
	Status    Status    `json:"status"`
	Error     string    `json:"error,omitempty"`
	PartSize  int       `json:"part_size,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	// ActiveElapsed is wall time spent actually downloading (excludes paused).
	ActiveElapsed time.Duration `json:"active_elapsed,omitempty"`
	// AutoPausedByQuit: set when pause is caused by app quit (not manual pause).
	// Only these tasks prompt "恢复下载" on next startup.
	AutoPausedByQuit bool `json:"auto_paused_by_quit,omitempty"`

	doneAt  atomic.Int64
	cancel  context.CancelFunc
	wg      sync.WaitGroup // set while run() is active
	running atomic.Bool
}

// Snapshot is a copy safe for UI/CLI.
type Snapshot struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Dest      string        `json:"dest"`
	FID       string        `json:"fid,omitempty"`
	Total     int64         `json:"total"`
	Done      int64         `json:"done"`
	Speed     float64       `json:"speed"`
	Status    Status        `json:"status"`
	Error     string        `json:"error,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	StartedAt time.Time     `json:"started_at,omitempty"`
	EndedAt   time.Time     `json:"ended_at,omitempty"`
	Percent   float64       `json:"percent"`
	Elapsed   time.Duration `json:"elapsed"`             // time spent downloading so far
	ETA       time.Duration `json:"eta,omitempty"`       // estimated remaining (0 if unknown)
	HasETA    bool          `json:"has_eta"`             // true when ETA is meaningful
}

// PersistTask is disk form for download history (active + completed).
type PersistTask struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Dest             string    `json:"dest"`
	FID              string    `json:"fid,omitempty"`
	Total            int64     `json:"total"`
	Done             int64     `json:"done"`
	PartSize         int       `json:"part_size,omitempty"`
	Status           Status    `json:"status"`
	Error            string    `json:"error,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	EndedAt          time.Time `json:"ended_at,omitempty"`
	ActiveElapsed    int64     `json:"active_elapsed_ns,omitempty"` // nanoseconds
	AutoPausedByQuit bool      `json:"auto_paused_by_quit,omitempty"`
}

// maxFinishedKeep caps completed/cancelled rows kept on disk (active always kept).
const maxFinishedKeep = 200

// Manager tracks concurrent downloads for TUI + CLI.
type Manager struct {
	mu        sync.RWMutex
	tasks     []*Task
	seq       int64
	client    *client.Client
	destDir   string
	statePath string
	loaded    bool
	// optional: re-resolve URL from fid on resume
	ResolveURL func(fid string) (url string, total int64, err error)
}

var (
	globalMu sync.Mutex
	global   *Manager
)

// Global returns the process-wide download manager (lazy).
func Global() *Manager {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global == nil {
		global = NewManager(nil, "downloads")
	}
	return global
}

func NewManager(c *client.Client, destDir string) *Manager {
	if destDir == "" {
		destDir = "downloads"
	}
	m := &Manager{client: c, destDir: destDir}
	m.statePath = defaultStatePath()
	return m
}

func defaultStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".goquark-downloads.json")
	}
	return filepath.Join(home, ".config", "goquark", "downloads.json")
}

// ensureLoaded reads history from disk once per process.
func (m *Manager) ensureLoaded() {
	m.mu.Lock()
	if m.loaded {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	_, _ = m.LoadPersisted()
	m.mu.Lock()
	m.loaded = true
	m.mu.Unlock()
}

func (m *Manager) BindClient(c *client.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.client = c
}

func (m *Manager) SetDestDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dir != "" {
		m.destDir = dir
	}
}

func (m *Manager) DestDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.destDir
}

func (m *Manager) SetStatePath(p string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p != "" {
		m.statePath = p
	}
}

// EnqueueOpts for new tasks.
type EnqueueOpts struct {
	Name string
	URL  string
	Dest string
	FID  string
}

// Enqueue starts a background download.
func (m *Manager) Enqueue(name, urlStr, dest string) (*Task, error) {
	return m.EnqueueEx(EnqueueOpts{Name: name, URL: urlStr, Dest: dest})
}

// EnqueueEx starts a background download with optional FID (for resume).
func (m *Manager) EnqueueEx(opt EnqueueOpts) (*Task, error) {
	m.ensureLoaded()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil {
		return nil, fmt.Errorf("download manager: no client bound")
	}
	if opt.URL == "" && opt.FID == "" {
		return nil, fmt.Errorf("download manager: need url or fid")
	}
	dest := opt.Dest
	if dest == "" {
		_ = os.MkdirAll(m.destDir, 0o755)
		dest = filepath.Join(m.destDir, sanitize(opt.Name))
	} else {
		_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	}
	m.seq++
	id := fmt.Sprintf("dl-%d-%d", time.Now().Unix()%100000, m.seq)
	t := &Task{
		ID:        id,
		Name:      opt.Name,
		Dest:      dest,
		FID:       opt.FID,
		URL:       opt.URL,
		Status:    StatusQueued,
		CreatedAt: time.Now(),
	}
	m.tasks = append(m.tasks, t)
	cl := m.client
	go m.run(cl, t, false)
	_ = m.saveLocked()
	return t, nil
}

func (m *Manager) run(cl *client.Client, t *Task, resume bool) {
	t.wg.Add(1)
	t.running.Store(true)
	defer func() {
		t.running.Store(false)
		t.wg.Done()
	}()

	ctx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	// Only abort if cancelled while waiting to start.
	// StatusPaused / StatusPausing here means a pause won the race after we were queued.
	if t.Status == StatusCancel {
		t.cancel = cancel
		cancel()
		m.mu.Unlock()
		return
	}
	if t.Status == StatusPaused || t.Status == StatusPausing {
		t.cancel = cancel
		cancel()
		m.mu.Unlock()
		return
	}
	t.cancel = cancel
	t.Status = StatusRunning
	// first start or resume: mark segment start for elapsed accumulation
	t.StartedAt = time.Now()
	t.Error = ""
	urlStr := t.URL
	fid := t.FID
	dest := t.Dest
	resolve := m.ResolveURL
	m.mu.Unlock()

	// refresh URL if we have fid resolver (CDN links expire)
	if (urlStr == "" || resume) && fid != "" && resolve != nil {
		u, _, err := resolve(fid)
		if err == nil && u != "" {
			urlStr = u
			m.mu.Lock()
			t.URL = u
			m.mu.Unlock()
		}
	}
	if urlStr == "" {
		m.finish(t, StatusError, fmt.Errorf("empty download url"))
		return
	}

	// try download; on failure refresh CDN URL once and retry (common for small files)
	err := File(ctx, cl, urlStr, dest, Options{
		Resume: resume,
		OnProgress: func(done, total int64, speed float64) {
			t.doneAt.Store(done)
			m.mu.Lock()
			t.Done = done
			t.Total = total
			t.Speed = speed
			// When bytes hit 100% but File() has not returned, we are flushing/closing.
			// Surface that so TUI can show spinner instead of stuck "下载中 100%".
			if t.Status == StatusRunning && total > 0 && done >= total {
				t.Status = StatusFinalizing
				t.Speed = 0
			}
			m.mu.Unlock()
		},
	})
	if err != nil && ctx.Err() == nil && fid != "" && resolve != nil {
		if u, _, rerr := resolve(fid); rerr == nil && u != "" && u != urlStr {
			urlStr = u
			m.mu.Lock()
			t.URL = u
			m.mu.Unlock()
			err = File(ctx, cl, urlStr, dest, Options{
				Resume: true,
				OnProgress: func(done, total int64, speed float64) {
					t.doneAt.Store(done)
					m.mu.Lock()
					t.Done = done
					t.Total = total
					t.Speed = speed
					if t.Status == StatusRunning && total > 0 && done >= total {
						t.Status = StatusFinalizing
						t.Speed = 0
					}
					m.mu.Unlock()
				},
			})
		}
	}

	// accumulate this run's active time before status finalization
	m.mu.Lock()
	if !t.StartedAt.IsZero() {
		t.ActiveElapsed += time.Since(t.StartedAt)
		t.StartedAt = time.Time{} // close segment so snap() won't double-count
	}
	m.mu.Unlock()

	if err != nil {
		if ctx.Err() != nil {
			// paused/cancelled — keep partial progress; NEVER disk-I/O under lock
			// (save would block List() and freeze the TUI).
			m.mu.Lock()
			t.EndedAt = time.Now()
			t.Done = t.doneAt.Load()
			t.Speed = 0
			// pauseMany sets Pausing first; settle to Paused here
			if t.Status == StatusPausing || t.Status == StatusPaused {
				t.Status = StatusPaused
			} else if t.Status != StatusPaused {
				t.Status = StatusCancel
				t.Error = "cancelled"
			}
			m.mu.Unlock()
			// async persist so UI keeps spinning
			go m.saveAsync()
			return
		}
		// shorten huge URL noise in UI: keep first line / host error only
		m.finish(t, StatusError, shortErr(err))
		return
	}
	m.finish(t, StatusDone, nil)
}

func shortErr(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	// strip long query strings from URLs in errors
	if i := strings.Index(s, "https://"); i >= 0 {
		rest := s[i:]
		// cut at ? or space
		end := len(rest)
		if j := strings.IndexAny(rest, "? 	\n"); j >= 0 {
			end = j
		}
		// keep host path only
		host := rest[:end]
		if len(host) > 80 {
			host = host[:80] + "…"
		}
		prefix := strings.TrimSpace(s[:i])
		if prefix == "" || prefix == "Get" || prefix == "download:" {
			return fmt.Errorf("下载失败: %s", host)
		}
		return fmt.Errorf("%s %s", prefix, host)
	}
	if len(s) > 160 {
		return fmt.Errorf("%s…", s[:160])
	}
	return err
}

func (m *Manager) finish(t *Task, st Status, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.EndedAt = time.Now()
	t.Done = t.doneAt.Load()
	t.Speed = 0
	t.Status = st
	if err != nil {
		t.Error = err.Error()
	}
	if st == StatusDone {
		if t.Total == 0 {
			if fi, e := os.Stat(t.Dest); e == nil {
				t.Total = fi.Size()
				t.Done = fi.Size()
			}
		} else if t.Done < t.Total {
			t.Done = t.Total
		}
		// part map removal is best-effort and non-critical for UI
		go func(path string) { _ = os.Remove(partMapPath(path)) }(t.Dest)
	}
	_ = m.saveLocked()
}

// PauseAll signals pause on all running/queued/finalizing tasks and returns immediately.
// Worker teardown + state save happen in a background goroutine so the TUI
// never blocks. Prefer this for interactive pause.
func (m *Manager) PauseAll() int {
	return m.pauseMany(nil, false)
}

// PauseAllWait is like PauseAll but waits until in-flight parts finish.
// Call only from a tea.Cmd / background worker — never from the UI event loop.
func (m *Manager) PauseAllWait() int {
	return m.pauseMany(nil, true)
}

// PauseID signals pause on one task and returns immediately (non-blocking).
func (m *Manager) PauseID(id string) bool {
	if id == "" {
		return false
	}
	return m.pauseMany(map[string]bool{id: true}, false) > 0
}

// pauseMany marks matching tasks as Pausing and cancels them.
// UI shows spinner on StatusPausing; background settles to StatusPaused.
// wait=true blocks until workers finish (quit path only) and marks AutoPausedByQuit.
func (m *Manager) pauseMany(ids map[string]bool, wait bool) int {
	m.mu.Lock()
	var active []*Task
	for _, t := range m.tasks {
		switch t.Status {
		case StatusRunning, StatusQueued, StatusFinalizing:
			// ok to pause
		default:
			continue
		}
		if ids != nil && !ids[t.ID] {
			continue
		}
		// Intermediate state: UI shows "暂停中" + spinner; TUI never waits.
		t.Status = StatusPausing
		t.Speed = 0
		// wait=true is only used by quit → these should auto-resume next launch
		if wait {
			t.AutoPausedByQuit = true
		} else {
			// manual pause: never auto-prompt on next start
			t.AutoPausedByQuit = false
		}
		if t.cancel != nil {
			t.cancel()
		}
		active = append(active, t)
	}
	m.mu.Unlock()
	if len(active) == 0 {
		return 0
	}
	finish := func() {
		for _, t := range active {
			t.wg.Wait()
		}
		m.mu.Lock()
		for _, t := range active {
			// run() may already have set Paused; ensure settled
			if t.Status == StatusPausing || t.Status == StatusPaused {
				t.Status = StatusPaused
				t.Done = t.doneAt.Load()
				t.EndedAt = time.Now()
				t.Speed = 0
			}
		}
		m.mu.Unlock()
		// disk I/O outside lock — critical for non-blocking TUI
		m.saveAsync()
	}
	if wait {
		finish()
	} else {
		go finish()
	}
	return len(active)
}

// CancelID cancels one task without blocking the caller.
func (m *Manager) CancelID(id string) bool {
	if id == "" {
		return false
	}
	return m.cancelMany(map[string]bool{id: true}, false) > 0
}

// CancelAll cancels all incomplete tasks without blocking the caller.
func (m *Manager) CancelAll() int {
	return m.cancelMany(nil, false)
}

// cancelMany marks tasks cancelled. ids nil = all incomplete.
func (m *Manager) cancelMany(ids map[string]bool, wait bool) int {
	m.mu.Lock()
	var active []*Task
	n := 0
	for _, t := range m.tasks {
		switch t.Status {
		case StatusRunning, StatusQueued, StatusFinalizing, StatusPaused, StatusError:
			if ids != nil && !ids[t.ID] {
				continue
			}
			if t.Status == StatusRunning || t.Status == StatusQueued || t.Status == StatusFinalizing || t.Status == StatusPausing {
				if t.cancel != nil {
					t.cancel()
				}
				active = append(active, t)
			}
			t.Status = StatusCancel
			t.Error = "cancelled by user"
			t.Speed = 0
			t.EndedAt = time.Now()
			n++
		}
	}
	m.mu.Unlock()
	finish := func() {
		for _, t := range active {
			t.wg.Wait()
		}
		m.mu.Lock()
		_ = m.saveLocked()
		m.mu.Unlock()
	}
	if len(active) > 0 {
		if wait {
			finish()
		} else {
			go finish()
		}
	} else {
		m.mu.Lock()
		_ = m.saveLocked()
		m.mu.Unlock()
	}
	return n
}

// ResumeID resumes one paused/error task.
func (m *Manager) ResumeID(id string) bool {
	m.mu.Lock()
	var t *Task
	cl := m.client
	for _, x := range m.tasks {
		// allow retry on any error, and resume paused/incomplete
		if x.ID != id {
			continue
		}
		switch x.Status {
		case StatusPaused, StatusError, StatusCancel:
			// ok
		default:
			continue
		}
		if x.running.Load() {
			m.mu.Unlock()
			return false
		}
		// CRITICAL: must leave StatusPaused before go run().
		// run() aborts immediately if it still sees StatusPaused (pause-race guard).
		x.Status = StatusQueued
		x.Error = ""
		x.Speed = 0
		x.AutoPausedByQuit = false
		t = x
		break
	}
	m.mu.Unlock()
	if t == nil || cl == nil {
		return false
	}
	go m.run(cl, t, true)
	return true
}


// AtDisplay returns snapshot for display index (active first, then finished), or nil.
func (m *Manager) AtDisplay(i int) *Snapshot {
	list := m.List()
	if i < 0 || i >= len(list) {
		return nil
	}
	s := list[i]
	return &s
}

// ActiveCount returns running+queued+finalizing.
func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, t := range m.tasks {
		if t.Status == StatusRunning || t.Status == StatusQueued || t.Status == StatusFinalizing || t.Status == StatusPausing {
			n++
		}
	}
	return n
}

// healCompletePaused: if a task is paused/error but already has all bytes, mark done.
// Happens when user paused during finalizing or quit at 100%.
func (m *Manager) healCompletePaused() {
	m.mu.Lock()
	changed := false
	for _, t := range m.tasks {
		if t.Status != StatusPaused && t.Status != StatusPausing && t.Status != StatusError {
			continue
		}
		done := t.doneAt.Load()
		if done == 0 {
			done = t.Done
		}
		if t.Total > 0 && done >= t.Total {
			t.Status = StatusDone
			t.Done = t.Total
			t.doneAt.Store(t.Total)
			t.Speed = 0
			t.Error = ""
			t.AutoPausedByQuit = false
			if t.EndedAt.IsZero() {
				t.EndedAt = time.Now()
			}
			changed = true
		}
	}
	m.mu.Unlock()
	if changed {
		m.saveAsync()
	}
}

// IncompleteCount returns incomplete tasks that should prompt resume-on-start.
// Manual pause / user cancel do NOT count — only quit-induced pause (or legacy
// crash-recovered running tasks without the flag yet treated carefully).
func (m *Manager) IncompleteCount() int {
	return m.QuitPausedCount()
}

// QuitPausedCount is tasks paused because the app exited while they were downloading.
func (m *Manager) QuitPausedCount() int {
	m.ensureLoaded()
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, t := range m.tasks {
		if t.Status == StatusPaused && t.AutoPausedByQuit {
			n++
		}
	}
	return n
}

// ClearQuitResumeFlags drops auto-resume markers after user declines startup prompt.
// Tasks stay paused; they won't re-prompt until next quit-pause.
func (m *Manager) ClearQuitResumeFlags() {
	m.mu.Lock()
	changed := false
	for _, t := range m.tasks {
		if t.AutoPausedByQuit {
			t.AutoPausedByQuit = false
			changed = true
		}
	}
	m.mu.Unlock()
	if changed {
		m.saveAsync()
	}
}

// isActiveGroup: still "in progress" section (top of download center).
func isActiveGroup(s Status) bool {
	switch s {
	case StatusRunning, StatusQueued, StatusFinalizing, StatusPausing, StatusPaused, StatusError:
		return true
	default:
		return false
	}
}

// List returns snapshots in display order:
//  1. active group (running/queued/paused/error) — newest first
//  2. finished group (done/cancelled) — newest first
// Completed tasks are kept (not auto-removed).
// Within each group: later downloads on top, earlier ones below.
func (m *Manager) List() []Snapshot {
	m.ensureLoaded()
	m.healCompletePaused()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var active, finished []Snapshot
	for _, t := range m.tasks {
		s := snap(t)
		// live progress for running tasks
		if t.Status == StatusRunning || t.Status == StatusQueued || t.Status == StatusFinalizing || t.Status == StatusPausing {
			s.Done = t.doneAt.Load()
			if s.Total > 0 {
				s.Percent = float64(s.Done) * 100 / float64(s.Total)
			}
		}
		if isActiveGroup(t.Status) {
			active = append(active, s)
		} else {
			finished = append(finished, s)
		}
	}
	// 组内：后下载的在上面（新 → 旧）
	sort.SliceStable(active, func(i, j int) bool {
		return active[i].CreatedAt.After(active[j].CreatedAt)
	})
	sort.SliceStable(finished, func(i, j int) bool {
		// prefer end time when available so completion order is stable
		ai, aj := finished[i].EndedAt, finished[j].EndedAt
		if !ai.IsZero() && !aj.IsZero() && !ai.Equal(aj) {
			return ai.After(aj)
		}
		return finished[i].CreatedAt.After(finished[j].CreatedAt)
	})
	out := make([]Snapshot, 0, len(active)+len(finished))
	out = append(out, active...)
	out = append(out, finished...)
	return out
}

// ClearFinished removes done/error/cancelled (not paused/running).
// Explicit user action only — completed items stay until cleared.
func (m *Manager) ClearFinished() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.tasks[:0]
	removed := 0
	for _, t := range m.tasks {
		if t.Status == StatusDone || t.Status == StatusError || t.Status == StatusCancel {
			removed++
			continue
		}
		kept = append(kept, t)
	}
	m.tasks = kept
	_ = m.saveLocked()
	return removed
}

// LoadPersisted loads tasks from disk (does not auto-start).
// Completed tasks are restored so the download center keeps history.
func (m *Manager) LoadPersisted() ([]PersistTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := m.statePath
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []PersistTask
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, err
	}
	for _, it := range items {
		// skip if already present
		exists := false
		for _, t := range m.tasks {
			if t.ID == it.ID || (t.Dest == it.Dest && t.Name == it.Name && t.FID == it.FID) {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		st := it.Status
		if st == "" {
			st = StatusPaused
		}
		// incomplete that was running at crash → treat as quit-pause (offer resume)
		autoQuit := it.AutoPausedByQuit
		switch st {
		case StatusRunning, StatusQueued, StatusFinalizing, StatusPausing:
			st = StatusPaused
			autoQuit = true // abrupt exit / old build without flag
		case StatusDone, StatusCancel, StatusPaused, StatusError:
			// keep as-is; AutoPausedByQuit from disk decides resume prompt
		default:
			st = StatusPaused
		}
		t := &Task{
			ID:               it.ID,
			Name:             it.Name,
			Dest:             it.Dest,
			FID:              it.FID,
			Total:            it.Total,
			Done:             it.Done,
			PartSize:         it.PartSize,
			Status:           st,
			Error:            it.Error,
			CreatedAt:        it.CreatedAt,
			EndedAt:          it.EndedAt,
			ActiveElapsed:    time.Duration(it.ActiveElapsed),
			AutoPausedByQuit: autoQuit,
		}
		if t.ID == "" {
			m.seq++
			t.ID = fmt.Sprintf("dl-resume-%d", m.seq)
		}
		if t.Status == StatusDone && t.Done == 0 && t.Total > 0 {
			t.Done = t.Total
		}
		t.doneAt.Store(t.Done)
		m.tasks = append(m.tasks, t)
	}
	return items, nil
}

// FindByFIDInDir returns the newest task with same FID whose dest is under destDir.
// destDir compared via filepath.Clean. Empty fid returns nil.
func (m *Manager) FindByFIDInDir(fid, destDir string) *Snapshot {
	if fid == "" {
		return nil
	}
	destDir = filepath.Clean(destDir)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best *Task
	for _, t := range m.tasks {
		if t.FID != fid {
			continue
		}
		if filepath.Clean(filepath.Dir(t.Dest)) != destDir {
			continue
		}
		// prefer active/paused over done; among same class take last
		if best == nil {
			best = t
			continue
		}
		if rankStatus(t.Status) >= rankStatus(best.Status) {
			best = t
		}
	}
	if best == nil {
		return nil
	}
	s := snap(best)
	return &s
}

func rankStatus(s Status) int {
	switch s {
	case StatusRunning:
		return 5
	case StatusFinalizing:
		return 5
	case StatusPausing:
		return 4
	case StatusQueued:
		return 4
	case StatusPaused:
		return 3
	case StatusError:
		return 2
	case StatusDone:
		return 1
	default:
		return 0
	}
}

// DisplayIndex returns display-order index of task id, or -1.
func (m *Manager) DisplayIndex(id string) int {
	list := m.List()
	for i := range list {
		if list[i].ID == id {
			return i
		}
	}
	return -1
}


// ResumePaused starts all paused tasks that were auto-paused by quit.
// Manual pauses stay paused unless the user resumes them individually / with r after selecting.
// For the bulk "r" key and startup confirm, we only resume AutoPausedByQuit.
// Pass all=true to resume every paused/error task (download-center "r").
func (m *Manager) ResumePaused() int {
	return m.resumePaused(true)
}

// ResumeQuitPaused resumes only quit-induced pauses (startup prompt).
func (m *Manager) ResumeQuitPaused() int {
	return m.resumePaused(false)
}

func (m *Manager) resumePaused(all bool) int {
	m.mu.Lock()
	var toStart []*Task
	cl := m.client
	for _, t := range m.tasks {
		switch t.Status {
		case StatusPaused:
			if !all && !t.AutoPausedByQuit {
				continue
			}
		case StatusError, StatusCancel:
			if !all {
				continue // startup only cares about quit-paused
			}
		default:
			continue
		}
		if t.running.Load() {
			continue
		}
		// leave paused before run() — see ResumeID comment
		t.Status = StatusQueued
		t.Error = ""
		t.Speed = 0
		t.AutoPausedByQuit = false // clear once we resume
		toStart = append(toStart, t)
	}
	m.mu.Unlock()
	if cl == nil {
		return 0
	}
	n := 0
	for _, t := range toStart {
		go m.run(cl, t, true)
		n++
	}
	return n
}

// RemoveID drops a task from the list (and from disk state). Returns true if found.
// Does not delete the local file.
func (m *Manager) RemoveID(id string) bool {
	if id == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.tasks[:0]
	found := false
	for _, t := range m.tasks {
		if t.ID == id {
			found = true
			// stop if still running
			if t.cancel != nil && (t.Status == StatusRunning || t.Status == StatusQueued) {
				t.cancel()
			}
			continue
		}
		kept = append(kept, t)
	}
	if found {
		m.tasks = kept
		_ = m.saveLocked()
	}
	return found
}

// ClearStaleDone removes done tasks whose Dest file no longer exists.
// Returns number of records removed.
func (m *Manager) ClearStaleDone() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.tasks[:0]
	n := 0
	for _, t := range m.tasks {
		if t.Status == StatusDone {
			if _, err := os.Stat(t.Dest); err != nil {
				n++
				continue
			}
		}
		kept = append(kept, t)
	}
	if n > 0 {
		m.tasks = kept
		_ = m.saveLocked()
	}
	return n
}

// UniqueDest returns path with " (2)", " (3)" … before extension if path exists.
func UniqueDest(path string) string {
	if _, err := os.Stat(path); err != nil {
		return path
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	for i := 2; i < 1000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", name, i, ext))
		if _, err := os.Stat(cand); err != nil {
			return cand
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s (%d)%s", name, time.Now().Unix()%1000, ext))
}

// HasPersistedIncomplete reports whether state file has incomplete jobs.
func HasPersistedIncomplete() (int, error) {
	path := defaultStatePath()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var items []PersistTask
	if err := json.Unmarshal(b, &items); err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		if it.Status == StatusDone || it.Status == StatusCancel {
			continue
		}
		if it.Total > 0 && it.Done >= it.Total && it.Status != StatusError {
			continue
		}
		n++
	}
	return n, nil
}

func (m *Manager) saveLocked() error {
	path := m.statePath
	if path == "" {
		return nil
	}
	b, err := m.marshalStateLocked()
	if err != nil {
		return err
	}
	// CRITICAL: never block UI/list under this lock with disk I/O.
	// Callers hold m.mu; write async.
	go func(p string, data []byte) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, data, 0o600)
	}(path, b)
	return nil
}

// saveAsync snapshots under RLock and writes off the hot path.
func (m *Manager) saveAsync() {
	m.mu.RLock()
	path := m.statePath
	if path == "" {
		m.mu.RUnlock()
		return
	}
	b, err := m.marshalStateLocked()
	m.mu.RUnlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, b, 0o600)
}

func (m *Manager) marshalStateLocked() ([]byte, error) {
	var active, finished []PersistTask
	for _, t := range m.tasks {
		// skip pure empty errors that never started
		if t.Status == StatusError && t.Done == 0 && t.Total == 0 {
			continue
		}
		item := PersistTask{
			ID:            t.ID,
			Name:          t.Name,
			Dest:          t.Dest,
			FID:           t.FID,
			Total:         t.Total,
			Done:          t.doneAt.Load(),
			PartSize:      t.PartSize,
			Status:        t.Status,
			Error:         t.Error,
			CreatedAt:     t.CreatedAt,
			EndedAt:       t.EndedAt,
			ActiveElapsed:    t.ActiveElapsed.Nanoseconds(),
			AutoPausedByQuit: t.AutoPausedByQuit,
		}
		if item.Done == 0 && t.Done > 0 {
			item.Done = t.Done
		}
		if isActiveGroup(t.Status) {
			// running/queued/pausing/finalizing persist as paused so restart is safe
			switch item.Status {
			case StatusRunning, StatusQueued, StatusFinalizing, StatusPausing:
				item.Status = StatusPaused
			}
			active = append(active, item)
		} else {
			finished = append(finished, item)
		}
	}
	// keep finished history (cap oldest)
	if len(finished) > maxFinishedKeep {
		sort.SliceStable(finished, func(i, j int) bool {
			return finished[i].CreatedAt.Before(finished[j].CreatedAt)
		})
		finished = finished[len(finished)-maxFinishedKeep:]
	}
	items := make([]PersistTask, 0, len(active)+len(finished))
	items = append(items, active...)
	items = append(items, finished...)
	return json.MarshalIndent(items, "", "  ")
}

func snap(t *Task) Snapshot {
	s := Snapshot{
		ID:        t.ID,
		Name:      t.Name,
		Dest:      t.Dest,
		FID:       t.FID,
		Total:     t.Total,
		Done:      t.Done,
		Speed:     t.Speed,
		Status:    t.Status,
		Error:     t.Error,
		CreatedAt: t.CreatedAt,
		StartedAt: t.StartedAt,
		EndedAt:   t.EndedAt,
	}
	if t.Total > 0 {
		s.Percent = float64(t.Done) * 100 / float64(t.Total)
	}
	// elapsed: accumulated active time + current running segment
	elapsed := t.ActiveElapsed
	if t.Status == StatusRunning && !t.StartedAt.IsZero() {
		elapsed += time.Since(t.StartedAt)
	}
	// fallbacks when ActiveElapsed was never recorded (older builds / crash)
	if elapsed <= 0 {
		switch {
		case !t.StartedAt.IsZero() && !t.EndedAt.IsZero():
			elapsed = t.EndedAt.Sub(t.StartedAt)
		case !t.CreatedAt.IsZero() && !t.EndedAt.IsZero() &&
			(t.Status == StatusDone || t.Status == StatusPaused || t.Status == StatusError || t.Status == StatusCancel):
			// rough: created→ended (includes idle, but better than blank for history)
			elapsed = t.EndedAt.Sub(t.CreatedAt)
		}
		if elapsed < 0 {
			elapsed = 0
		}
	}
	s.Elapsed = elapsed

	// ETA from current speed (preferred) or average speed from elapsed
	// Finalizing = bytes done; no remaining ETA.
	if t.Status == StatusRunning || t.Status == StatusQueued {
		remain := t.Total - t.Done
		if remain < 0 {
			remain = 0
		}
		spd := t.Speed
		if spd <= 0 && elapsed > time.Second && t.Done > 0 {
			spd = float64(t.Done) / elapsed.Seconds()
		}
		if spd > 1 && t.Total > 0 && remain > 0 {
			s.ETA = time.Duration(float64(remain)/spd*float64(time.Second)) + time.Second/2
			// clamp insane ETAs
			if s.ETA > 99*time.Hour {
				s.ETA = 99 * time.Hour
			}
			s.HasETA = true
		}
	}
	return s
}

// FormatDuration formats d as human short string: 3s / 1m20s / 1h05m / 2d03h
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	return fmt.Sprintf("%dd%02dh", days, h)
}

func sanitize(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return "download.bin"
	}
	return string(out)
}
