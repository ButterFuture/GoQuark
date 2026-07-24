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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ButterFuture/GoQuark/internal/api"
	"github.com/ButterFuture/GoQuark/internal/auth"
	"github.com/ButterFuture/GoQuark/internal/client"
	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/device"
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
	nameDir    = lipgloss.NewStyle().Foreground(lipgloss.Color("117")) // blue folders
	nameFile   = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // light green files
	descDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("243")) // attributes (normal)
	descOnSel  = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))  // attributes on highlight

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
	btnGapSt = lipgloss.NewStyle().Background(lipgloss.Color("235")).Render("  ")
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
	cursor   int            // keyboard / focus index into rows
	selected map[int]bool   // multi-select set
	offset   int            // scroll offset
	listTop  int            // y of first list row on screen
	listH    int            // visible rows

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
	modalKind  string // "error" | "info" | "confirm"
	modalBack  mode   // mode to restore on close
	// confirm action id when modalKind=="confirm"
	confirmAction string
	// keyboard focus: 1=primary(OK) 2=secondary(Cancel). Default = primary.
	btnFocus int
	// mouse hover: 0=none 1=primary 2=secondary. When non-zero, overrides
	// keyboard focus for highlight; left/right keys are disabled.
	btnHover int
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
	dlMgr  *download.Manager
	// downloads center cursor
	dlCursor int
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
	dlFocusID string
	dlFocusUntil time.Time

	// login
	loginBusy   bool
	loginStatus string
	loginQRPath string
	loginQRURL  string
	loginQRArt  string // terminal ASCII QR
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
	res *update.Result
	err error
	manual bool
}

type updateApplyMsg struct {
	res *update.Result
	err error
}
type pauseDoneMsg struct{}
type dlJob struct {
	name, url, dest, fid string
	resume               bool
	replace              bool // delete existing dest before download
}
type dlEnqueueMsg struct {
	jobs     []dlJob
	focusID  string // jump/highlight existing task
	status   string
	err      error
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

	mgr := download.Global()
	mgr.BindClient(c)
	dlDir := cfg.EffectiveDownloadDir()
	_ = os.MkdirAll(dlDir, 0o755)
	mgr.SetDestDir(dlDir)
	// CDN URL expires — re-resolve via fid on resume
	mgr.ResolveURL = func(fid string) (string, int64, error) {
		return api.GetDownloadURL(c, fid)
	}
	_, _ = mgr.LoadPersisted()

	ver := opts.Version
	if ver == "" {
		ver = "dev"
	}
	m := model{
		cfg:         cfg,
		client:      c,
		spinner:     sp,
		stack:       []navFrame{{fid: "0", path: "/", name: "根目录"}},
		selected:    map[int]bool{},
		loginQRPath: "qrcode.png",
		dlMgr:       mgr,
		appVersion:  ver,
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

func (m model) cmdApplyUpdate(res *update.Result) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := update.Apply(ctx, res)
		return updateApplyMsg{res: res, err: err}
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

func (m model) startLogin() tea.Cmd {
	cfg := m.cfg
	qrPath := m.loginQRPath
	return func() tea.Msg {
		statusCh := make(chan string, 32)
		doneCh := make(chan error, 1)
		qrCh := make(chan qrReadyMsg, 1)
		go func() {
			_, err := auth.LoginQR(cfg, auth.Options{
				QRPngPath:  qrPath,
				QRURLPath:  "qrcode.url.txt",
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
		return loginPumpMsg{statusCh: statusCh, doneCh: doneCh, qrCh: qrCh}
	}
}

type qrReadyMsg struct {
	url   string
	ascii string
}

type loginPumpMsg struct {
	statusCh <-chan string
	doneCh   <-chan error
	qrCh     <-chan qrReadyMsg
}
type loginPumpTick struct {
	statusCh <-chan string
	doneCh   <-chan error
	qrCh     <-chan qrReadyMsg
}

func pollLogin(statusCh <-chan string, doneCh <-chan error, qrCh <-chan qrReadyMsg) tea.Cmd {
	return func() tea.Msg {
		return loginPumpTick{statusCh: statusCh, doneCh: doneCh, qrCh: qrCh}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

	case loginPumpMsg:
		m.loginBusy = true
		m.loginStatus = "正在获取二维码…"
		m.loginQRArt = ""
		m.loginQRURL = ""
		return m, pollLogin(msg.statusCh, msg.doneCh, msg.qrCh)

	case loginPumpTick:
		select {
		case err := <-msg.doneCh:
			m.loginBusy = false
			if err != nil {
				return m.showModal("登录失败", err.Error(), "error", modeLogin), nil
			}
			m.mode = modeBrowse
			m.client = client.New(m.cfg)
			m.loading = true
			m.status = "登录成功，加载目录…"
			m.stack = []navFrame{{fid: "0", path: "/", name: "根目录"}}
			m.selected = map[int]bool{}
			m.loginQRArt = ""
			return m, m.loadCurrent()
		case qr := <-msg.qrCh:
			m.loginQRURL = qr.url
			m.loginQRArt = qr.ascii
			m.loginStatus = "请使用夸克 APP 扫码"
			return m, pollLogin(msg.statusCh, msg.doneCh, msg.qrCh)
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
					m.loginStatus = "请使用夸克 APP 扫码"
				}
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
			return m, pollLogin(msg.statusCh, msg.doneCh, msg.qrCh)
		default:
			return m, tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg {
				return loginPumpTick{statusCh: msg.statusCh, doneCh: msg.doneCh, qrCh: msg.qrCh}
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
					m.dlMgr.BindClient(m.client)
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
		if msg.err != nil {
			if client.IsAuthError(msg.err) {
				m.cfg.ClearSession()
				_ = m.cfg.Save()
				m.mode = modeLogin
				m.client = client.New(m.cfg)
				if m.dlMgr != nil {
					m.dlMgr.BindClient(m.client)
					m.dlMgr.ResolveURL = func(fid string) (string, int64, error) {
						return api.GetDownloadURL(m.client, fid)
					}
				}
				return m.showModal("需要重新登录", msg.err.Error(), "error", modeLogin), m.startLogin()
			}
			return m.showModal("下载失败", msg.err.Error(), "error", modeBrowse), nil
		}
		if m.dlMgr == nil {
			m.dlMgr = download.Global()
			m.dlMgr.BindClient(m.client)
			m.dlMgr.SetDestDir(m.cfg.EffectiveDownloadDir())
		}
		// queue conflicts for sequential prompts
		if len(msg.conflicts) > 0 {
			m.conflictQ = append(m.conflictQ, msg.conflicts...)
		}
		var lastID string
		for _, j := range msg.jobs {
			if j.replace {
				_ = os.Remove(j.dest)
				_ = os.Remove(j.dest + ".gqparts")
			}
			t, err := m.dlMgr.EnqueueEx(download.EnqueueOpts{
				Name: j.name, URL: j.url, Dest: j.dest, FID: j.fid,
			})
			if err != nil {
				return m.showModal("下载失败", err.Error(), "error", modeBrowse), nil
			}
			if t != nil {
				lastID = t.ID
			}
		}
		m.mode = modeDownloads
		focus := msg.focusID
		if focus == "" {
			focus = lastID
		}
		if focus != "" {
			m.dlFocusID = focus
			m.dlFocusUntil = time.Now().Add(4 * time.Second)
			if idx := m.dlMgr.DisplayIndex(focus); idx >= 0 {
				m.dlCursor = idx
			}
		} else {
			m.dlCursor = 0
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
		body := fmt.Sprintf("发现新版本 %s（当前 %s）\n", res.Latest, res.Current)
		if res.CanApply {
			body += "点击「立即更新」即可安装。"
		} else {
			body += "无法自动更新（自编译或无对应平台包），请到发布页手动下载。"
			if res.ReleaseURL != "" {
				body += "\n" + res.ReleaseURL
			}
		}
		return m.showConfirm("发现新版本", body, "update_available"), nil

	case updateApplyMsg:
		m.loading = false
		if msg.err != nil {
			return m.showModal("更新失败", msg.err.Error(), "error", modeBrowse), nil
		}
		ver := ""
		if msg.res != nil {
			ver = msg.res.Latest
		}
		return m.showModal("更新完成", "已安装 "+ver+"\n请退出后重新启动 GoQuark。", "info", modeBrowse), nil

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
	// modal: clickable buttons for confirm; hover highlights
	if m.mode == modeModal {
		if m.quitting {
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
				if m.hitOK(msg.X, msg.Y) {
					return m.activateConfirmBtn(1)
				}
				if m.hitCancel(msg.X, msg.Y) {
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
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonRight {
			// map y to display row roughly (header ~3 lines: title, path, blank-ish)
			// simple: each task ~3 lines; cursor already tracks keyboard
			// still try to pick row near click
			if m.dlMgr != nil {
				n := len(m.dlMgr.List())
				// list starts after title+path (~2 lines) + blank
				rel := msg.Y - 3
				if rel >= 0 && n > 0 {
					// 3 lines per task in viewDownloads
					idx := rel / 3
					if idx >= n {
						idx = n - 1
					}
					m.dlCursor = idx
				}
			}
			return m.openMenuAt(msg.X, msg.Y), nil
		}
		if msg.Button == tea.MouseButtonRight {
			return m, nil
		}
		if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
			if m.dlMgr != nil {
				n := len(m.dlMgr.List())
				rel := msg.Y - 3
				if rel >= 0 && n > 0 {
					idx := rel / 3
					if idx >= n {
						idx = n - 1
					}
					m.dlCursor = idx
				}
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelUp {
			if m.dlCursor > 0 {
				m.dlCursor--
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			if m.dlMgr != nil {
				n := len(m.dlMgr.List())
				if m.dlCursor < n-1 {
					m.dlCursor++
				}
			}
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
			return m.showConfirm("退出程序", "确定要退出 GoQuark 吗？", "quit"), nil
		case "ctrl+c":
			return m.showConfirm("退出程序", "确定要退出 GoQuark 吗？", "quit"), nil
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
		switch key {
		case "esc", "backspace", "h", "left", "b":
			m.mode = modeBrowse
			return m, nil
		case "q":
			return m.showConfirm("退出程序", "确定要退出 GoQuark 吗？", "quit"), nil
		case "ctrl+c":
			return m.showConfirm("退出程序", "确定要退出 GoQuark 吗？", "quit"), nil
		case "up", "k":
			if m.dlCursor > 0 {
				m.dlCursor--
			}
		case "down", "j":
			if m.dlMgr != nil {
				n := len(m.dlMgr.List())
				if m.dlCursor < n-1 {
					m.dlCursor++
				}
			}
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
			// keyboard open download menu
			return m.openMenuAt(4, 4), nil
		}
		return m, nil
	}

	// browse
	switch key {
	case "u":
		m.status = "正在检查更新…"
		return m, m.cmdCheckUpdate(true)
	case "q":
		return m.showConfirm("退出程序", "确定要退出 GoQuark 吗？", "quit"), nil
	case "ctrl+c":
		return m.showConfirm("退出程序", "确定要退出 GoQuark 吗？", "quit"), nil
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
		m.dlCursor = 0
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
	var snap *download.Snapshot
	if m.dlMgr != nil {
		snap = m.dlMgr.AtDisplay(m.dlCursor)
	}
	if snap != nil {
		switch snap.Status {
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
		m.dlCursor = 0
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
		return m.showConfirm("退出程序", "确定要退出 GoQuark 吗？", "quit"), nil
	// download-center menu
	case "dl_pause":
		if m.dlMgr != nil {
			if s := m.dlMgr.AtDisplay(m.dlCursor); s != nil {
				if m.dlMgr.PauseID(s.ID) {
					m.status = "正在暂停: " + s.Name
				}
			}
		}
		m.mode = modeDownloads
		return m, nil
	case "dl_resume":
		if m.dlMgr != nil {
			if s := m.dlMgr.AtDisplay(m.dlCursor); s != nil {
				if m.dlMgr.ResumeID(s.ID) {
					m.status = "继续下载: " + s.Name
				} else {
					m.status = "无法继续: " + s.Name + "（可能已在运行或状态不对）"
				}
			}
		}
		m.mode = modeDownloads
		return m, nil
	case "dl_cancel":
		if m.dlMgr != nil {
			if s := m.dlMgr.AtDisplay(m.dlCursor); s != nil {
				if m.dlMgr.CancelID(s.ID) {
					m.status = "已取消: " + s.Name
				}
			}
		}
		m.mode = modeDownloads
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
		if m.dlMgr != nil {
			if s := m.dlMgr.AtDisplay(m.dlCursor); s != nil {
				m.status = s.Dest
			}
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
			m.mode = modeModal
			m.modalKind = "info"
			m.modalTitle = "正在更新"
			m.modalBody = m.spinner.View() + " 正在下载 " + res.AssetName + " …"
			m.confirmAction = ""
			m.loading = true
			return m, tea.Batch(m.spinner.Tick, m.cmdApplyUpdate(res))
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
		_ = os.Remove(dest)
		_ = os.Remove(dest + ".gqparts")
	} else {
		dest = download.UniqueDest(dest)
	}
	if m.dlMgr == nil {
		m.dlMgr = download.Global()
		m.dlMgr.BindClient(m.client)
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
	if m.dlMgr == nil || m.dlMgr.ActiveCount() == 0 {
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

func (m model) doLogout() (tea.Model, tea.Cmd) {
	m.cfg.ClearSession()
	_ = m.cfg.Save()
	m.mode = modeLogin
	m.client = client.New(m.cfg)
	m.rows = nil
	m.selected = map[int]bool{}
	m.status = "已退出登录"
	m.loginStatus = "按 l 开始扫码登录"
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
	// if multi-selected files only → download all
	if n := m.selectedFileCount(); n > 1 {
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

func (m model) selectedFileCount() int {
	n := 0
	for i, on := range m.selected {
		if !on || i < 0 || i >= len(m.rows) {
			continue
		}
		r := m.rows[i]
		if !r.entry.Dir && r.entry.Name != ".." {
			n++
		}
	}
	return n
}

func (m model) selectedFiles() []row {
	// if multi selection has files, use them; else cursor file
	var out []row
	for i, on := range m.selected {
		if !on || i < 0 || i >= len(m.rows) {
			continue
		}
		r := m.rows[i]
		if !r.entry.Dir && r.entry.Name != ".." {
			out = append(out, r)
		}
	}
	if len(out) == 0 && len(m.rows) > 0 {
		r := m.rows[m.cursor]
		if !r.entry.Dir && r.entry.Name != ".." {
			out = append(out, r)
		}
	}
	return out
}

func (m model) doDownload() (tea.Model, tea.Cmd) {
	files := m.selectedFiles()
	if len(files) == 0 {
		m.status = "请选择文件再下载"
		return m, nil
	}
	// always refresh dest dir from config (user may have changed download_dir)
	destDir := m.cfg.EffectiveDownloadDir()
	if m.dlMgr == nil {
		m.dlMgr = download.Global()
		m.dlMgr.BindClient(m.client)
	}
	m.dlMgr.SetDestDir(destDir)
	cl := m.client
	mgr := m.dlMgr
	return m, func() tea.Msg {
		_ = os.MkdirAll(destDir, 0o755)
		// drop done records whose files are gone (before dedupe checks)
		if n := mgr.ClearStaleDone(); n > 0 {
			// silent cleanup; notes below if user re-downloads same file
			_ = n
		}
		var jobs []dlJob
		var conflicts []dlConflict
		var focusID string
		var notes []string
		for _, f := range files {
			fid := f.entry.FID
			name := f.entry.Name
			dest := filepath.Join(destDir, sanitizeName(name))

			// Dedup only within the CURRENT download_dir.
			// Changing download_dir allows a fresh download of the same FID.
			if existing := mgr.FindByFIDInDir(fid, destDir); existing != nil {
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
					case download.StatusRunning, download.StatusQueued, download.StatusFinalizing:
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

			urlStr, _, err := api.GetDownloadURL(cl, fid)
			if err != nil {
				return dlEnqueueMsg{err: fmt.Errorf("%s: %w", name, err)}
			}
			jobs = append(jobs, dlJob{name: name, url: urlStr, dest: dest, fid: fid})
		}

		status := ""
		if len(notes) > 0 {
			status = strings.Join(notes, "；")
		}
		if len(jobs) == 0 && len(conflicts) == 0 {
			if status == "" {
				status = "没有新的下载任务"
			}
			return dlEnqueueMsg{focusID: focusID, status: status}
		}
		return dlEnqueueMsg{jobs: jobs, conflicts: conflicts, focusID: focusID, status: status}
	}
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
	if selN := len(m.selected); selN > 0 {
		foot = fmt.Sprintf("已选 %d  ·  %s", selN, foot)
	}
	b.WriteString(statusStyle.Width(max(1, m.width)).Render(truncate(foot, m.width-2)))
	b.WriteString("\n")
	// always show shortcuts help (truncate only if terminal is tiny)
	help := "单击选中 · 空格多选 · a全选 · c清除 · 双击/enter打开 · 右键/m菜单 · d下载 · t下载中心 · u检查更新 · i信息 · o退出登录 · q退出"
	b.WriteString(helpStyle.Render(truncate(help, max(20, m.width))))
	return b.String()
}

func (m model) viewDownloads() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("GoQuark 下载中心"))
	if m.dlMgr != nil {
		n := m.dlMgr.ActiveCount()
		if n > 0 {
			b.WriteString("  " + m.spinner.View() + " " + fmt.Sprintf("%d 进行中", n))
		}
	}
	b.WriteString("\n")

	// show download root path (always)
	dlRoot := m.cfg.EffectiveDownloadDir()
	if m.dlMgr != nil && m.dlMgr.DestDir() != "" {
		dlRoot = m.dlMgr.DestDir()
	}
	b.WriteString(pathStyle.Render("保存到: " + dlRoot))
	b.WriteString("\n")

	var list []download.Snapshot
	if m.dlMgr != nil {
		list = m.dlMgr.List()
	}
	if len(list) == 0 {
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("暂无下载任务。在网盘选中文件后按 d 开始下载。"))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("可在 ~/.config/goquark/config.json 修改 download_dir"))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("esc/b 返回网盘 · t 打开本页"))
		return b.String()
	}
	if m.dlCursor >= len(list) {
		m.dlCursor = len(list) - 1
	}
	if m.dlCursor < 0 {
		m.dlCursor = 0
	}

	// List() already: active group on top, finished below; chronological within group.
	for i, s := range list {
		focused := i == m.dlCursor
		b.WriteString(m.renderDownloadItem(s, focused, dlRoot))
		b.WriteString("\n")
	}
	// section summary — do NOT count paused/error as "进行中"
	runningN, pausedN, doneN := 0, 0, 0
	for _, s := range list {
		switch s.Status {
		case download.StatusRunning, download.StatusQueued, download.StatusFinalizing, download.StatusPausing:
			runningN++
		case download.StatusPaused:
			pausedN++
		case download.StatusDone, download.StatusCancel:
			doneN++
		case download.StatusError:
			// show with paused group in summary as "其它" folded into pausedN for compactness
			pausedN++
		default:
			pausedN++
		}
	}
	b.WriteString("\n")
	summary := fmt.Sprintf("下载中 %d · 已暂停 %d · 已完成 %d · 共 %d · %s",
		runningN, pausedN, doneN, len(list), dlRoot)
	b.WriteString(statusStyle.Width(max(1, m.width)).Render(truncate(summary, m.width-2)))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑↓ · 右键/m菜单 · p暂停 · r恢复 · x取消 · c清除已完成 · u检查更新 · esc/b返回"))
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

	stLabel := string(s.Status)
	var stFG string
	switch s.Status {
	case download.StatusRunning:
		stLabel = "下载中"
		stFG = "39"
	case download.StatusFinalizing:
		stLabel = "收尾中"
		stFG = "213" // pink/magenta spinner-ish
	case download.StatusPausing:
		stLabel = "暂停中"
		stFG = "214"
	case download.StatusQueued:
		stLabel = "排队"
		stFG = "117"
	case download.StatusPaused:
		stLabel = "已暂停"
		stFG = "214"
	case download.StatusDone:
		stLabel = "完成"
		stFG = "42"
	case download.StatusError:
		stLabel = "失败"
		stFG = "196"
	case download.StatusCancel:
		stLabel = "取消"
		stFG = "244"
	default:
		stFG = "252"
	}
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
		prefix = stStyle.Render(m.spinner.View()+" "+stLabel)
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

	lines := []string{nameLine, subLine, pathColored}

	var out strings.Builder
	for i, line := range lines {
		if focused {
			// accent + pad + content (all bg 237) + right fill
			accent := lipgloss.NewStyle().
				Foreground(lipgloss.Color("135")).
				Background(lipgloss.Color(hlBG)).
				Render("┃")
			padL := lipgloss.NewStyle().Background(lipgloss.Color(hlBG)).Render("  ")
			used := 1 + 2 + lipgloss.Width(line)
			fillN := w - used
			if fillN < 0 {
				fillN = 0
			}
			padR := lipgloss.NewStyle().Background(lipgloss.Color(hlBG)).Render(strings.Repeat(" ", fillN))
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
		b.WriteString(helpStyle.Render("夸克 APP → 搜索框相机 → 扫码"))
		b.WriteString("\n")
	} else if m.loginBusy {
		b.WriteString(helpStyle.Render("正在生成二维码…"))
		b.WriteString("\n")
	} else {
		b.WriteString(panelStyle.Width(min(m.width-2, 48)).Render("按 l 获取登录二维码"))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("l 刷新二维码 · q 退出"))
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

func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\x00", "")
	if name == "" || name == "." || name == ".." {
		return "download.bin"
	}
	return name
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
