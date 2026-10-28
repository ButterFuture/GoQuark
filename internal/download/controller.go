package download

// Controller is what the TUI, MCP and CLI drive downloads through: a Manager
// in this process, or a client of the download service (internal/dlsvc) that
// runs one Manager for every GoQuark process of the same config.
type Controller interface {
	List() []Snapshot
	LiveCount() (live, queued, limit int)
	ActiveCount() int
	IncompleteCount() int
	DestDir() string
	SetDestDir(dir string)
	SetMaxLive(n int) int
	// Add queues a download and returns its task id.
	Add(opt EnqueueOpts) (string, error)
	PauseAll() int
	// PauseAllWait pauses everything and returns once in-flight parts are
	// written; call it off the UI goroutine.
	PauseAllWait() int
	PauseID(id string) bool
	PauseIDs(ids []string) int
	CancelID(id string) bool
	CancelIDs(ids []string) int
	CancelAll() int
	ResumeID(id string) bool
	ResumeIDs(ids []string) int
	ResumePaused() int
	ResumeQuitPaused() int
	RemoveID(id string) bool
	ClearFinished() int
	ClearStaleDone() int
	ClearQuitResumeFlags()
	FindAtDest(fid, dest string) *Snapshot
	BusyAtDest(dest string) *Snapshot
	// ReloadSession re-reads the login from the config, after a login or
	// logout, so downloads use the current account.
	ReloadSession()
}

var _ Controller = (*Manager)(nil)

// Add is EnqueueEx returning only the task id.
func (m *Manager) Add(opt EnqueueOpts) (string, error) {
	t, err := m.EnqueueEx(opt)
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// ReloadSession runs the OnReloadSession hook, if one is set.
func (m *Manager) ReloadSession() {
	m.mu.RLock()
	f := m.OnReloadSession
	m.mu.RUnlock()
	if f != nil {
		f()
	}
}

// WaitSaves blocks until every pending state write is on disk.
func (m *Manager) WaitSaves() { m.saves.Wait() }
