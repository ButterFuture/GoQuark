// Package tui is a mouse-friendly cloud-drive browser for GoQuark.
//
// File-manager style (not chat):
//   - single-click selects a row
//   - space / ctrl+click multi-selects
//   - double-click / enter opens folder or downloads file(s)
//   - right-click opens a floating menu at the cursor
//   - errors show as a centered modal
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ButterFuture/GoQuark/internal/api"
	"github.com/ButterFuture/GoQuark/internal/auth"
	"github.com/ButterFuture/GoQuark/internal/client"
	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/device"
	"github.com/ButterFuture/GoQuark/internal/dlsvc"
	"github.com/ButterFuture/GoQuark/internal/download"
	"github.com/ButterFuture/GoQuark/internal/update"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

const (
	headerLines = 1 // title only
	footerLines = 2 // status + help
	rowHeight   = 2 // title + description
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	pathStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	statusStyle = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252")).Padding(0, 1)

	// bubbles-list-like: soft left accent + filled background.
	// Apply ONE style to the whole row block (including emoji). Nesting
	// Row chrome: ONLY background + left accent. Do NOT set Foreground —
	// nameDir/nameFile/descDim keep their own colors under highlight.
	rowNormal = lipgloss.NewStyle().PaddingLeft(3)
	rowCursor = lipgloss.NewStyle().
			Border(lipgloss.ThickBorder(), false, false, false, true).
			BorderForeground(lipgloss.Color("135")).
			Background(lipgloss.Color("237")).
			PaddingLeft(2)
	rowChecked = lipgloss.NewStyle().
			Border(lipgloss.ThickBorder(), false, false, false, true).
			BorderForeground(lipgloss.Color("99")).
			Background(lipgloss.Color("236")).
			PaddingLeft(2)
	rowBoth = lipgloss.NewStyle().
		Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderForeground(lipgloss.Color("141")).
		Background(lipgloss.Color("60")).
		PaddingLeft(2)

	// unfocused rows: folder blue, file light green.
	// desc dim gray normally; under selection highlight becomes white.
	nameDir   = lipgloss.NewStyle().Foreground(lipgloss.Color("117")) // blue folders
	nameFile  = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // light green files
	descDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("243")) // attributes (normal)
	descOnSel = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))  // attributes on highlight

	menuBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39")).Background(lipgloss.Color("235")).Padding(0, 1)
	menuItemSt = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	menuSelSt  = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("39"))
	modalBox   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("196")).Background(lipgloss.Color("235")).Padding(1, 2).Width(48)
	modalTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	okBox      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("42")).Background(lipgloss.Color("235")).Padding(1, 2).Width(48)
	confirmBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("214")).Background(lipgloss.Color("235")).Padding(1, 2).Width(48)
	// confirm buttons (default)
	btnOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("42")).Padding(0, 2).Bold(true)
	btnCancel = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("244")).Padding(0, 2)
	// hover: soft purple (same family as selected row)
	btnOKHover     = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("97")).Padding(0, 2).Bold(true)
	btnCancelHover = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("61")).Padding(0, 2).Bold(true)
	// gap between buttons must match modal bg (235), not terminal default
	btnGapSt   = lipgloss.NewStyle().Background(lipgloss.Color("235")).Render("  ")
	panelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 2)
)

type mode int

const (
	modeBrowse mode = iota
	modeLogin
	modeProfile
	modeMenu
	modeModal
	modeDownloads
)

type row struct {
	entry api.FileEntry
	path  string
}

type navFrame struct {
	fid  string
	path string
	name string
}

type menuEntry struct {
	id    string
	label string
}

type model struct {
	cfg    *config.Config
	client *client.Client
	mode   mode

	spinner spinner.Model
	loading bool
	status  string
	width   int
	height  int
	ready   bool

	stack []navFrame
	rows  []row

	// selection
	cursor   int          // keyboard / focus index into rows
	selected map[int]bool // multi-select set
	offset   int          // scroll offset
	listTop  int          // y of first list row on screen
	listH    int          // visible rows

	// mouse double-click
	lastClickIdx  int
	lastClickTime time.Time

	// floating menu at mouse position
	menuItems []menuEntry
	menuIdx   int
	menuX     int
	menuY     int
	// where menu was opened from — close/esc returns here
	menuFrom mode
	// Windows-like: menu opens on right-press and stays until left-click
	// item / outside / Esc. Ignore the matching right-button release.
	menuIgnoreRightRelease bool

	// centered modal
	modalTitle string
	modalBody  string
	modalKind  string // "error" | "info" | "confirm" | "progress" | "help"
	modalBack  mode   // mode to restore on close
	// confirm action id when modalKind=="confirm"
	confirmAction string
	// keyboard focus: 1=primary(OK) 2=secondary(Cancel). Default = primary.
	btnFocus int
	// mouse hover: 0=none 1=primary 2=secondary. When non-zero, overrides
	// keyboard focus for highlight; left/right keys are disabled.
	btnHover int

	// notesExpanded toggles the release notes in the update dialog between a
	// few lines and the full text.
	notesExpanded bool

	// swallowRelease drops the mouse release that follows a press we already
	// acted on. A click is press+release; without this the release is handed
	// to whatever the press turned the screen into.
	swallowRelease bool

	// updateCancel aborts the download behind the progress dialog.
	updateCancel context.CancelFunc
	// last rendered overlay geometry (for mouse hit testing)
	ovX, ovY, ovW, ovH int
	// confirm button rows/cols relative to overlay top-left (screen coords filled after render)
	btnOKX, btnOKY, btnOKW int
	btnNoX, btnNoY, btnNoW int
	btnRow                 int // y of button row on screen

	// profile
	profileText string

	// download
	dlBusy bool
	dlMsg  string
	dlMgr  download.Controller
	// scan is the folder expansion running behind the browse view. gen rises
	// with every new scan, so a result that arrives from one already cancelled
	// can be told apart and dropped.
	scan struct {
		busy   bool
		gen    uint64
		cancel context.CancelFunc
	}
	// downloads center: cursor and first visible row in the open level, the
	// folders opened to get there, and the row a menu was opened on
	dlCursor int
	dlOffset int
	dlPath   []dlCrumb
	dlTarget *dlRow
	// the row under the cursor at the last sync, and where it was: rows move
	// as their status changes, and a cursor nobody moved stays on its row
	dlCursorKey    string
	dlSyncedCursor int
	// where each row sits; a row that changes status moves only after a while
	dlPlaces dlPlaces
	// double-click on a folder row opens it
	dlLastClickIdx  int
	dlLastClickTime time.Time
	// startup resume prompt
	pendingResumeAsk int
	// version / update
	appVersion         string
	pendingUpdateCheck bool // auto-check after first paint
	updateManual       bool // true if user pressed U
	updateRes          *update.Result
	// quitting with active downloads
	quitting bool
	// local file conflict queue (replace vs rename)
	conflictQ []dlConflict
	// highlight task id briefly in downloads center
	dlFocusID    string
	dlFocusUntil time.Time

	// login
	loginBusy   bool
	loginStatus string
	loginQRPath string
	loginQRURL  string
	loginQRArt  string // terminal ASCII QR
	loginMethod auth.Method
	loginCtl    *loginControl
	loggingOut  bool
	// quit dialog: remember the choice (下次不再询问), checked by default
	quitRemember bool
}

// loginControl is shared by every copy of the model, so starting a login can
// cancel the one still polling and the old pump can tell it is stale.
type loginControl struct {
	mu     sync.Mutex
	gen    int
	cancel context.CancelFunc
}

func (c *loginControl) begin() (context.Context, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.gen++
	return ctx, c.gen
}

func (c *loginControl) current(gen int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return gen == c.gen
}

type loadedMsg struct {
	entries []api.FileEntry
	err     error
	path    string
}
type dlDoneMsg struct {
	path string
	err  error
	n    int
}
type profileMsg struct {
	text string
	err  error
}
type dlTickMsg struct{}
type resumeAskMsg struct{ n int }

type updateCheckMsg struct {
	res    *update.Result
	err    error
	manual bool
}

type updateApplyMsg struct {
	res *update.Result
	err error
}
type pauseDoneMsg struct{}
type dlJob struct {
	name, url, dest, fid string
	size                 int64 // as listed; 0 if unknown
	resume               bool
	replace              bool // delete existing dest before download
}
type dlEnqueueMsg struct {
	// scanGen identifies the expansion this result came from; zero means it was
	// not produced by one.
	scanGen uint64
	jobs    []dlJob
	focusID string // jump/highlight existing task
	status  string
	err     error
	// conflicts deferred to UI
	conflicts []dlConflict
}
type dlConflict struct {
	name, url, dest, fid string
	size                 int64
	reason               string
}

// Options for TUI.
type Options struct {
	Version string // embedded app version from main
}

// Run starts the interactive TUI.

// keyLetter normalizes a single a-z/A-Z key to lowercase so bindings are
// case-insensitive. Non-letter keys (enter, ctrl+c, f10, …) pass through.
func keyLetter(s string) string {
	// A Chinese input method types full-width forms (？ ｄ　): fold them to
	// ASCII so ? and the letter keys work without switching to English.
	if r := []rune(s); len(r) == 1 {
		switch {
		case r[0] >= 0xFF01 && r[0] <= 0xFF5E:
			s = string(r[0] - 0xFEE0)
		case r[0] == 0x3000:
			s = " "
		}
	}
	if len(s) == 1 {
		c := s[0]
		if c >= 'A' && c <= 'Z' {
			return string(c + ('a' - 'A'))
		}
	}
	return s
}

func Run(cfg *config.Config, opts Options) error {
	if err := device.EnsureDevice(cfg, ""); err != nil {
		return err
	}
	c := client.New(cfg)
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))

	// Downloads run in the download service shared by every GoQuark process
	// of this config; it holds the session, the queue and downloads.json.
	mgr := dlsvc.Open(cfg.Path())
	mgr.SetMaxLive(cfg.MaxDownloads)
	dlDir := cfg.EffectiveDownloadDir()
	_ = os.MkdirAll(dlDir, 0o755)
	mgr.SetDestDir(dlDir)

	ver := opts.Version
	if ver == "" {
		ver = "dev"
	}
	m := model{
		cfg:          cfg,
		client:       c,
		spinner:      sp,
		stack:        []navFrame{{fid: "0", path: "/", name: "根目录"}},
		selected:     map[int]bool{},
		loginQRPath:  filepath.Join(filepath.Dir(cfg.Path()), "qrcode.png"),
		loginMethod:  auth.MethodQuark,
		loginCtl:     &loginControl{},
		quitRemember: true,
		dlMgr:        mgr,
		appVersion:   ver,
	}
	if err := cfg.RequireLogin(); err != nil {
		m.mode = modeLogin
		m.status = "未登录"
	} else {
		m.mode = modeBrowse
		m.loading = true
		m.status = "加载中…"
		// ask to resume incomplete downloads after first paint
		if n := mgr.IncompleteCount(); n > 0 {
			m.pendingResumeAsk = n
		}
		// TUI proactive update check (unless user disabled auto-check)
		if !cfg.DisableUpdateAutoCheck {
			m.pendingUpdateCheck = true
		}
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())
	_, err := p.Run()
	return err
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, tickDownloads()}
	if m.mode == modeLogin {
		cmds = append(cmds, m.startLogin())
		return tea.Batch(cmds...)
	}
	cmds = append(cmds, m.loadCurrent())
	if m.pendingResumeAsk > 0 {
		cmds = append(cmds, func() tea.Msg {
			return resumeAskMsg{n: m.pendingResumeAsk}
		})
	}
	if m.pendingUpdateCheck {
		cmds = append(cmds, m.cmdCheckUpdate(false))
	}
	return tea.Batch(cmds...)
}

func (m model) cmdCheckUpdate(manual bool) tea.Cmd {
	ver := m.appVersion
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		res, err := update.Check(ctx, ver, update.RepoFromEnv())
		return updateCheckMsg{res: res, err: err, manual: manual}
	}
}

// cmdApplyUpdate runs the update under a context the dialog can cancel, so
// the goroutine is not left downloading after the user has backed out.
func cmdApplyUpdate(ctx context.Context, res *update.Result) tea.Cmd {
	return func() tea.Msg {
		return updateApplyMsg{res: res, err: update.Apply(ctx, res)}
	}
}

func tickDownloads() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return dlTickMsg{}
	})
}

func (m model) current() navFrame {
	if len(m.stack) == 0 {
		return navFrame{fid: "0", path: "/", name: "根目录"}
	}
	return m.stack[len(m.stack)-1]
}

func (m model) loadCurrent() tea.Cmd {
	cur := m.current()
	cl := m.client
	return func() tea.Msg {
		entries, err := api.ListDir(cl, cur.fid, 1, 500)
		return loadedMsg{entries: entries, err: err, path: cur.path}
	}
}

// startLogin starts a QR login with the selected method, cancelling any
// login still in progress.
func (m model) startLogin() tea.Cmd {
	cfg := m.cfg
	qrPath := m.loginQRPath
	method := m.loginMethod
	ctx, gen := m.loginCtl.begin()
	return func() tea.Msg {
		statusCh := make(chan string, 32)
		doneCh := make(chan error, 1)
		qrCh := make(chan qrReadyMsg, 1)
		go func() {
			_, err := auth.Login(cfg, method, auth.Options{
				Ctx:        ctx,
				QRPngPath:  qrPath,
				QRURLPath:  filepath.Join(filepath.Dir(qrPath), "qrcode.url.txt"),
				PrintASCII: false,
				Quiet:      true,
				Timeout:    5 * time.Minute,
				OnQR: func(url, ascii string) {
					select {
					case qrCh <- qrReadyMsg{url: url, ascii: ascii}:
					default:
					}
				},
				OnStatus: func(msg string) {
					select {
					case statusCh <- msg:
					default:
					}
				},
			})
			doneCh <- err
		}()
		return loginPumpMsg{loginPump{gen: gen, statusCh: statusCh, doneCh: doneCh, qrCh: qrCh}}
	}
}

type qrReadyMsg struct {
	url   string
	ascii string
}

// loginPump carries one login's channels; gen tells a cancelled login apart.
type loginPump struct {
	gen      int
	statusCh <-chan string
	doneCh   <-chan error
	qrCh     <-chan qrReadyMsg
}
type loginPumpMsg struct{ loginPump }
type loginPumpTick struct{ loginPump }

func pollLogin(p loginPump) tea.Cmd {
	return func() tea.Msg {
		return loginPumpTick{p}
	}
}

// scanPrompt is the login status once the QR is up.
func (m model) scanPrompt() string {
	if m.loginMethod == auth.MethodWeChat {
		return "请使用微信扫码"
	}
	return "请使用夸克 APP 扫码"
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	// The download list changes under the cursor on every tick, so the cursor
	// and scroll window are put right after each message rather than in every
	// place that can move them.
	if nm, ok := next.(model); ok {
		if nm.inDownloads() {
			nm.syncDownloads()
		} else if _, tick := msg.(dlTickMsg); tick {
			nm.settleDownloads()
		}
		next = nm
	}
	return next, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.listTop = headerLines
		// each item occupies rowHeight screen lines
		avail := msg.Height - headerLines - footerLines
		if avail < 4 {
			avail = 4
		}
		m.listH = avail / rowHeight
		if m.listH < 2 {
			m.listH = 2
		}
		m.ready = true
		m.ensureVisible()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// keep spinner alive while quitting so "正在退出" animates
		if m.quitting {
			m.modalBody = m.spinner.View() + " 正在安全暂停下载，请稍候…\n（等待当前分块写完，界面不会卡住）"
			// always re-schedule tick while quitting (don't drop animation)
			if cmd == nil {
				cmd = m.spinner.Tick
			}
		}
		return m, cmd

	case logoutDoneMsg:
		return m.finishLogout(msg.err, msg.paused)

	case loginPumpMsg:
		if !m.loginCtl.current(msg.gen) {
			return m, nil
		}
		m.loginBusy = true
		m.loginStatus = "正在获取二维码…"
		m.loginQRArt = ""
		m.loginQRURL = ""
		return m, pollLogin(msg.loginPump)

	case loginPumpTick:
		if !m.loginCtl.current(msg.gen) {
			return m, nil // superseded by a newer login
		}
		select {
		case err := <-msg.doneCh:
			m.loginBusy = false
			if errors.Is(err, context.Canceled) {
				return m, nil
			}
			if err != nil {
				return m.showModal("登录失败", err.Error(), "error", modeLogin), nil
			}
			m.mode = modeBrowse
			m.client = client.New(m.cfg)
			if m.dlMgr != nil {
				m.dlMgr.ReloadSession()
			}
			m.loading = true
			m.status = "登录成功，加载目录…"
			m.stack = []navFrame{{fid: "0", path: "/", name: "根目录"}}
			m.selected = map[int]bool{}
			m.loginQRArt = ""
			return m, m.loadCurrent()
		case qr := <-msg.qrCh:
			m.loginQRURL = qr.url
			m.loginQRArt = qr.ascii
			m.loginStatus = m.scanPrompt()
			return m, pollLogin(msg.loginPump)
		case s := <-msg.statusCh:
			switch {
			case s == "waiting_scan":
				if m.loginQRArt != "" {
					m.loginStatus = "等待扫码…"
				} else {
					m.loginStatus = "等待二维码…"
				}
			case strings.Contains(s, "qr ready"):
				if m.loginStatus == "" || m.loginStatus == "正在获取二维码…" {
					m.loginStatus = m.scanPrompt()
				}
			case s == "scanned":
				m.loginStatus = "已扫码，请在微信里确认…"
			case s == "cancelled":
				m.loginStatus = "已在微信里取消，可重新扫码"
			case strings.HasPrefix(s, "poll err:"):
				// keep quiet-ish; show short status
				m.loginStatus = "网络波动，重试中…"
			case s == "scan confirmed":
				m.loginStatus = "扫码成功，正在登录…"
			default:
				if s != "" && !strings.Contains(s, "Query result") {
					m.loginStatus = s
				}
			}
			return m, pollLogin(msg.loginPump)
		default:
			return m, tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg {
				return loginPumpTick{msg.loginPump}
			})
		}

	case loadedMsg:
		m.loading = false
		if msg.err != nil {
			if client.IsAuthError(msg.err) {
				// Session dead: clear and force QR login (no refresh_token exists).
				m.cfg.ClearSession()
				_ = m.cfg.Save()
				m.mode = modeLogin
				m.client = client.New(m.cfg)
				m.rows = nil
				m.selected = map[int]bool{}
				m.loginQRArt = ""
				m.loginStatus = "登录已失效，请重新扫码"
				m.loginBusy = false
				return m.showModal("需要重新登录", "会话已失效（31001/403）。夸克 PC 没有 refresh_token，请扫码重新登录。\n\n"+msg.err.Error(), "error", modeLogin), m.startLogin()
			}
			return m.showModal("加载失败", msg.err.Error(), "error", modeBrowse), nil
		}
		m.rows = buildRows(m.stack, msg.entries, msg.path)
		m.cursor = 0
		m.offset = 0
		m.selected = map[int]bool{}
		m.status = fmt.Sprintf("%d 项", len(msg.entries))
		return m, nil

	case dlDoneMsg:
		m.dlBusy = false
		if msg.err != nil {
			if client.IsAuthError(msg.err) {
				m.cfg.ClearSession()
				_ = m.cfg.Save()
				m.mode = modeLogin
				m.client = client.New(m.cfg)
				if m.dlMgr != nil {
					m.dlMgr.ReloadSession()
				}
				m.loginStatus = "登录已失效，请重新扫码"
				return m.showModal("需要重新登录", msg.err.Error(), "error", modeLogin), m.startLogin()
			}
			// stay in downloads center if open
			back := modeBrowse
			if m.mode == modeDownloads {
				back = modeDownloads
			}
			return m.showModal("下载失败", msg.err.Error(), "error", back), nil
		}
		m.dlMsg = fmt.Sprintf("已保存 %d 个文件 → %s", msg.n, msg.path)
		m.status = "下载完成"
		return m, nil

	case dlTickMsg:
		// refresh downloads UI / status badge
		if m.dlMgr != nil {
			n := m.dlMgr.ActiveCount()
			if n > 0 {
				m.dlBusy = true
				m.dlMsg = fmt.Sprintf("下载中 %d 个任务", n)
			} else if m.dlBusy && m.mode != modeDownloads {
				// leave last completion message
				m.dlBusy = false
			}
		}
		return m, tickDownloads()

	case dlEnqueueMsg:
		if msg.scanGen != 0 {
			// A scan cancelled by quit or logout may still deliver; its result
			// describes a session that is gone.
			if !m.scan.busy || msg.scanGen != m.scan.gen || m.quitting {
				return m, nil
			}
			m.stopScan()
		}
		if msg.err != nil {
			if client.IsAuthError(msg.err) {
				m.cfg.ClearSession()
				_ = m.cfg.Save()
				m.mode = modeLogin
				m.client = client.New(m.cfg)
				if m.dlMgr != nil {
					m.dlMgr.ReloadSession()
				}
				return m.showModal("需要重新登录", msg.err.Error(), "error", modeLogin), m.startLogin()
			}
			return m.showModal("下载失败", msg.err.Error(), "error", modeBrowse), nil
		}
		if m.dlMgr == nil {
			m.dlMgr = dlsvc.Open(m.cfg.Path())
			m.dlMgr.SetDestDir(m.cfg.EffectiveDownloadDir())
		}
		// queue conflicts for sequential prompts
		if len(msg.conflicts) > 0 {
			m.conflictQ = append(m.conflictQ, msg.conflicts...)
		}
		var lastID string
		for _, j := range msg.jobs {
			if j.replace {
				if m.dlMgr.BusyAtDest(j.dest) != nil {
					return m.showModal("下载失败", "目标路径已有下载任务："+j.dest, "error", modeDownloads), nil
				}
				_ = os.Remove(j.dest)
				_ = os.Remove(j.dest + ".gqparts")
			}
			id, err := m.dlMgr.Add(download.EnqueueOpts{
				Name: j.name, URL: j.url, Dest: j.dest, FID: j.fid, Size: j.size,
			})
			if err != nil {
				return m.showModal("下载失败", err.Error(), "error", modeBrowse), nil
			}
			if id != "" {
				lastID = id
			}
		}
		m.mode = modeDownloads
		m.dlHome()
		focus := msg.focusID
		if focus == "" {
			focus = lastID
		}
		if focus != "" {
			m.dlFocusID = focus
			m.dlFocusUntil = time.Now().Add(4 * time.Second)
			// a file of a folder download lands on its top-level folder
			if idx := dlRowOf(m.dlRows(), focus); idx >= 0 {
				m.dlCursor = idx
			}
		}
		if msg.status != "" {
			m.dlMsg = msg.status
			m.status = msg.status
		} else if len(msg.jobs) > 0 {
			m.dlBusy = true
			m.dlMsg = fmt.Sprintf("已加入 %d 个下载任务", len(msg.jobs))
			m.status = m.dlMsg
		}
		// if conflicts pending, show first after entering center
		if len(m.conflictQ) > 0 {
			return m.promptNextConflict()
		}
		return m, nil

	case resumeAskMsg:
		if msg.n <= 0 {
			return m, nil
		}
		// don't steal focus if another modal is up
		if m.mode == modeModal {
			return m, nil
		}
		return m.showConfirm(
			"恢复下载",
			fmt.Sprintf("检测到 %d 个因上次退出而暂停的下载任务。\n是否继续下载？\n（手动暂停的任务不会自动提示）", msg.n),
			"resume_downloads",
		), nil

	case updateCheckMsg:
		m.pendingUpdateCheck = false
		if msg.err != nil {
			if msg.manual {
				return m.showModal("检查更新失败", msg.err.Error(), "error", modeBrowse), nil
			}
			// silent on auto-check network failures
			m.status = "检查更新失败（可按 u 重试）"
			return m, nil
		}
		res := msg.res
		if res == nil || !res.UpdateAvailable {
			if msg.manual {
				cur := m.appVersion
				if res != nil && res.Current != "" {
					cur = res.Current
				}
				return m.showModal("已是最新", "当前版本 "+cur+"，无需更新。", "info", modeBrowse), nil
			}
			return m, nil
		}
		m.updateRes = res
		m.updateManual = msg.manual
		m.notesExpanded = false
		return m.showConfirm("发现新版本", m.updateConfirmBody(), "update_available"), nil

	case updateApplyMsg:
		m.loading = false
		if m.updateCancel != nil {
			m.updateCancel()
			m.updateCancel = nil
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				// Nothing is installed until the download has been verified,
				// so backing out during it leaves the binary untouched.
				return m.showModal("已取消更新",
					"下载已中止，当前版本没有任何改动。", "info", modeBrowse), nil
			}
			if errors.Is(msg.err, context.DeadlineExceeded) {
				return m.showModal("更新超时",
					"下载超时已中止，当前版本没有任何改动。", "error", modeBrowse), nil
			}
			if errors.Is(msg.err, update.ErrUpdateInProgress) {
				// A second trigger, not a failure — the first one is still
				// downloading and will report on its own.
				return m.showModal("更新进行中",
					"已有一个更新正在进行，请等它结束。", "info", modeBrowse), nil
			}
			return m.showModal("更新失败", msg.err.Error(), "error", modeBrowse), nil
		}
		ver := ""
		if msg.res != nil {
			ver = msg.res.Latest
		}
		done := "已安装 " + ver + "\n请退出后重新启动 GoQuark。"
		if note := m.updateNote(); note != "" {
			done += "\n" + note
		}
		if msg.res != nil && msg.res.PendingCleanup != "" {
			// Windows keeps the old image locked while this process runs it.
			done += "\n旧版本文件会在下次启动时自动清理。"
		}
		return m.showModal("更新完成", done, "info", modeBrowse), nil

	case pauseDoneMsg:
		m.quitting = false
		return m, tea.Quit

	case profileMsg:
		m.loading = false
		if msg.err != nil {
			if client.IsAuthError(msg.err) {
				m.cfg.ClearSession()
				_ = m.cfg.Save()
				m.mode = modeLogin
				m.client = client.New(m.cfg)
				m.loginStatus = "登录已失效，请重新扫码"
				return m.showModal("需要重新登录", msg.err.Error(), "error", modeLogin), m.startLogin()
			}
			return m.showModal("获取个人信息失败", msg.err.Error(), "error", modeBrowse), nil
		}
		m.profileText = msg.text
		m.mode = modeProfile
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func buildRows(stack []navFrame, entries []api.FileEntry, path string) []row {
	rows := make([]row, 0, len(entries)+1)
	if len(stack) > 1 {
		rows = append(rows, row{
			entry: api.FileEntry{Name: "..", Dir: true, FID: stack[len(stack)-2].fid},
			path:  stack[len(stack)-2].path,
		})
	}
	for _, e := range entries {
		rows = append(rows, row{entry: e, path: joinPath(path, e.Name)})
	}
	return rows
}

func (m model) showModal(title, body, kind string, back mode) model {
	m.modalTitle = title
	m.modalBody = body
	m.modalKind = kind
	m.modalBack = back
	m.confirmAction = ""
	m.btnFocus = 1
	m.btnHover = 0
	m.mode = modeModal
	return m
}

// progressCancelLabel is both the button text and what progressCancelHit
// looks for in the rendered modal, so the two cannot drift apart.
const progressCancelLabel = "取消更新"

// showProgress puts up a dialog that stays until the work behind it reports
// back. Unlike an info modal it is not dismissed by a stray click — the
// release half of the very click that opened it used to do exactly that —
// and it carries the one action that makes sense while work is running.
func (m model) showProgress(title, body string) model {
	m.modalTitle = title
	m.modalBody = body
	m.modalKind = "progress"
	m.modalBack = modeBrowse
	m.confirmAction = ""
	m.btnFocus = 0
	m.btnHover = 0
	m.mode = modeModal
	return m
}

// cancelUpdate aborts the download. The apply goroutine notices the cancelled
// context, clears its staging file and reports back, so the dialog is
// replaced from updateApplyMsg rather than here.
func (m model) cancelUpdate() (tea.Model, tea.Cmd) {
	if m.updateCancel != nil {
		m.updateCancel()
	}
	m.modalBody = "正在中止…"
	return m, nil
}

func (m model) showConfirm(title, body, action string) model {
	m.modalTitle = title
	m.modalBody = body
	m.modalKind = "confirm"
	// resume / cancel-all / conflict stay over downloads when relevant
	back := modeBrowse
	switch action {
	case "resume_downloads", "cancel_all_downloads", "conflict_replace":
		if m.mode == modeDownloads || m.modalBack == modeDownloads {
			back = modeDownloads
		}
	}
	// if already in downloads or action is download-related from center
	if m.mode == modeDownloads {
		back = modeDownloads
	}
	m.modalBack = back
	m.confirmAction = action
	// default highlight on primary (确认 / 替换)
	m.btnFocus = 1
	m.btnHover = 0
	m.mode = modeModal
	return m
}

// confirmLabels returns primary/secondary button captions for the current confirm modal.
// Primary (okLabel) is the default focus (btnFocus=1) and Esc target.
func (m model) confirmLabels() (okLabel, noLabel string) {
	okLabel, noLabel = "确认", "取消"
	switch m.confirmAction {
	case "conflict_replace":
		okLabel, noLabel = "替换", "重命名"
	case "update_available":
		if m.updateRes != nil && m.updateRes.CanApply {
			okLabel, noLabel = "立即更新", "稍后"
		} else {
			okLabel, noLabel = "知道了", "不再提示"
		}
	case "update_disable_auto":
		// Default = keep auto-check ON (user asked: 默认保持开启)
		okLabel, noLabel = "保持开启", "关闭自动检查"
	case "quit_choice":
		okLabel, noLabel = "暂停下载并退出", "后台继续下载"
	}
	return okLabel, noLabel
}

// confirmHighlight returns which button is visually focused: 1 primary, 2 secondary.
// Mouse hover wins when present; otherwise keyboard focus.
func (m model) confirmHighlight() int {
	if m.btnHover == 1 || m.btnHover == 2 {
		return m.btnHover
	}
	if m.btnFocus == 2 {
		return 2
	}
	return 1
}

// activateConfirmBtn runs primary (1) or secondary (2) confirm button.
func (m model) activateConfirmBtn(which int) (tea.Model, tea.Cmd) {
	if m.modalKind != "confirm" {
		m.mode = m.modalBack
		return m, nil
	}
	act := m.confirmAction
	m.btnHover = 0
	m.btnFocus = 1
	if act == "quit_choice" {
		m.confirmAction = ""
		if which == 2 {
			return m.chooseQuit(quitBackground)
		}
		return m.chooseQuit(quitPause)
	}
	if which == 2 {
		// secondary
		m.confirmAction = ""
		if act == "conflict_replace" {
			return m.resolveConflict(false)
		}
		// user declined quit-resume prompt: clear auto-resume flags so we don't re-ask
		if act == "resume_downloads" && m.dlMgr != nil {
			m.dlMgr.ClearQuitResumeFlags()
		}
		// update prompt: cancel = later; if auto-check was on, offer disable
		if act == "update_available" {
			res := m.updateRes
			if res != nil && !res.CanApply {
				// secondary "不再提示" → turn off startup auto-check
				if m.cfg != nil {
					m.cfg.DisableUpdateAutoCheck = true
					_ = m.cfg.Save()
				}
				m.mode = modeBrowse
				m.status = "已关闭启动时自动检查（按 u 仍可检查）"
				return m, nil
			}
			if m.cfg != nil && !m.cfg.DisableUpdateAutoCheck && !m.updateManual {
				return m.showConfirm(
					"更新提示",
					"是否保留启动时自动检查更新？\n\n关闭后，每次启动将不再自动检查（随时按 u 可手动检查）。",
					"update_disable_auto",
				), nil
			}
			m.mode = modeBrowse
			m.status = "已忽略本次更新（按 u 可再检查）"
			return m, nil
		}
		// update_disable_auto secondary = 关闭自动检查
		if act == "update_disable_auto" {
			if m.cfg != nil {
				m.cfg.DisableUpdateAutoCheck = true
				_ = m.cfg.Save()
			}
			m.mode = modeBrowse
			m.status = "已关闭启动时自动检查（按 u 仍可检查）"
			return m, nil
		}
		// plain cancel
		m.mode = m.modalBack
		return m, nil
	}
	// primary = default button
	m.confirmAction = ""
	switch act {
	case "conflict_replace", "cancel_all_downloads", "resume_downloads":
		m.mode = modeDownloads
	default:
		if m.modalBack != 0 {
			// keep mode for runConfirmed to set
		}
		m.mode = modeBrowse
	}
	if act == "" {
		m.mode = m.modalBack
		return m, nil
	}
	return m.runConfirmed(act)
}

func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// A click arrives as a press and then a release. When the press already
	// acted, the release must not be handed to whatever the screen has become
	// in the meantime: pressing 立即更新 swapped the dialog for the progress
	// one, and the release was then read by that dialog as "click anywhere to
	// dismiss", so it vanished in the frame it appeared.
	switch msg.Action {
	case tea.MouseActionRelease:
		if m.swallowRelease {
			m.swallowRelease = false
			return m, nil
		}
	case tea.MouseActionPress:
		m.swallowRelease = false
	}

	// modal: clickable buttons for confirm; hover highlights
	if m.mode == modeModal {
		if m.quitting {
			return m, nil
		}
		if m.modalKind == "progress" {
			if msg.Action == tea.MouseActionMotion {
				m.btnHover = 0
				if m.progressCancelHit(msg.X, msg.Y) {
					m.btnHover = 2
				}
				return m, nil
			}
			if msg.Button == tea.MouseButtonLeft &&
				(msg.Action == tea.MouseActionPress || msg.Action == tea.MouseActionRelease) &&
				m.progressCancelHit(msg.X, msg.Y) {
				m.swallowRelease = msg.Action == tea.MouseActionPress
				return m.cancelUpdate()
			}
			// Anything else leaves the download running and the dialog up.
			return m, nil
		}
		if m.modalKind == "confirm" {
			// hover tracking (needs WithMouseAllMotion)
			if msg.Action == tea.MouseActionMotion {
				switch {
				case m.hitOK(msg.X, msg.Y):
					m.btnHover = 1
				case m.hitCancel(msg.X, msg.Y):
					m.btnHover = 2
				default:
					// leave keyboard focus as-is when mouse leaves buttons
					m.btnHover = 0
				}
				return m, nil
			}
			// Accept both press and release — some terminals only emit one.
			if msg.Button == tea.MouseButtonLeft &&
				(msg.Action == tea.MouseActionRelease || msg.Action == tea.MouseActionPress) {
				if m.notesToggleHit(msg.X, msg.Y) {
					if msg.Action == tea.MouseActionRelease {
						return m.toggleUpdateNotes(), nil
					}
					return m, nil
				}
				if m.quitRememberHit(msg.X, msg.Y) {
					if msg.Action == tea.MouseActionRelease {
						return m.toggleQuitRemember(), nil
					}
					return m, nil
				}
				if m.hitOK(msg.X, msg.Y) {
					m.swallowRelease = msg.Action == tea.MouseActionPress
					return m.activateConfirmBtn(1)
				}
				if m.hitCancel(msg.X, msg.Y) {
					m.swallowRelease = msg.Action == tea.MouseActionPress
					return m.activateConfirmBtn(2)
				}
				// outside click = secondary only on release (avoid accidental)
				if msg.Action == tea.MouseActionRelease && !m.hitOverlay(msg.X, msg.Y) {
					return m.activateConfirmBtn(2)
				}
				return m, nil
			}
			return m, nil
		}
		if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
			// info/error: click anywhere closes
			m.mode = m.modalBack
			return m, nil
		}
		return m, nil
	}
	if m.mode == modeProfile {
		if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
			m.mode = modeBrowse
			return m, nil
		}
		return m, nil
	}
	if m.mode == modeLogin {
		return m, nil
	}

	// floating menu — Windows-like sticky menu
	if m.mode == modeMenu {
		// Ignore the right-button release that follows the opening press.
		if msg.Button == tea.MouseButtonRight {
			if msg.Action == tea.MouseActionRelease && m.menuIgnoreRightRelease {
				m.menuIgnoreRightRelease = false
				return m, nil
			}
			// another right-press: reopen at new position
			if msg.Action == tea.MouseActionPress {
				if idx, ok := m.rowAt(msg.Y); ok {
					m.cursor = idx
					if !m.selected[idx] {
						m.selected = map[int]bool{idx: true}
					}
				}
				return m.openMenuAt(msg.X, msg.Y), nil
			}
			return m, nil
		}
		// hover highlight
		if msg.Action == tea.MouseActionMotion {
			if idx, ok := m.menuHit(msg.X, msg.Y); ok {
				m.menuIdx = idx
			}
			return m, nil
		}
		// left click: select item or close outside
		if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
			if idx, ok := m.menuHit(msg.X, msg.Y); ok {
				m.menuIdx = idx
				return m.applyMenu(m.menuItems[idx].id)
			}
			// close → return to origin view
			from := m.menuFrom
			if from != modeDownloads {
				from = modeBrowse
			}
			m.mode = from
			m.menuIgnoreRightRelease = false
			return m, nil
		}
		return m, nil
	}

	// download center: right-click opens download management menu (stay here)
	if m.mode == modeDownloads {
		rows := m.dlRows()
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonRight {
			if idx, ok := m.dlRowAt(msg.Y, len(rows)); ok {
				m.dlCursor = idx
			}
			return m.openMenuAt(msg.X, msg.Y), nil
		}
		if msg.Button == tea.MouseButtonRight {
			return m, nil
		}
		if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
			idx, ok := m.dlRowAt(msg.Y, len(rows))
			if !ok {
				return m, nil
			}
			now := time.Now()
			if idx == m.dlLastClickIdx && now.Sub(m.dlLastClickTime) < 400*time.Millisecond && rows[idx].dir {
				m.dlLastClickTime = time.Time{}
				m.dlCursor = idx
				m.dlOpenFolder(rows[idx])
				return m, nil
			}
			m.dlLastClickIdx = idx
			m.dlLastClickTime = now
			m.dlCursor = idx
			return m, nil
		}
		// syncDownloads clamps the cursor and scrolls it into view afterwards
		if msg.Button == tea.MouseButtonWheelUp {
			m.dlCursor--
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			m.dlCursor++
			return m, nil
		}
		return m, nil
	}

	// browse mode: open sticky menu on right-press (not release)
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonRight {
		if idx, ok := m.rowAt(msg.Y); ok {
			m.cursor = idx
			if !m.selected[idx] {
				m.selected = map[int]bool{idx: true}
			}
		}
		return m.openMenuAt(msg.X, msg.Y), nil
	}
	// swallow right-release in browse (menu already open handles its own)
	if msg.Button == tea.MouseButtonRight {
		return m, nil
	}

	if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
		idx, ok := m.rowAt(msg.Y)
		if !ok {
			return m, nil
		}
		now := time.Now()
		if idx == m.lastClickIdx && now.Sub(m.lastClickTime) < 400*time.Millisecond {
			m.lastClickTime = time.Time{}
			m.cursor = idx
			return m.doActivate()
		}
		m.lastClickIdx = idx
		m.lastClickTime = now
		m.cursor = idx
		m.selected = map[int]bool{idx: true}
		if m.rows[idx].entry.Name != ".." {
			m.status = fmt.Sprintf("%d 项", countItems(m.rows))
		}
		return m, nil
	}

	// wheel scroll
	if msg.Button == tea.MouseButtonWheelUp {
		m.cursor = max(0, m.cursor-1)
		m.ensureVisible()
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelDown {
		if len(m.rows) > 0 {
			m.cursor = min(len(m.rows)-1, m.cursor+1)
			m.ensureVisible()
		}
		return m, nil
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// All letter shortcuts are case-insensitive; UI always shows lowercase.
	key := keyLetter(msg.String())
	switch m.mode {
	case modeModal:
		// while quitting, ignore keys/clicks
		if m.quitting {
			return m, nil
		}
		if m.modalKind == "confirm" && m.confirmAction == "quit_choice" {
			switch key {
			case " ":
				return m.toggleQuitRemember(), nil
			case "esc", "q", "n":
				// the dialog's own way out: stay, nothing paused
				m.mode = m.modalBack
				m.confirmAction = ""
				return m, nil
			}
		}
		if m.modalKind == "help" {
			m.mode = m.modalBack
			switch key {
			case "?", "esc", "enter":
				return m, nil
			}
			// any other key is the shortcut it shows: run it on the view below
			return m.handleKey(msg)
		}
		if m.modalKind == "progress" {
			// Only an explicit abort does anything; a stray Enter must not
			// throw away a download that is half done.
			switch key {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				return m.cancelUpdate()
			}
			return m, nil
		}
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "left", "h":
			// keyboard focus only when mouse is not over a button
			if m.modalKind == "confirm" && m.btnHover == 0 {
				m.btnFocus = 1
			}
			return m, nil
		case "right", "l":
			if m.modalKind == "confirm" && m.btnHover == 0 {
				m.btnFocus = 2
			}
			return m, nil
		case "tab":
			if m.modalKind == "confirm" && m.btnHover == 0 {
				if m.btnFocus == 2 {
					m.btnFocus = 1
				} else {
					m.btnFocus = 2
				}
			}
			return m, nil
		case "esc":
			// Esc → default (primary) button
			if m.modalKind == "confirm" && m.confirmAction != "" {
				return m.activateConfirmBtn(1)
			}
			m.mode = m.modalBack
			m.confirmAction = ""
			return m, nil
		case "d":
			// release notes expand / collapse
			if m.modalKind == "confirm" && m.confirmAction == "update_available" {
				return m.toggleUpdateNotes(), nil
			}
			return m, nil
		case "n":
			// explicit secondary / cancel
			if m.modalKind == "confirm" {
				return m.activateConfirmBtn(2)
			}
			m.mode = m.modalBack
			return m, nil
		case "q":
			// q still means secondary/cancel for safety on dangerous dialogs
			if m.modalKind == "confirm" {
				return m.activateConfirmBtn(2)
			}
			m.mode = m.modalBack
			m.confirmAction = ""
			return m, nil
		case "enter", "y", " ":
			if m.modalKind == "confirm" && m.confirmAction != "" {
				// Enter triggers currently highlighted button
				return m.activateConfirmBtn(m.confirmHighlight())
			}
			m.mode = m.modalBack
			return m, nil
		}
		return m, nil
	case modeLogin:
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "l", "r":
			if !m.loginBusy {
				m.loginQRArt = ""
				m.loginQRURL = ""
				m.loginStatus = "正在获取二维码…"
				return m, m.startLogin()
			}
		case "tab":
			if m.loginMethod == auth.MethodWeChat {
				m.loginMethod = auth.MethodQuark
			} else {
				m.loginMethod = auth.MethodWeChat
			}
			m.loginQRArt = ""
			m.loginQRURL = ""
			m.loginStatus = "正在获取二维码…"
			return m, m.startLogin()
		}
		return m, nil
	case modeProfile:
		switch key {
		case "esc", "enter", "q", " ":
			m.mode = modeBrowse
			return m, nil
		case "u":
			m.status = "正在检查更新…"
			return m, m.cmdCheckUpdate(true)
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	case modeMenu:
		switch key {
		case "esc":
			from := m.menuFrom
			if from != modeDownloads {
				from = modeBrowse
			}
			m.mode = from
			m.menuIgnoreRightRelease = false
		case "q":
			// dangerous: confirm quit
			m.mode = modeBrowse
			return m.askQuit(), nil
		case "ctrl+c":
			return m.askQuit(), nil
		case "up", "k":
			if m.menuIdx > 0 {
				m.menuIdx--
			}
		case "down", "j":
			if m.menuIdx < len(m.menuItems)-1 {
				m.menuIdx++
			}
		case "enter":
			return m.applyMenu(m.menuItems[m.menuIdx].id)
		case "m", "f10":
			// reopen at same place is fine
		}
		return m, nil
	case modeDownloads:
		// syncDownloads clamps the cursor afterwards, so moves need no bounds
		switch key {
		case "esc", "backspace", "h", "left", "b":
			if len(m.dlPath) > 0 {
				m.dlLeaveFolder()
				return m, nil
			}
			m.mode = modeBrowse
			return m, nil
		case "enter", "l", "right":
			if rows := m.dlRows(); m.dlCursor >= 0 && m.dlCursor < len(rows) && rows[m.dlCursor].dir {
				m.dlOpenFolder(rows[m.dlCursor])
			}
		case "?":
			return m.showShortcuts(), nil
		case "q":
			return m.askQuit(), nil
		case "ctrl+c":
			return m.askQuit(), nil
		case "up", "k":
			m.dlCursor--
		case "down", "j":
			m.dlCursor++
		case "pgup":
			m.dlCursor -= m.dlListH()
		case "pgdown":
			m.dlCursor += m.dlListH()
		case "home", "g":
			m.dlCursor = 0
		case "end", "e":
			m.dlCursor = len(m.dlRows()) - 1
		case "c":
			if m.dlMgr != nil {
				n := m.dlMgr.ClearFinished()
				m.status = fmt.Sprintf("已清除 %d 条完成记录", n)
			}
		case "p": // pause all — non-blocking
			if m.dlMgr != nil {
				n := m.dlMgr.PauseAll() // async wait inside manager
				m.status = fmt.Sprintf("正在暂停 %d 个任务…", n)
			}
		case "u":
			m.status = "正在检查更新…"
			return m, m.cmdCheckUpdate(true)
		case "r": // resume all
			if m.dlMgr != nil {
				n := m.dlMgr.ResumePaused()
				m.status = fmt.Sprintf("已恢复 %d 个任务", n)
			}
		case "x": // cancel all (confirm)
			return m.showConfirm("全部取消", "确定取消全部下载任务？\n未完成的进度会保留在本地，但任务将标记为取消。", "cancel_all_downloads"), nil
		case "m", "f10":
			// keyboard: open the menu beside the focused row
			return m.openMenuAt(4, dlHeaderLines+(m.dlCursor-m.dlOffset)*dlRowLines+1), nil
		}
		return m, nil
	}

	// browse
	switch key {
	case "u":
		m.status = "正在检查更新…"
		return m, m.cmdCheckUpdate(true)
	case "q":
		return m.askQuit(), nil
	case "ctrl+c":
		return m.askQuit(), nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.ensureVisible()
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
			m.ensureVisible()
		}
	case "pgup":
		m.cursor = max(0, m.cursor-m.listH)
		m.ensureVisible()
	case "pgdown":
		m.cursor = min(len(m.rows)-1, m.cursor+m.listH)
		m.ensureVisible()
	case "home", "g":
		m.cursor = 0
		m.ensureVisible()
	case "end", "e": // end of list (e = end; g = top)
		if len(m.rows) > 0 {
			m.cursor = len(m.rows) - 1
			m.ensureVisible()
		}
	case " ": // toggle multi-select on cursor
		if len(m.rows) == 0 {
			return m, nil
		}
		if m.selected[m.cursor] {
			delete(m.selected, m.cursor)
		} else {
			m.selected[m.cursor] = true
		}
		m.status = ""
	case "a", "ctrl+a": // select all
		m.selected = map[int]bool{}
		for i, r := range m.rows {
			if r.entry.Name != ".." {
				m.selected[i] = true
			}
		}
		m.status = "已全选"
	case "c": // clear selection — lowercase/uppercase both OK (not Shift+A)
		m.selected = map[int]bool{}
		m.status = "已清除选择"
	case "enter", "l", "right":
		return m.doActivate()
	case "backspace", "h", "left":
		return m.doGoUp()
	case "r":
		m.loading = true
		m.status = "刷新…"
		return m, m.loadCurrent()
	case "d":
		return m.doDownload()
	case "t":
		// download center (avoid d/D case collision)
		m.mode = modeDownloads
		m.dlHome()
		return m, nil
	case "m", "f10":
		x, y := 4, m.listTop+(m.cursor-m.offset)*rowHeight
		return m.openMenuAt(x, y), nil
	case "i":
		m.loading = true
		m.status = "加载个人信息…"
		return m, m.fetchProfile()
	case "o":
		return m.showConfirm("退出登录", "确定退出当前账号？设备信息会保留。", "logout"), nil
	case "?":
		return m.showShortcuts(), nil
	}
	return m, nil
}

func (m model) openMenuAt(x, y int) model {
	// remember origin view so close doesn't dump to browse from downloads
	from := m.mode
	if from == modeMenu {
		from = m.menuFrom
	}
	if from != modeDownloads {
		from = modeBrowse
	}
	m.menuFrom = from
	m.mode = modeMenu
	m.menuIdx = 0
	m.menuX, m.menuY = x, y
	m.menuIgnoreRightRelease = true

	if from == modeDownloads {
		// Remember the row itself: the list keeps moving while the menu is
		// open, and the action has to hit what was clicked.
		m.dlTarget = nil
		if rows := m.dlRows(); m.dlCursor >= 0 && m.dlCursor < len(rows) {
			r := rows[m.dlCursor]
			m.dlTarget = &r
		}
		m.menuItems = m.downloadMenuItems()
	} else {
		m.menuItems = []menuEntry{
			{id: "profile", label: "个人信息"},
			{id: "refresh", label: "刷新"},
			{id: "download", label: "下载选中"},
			{id: "downloads", label: "下载中心"},
			{id: "select_all", label: "全选"},
			{id: "clear_sel", label: "清除选择"},
			{id: "check_update", label: "检查更新"},
			{id: "logout", label: "退出登录"},
			{id: "quit", label: "退出程序"},
			{id: "close", label: "关闭菜单"},
		}
	}
	// clamp so menu stays on screen
	mw, mh := m.menuSize()
	if m.menuX+mw > m.width {
		m.menuX = max(0, m.width-mw)
	}
	if m.menuY+mh > m.height {
		m.menuY = max(0, m.height-mh)
	}
	if m.menuX < 0 {
		m.menuX = 0
	}
	if m.menuY < 0 {
		m.menuY = 0
	}
	return m
}

func (m model) downloadMenuItems() []menuEntry {
	items := []menuEntry{}
	if r := m.dlTarget; r != nil && r.dir {
		items = append(items, menuEntry{id: "dl_open", label: "打开文件夹"})
		a := r.agg
		moving := a.status == download.StatusRunning || a.status == download.StatusQueued
		if moving {
			items = append(items, menuEntry{id: "dl_pause", label: "暂停整个文件夹"})
		}
		if a.doneN < a.files && !moving && a.status != download.StatusPausing {
			items = append(items, menuEntry{id: "dl_resume", label: "继续整个文件夹"})
		}
		if a.doneN < a.files && a.status != download.StatusCancel {
			items = append(items, menuEntry{id: "dl_cancel", label: "取消整个文件夹"})
		}
	} else if r != nil {
		switch r.tasks[0].Status {
		case download.StatusRunning, download.StatusQueued, download.StatusFinalizing:
			items = append(items, menuEntry{id: "dl_pause", label: "暂停此任务"})
			items = append(items, menuEntry{id: "dl_cancel", label: "取消此任务"})
		case download.StatusPausing:
			items = append(items, menuEntry{id: "dl_cancel", label: "取消此任务"})
		case download.StatusPaused, download.StatusError, download.StatusCancel:
			items = append(items, menuEntry{id: "dl_resume", label: "继续下载"})
			items = append(items, menuEntry{id: "dl_cancel", label: "取消此任务"})
		case download.StatusDone:
			items = append(items, menuEntry{id: "dl_open_path", label: "显示路径"})
		}
	}
	if len(m.dlPath) > 0 {
		items = append(items, menuEntry{id: "dl_up", label: "返回上一层"})
	}
	items = append(items,
		menuEntry{id: "dl_pause_all", label: "全部暂停"},
		menuEntry{id: "dl_resume_all", label: "全部恢复"},
		menuEntry{id: "dl_cancel_all", label: "全部取消"},
		menuEntry{id: "dl_clear", label: "清除已完成"},
		menuEntry{id: "check_update", label: "检查更新"},
		menuEntry{id: "dl_back", label: "返回网盘"},
		menuEntry{id: "close", label: "关闭菜单"},
	)
	return items
}

func (m model) menuSize() (w, h int) {
	// width must match renderMenu fixed cell width (+ borders/padding)
	// selected form: "› " + label + trailing pad space(s)
	maxW := 0
	for _, it := range m.menuItems {
		for _, prefix := range []string{"  ", "› "} {
			// +1 trailing space is always reserved (blue when selected)
			if ww := lipgloss.Width(prefix+it.label) + 1; ww > maxW {
				maxW = ww
			}
		}
	}
	if maxW < 12 {
		maxW = 12
	}
	// box padding (0,1) + border ≈ +4 visual; for hit-test use content+pad
	w = maxW + 4
	h = len(m.menuItems) + 2 // border
	return w, h
}

func (m model) menuHit(x, y int) (int, bool) {
	mw, mh := m.menuSize()
	if x < m.menuX || x >= m.menuX+mw || y < m.menuY || y >= m.menuY+mh {
		return 0, false
	}
	// content starts at menuY+1
	row := y - m.menuY - 1
	if row < 0 || row >= len(m.menuItems) {
		return 0, false
	}
	return row, true
}

func (m model) applyMenu(id string) (tea.Model, tea.Cmd) {
	from := m.menuFrom
	if from != modeDownloads {
		from = modeBrowse
	}
	m.mode = from
	m.menuIgnoreRightRelease = false
	switch id {
	case "profile":
		m.loading = true
		m.status = "加载个人信息…"
		return m, m.fetchProfile()
	case "refresh":
		m.loading = true
		m.status = "刷新…"
		return m, m.loadCurrent()
	case "download":
		return m.doDownload()
	case "downloads":
		m.mode = modeDownloads
		m.dlHome()
		return m, nil
	case "select_all":
		m.selected = map[int]bool{}
		for i, r := range m.rows {
			if r.entry.Name != ".." {
				m.selected[i] = true
			}
		}
		m.status = ""
		return m, nil
	case "clear_sel":
		m.selected = map[int]bool{}
		m.status = ""
		return m, nil
	case "check_update":
		m.mode = modeBrowse
		m.status = "正在检查更新…"
		return m, m.cmdCheckUpdate(true)
	case "logout":
		return m.showConfirm("退出登录", "确定退出当前账号？设备信息会保留。", "logout"), nil
	case "quit":
		return m.askQuit(), nil
	// download-center menu
	case "dl_open":
		m.mode = modeDownloads
		if r := m.dlTarget; r != nil && r.dir {
			m.dlOpenFolder(*r)
		}
		return m, nil
	case "dl_up":
		m.mode = modeDownloads
		if len(m.dlPath) > 0 {
			m.dlLeaveFolder()
		}
		return m, nil
	case "dl_pause":
		if r := m.dlTarget; m.dlMgr != nil && r != nil {
			if m.dlMgr.PauseIDs(r.ids()) > 0 {
				m.status = "正在暂停: " + r.name
			}
		}
		m.mode = modeDownloads
		return m, nil
	case "dl_resume":
		if r := m.dlTarget; m.dlMgr != nil && r != nil {
			if m.dlMgr.ResumeIDs(r.ids()) > 0 {
				m.status = "继续下载: " + r.name
			} else {
				m.status = "无法继续: " + r.name + "（可能已在运行或状态不对）"
			}
		}
		m.mode = modeDownloads
		return m, nil
	case "dl_cancel":
		m.mode = modeDownloads
		if r := m.dlTarget; r != nil && r.dir {
			return m.showConfirm("取消文件夹",
				fmt.Sprintf("确定取消「%s」里未完成的 %d 个文件？\n已完成的文件不受影响，未完成的进度会保留在本地。",
					r.name, r.agg.files-r.agg.doneN), "cancel_dl_folder"), nil
		}
		if r := m.dlTarget; m.dlMgr != nil && r != nil {
			if m.dlMgr.CancelIDs(r.ids()) > 0 {
				m.status = "已取消: " + r.name
			}
		}
		return m, nil
	case "dl_pause_all":
		if m.dlMgr != nil {
			n := m.dlMgr.PauseAll()
			m.status = fmt.Sprintf("正在暂停 %d 个任务…", n)
		}
		m.mode = modeDownloads
		return m, nil
	case "dl_resume_all":
		if m.dlMgr != nil {
			n := m.dlMgr.ResumePaused()
			m.status = fmt.Sprintf("已恢复 %d 个任务", n)
		}
		m.mode = modeDownloads
		return m, nil
	case "dl_cancel_all":
		m.mode = modeDownloads
		return m.showConfirm("全部取消", "确定取消全部下载任务？\n未完成的进度会保留在本地，但任务将标记为取消。", "cancel_all_downloads"), nil
	case "dl_clear":
		if m.dlMgr != nil {
			n := m.dlMgr.ClearFinished()
			m.status = fmt.Sprintf("已清除 %d 条完成记录", n)
		}
		m.mode = modeDownloads
		return m, nil
	case "dl_open_path":
		if r := m.dlTarget; r != nil && !r.dir {
			m.status = r.tasks[0].Dest
		}
		m.mode = modeDownloads
		return m, nil
	case "dl_back":
		m.mode = modeBrowse
		return m, nil
	case "close":
		m.mode = from
		return m, nil
	default:
		return m, nil
	}
}

func (m model) runConfirmed(action string) (tea.Model, tea.Cmd) {
	switch action {
	case "logout":
		return m.doLogout()
	case "quit":
		return m.beginQuit()
	case "resume_downloads":
		if m.dlMgr != nil {
			n := m.dlMgr.ResumeQuitPaused()
			m.status = fmt.Sprintf("已恢复 %d 个下载", n)
			m.mode = modeDownloads
		}
		return m, nil
	case "cancel_all_downloads":
		if m.dlMgr != nil {
			n := m.dlMgr.CancelAll()
			m.status = fmt.Sprintf("已取消 %d 个任务", n)
			m.mode = modeDownloads
		}
		return m, nil
	case "cancel_dl_folder":
		if r := m.dlTarget; m.dlMgr != nil && r != nil {
			n := m.dlMgr.CancelIDs(r.ids())
			m.status = fmt.Sprintf("已取消「%s」里的 %d 个文件", r.name, n)
		}
		m.mode = modeDownloads
		return m, nil
	case "conflict_replace":
		return m.resolveConflict(true)
	case "conflict_rename":
		return m.resolveConflict(false)
	case "update_available":
		res := m.updateRes
		if res == nil {
			m.mode = modeBrowse
			return m, nil
		}
		if res.CanApply {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			m.updateCancel = cancel
			m = m.showProgress("正在更新",
				m.spinner.View()+" 正在下载 "+res.AssetName+" …")
			m.loading = true
			return m, tea.Batch(m.spinner.Tick, cmdApplyUpdate(ctx, res))
		}
		// cannot apply — primary was "知道了"
		m.mode = modeBrowse
		m.status = fmt.Sprintf("新版本 %s 可手动获取", res.Latest)
		return m, nil
	case "update_disable_auto":
		// primary = 保持开启 → do nothing, just dismiss
		m.mode = modeBrowse
		m.status = "将继续启动时自动检查更新"
		return m, nil
	default:
		return m, nil
	}
}

func (m model) promptNextConflict() (tea.Model, tea.Cmd) {
	if len(m.conflictQ) == 0 {
		m.mode = modeDownloads
		return m, nil
	}
	c := m.conflictQ[0]
	body := fmt.Sprintf("文件：%s\n本地路径：%s\n\n%s\n\n确认 = 替换本地文件\n取消 = 另存为「名称 (2)」",
		c.name, c.dest, c.reason)
	// use confirm; cancel path will rename
	m.modalTitle = "本地文件冲突"
	m.modalBody = body
	m.modalKind = "confirm"
	m.modalBack = modeDownloads
	m.confirmAction = "conflict_replace"
	m.btnFocus = 1 // default primary = 替换
	m.btnHover = 0
	m.mode = modeModal
	return m, nil
}

func (m model) resolveConflict(replace bool) (tea.Model, tea.Cmd) {
	if len(m.conflictQ) == 0 {
		m.mode = modeDownloads
		return m, nil
	}
	c := m.conflictQ[0]
	m.conflictQ = m.conflictQ[1:]
	dest := c.dest
	if replace {
		if m.dlMgr != nil && m.dlMgr.BusyAtDest(dest) != nil {
			return m.showModal("下载失败", "目标路径已有下载任务："+dest, "error", modeDownloads), nil
		}
		_ = os.Remove(dest)
		_ = os.Remove(dest + ".gqparts")
	} else {
		dest = download.UniqueDest(dest)
	}
	if m.dlMgr == nil {
		m.dlMgr = dlsvc.Open(m.cfg.Path())
		m.dlMgr.SetDestDir(m.cfg.EffectiveDownloadDir())
	}
	// need URL — if empty, resolve async
	urlStr := c.url
	cl := m.client
	fid := c.fid
	name := c.name
	return m, func() tea.Msg {
		if urlStr == "" && fid != "" {
			u, _, err := api.GetDownloadURL(cl, fid)
			if err != nil {
				return dlEnqueueMsg{err: err}
			}
			urlStr = u
		}
		return dlEnqueueMsg{jobs: []dlJob{{
			name: name, url: urlStr, dest: dest, fid: fid, replace: replace,
		}}, status: fmt.Sprintf("已加入：%s", name)}
	}
}

// beginQuit pauses active downloads then quits.
// Wait runs in a tea.Cmd (background), TUI keeps spinning until done.
func (m model) beginQuit() (tea.Model, tea.Cmd) {
	m.stopScan()
	if m.dlMgr == nil || m.dlMgr.ActiveCount() == 0 || m.quitPref() == quitBackground {
		return m, tea.Quit
	}
	m.quitting = true
	m.mode = modeModal
	m.modalKind = "info"
	m.modalTitle = "正在退出"
	m.modalBody = m.spinner.View() + " 正在安全暂停下载，请稍候…\n（等待当前分块写完，界面不会卡住）"
	m.modalBack = modeBrowse
	m.confirmAction = ""
	mgr := m.dlMgr
	return m, tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			// block only this cmd goroutine — Bubble Tea keeps drawing
			mgr.PauseAllWait()
			return pauseDoneMsg{}
		},
	)
}

// updateNote is what a just-installed update means for running downloads.
func (m model) updateNote() string {
	if m.cfg == nil {
		return ""
	}
	return dlsvc.UpdateNote(m.cfg.Path())
}

func (m model) fetchProfile() tea.Cmd {
	cl, cfg := m.client, m.cfg
	ver := m.appVersion
	return func() tea.Msg {
		info, err := api.UserInfo(cl)
		if err != nil {
			return profileMsg{err: err}
		}
		text := formatProfile(info, cfg, ver)
		// best-effort update line (network may fail silently)
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if res, err := update.Check(ctx, ver, update.RepoFromEnv()); err == nil && res != nil {
			text += "\n" + formatUpdateStatus(res)
		} else if err != nil {
			text += fmt.Sprintf("\n版本:     %s\n更新:     检查失败（%v）\n提示:     TUI 按 u · CLI: goquark update\n", ver, err)
		}
		return profileMsg{text: text}
	}
}

// logoutDoneMsg reports the server half of a logout.
type logoutDoneMsg struct {
	err    error
	paused int // downloads paused when the logout began
}

// doLogout pauses the downloads, which need the session, and ends the session
// on the server first, like the PC client; the local cleanup in finishLogout
// runs when that returns, success or not.
func (m model) doLogout() (tea.Model, tea.Cmd) {
	if m.loggingOut {
		return m, nil
	}
	m.loggingOut = true
	m.status = "正在退出登录…"
	paused := 0
	if m.dlMgr != nil {
		paused = m.dlMgr.PauseAll()
	}
	cl := m.client
	return m, func() tea.Msg { return logoutDoneMsg{err: api.Logout(cl), paused: paused} }
}

func (m model) finishLogout(serverErr error, paused int) (tea.Model, tea.Cmd) {
	m.loggingOut = false
	m.stopScan()
	m.cfg.ClearSession()
	_ = m.cfg.Save()
	if m.dlMgr != nil {
		m.dlMgr.ReloadSession()
	}
	m.mode = modeLogin
	m.client = client.New(m.cfg)
	m.rows = nil
	m.selected = map[int]bool{}
	m.status = "已退出登录"
	m.loginStatus = "按 l 开始扫码登录"
	if serverErr != nil {
		m.loginStatus = "已退出登录，但服务端未确认（会话可能仍有效）· 按 l 扫码登录"
	}
	if paused > 0 {
		m.loginStatus = fmt.Sprintf("已暂停 %d 个下载 · ", paused) + m.loginStatus
	}
	m.loginBusy = false
	m.dlMsg = ""
	return m, nil
}

func (m model) doGoUp() (tea.Model, tea.Cmd) {
	if len(m.stack) <= 1 {
		m.status = "已在根目录"
		return m, nil
	}
	m.stack = m.stack[:len(m.stack)-1]
	m.loading = true
	m.status = "返回上级…"
	m.selected = map[int]bool{}
	return m, m.loadCurrent()
}

func (m model) doActivate() (tea.Model, tea.Cmd) {
	if len(m.rows) == 0 {
		return m, nil
	}
	// more than one thing ticked → download them all, folders included
	if n := m.selectedCount(); n > 1 {
		return m.doDownload()
	}
	it := m.rows[m.cursor]
	if it.entry.Name == ".." {
		return m.doGoUp()
	}
	if it.entry.Dir {
		m.stack = append(m.stack, navFrame{fid: it.entry.FID, path: it.path, name: it.entry.Name})
		m.loading = true
		m.status = "打开 " + it.entry.Name
		m.selected = map[int]bool{}
		return m, m.loadCurrent()
	}
	return m.doDownload()
}

// selectedCount counts the ticked rows that can be downloaded, folders
// included. The parent row is never one of them.
func (m model) selectedCount() int {
	n := 0
	for i, on := range m.selected {
		if !on || i < 0 || i >= len(m.rows) {
			continue
		}
		if m.rows[i].entry.Name != ".." {
			n++
		}
	}
	return n
}

// selectedEntries returns what the user picked: the ticked rows, or the row
// under the cursor when nothing is ticked. Folders are included — expanding
// them is the download planner's job, not this function's.
func (m model) selectedEntries() []api.FileEntry {
	var out []api.FileEntry
	// in row order, not map order: this is the order the files queue in
	for i := range m.rows {
		if !m.selected[i] {
			continue
		}
		if r := m.rows[i]; r.entry.Name != ".." {
			out = append(out, r.entry)
		}
	}
	if len(out) > 0 {
		return out
	}
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		if r := m.rows[m.cursor]; r.entry.Name != ".." {
			out = append(out, r.entry)
		}
	}
	return out
}

// stopScan cancels a running folder expansion and makes any result still in
// flight stale.
func (m *model) stopScan() {
	if m.scan.cancel != nil {
		m.scan.cancel()
	}
	m.scan.busy = false
	m.scan.cancel = nil
}

// doDownload expands the selection off the UI goroutine — a folder costs one
// directory listing per page, per level — and brings the result back as a
// dlEnqueueMsg.
func (m model) doDownload() (tea.Model, tea.Cmd) {
	if m.scan.busy {
		return m, nil
	}
	sel := m.selectedEntries()
	if len(sel) == 0 {
		m.status = "请选择文件或文件夹再下载"
		m.dlMsg = m.status
		return m, nil
	}
	// always refresh dest dir from config (user may have changed download_dir)
	destDir := m.cfg.EffectiveDownloadDir()
	if m.dlMgr == nil {
		m.dlMgr = dlsvc.Open(m.cfg.Path())
	}
	cl := m.client
	// Jobs carry a FID and no link; the download service fetches one with its
	// own session when a job reaches the front of the queue.
	m.dlMgr.SetDestDir(destDir)
	mgr := m.dlMgr
	ctx, cancel := context.WithCancel(context.Background())
	m.scan.busy = true
	m.scan.gen++
	m.scan.cancel = cancel
	gen := m.scan.gen
	return m, func() tea.Msg {
		msg := planDownloads(ctx, cl, mgr, destDir, sel)
		msg.scanGen = gen
		return msg
	}
}

// destUnavailable reports why dest cannot be written, or nil when it can.
// Anything that is not a plain file is refused rather than opened: following a
// symlink here would write wherever it points, and the name it was built from
// came from the drive.
func destUnavailable(mgr download.Controller, dest, fid string) error {
	if busy := mgr.BusyAtDest(dest); busy != nil && busy.FID != fid {
		return fmt.Errorf("目标路径已有其他文件的下载任务：%s", dest)
	}
	info, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("检查目标文件 %s：%w", dest, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("目标路径不是普通文件，拒绝写入：%s", dest)
	}
	return nil
}

// planDownloads expands the selection and then decides, file by file, whether
// to queue it, skip it, or put the choice to the user. It runs on a tea.Cmd
// goroutine, so it touches no model state.
func planDownloads(ctx context.Context, cl *client.Client, mgr download.Controller, destDir string, sel []api.FileEntry) dlEnqueueMsg {
	tree, err := download.Expand(ctx, sel, func(fid string, page, size int) ([]api.FileEntry, error) {
		return api.ListDir(cl, fid, page, size)
	})
	if err != nil {
		return dlEnqueueMsg{err: err}
	}
	// Folders are made now, not when the first file in them starts: an empty
	// folder has no file to trigger it and would otherwise be lost.
	if err := tree.MakeDirs(destDir); err != nil {
		return dlEnqueueMsg{err: err}
	}
	// drop done records whose files are gone (before dedupe checks)
	mgr.ClearStaleDone()

	var jobs []dlJob
	var conflicts []dlConflict
	var focusID string
	var notes []string
	for _, item := range tree.Files() {
		if err := ctx.Err(); err != nil {
			return dlEnqueueMsg{err: err}
		}
		fid := item.Entry.FID
		// Name is the path inside the download dir, so the download center can
		// tell apart two same-named files from different folders.
		name := item.Rel
		dest := filepath.Join(destDir, item.Rel)

		if err := destUnavailable(mgr, dest, fid); err != nil {
			return dlEnqueueMsg{err: err}
		}

		// Dedup against this exact path. One cloud file can legitimately be
		// wanted at two paths in one recursive download, so matching on the
		// directory would drop all but the first.
		if existing := mgr.FindAtDest(fid, dest); existing != nil {
			// Done but local file missing → drop record and re-download
			if existing.Status == download.StatusDone {
				if _, err := os.Stat(existing.Dest); err != nil {
					mgr.RemoveID(existing.ID)
					// fall through to new download
				} else if existing.Total > 0 && fileSize(existing.Dest) == existing.Total {
					focusID = existing.ID
					notes = append(notes, fmt.Sprintf("「%s」已经下载完成", name))
					continue
				} else if existing.Total > 0 && fileSize(existing.Dest) != existing.Total {
					// local modified → replace/rename prompt
					urlStr, _, uerr := api.GetDownloadURL(cl, fid)
					if uerr != nil {
						return dlEnqueueMsg{err: fmt.Errorf("%s: %w", name, uerr)}
					}
					conflicts = append(conflicts, dlConflict{
						name: name, url: urlStr, dest: existing.Dest, fid: fid,
						size: fileSize(existing.Dest),
						reason: fmt.Sprintf("本地文件大小 %s 与网盘 %s 不一致（可能被修改）",
							humanSize(fileSize(existing.Dest)), humanSize(existing.Total)),
					})
					continue
				} else {
					// total unknown but file exists
					focusID = existing.ID
					notes = append(notes, fmt.Sprintf("「%s」已经下载完成", name))
					continue
				}
			} else {
				switch existing.Status {
				case download.StatusRunning, download.StatusQueued, download.StatusFinalizing, download.StatusPausing:
					focusID = existing.ID
					notes = append(notes, fmt.Sprintf("「%s」已在下载中", name))
					continue
				case download.StatusPaused, download.StatusError:
					focusID = existing.ID
					// resume this one only
					if mgr.ResumeID(existing.ID) {
						notes = append(notes, fmt.Sprintf("「%s」已恢复下载", name))
					} else {
						notes = append(notes, fmt.Sprintf("「%s」已在任务列表", name))
					}
					continue
				case download.StatusCancel:
					// remove cancel record and allow re-download
					mgr.RemoveID(existing.ID)
				}
			}
		}

		// dest file exists without matching active task in this dir?
		if fi, err := os.Stat(dest); err == nil {
			urlStr, size, uerr := api.GetDownloadURL(cl, fid)
			if uerr != nil {
				return dlEnqueueMsg{err: fmt.Errorf("%s: %w", name, uerr)}
			}
			if size > 0 && fi.Size() == size {
				notes = append(notes, fmt.Sprintf("「%s」本地已存在且大小一致", name))
				continue
			}
			conflicts = append(conflicts, dlConflict{
				name: name, url: urlStr, dest: dest, fid: fid, size: fi.Size(),
				reason: "本地已存在同名文件",
			})
			continue
		}

		// No link yet: it is fetched once the job takes a slot.
		jobs = append(jobs, dlJob{name: name, dest: dest, fid: fid, size: item.Entry.Size})
	}

	status := ""
	if len(notes) > 0 {
		status = strings.Join(notes, "；")
	}
	if len(jobs) == 0 && len(conflicts) == 0 {
		if status == "" {
			status = "没有新的下载任务"
			if n := tree.Dirs(); n > 0 {
				status = fmt.Sprintf("已创建 %d 个文件夹，没有需要下载的文件", n)
			}
		}
		return dlEnqueueMsg{focusID: focusID, status: status}
	}
	return dlEnqueueMsg{jobs: jobs, conflicts: conflicts, focusID: focusID, status: status}
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (m model) rowAt(y int) (int, bool) {
	if y < m.listTop {
		return 0, false
	}
	// two screen lines per row
	rel := y - m.listTop
	idx := m.offset + rel/rowHeight
	if idx < 0 || idx >= len(m.rows) {
		return 0, false
	}
	// only hit within visible window
	if idx >= m.offset+m.listH {
		return 0, false
	}
	return idx, true
}

func (m *model) ensureVisible() {
	if m.listH <= 0 {
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.listH {
		m.offset = m.cursor - m.listH + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m model) View() string {
	if !m.ready {
		return "启动中…"
	}
	switch m.mode {
	case modeLogin:
		return m.viewLogin()
	case modeProfile:
		return m.viewProfile()
	case modeDownloads:
		return m.viewDownloads()
	case modeModal:
		base := m.viewBrowse()
		if m.modalBack == modeDownloads {
			base = m.viewDownloads()
		}
		return m.viewWithOverlay(base, m.renderModal())
	case modeMenu:
		base := m.viewBrowse()
		if m.menuFrom == modeDownloads {
			base = m.viewDownloads()
		}
		return m.viewWithOverlay(base, m.renderMenu())
	default:
		return m.viewBrowse()
	}
}

func (m model) viewBrowse() string {
	var b strings.Builder
	cur := m.current()
	head := titleStyle.Render("GoQuark") + "  " + pathStyle.Render(cur.path)
	if m.loading {
		head += "  " + m.spinner.View() + " " + m.status
	}
	b.WriteString(head)
	b.WriteString("\n")

	// list (2 lines per item)
	end := min(len(m.rows), m.offset+m.listH)
	linesUsed := 0
	for i := m.offset; i < end; i++ {
		b.WriteString(m.renderRow(i))
		linesUsed += rowHeight
	}
	need := m.listH * rowHeight
	for linesUsed < need {
		b.WriteByte('\n')
		linesUsed++
	}

	// status: selection + item count / download / loading
	nItems := countItems(m.rows)
	foot := fmt.Sprintf("%d 项", nItems)
	if m.dlMsg != "" {
		foot = m.dlMsg
	}
	if m.loading && m.status != "" {
		foot = m.spinner.View() + " " + m.status
	}
	if m.scan.busy {
		foot = m.spinner.View() + " 正在展开文件夹，准备下载…"
	}
	if selN := len(m.selected); selN > 0 {
		foot = fmt.Sprintf("已选 %d  ·  %s", selN, foot)
	}
	b.WriteString(statusStyle.Width(max(1, m.width)).Render(truncate(foot, m.width-2)))
	b.WriteString("\n")
	// as many shortcuts as fit, then "?全部快捷键" for the rest
	b.WriteString(helpStyle.Render(hintLine(browseHints, max(20, m.width))))
	return b.String()
}

func (m model) viewDownloads() string {
	var b strings.Builder
	head := titleStyle.Render("GoQuark 下载中心")
	for _, c := range m.dlPath {
		head += pathStyle.Render(" › ") + nameDir.Render(c.name)
	}
	if m.dlMgr != nil {
		if live, queued, limit := m.dlMgr.LiveCount(); live+queued > 0 {
			head += "  " + m.spinner.View() + " " + fmt.Sprintf("同时下载 %d/%d · 排队 %d", live, limit, queued)
		}
	}
	// one line each, however narrow the terminal: the rows below are counted
	b.WriteString(xansi.Truncate(head, max(1, m.width), "…"))
	b.WriteString("\n")

	// show download root path (always), or the folder that is open
	dlRoot := m.cfg.EffectiveDownloadDir()
	if m.dlMgr != nil && m.dlMgr.DestDir() != "" {
		dlRoot = m.dlMgr.DestDir()
	}
	where := dlRoot
	if len(m.dlPath) > 0 {
		where = m.dlPath[len(m.dlPath)-1].key
	}
	b.WriteString(pathStyle.Render(truncate("保存到: "+where, max(20, m.width))))
	b.WriteString("\n")

	rows := m.dlRows()
	if len(rows) == 0 {
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("暂无下载任务。在网盘选中文件或文件夹后按 d 开始下载。"))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("可在 ~/.config/goquark/config.json 修改 download_dir"))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("esc/b 返回网盘 · t 打开本页"))
		return b.String()
	}

	// Only the window that fits is drawn, padded to its full height so the
	// footer stays put. Drawing every row let the terminal cut the overflow
	// off the top, and the cursor went with it.
	h := m.dlListH()
	cursor, offset := clampWindow(m.dlCursor, m.dlOffset, len(rows), h)
	shown := 0
	for i := offset; i < len(rows) && shown < h; i++ {
		b.WriteString(m.renderDownloadRow(rows[i], i == cursor, dlRoot))
		b.WriteString("\n")
		shown++
	}
	for ; shown < h; shown++ {
		b.WriteString(strings.Repeat("\n", dlRowLines))
	}

	// section summary over every file in this level
	runningN, queuedN, pausedN, errN, doneN, files := 0, 0, 0, 0, 0, 0
	for _, r := range rows {
		for _, s := range r.tasks {
			files++
			switch s.Status {
			case download.StatusRunning, download.StatusFinalizing:
				runningN++
			case download.StatusQueued:
				queuedN++
			case download.StatusError:
				errN++
			case download.StatusDone, download.StatusCancel:
				doneN++
			default:
				pausedN++
			}
		}
	}
	b.WriteString("\n")
	summary := fmt.Sprintf("下载中 %d · 失败 %d · 排队 %d · 已暂停 %d · 已完成 %d · 共 %d 个文件 · 第 %d/%d 项",
		runningN, errN, queuedN, pausedN, doneN, files, cursor+1, len(rows))
	b.WriteString(statusStyle.Width(max(1, m.width)).Render(truncate(summary, m.width-2)))
	b.WriteString("\n")
	back := keyHint{"esc/b", "返回网盘"}
	if len(m.dlPath) > 0 {
		back.desc = "返回上一层"
	}
	hints := append([]keyHint{downloadHints[0], back}, downloadHints[1:]...)
	b.WriteString(helpStyle.Render(hintLine(hints, max(20, m.width))))
	return b.String()
}

// progressBar renders npm-like bar: filled solid blocks + dotted empty cells.
// filled color depends on task status. bg is optional highlight background.
func progressBar(filled, total int, st download.Status, bg string) string {
	if total < 1 {
		total = 1
	}
	if filled < 0 {
		filled = 0
	}
	if filled > total {
		filled = total
	}
	// empty: dense dotted block (npm-style remaining track)
	emptyCh := "⣿"
	fillCh := "█"
	var fillFG string
	switch st {
	case download.StatusDone:
		fillFG = "42" // green
	case download.StatusPaused, download.StatusPausing:
		fillFG = "214" // orange
	case download.StatusError:
		fillFG = "196" // red
	case download.StatusCancel:
		fillFG = "244" // gray
	default: // running / queued
		fillFG = "39" // blue
	}
	fillStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(fillFG))
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	if bg != "" {
		fillStyle = fillStyle.Background(lipgloss.Color(bg))
		emptyStyle = emptyStyle.Background(lipgloss.Color(bg))
	}
	var b strings.Builder
	if filled > 0 {
		b.WriteString(fillStyle.Render(strings.Repeat(fillCh, filled)))
	}
	if total-filled > 0 {
		b.WriteString(emptyStyle.Render(strings.Repeat(emptyCh, total-filled)))
	}
	return b.String()
}

// withBG applies optional background without clearing an existing foreground.
func withBG(st lipgloss.Style, bg string) lipgloss.Style {
	if bg == "" {
		return st
	}
	return st.Background(lipgloss.Color(bg))
}

// renderDownloadItem builds one task block with consistent left indent whether
// focused or not. When focused, EVERY cell (including text) gets highlight bg;
// dim/gray attribute text becomes white (same rule as file browser).
func (m model) renderDownloadItem(s download.Snapshot, focused bool, dlRoot string) string {
	w := max(24, m.width)
	const gutterCells = 3 // "┃  " or "   "
	hlBG := ""
	if focused {
		hlBG = "237" // same as rowCursor background
	}

	barW := 24
	if w > 60 {
		barW = min(40, w/3)
	}
	filled := 0
	if s.Total > 0 {
		filled = int(float64(barW) * s.Percent / 100)
		if filled > barW {
			filled = barW
		}
		if s.Status == download.StatusDone && filled < barW {
			filled = barW
		}
	}
	bar := progressBar(filled, barW, s.Status, hlBG)

	stLabel, stFG := dlStatusLabel(s.Status)
	stStyle := withBG(lipgloss.NewStyle().Foreground(lipgloss.Color(stFG)).Bold(true), hlBG)

	// filename keeps type color (green file / blue when done)
	nameSt := nameFile
	if s.Status == download.StatusDone {
		nameSt = nameDir
	}
	nameSt = withBG(nameSt, hlBG)
	// spacer between status and name must also carry bg
	sp := withBG(lipgloss.NewStyle(), hlBG).Render("  ")
	// finalizing / pausing: spinner so UI never looks frozen
	prefix := stStyle.Render(stLabel)
	if s.Status == download.StatusFinalizing || s.Status == download.StatusPausing {
		prefix = stStyle.Render(m.spinner.View() + " " + stLabel)
	}
	nameLine := prefix + sp + nameSt.Render(s.Name)

	speed := ""
	if s.Speed > 0 {
		speed = fmt.Sprintf("  %.1f MiB/s", s.Speed/1024/1024)
	}
	pct := ""
	if s.Total > 0 {
		pct = fmt.Sprintf("  %.1f%%", s.Percent)
	} else if s.Status == download.StatusRunning {
		pct = "  …"
	}
	size := ""
	if s.Total > 0 {
		size = fmt.Sprintf("  %s / %s", humanSize(s.Done), humanSize(s.Total))
	}
	// elapsed + ETA
	timeInfo := ""
	if s.Elapsed > 0 || s.Status == download.StatusRunning || s.Status == download.StatusDone {
		el := download.FormatDuration(s.Elapsed)
		switch s.Status {
		case download.StatusRunning, download.StatusQueued:
			if s.HasETA {
				timeInfo = fmt.Sprintf("  已用 %s · 剩余 %s", el, download.FormatDuration(s.ETA))
			} else if s.Elapsed > 0 {
				timeInfo = fmt.Sprintf("  已用 %s", el)
			}
		case download.StatusFinalizing:
			if s.Elapsed > 0 {
				timeInfo = fmt.Sprintf("  已用 %s · 正在写入收尾…", el)
			} else {
				timeInfo = "  正在写入收尾…"
			}
		case download.StatusPausing:
			if s.Elapsed > 0 {
				timeInfo = fmt.Sprintf("  已用 %s · 正在安全暂停…", el)
			} else {
				timeInfo = "  正在安全暂停…"
			}
		case download.StatusPaused, download.StatusError:
			if s.Elapsed > 0 {
				timeInfo = fmt.Sprintf("  已用 %s", el)
			}
		case download.StatusDone:
			if s.Elapsed > 0 {
				timeInfo = fmt.Sprintf("  用时 %s", el)
			}
		}
	}

	// meta (percent/speed/size/time): dim gray normally → white when focused
	metaStyle := descDim
	if focused {
		metaStyle = descOnSel
	}
	metaStyle = withBG(metaStyle, hlBG)
	meta := ""
	if pct != "" || speed != "" || size != "" || timeInfo != "" {
		meta = metaStyle.Render(pct + speed + size + timeInfo)
	}
	brStyle := withBG(lipgloss.NewStyle().Foreground(lipgloss.Color("252")), hlBG)
	subLine := brStyle.Render("[") + bar + brStyle.Render("]") + meta
	if s.Error != "" && s.Status != download.StatusDone {
		errSt := withBG(lipgloss.NewStyle().Foreground(lipgloss.Color("196")), hlBG)
		subLine = errSt.Render(truncate(s.Error, max(16, w-gutterCells-2)))
	}

	pathLine := s.Dest
	if pathLine == "" {
		pathLine = filepath.Join(dlRoot, s.Name)
	}
	pathLine = truncate(pathLine, max(16, w-gutterCells-2))
	// path is gray attribute → white under highlight
	pathSt := pathStyle
	if focused {
		pathSt = descOnSel
	}
	pathSt = withBG(pathSt, hlBG)
	pathColored := pathSt.Render(pathLine)

	return dlBlock([]string{nameLine, subLine, pathColored}, focused, w)
}

func (m model) renderRow(i int) string {
	r := m.rows[i]
	w := m.width
	if w < 24 {
		w = 24
	}

	focused := i == m.cursor
	checked := m.selected[i]
	highlighted := focused || checked

	// Fixed columns — title and subtitle share the same name-column edge:
	//   [mark 2][icon 2][sp 1][name...]
	//   [      indent = gutterW      ][desc...]
	// Left chrome (border+pad) is applied to BOTH lines the same way.
	mark := "  "
	if checked {
		mark = "✓ "
	}
	for lipgloss.Width(mark) < 2 {
		mark += " "
	}
	if lipgloss.Width(mark) > 2 {
		mark = "✓ "
	}

	icon := "📄"
	desc := humanSize(r.entry.Size)
	isDir := r.entry.Dir
	if isDir {
		icon = "📁"
		if r.entry.Name == ".." {
			desc = "返回上级"
		} else {
			desc = "文件夹"
		}
	}

	gutter := mark + icon + " "
	gutterW := lipgloss.Width(gutter)
	nameBudget := w - gutterW - 4
	if nameBudget < 8 {
		nameBudget = 8
	}
	name := truncateCells(r.entry.Name, nameBudget)

	// Color name by type (always keep blue/green).
	// Desc: dim gray normally; white when row is highlighted.
	var nameSt, descSt lipgloss.Style
	if isDir {
		nameSt = nameDir
	} else {
		nameSt = nameFile
	}
	if highlighted {
		descSt = descOnSel
	} else {
		descSt = descDim
	}

	title := nameSt.Render(gutter + name)
	// subtitle indented to the name column (same gutterW as title's name start)
	sub := descSt.Render(strings.Repeat(" ", gutterW) + desc)
	content := title + "\n" + sub

	switch {
	case focused && checked:
		return rowBoth.Width(w).Render(content) + "\n"
	case focused:
		return rowCursor.Width(w).Render(content) + "\n"
	case checked:
		return rowChecked.Width(w).Render(content) + "\n"
	}

	// Unfocused: same left indent as bordered rows (PaddingLeft 3).
	// Apply pad to EACH line so subtitle stays aligned with the name column.
	pad := "   "
	block := pad + title + "\n" + pad + sub
	return lipgloss.NewStyle().Width(w).Render(block) + "\n"
}

func (m model) renderMenu() string {
	// Fixed width = max of (prefix+label) + 1 trailing space for all items.
	// The trailing space is always present and turns blue with the selection
	// highlight so the bar never ends abruptly on the last character.
	maxW := 0
	for _, it := range m.menuItems {
		for _, prefix := range []string{"  ", "› "} {
			if ww := lipgloss.Width(prefix + it.label); ww > maxW {
				maxW = ww
			}
		}
	}
	// reserve the trailing pad space in the fixed width
	maxW++
	if maxW < 12 {
		maxW = 12
	}

	var lines []string
	for i, it := range m.menuItems {
		var text string
		if i == m.menuIdx {
			text = "› " + it.label
		} else {
			text = "  " + it.label
		}
		// pad to fixed width (includes the reserved trailing space)
		for lipgloss.Width(text) < maxW {
			text += " "
		}
		if i == m.menuIdx {
			// entire padded text (incl. trailing space) gets blue background
			lines = append(lines, menuSelSt.Render(text))
		} else {
			lines = append(lines, menuItemSt.Render(text))
		}
	}
	return menuBox.Render(strings.Join(lines, "\n"))
}

func (m model) viewLogin() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("GoQuark 登录"))
	b.WriteString("\n")
	var tabs []string
	for _, meth := range []auth.Method{auth.MethodQuark, auth.MethodWeChat} {
		if meth == m.loginMethod {
			tabs = append(tabs, menuSelSt.Render(" "+meth.Label()+" "))
		} else {
			tabs = append(tabs, helpStyle.Render(" "+meth.Label()+" "))
		}
	}
	b.WriteString(strings.Join(tabs, " ") + "\n")

	// status line
	status := m.loginStatus
	if status == "" {
		status = "按 l 开始扫码登录"
	}
	if m.loginBusy || m.loginQRArt != "" {
		b.WriteString(m.spinner.View() + " " + status)
	} else {
		b.WriteString(status)
	}
	b.WriteString("\n\n")

	// QR directly in TUI (human only — no agent URL dump)
	if m.loginQRArt != "" {
		// center-ish: leave left padding if wide terminal
		art := strings.TrimRight(m.loginQRArt, "\n")
		b.WriteString(art)
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render(m.loginMethod.ScanHint()))
		b.WriteString("\n")
	} else if m.loginBusy {
		b.WriteString(helpStyle.Render("正在生成二维码…"))
		b.WriteString("\n")
	} else {
		b.WriteString(panelStyle.Width(min(m.width-2, 48)).Render("按 l 获取登录二维码"))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("l 刷新二维码 · Tab 切换夸克/微信 · q 退出"))
	return b.String()
}

func (m model) viewProfile() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("个人信息"))
	b.WriteString("\n\n")
	b.WriteString(panelStyle.Width(min(m.width-2, 72)).Render(m.profileText))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("u 检查更新 · Esc / Enter / 单击 返回"))
	return b.String()
}

// Release-notes block inside the update dialog. Children of confirmBox must
// set both foreground and background, or terminals that do not inherit the
// box background paint them on the default one.
var (
	notesHeadSt = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("45")).
			Background(lipgloss.Color("235"))
	notesBodySt = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Background(lipgloss.Color("235"))
	notesToggleSt = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("235")).
			Background(lipgloss.Color("45"))
	notesHintSt = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Background(lipgloss.Color("235"))
)

const (
	// updateNotesWidth matches the confirm box's inner width.
	updateNotesWidth = 44
	// updateNotesCollapsed is how many lines of notes show before the reader
	// asks for the rest.
	updateNotesCollapsed = 6
)

// updateNotesLines wraps the release notes to the dialog width. The text has
// already been stripped of escape sequences by the update package.
func updateNotesLines(notes string) []string {
	if strings.TrimSpace(notes) == "" {
		return nil
	}
	wrapped := lipgloss.NewStyle().Width(updateNotesWidth).Render(notes)
	return strings.Split(strings.TrimRight(wrapped, "\n"), "\n")
}

// updateNotesMaxLines is how many note lines the expanded dialog may use
// before it would run off a short terminal.
func (m model) updateNotesMaxLines() int {
	// title, version lines, blanks, heading, toggle, buttons, hint, borders
	const chrome = 16
	if avail := m.height - chrome; avail > updateNotesCollapsed {
		return avail
	}
	return updateNotesCollapsed
}

// notesToggleLabel is both the rendered button text and what notesToggleHit
// looks for in the rendered modal, so the two cannot drift apart.
func (m model) notesToggleLabel(total int) string {
	if m.notesExpanded {
		return "▴ d 收起说明"
	}
	return fmt.Sprintf("▾ d 展开完整说明（共 %d 行）", total)
}

// updateConfirmBody renders the "new version" dialog: the version line, then
// the release notes, collapsed until the reader expands them.
func (m model) updateConfirmBody() string {
	res := m.updateRes
	if res == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "发现新版本 %s（当前 %s）\n", res.Latest, res.Current)
	if res.CanApply {
		b.WriteString("点击「立即更新」即可安装。")
	} else {
		b.WriteString("无法自动更新（自编译或无对应平台包），请到发布页手动下载。")
		if res.ReleaseURL != "" {
			b.WriteString("\n" + res.ReleaseURL)
		}
	}

	lines := updateNotesLines(res.Notes)
	if len(lines) == 0 {
		return b.String()
	}

	shown := lines
	clipped := false
	if m.notesExpanded {
		if maxLines := m.updateNotesMaxLines(); len(lines) > maxLines {
			shown, clipped = lines[:maxLines], true
		}
	} else if len(lines) > updateNotesCollapsed {
		shown = lines[:updateNotesCollapsed]
	}

	b.WriteString("\n\n" + notesHeadSt.Render("更新说明") + "\n")
	b.WriteString(notesBodySt.Render(strings.Join(shown, "\n")))
	if len(lines) > updateNotesCollapsed {
		b.WriteString("\n" + notesToggleSt.Render(" "+m.notesToggleLabel(len(lines))+" "))
	}
	if clipped {
		b.WriteString("\n" + notesHintSt.Render("终端高度有限，完整说明见发布页"))
	}
	return b.String()
}

// toggleUpdateNotes flips the notes between collapsed and expanded and
// rebuilds the dialog body, which renderModal reads.
func (m model) toggleUpdateNotes() model {
	if m.updateRes == nil || len(updateNotesLines(m.updateRes.Notes)) <= updateNotesCollapsed {
		return m
	}
	m.notesExpanded = !m.notesExpanded
	m.modalBody = m.updateConfirmBody()
	return m
}

// notesToggleHit reports whether (x, y) lands on the expand/collapse button.
func (m model) notesToggleHit(x, y int) bool {
	if m.modalKind != "confirm" || m.confirmAction != "update_available" || m.updateRes == nil {
		return false
	}
	lines := updateNotesLines(m.updateRes.Notes)
	if len(lines) <= updateNotesCollapsed {
		return false
	}
	label := m.notesToggleLabel(len(lines))
	ox, oy, _, _ := m.overlayOrigin()
	for i, line := range strings.Split(m.renderModal(), "\n") {
		plain := xansi.Strip(line)
		j := strings.Index(plain, label)
		if j < 0 {
			continue
		}
		rowY := oy + i
		if y < rowY-1 || y > rowY+1 {
			continue
		}
		// one cell of slack each side for the padding spaces
		x0 := ox + xansi.StringWidth(plain[:j]) - 1
		x1 := x0 + xansi.StringWidth(label) + 2
		return x >= x0 && x <= x1
	}
	return false
}

// progressCancelHit reports whether (x, y) lands on the progress dialog's
// abort button.
func (m model) progressCancelHit(x, y int) bool {
	if m.modalKind != "progress" {
		return false
	}
	ox, oy, _, _ := m.overlayOrigin()
	w := lipgloss.Width(btnCancel.Render(progressCancelLabel))
	for i, line := range strings.Split(m.renderModal(), "\n") {
		plain := xansi.Strip(line)
		j := strings.Index(plain, progressCancelLabel)
		if j < 0 {
			continue
		}
		rowY := oy + i
		if y < rowY-1 || y > rowY+1 {
			continue
		}
		x0 := ox + xansi.StringWidth(plain[:j])
		return x >= x0-1 && x < x0+w
	}
	return false
}

func (m model) renderModal() string {
	switch m.modalKind {
	case "confirm":
		title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")).Render(m.modalTitle)
		body := m.modalBody

		okLabel, noLabel := m.confirmLabels()
		// highlight = mouse hover if any, else keyboard focus (default primary)
		hl := m.confirmHighlight()
		var ok, no string
		switch hl {
		case 2:
			ok = btnOK.Render(okLabel)
			no = btnCancelHover.Render(noLabel)
		default:
			ok = btnOKHover.Render(okLabel)
			no = btnCancel.Render(noLabel)
		}
		// gap uses modal background so no terminal-default strip between buttons
		btns := ok + btnGapSt + no
		innerW := 44
		pad := max(0, (innerW-lipgloss.Width(btns))/2)
		padSt := lipgloss.NewStyle().Background(lipgloss.Color("235")).Render(strings.Repeat(" ", pad))
		btnLine := padSt + btns
		rest := max(0, innerW-lipgloss.Width(btnLine))
		if rest > 0 {
			btnLine += lipgloss.NewStyle().Background(lipgloss.Color("235")).Render(strings.Repeat(" ", rest))
		}
		hint := "←/→ 切换 · Enter 当前高亮 · Esc 默认按钮 · 可点击"
		if m.confirmAction == "conflict_replace" {
			hint = "←/→ 切换 · Enter 当前高亮 · Esc 替换 · 可点击"
		}
		if m.confirmAction == "quit_choice" {
			hint = "←/→ 切换 · Enter 确认 · 空格 勾选 · Esc 取消 · 可点击"
		}
		if m.confirmAction == "update_available" && m.updateRes != nil &&
			len(updateNotesLines(m.updateRes.Notes)) > updateNotesCollapsed {
			hint = "←/→ 切换 · d 展开/收起说明 · Enter 确认 · 可点击"
		}
		// Footer must set BOTH fg+bg: nesting helpStyle inside confirmBox.Render
		// drops dim color on some terminals and "可点击" falls back to bright white.
		footerDim := lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Background(lipgloss.Color("235"))
		// Also dim the body a touch less for readability — body stays plain;
		// only footer is forced gray.
		content := title + "\n\n" + body + "\n\n" + btnLine + "\n\n" +
			footerDim.Render(hint)
		// Render box WITHOUT recoloring children: unset foreground so pre-styled
		// footer ANSI is preserved.
		box := confirmBox.Copy().UnsetForeground().UnsetBold()
		return box.Render(content)
	case "progress":
		title := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("42")).
			Render(m.modalTitle)
		btn := btnCancel.Render(progressCancelLabel)
		if m.btnHover == 2 {
			btn = btnCancelHover.Render(progressCancelLabel)
		}
		innerW := 44
		bg := lipgloss.NewStyle().Background(lipgloss.Color("235"))
		pad := max(0, (innerW-lipgloss.Width(btn))/2)
		btnLine := bg.Render(strings.Repeat(" ", pad)) + btn
		if rest := max(0, innerW-lipgloss.Width(btnLine)); rest > 0 {
			btnLine += bg.Render(strings.Repeat(" ", rest))
		}
		footerDim := lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Background(lipgloss.Color("235"))
		// The hint deliberately avoids repeating the button label: the hit
		// test finds the button by that text.
		content := title + "\n\n" + m.modalBody + "\n\n" + btnLine + "\n\n" +
			footerDim.Render("Esc 或点击上方按钮中止 · 其他操作不影响下载")
		return confirmBox.Copy().UnsetForeground().UnsetBold().Render(content)
	case "help":
		footerDim := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Background(lipgloss.Color("235"))
		title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45")).Render(m.modalTitle)
		body := title + "\n\n" + m.modalBody + "\n\n" + footerDim.Render("按快捷键直接执行 · ? / Esc / Enter 关闭")
		return okBox.Copy().UnsetForeground().Render(body)
	case "error":
		footerDim := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Background(lipgloss.Color("235"))
		body := modalTitle.Render(m.modalTitle) + "\n\n" + m.modalBody + "\n\n" + footerDim.Render("Enter / Esc 关闭 · 可点击")
		return modalBox.Copy().UnsetForeground().Render(body)
	default:
		// loading / quitting info modal — show spinner-friendly body as-is
		titleCol := "42"
		if m.quitting {
			titleCol = "214"
		}
		footerDim := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Background(lipgloss.Color("235"))
		body := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(titleCol)).Render(m.modalTitle) + "\n\n" + m.modalBody
		if !m.quitting {
			body += "\n\n" + footerDim.Render("Enter / Esc 关闭 · 可点击")
		}
		return okBox.Copy().UnsetForeground().Render(body)
	}
}

// viewWithOverlay composites a fully-styled overlay onto base WITHOUT blanking
// the left/right of the row. Uses charmbracelet/x/ansi.Truncate which is
// ANSI-aware and wide-char/emoji-aware (official mature solution).
func (m model) viewWithOverlay(base, overlay string) string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	baseLines := strings.Split(base, "\n")
	for len(baseLines) < h {
		baseLines = append(baseLines, "")
	}
	if len(baseLines) > h {
		baseLines = baseLines[:h]
	}

	ow, oh := lipgloss.Size(overlay)
	var ox, oy int
	if m.mode == modeModal {
		ox = max(0, (w-ow)/2)
		oy = max(0, (h-oh)/2)
	} else {
		ox, oy = m.menuX, m.menuY
		if ox+ow > w {
			ox = max(0, w-ow)
		}
		if oy+oh > h {
			oy = max(0, h-oh)
		}
		if ox < 0 {
			ox = 0
		}
		if oy < 0 {
			oy = 0
		}
	}
	// publish geometry for hit testing (value receiver View can't store;
	// we compute hit boxes from known layout in hit helpers using last known).
	// For mouse, Update runs after View so we store via pointer trick below
	// by returning geometry through a package-level last frame — avoided.
	// Instead hit helpers recompute from m.menuX/Y or center.

	ovLines := strings.Split(overlay, "\n")
	for i, ol := range ovLines {
		y := oy + i
		if y < 0 || y >= h {
			continue
		}
		baseLines[y] = spliceANSI(baseLines[y], ol, ox, w)
	}

	// Stash geometry on a copy is impossible in View; use mutable fields via
	// unsafe pattern: View is value receiver. We recompute in hit helpers.
	_ = oh
	return strings.Join(baseLines, "\n")
}

// spliceANSI inserts overlay at cell column `ox` into an ANSI-styled base line,
// preserving left and right styled content. Uses official x/ansi.Truncate.
//
// Algorithm (mature, same idea as bubbletea-overlay / lipgloss v2 composite):
//
//	left  = Truncate(base, ox, "")
//	right = cell-slice of base starting at ox+overlayWidth
//	out   = left + overlay + right + reset
func spliceANSI(base, overlay string, ox, termW int) string {
	if ox < 0 {
		ox = 0
	}
	olW := xansi.StringWidth(overlay)
	// left: first ox cells of base, ANSI-safe
	left := xansi.Truncate(base, ox, "")
	// pad left if base shorter than ox
	for xansi.StringWidth(left) < ox {
		left += " "
	}
	// right starts after covered region
	rightStart := ox + olW
	right := cutStyledFrom(base, rightStart)
	out := left + overlay + right
	// ensure we don't exceed terminal width in cells
	if termW > 0 && xansi.StringWidth(out) > termW {
		out = xansi.Truncate(out, termW, "")
	}
	// reset styles so following lines aren't polluted
	return out + "\x1b[0m"
}

// cutStyledFrom returns the suffix of s starting at visual cell `start`,
// keeping ANSI sequences intact. Implemented as: total - prefix.
// We walk with Truncate: prefix = Truncate(s, start, ""); then drop prefix
// bytes carefully by scanning with the same width rules.
func cutStyledFrom(s string, start int) string {
	if start <= 0 {
		return s
	}
	total := xansi.StringWidth(s)
	if start >= total {
		return ""
	}
	// Drop the first `start` cells while retaining later ANSI + text.
	// Approach: iterate graphemes/ANSI like Truncate, but collect only after start.
	return dropCells(s, start)
}

// dropCells drops the first n visual cells from an ANSI string (keeps sequences).
// Adapted from charmbracelet/x/ansi.Truncate parser loop.
func dropCells(s string, n int) string {
	if n <= 0 {
		return s
	}
	// Reuse Truncate idea inverted: build from full string by skipping first n cells.
	// Simple reliable method: binary search on byte index is hard; walk runes with lipgloss
	// after stripping is wrong. Use Truncate to get prefix, then find byte length of prefix
	// by growing until Truncate matches — expensive but fine for TUI lines.
	//
	// Better: walk with uniseg via xansi by using Hardwrap? Not available as Cut.
	// Practical approach used in production overlays:
	// prefix := Truncate(s, n, "")
	// if strings.HasPrefix(s, prefix) { return s[len(prefix):] } — FAILS because Truncate
	// may rewrite. So walk parser:
	return dropCellsWalk(s, n)
}

func dropCellsWalk(s string, n int) string {
	// Walk byte-by-byte keeping ANSI; count printable width with lipgloss/xansi via Truncate probes.
	// For each next printable cluster, if still dropping, skip cluster but keep ANSI codes.
	// Implement minimal state machine for CSI/OSC and UTF-8.
	b := []byte(s)
	var out strings.Builder
	out.Grow(len(s))
	cell := 0
	i := 0
	for i < len(b) {
		// ESC sequence
		if b[i] == 0x1b {
			// copy whole sequence to out always (styles must continue into suffix)
			j := i + 1
			if j < len(b) && b[j] == '[' { // CSI
				j++
				for j < len(b) {
					c := b[j]
					j++
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
				out.Write(b[i:j])
				i = j
				continue
			}
			if j < len(b) && b[j] == ']' { // OSC ... BEL or ST
				j++
				for j < len(b) {
					if b[j] == 0x07 {
						j++
						break
					}
					if b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
				out.Write(b[i:j])
				i = j
				continue
			}
			// other ESC: copy ESC + next byte
			if j < len(b) {
				j++
			}
			out.Write(b[i:j])
			i = j
			continue
		}
		// UTF-8 cluster: take one rune (good enough; emoji ZWJ may be 2 cells via Width)
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			// skip invalid
			i++
			continue
		}
		rw := xansi.StringWidth(string(r))
		if rw <= 0 {
			rw = 1
		}
		if cell+rw <= n {
			// still in dropped region
			cell += rw
			i += size
			continue
		}
		// keep this rune and the rest of the string as-is (including following bytes)
		out.Write(b[i:])
		return out.String()
	}
	return out.String()
}

func (m model) overlayOrigin() (ox, oy, ow, oh int) {
	// Recompute overlay position the same way as viewWithOverlay.
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	var overlay string
	switch m.mode {
	case modeModal:
		overlay = m.renderModal()
	case modeMenu:
		overlay = m.renderMenu()
	default:
		return 0, 0, 0, 0
	}
	ow, oh = lipgloss.Size(overlay)
	if m.mode == modeModal {
		ox = max(0, (w-ow)/2)
		oy = max(0, (h-oh)/2)
	} else {
		ox, oy = m.menuX, m.menuY
		if ox+ow > w {
			ox = max(0, w-ow)
		}
		if oy+oh > h {
			oy = max(0, h-oh)
		}
	}
	return ox, oy, ow, oh
}

func (m model) hitOverlay(x, y int) bool {
	ox, oy, ow, oh := m.overlayOrigin()
	return x >= ox && x < ox+ow && y >= oy && y < oy+oh
}

func (m model) confirmButtonHit(x, y int) (ok, cancel bool) {
	if m.modalKind != "confirm" {
		return false, false
	}
	ox, oy, ow, oh := m.overlayOrigin()
	// Button row is near the bottom of the modal content.
	// renderModal structure:
	// border top
	// pad
	// title
	// blank
	// body lines
	// blank
	// buttons
	// blank
	// help
	// pad
	// border bottom
	// Easier: scan rendered overlay lines for button styles positions.
	ov := m.renderModal()
	lines := strings.Split(ov, "\n")
	okLabel, noLabel := m.confirmLabels()
	// Widths must match rendered buttons (hover and normal styles share padding).
	okW := lipgloss.Width(btnOK.Render(okLabel))
	noW := lipgloss.Width(btnCancel.Render(noLabel))
	for i, line := range lines {
		plain := xansi.Strip(line)
		// Prefer exact label match; labels are unique on the button row.
		idxOK := strings.Index(plain, okLabel)
		if idxOK < 0 {
			continue
		}
		btnY := oy + i
		if y != btnY {
			// also allow 1-row slack for terminals that report slightly off
			if y < btnY-1 || y > btnY+1 {
				continue
			}
		}
		okX0 := ox + lipgloss.Width(plain[:idxOK])
		okX1 := okX0 + okW
		noX0 := okX1 + lipgloss.Width(btnGapSt)
		if j := strings.Index(plain, noLabel); j >= 0 {
			noX0 = ox + lipgloss.Width(plain[:j])
		}
		noX1 := noX0 + noW
		if x >= okX0 && x < okX1 {
			return true, false
		}
		if x >= noX0 && x < noX1 {
			return false, true
		}
		_, _ = ow, oh
	}
	return false, false
}

func (m model) hitOK(x, y int) bool {
	ok, _ := m.confirmButtonHit(x, y)
	return ok
}

func (m model) hitCancel(x, y int) bool {
	_, c := m.confirmButtonHit(x, y)
	return c
}

// truncateCells truncates plain text to at most n terminal cells (emoji-aware via lipgloss).
func truncateCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	// leave room for ellipsis
	budget := n - 1
	if budget < 1 {
		budget = n
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > budget {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

func formatProfile(info map[string]any, cfg *config.Config, version string) string {
	nick := strAny(info["nickname"])
	if nick == "" {
		nick = strAny(info["name"])
	}
	uid := strAny(info["uid"])
	if uid == "" {
		uid = strAny(info["uidu"])
	}
	member := strAny(info["member_type"])
	useCap := i64Any(info["use_capacity"])
	totalCap := i64Any(info["total_capacity"])
	var b strings.Builder
	fmt.Fprintf(&b, "昵称:     %s\n", emptyDash(nick))
	fmt.Fprintf(&b, "UID:      %s\n", emptyDash(uid))
	fmt.Fprintf(&b, "会员:     %s\n", emptyDash(member))
	if totalCap > 0 {
		fmt.Fprintf(&b, "容量:     %s / %s\n", humanSize(useCap), humanSize(totalCap))
	}
	fmt.Fprintf(&b, "\n设备名:   %s\n", cfg.Device.DeviceName)
	fmt.Fprintf(&b, "型号:     %s\n", cfg.Device.Model)
	fmt.Fprintf(&b, "登录方式: %s\n", emptyDash(cfg.Session.LoginWay))
	fmt.Fprintf(&b, "登录时间: %s\n", emptyDash(cfg.Session.LoggedInAt))
	if version != "" {
		fmt.Fprintf(&b, "\n当前版本: %s\n", version)
	}
	return b.String()
}

// formatUpdateStatus appends human-readable update info for profile / info pages.
func formatUpdateStatus(res *update.Result) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "最新版本: %s\n", res.Latest)
	if !res.UpdateAvailable {
		b.WriteString("更新:     已是最新\n")
		b.WriteString("提示:     TUI 按 u 检查 · CLI: goquark update\n")
		return b.String()
	}
	b.WriteString("更新:     发现新版本，可更新\n")
	if res.CanApply {
		b.WriteString("自动更新: 可用\n")
		b.WriteString("提示:     TUI 按 u 或点「立即更新」· CLI: goquark update --yes\n")
	} else {
		b.WriteString("自动更新: 不可用（自编译/无对应资产）\n")
		if res.ReleaseURL != "" {
			fmt.Fprintf(&b, "发布页:   %s\n", res.ReleaseURL)
		}
		b.WriteString("提示:     请手动下载 · CLI: goquark update\n")
	}
	return b.String()
}

func joinPath(base, name string) string {
	if base == "/" || base == "" {
		return "/" + name
	}
	return strings.TrimRight(base, "/") + "/" + name
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB"} {
		f /= 1024
		if f < 1024 {
			return fmt.Sprintf("%.1f %s", f, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", f/1024)
}

func strAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func i64Any(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	default:
		return 0
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncate(s string, w int) string {
	if w <= 0 {
		return s
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	if len(r) <= 1 {
		return s
	}
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func countItems(rows []row) int {
	n := 0
	for _, r := range rows {
		if r.entry.Name != ".." {
			n++
		}
	}
	return n
}
