package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ButterFuture/GoQuark/internal/api"
	"github.com/ButterFuture/GoQuark/internal/auth"
	"github.com/ButterFuture/GoQuark/internal/client"
	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/device"
	"github.com/ButterFuture/GoQuark/internal/dlsvc"
	"github.com/ButterFuture/GoQuark/internal/download"
	"github.com/ButterFuture/GoQuark/internal/output"
	"github.com/ButterFuture/GoQuark/internal/tui"
	"github.com/ButterFuture/GoQuark/internal/update"
	mcpserver "github.com/ButterFuture/GoQuark/mcp"
)

// Version metadata — injected at build time from VERSION file via:
//
//	go build -ldflags "-X main.Version=... -X main.Commit=... -X main.BuildDate=..."
//
// Defaults are for `go run` / local dev without scripts/build.sh.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func main() {
	// A previous update can leave its replaced image behind: Windows will not
	// let the binary of a running process be deleted, so the old one is still
	// on disk when that process finally exits. Clear it, and any staging file
	// an interrupted download left, on the way in.
	update.CleanupLeftovers()

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "help", "-h", "--help":
		usage()
	case "version", "-v", "--version":
		printVersion()
	case "login":
		cmdLogin(args)
	case "logout":
		cmdLogout(args)
	case "device":
		cmdDevice(args)
	case "whoami":
		cmdWhoami(args)
	case "ls":
		cmdLS(args)
	case "download":
		cmdDownload(args)
	case "downloads", "dl", "progress":
		cmdDownloads(args)
	case "tui":
		cmdTUI(args)
	case "download-service":
		cmdDownloadService(args)
	case "mcp":
		if err := mcpserver.Run(Version); err != nil {
			fatal(err)
		}
	case "update", "check-update", "upgrade":
		cmdUpdate(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func printVersion() {
	if Commit == "unknown" && BuildDate == "unknown" {
		fmt.Printf("version: %s\n", Version)
	} else {
		fmt.Printf("version: %s (commit %s, built %s)\n", Version, Commit, BuildDate)
	}
	// passive: show whether a newer release exists
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	res, err := update.Check(ctx, Version, update.RepoFromEnv())
	if err != nil {
		fmt.Fprintf(os.Stderr, "update check: %v\n", err)
		return
	}
	if !res.UpdateAvailable {
		fmt.Printf("update:  up to date (latest %s)\n", res.Latest)
		fmt.Println("hint:    goquark update   # re-check / apply with --yes")
		return
	}
	fmt.Printf("update:  NEW %s available (current %s)\n", res.Latest, res.Current)
	if res.CanApply {
		fmt.Printf("asset:   %s\n", res.AssetName)
		fmt.Println("apply:   goquark update --yes")
	} else {
		fmt.Println("apply:   not available for this build/platform (self-built?)")
		if res.ReleaseURL != "" {
			fmt.Printf("manual:  %s\n", res.ReleaseURL)
		}
		fmt.Println("hint:    goquark update   # details")
	}
}

func usage() {
	fmt.Print(`GoQuark — Quark drive login & high-speed download

Usage:
  goquark login [--config PATH] [--nickname NAME] [--token-only] [--wechat]
  goquark logout [--config PATH] [--json]   # ends the session; the device is kept
  goquark device [--config PATH] [--json]
  goquark whoami [--config PATH] [--json]
  goquark ls [PATH] [--config PATH] [--json]
  goquark download <remote-path|fid> <local-path> [--config PATH]
  goquark download <remote-path|fid> [--config PATH]   # saves to download_dir
  goquark downloads [--watch] [--config PATH] [--json]
  goquark update [--check] [--yes] [--allow-custom-repo] [--json]   # check / apply update from GitHub Release
  goquark tui [--config PATH]
  goquark mcp
  goquark version

Notes:
  --json                 force machine-readable JSON on stdout
  GOQUARK_OUTPUT=json    same as --json (good for agents)
  When stdout is a pipe, whoami/device/downloads default to JSON automatically.
  Human progress / "加载中" goes to stderr and is cleared after success.
  downloads              list download tasks / progress (alias: dl, progress)
  downloads --watch      refresh every second until idle (Ctrl+C to stop)
  update                 passive: check latest Release tag; apply with --yes
  download_dir           in ~/.config/goquark/config.json (default: ~/Downloads/GoQuark)

Config default: ~/.config/goquark/config.json
`)
}

func loadCfg(args []string) (*config.Config, []string, bool) {
	fs := flag.NewFlagSet("cfg", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", "", "config path")
	jsonOut := false
	var rest []string
	var filtered []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-config" || a == "--config" {
			if i+1 < len(args) {
				filtered = append(filtered, "-config", args[i+1])
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "-config=") || strings.HasPrefix(a, "--config=") {
			filtered = append(filtered, "-config="+strings.SplitN(a, "=", 2)[1])
			continue
		}
		if a == "-json" || a == "--json" {
			jsonOut = true
			continue
		}
		rest = append(rest, a)
	}
	_ = fs.Parse(filtered)
	path := *cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		fatal(err)
	}
	if err := device.EnsureDevice(cfg, ""); err != nil {
		fatal(err)
	}
	return cfg, rest, jsonOut
}

func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config path")
	nick := fs.String("nickname", "", "device display name override")
	tokenOnly := fs.Bool("token-only", false, "only emit QR")
	wechat := fs.Bool("wechat", false, "scan with WeChat instead of the Quark app")
	_ = fs.Parse(args)
	method := auth.MethodQuark
	if *wechat {
		method = auth.MethodWeChat
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal(err)
	}
	if err := device.EnsureDevice(cfg, *nick); err != nil {
		fatal(err)
	}
	fmt.Printf("device: %s | %s | %s | id=%s\n",
		cfg.Device.DeviceName, cfg.Device.Model, cfg.Device.Platform, cfg.Device.MachineID)

	res, err := auth.Login(cfg, method, auth.Options{
		TokenOnly:  *tokenOnly,
		PrintASCII: true,
		OnStatus: func(msg string) {
			fmt.Println("[login]", msg)
		},
	})
	if err != nil {
		fatal(err)
	}
	if *tokenOnly {
		fmt.Println("token-only done")
		return
	}
	fmt.Println("LOGIN OK")
	fmt.Printf("cookies: %d keys\n", len(res.Cookies))
	fmt.Printf("config: %s\n", cfg.Path())
}

// cmdDownloadService runs the shared download service. GoQuark starts it on
// its own when a download needs it; it is not meant to be run by hand.
func cmdDownloadService(args []string) {
	fs := flag.NewFlagSet("download-service", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config path")
	_ = fs.Parse(args)
	if err := dlsvc.Serve(*cfgPath, Version); err != nil {
		fatal(err)
	}
}

// cmdLogout ends the session on the server, then clears it locally whatever
// the server said, keeping the device — the same order as the PC client.
func cmdLogout(args []string) {
	cfg, _, jsonFlag := loadCfg(args)
	was := cfg.HasSessionCookie()
	var serverErr error
	if was {
		done := output.Loading("正在退出登录…")
		serverErr = api.Logout(client.New(cfg))
		done()
	}
	cfg.ClearSession()
	if err := cfg.Save(); err != nil {
		fatal(err)
	}
	if output.WantJSON(jsonFlag) {
		out := map[string]any{
			"success":       true,
			"was_logged_in": was,
			"server_logout": was && serverErr == nil,
			"config":        cfg.Path(),
		}
		if serverErr != nil {
			out["server_error"] = serverErr.Error()
		}
		_ = output.PrintJSON(os.Stdout, out)
		return
	}
	switch {
	case !was:
		fmt.Println("当前未登录，无需退出")
	case serverErr != nil:
		fmt.Println("已退出登录（设备信息已保留）")
		fmt.Fprintf(os.Stderr, "注意：服务端未确认退出，会话可能仍然有效：%v\n", serverErr)
	default:
		fmt.Println("已退出登录（设备信息已保留）")
	}
}

func cmdDevice(args []string) {
	cfg, _, jsonFlag := loadCfg(args)
	if output.WantJSON(jsonFlag) {
		_ = output.PrintJSON(os.Stdout, cfg.Device)
		return
	}
	fmt.Printf("设备名:   %s\n", cfg.Device.DeviceName)
	fmt.Printf("型号:     %s\n", cfg.Device.Model)
	fmt.Printf("系统:     %s %s (%s)\n", cfg.Device.Platform, cfg.Device.OSVersion, cfg.Device.Arch)
	fmt.Printf("机器码:   %s\n", cfg.Device.MachineID)
	fmt.Printf("创建时间: %s\n", cfg.Device.CreatedAt)
}

func cmdWhoami(args []string) {
	cfg, _, jsonFlag := loadCfg(args)
	if err := cfg.RequireLogin(); err != nil {
		fatal(err)
	}
	c := client.New(cfg)

	// Human progress on stderr; cleared after load so agents piping stdout stay clean.
	done := output.Loading("加载中…")
	info, err := api.UserInfo(c)
	done()
	if err != nil {
		fatal(err)
	}

	if output.WantJSON(jsonFlag) {
		// Keep full payload for agents; add a small envelope for stability.
		out := map[string]any{
			"success": true,
			"data":    info,
			"device": map[string]string{
				"device_name": cfg.Device.DeviceName,
				"model":       cfg.Device.Model,
			},
			"session": map[string]string{
				"login_way":    cfg.Session.LoginWay,
				"logged_in_at": cfg.Session.LoggedInAt,
			},
			"version": Version,
		}
		if urec := checkUpdateRecord(); urec != nil {
			out["update"] = urec
		}
		if err := output.PrintJSON(os.Stdout, out); err != nil {
			fatal(err)
		}
		return
	}

	fmt.Println(output.FormatWhoami(info, cfg.Device.DeviceName, cfg.Session.LoginWay, cfg.Session.LoggedInAt))
	fmt.Printf("版本:     %s\n", Version)
	printUpdateHuman()
}

// checkUpdateRecord returns a map for JSON info pages (nil if check failed hard).
func checkUpdateRecord() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	res, err := update.Check(ctx, Version, update.RepoFromEnv())
	if err != nil {
		return map[string]any{"error": err.Error(), "current": Version}
	}
	m := map[string]any{
		"current":          res.Current,
		"latest":           res.Latest,
		"update_available": res.UpdateAvailable,
		"can_apply":        res.CanApply,
		"release_url":      res.ReleaseURL,
		"asset_name":       res.AssetName,
	}
	if res.UpdateAvailable && res.CanApply {
		m["apply_hint"] = "goquark update --yes"
	} else if res.UpdateAvailable {
		m["apply_hint"] = "manual download from release_url (self-built / no matching asset)"
	} else {
		m["apply_hint"] = "goquark update"
	}
	return m
}

func printUpdateHuman() {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	res, err := update.Check(ctx, Version, update.RepoFromEnv())
	if err != nil {
		fmt.Printf("更新:     检查失败（%v）\n", err)
		fmt.Println("提示:     CLI: goquark update · TUI: 按 u 或菜单「检查更新」")
		return
	}
	if !res.UpdateAvailable {
		fmt.Printf("更新:     已是最新（%s）\n", res.Latest)
		fmt.Println("提示:     CLI: goquark update · TUI: 按 u")
		return
	}
	fmt.Printf("更新:     发现新版本 %s（当前 %s）\n", res.Latest, res.Current)
	if res.CanApply {
		fmt.Printf("资产:     %s\n", res.AssetName)
		fmt.Println("自动更新: goquark update --yes")
		fmt.Println("TUI:      按 u 或点「立即更新」")
	} else {
		fmt.Println("自动更新: 不可用（自编译或无对应平台资产）")
		if res.ReleaseURL != "" {
			fmt.Printf("发布页:   %s\n", res.ReleaseURL)
		}
		fmt.Println("提示:     CLI: goquark update · TUI: 按 u 查看详情")
	}
}

func cmdLS(args []string) {
	cfg, rest, jsonFlag := loadCfg(args)
	if err := cfg.RequireLogin(); err != nil {
		fatal(err)
	}
	path := "/"
	if len(rest) > 0 {
		path = rest[0]
	}
	c := client.New(cfg)
	done := output.Loading("加载中…")
	fid, isDir, err := api.ResolvePath(c, path)
	if err != nil {
		done()
		fatal(err)
	}
	if !isDir && path != "/" {
		done()
		if output.WantJSON(jsonFlag) {
			_ = output.PrintJSON(os.Stdout, map[string]any{"path": path, "fid": fid, "dir": false})
			return
		}
		fmt.Printf("%s  fid=%s  (file)\n", path, fid)
		return
	}
	entries, err := api.ListDir(c, fid, 1, 100)
	done()
	if err != nil {
		fatal(err)
	}
	if output.WantJSON(jsonFlag) {
		_ = output.PrintJSON(os.Stdout, map[string]any{"path": path, "fid": fid, "list": entries})
		return
	}
	for _, e := range entries {
		kind := "F"
		if e.Dir {
			kind = "D"
		}
		fmt.Printf("%s  %12d  %s  %s\n", kind, e.Size, e.FID, e.Name)
	}
}

func cmdDownload(args []string) {
	cfg, rest, _ := loadCfg(args)
	if err := cfg.RequireLogin(); err != nil {
		fatal(err)
	}
	if len(rest) < 1 {
		fatal(fmt.Errorf("usage: goquark download <remote-path|fid> [local-path]"))
	}
	remote := rest[0]
	local := ""
	if len(rest) >= 2 {
		local = rest[1]
	}
	c := client.New(cfg)

	fid := remote
	if strings.Contains(remote, "/") || remote == "" {
		var err error
		var isDir bool
		fid, isDir, err = api.ResolvePath(c, remote)
		if err != nil {
			fatal(err)
		}
		if isDir {
			fatal(fmt.Errorf("remote is a directory; pick a file"))
		}
	}

	urlStr, size, err := api.GetDownloadURL(c, fid)
	if err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "size=%d\n", size)

	// default destination: config download_dir / system Downloads/GoQuark
	if local == "" || local == "." {
		dir := cfg.EffectiveDownloadDir()
		_ = os.MkdirAll(dir, 0o755)
		// use remote basename if path-like; otherwise fid
		base := filepath.Base(remote)
		if base == "." || base == "/" || base == "" || !strings.Contains(remote, "/") {
			// try resolve name later from dest; keep fid-ish
			base = fid
			if len(base) > 16 {
				base = base[:16]
			}
			base = "file-" + base
		}
		local = filepath.Join(dir, base)
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil && !os.IsExist(err) {
		_ = err
	}

	// The download runs in the download service, next to the TUI's and MCP's.
	mgr := dlsvc.Open(cfg.Path())
	mgr.SetDestDir(cfg.EffectiveDownloadDir())
	name := filepath.Base(local)
	taskID, err := mgr.Add(download.EnqueueOpts{
		Name: name, URL: urlStr, Dest: local, FID: fid,
	})
	if err != nil {
		fatal(err)
	}
	// The service would carry on after this process: Ctrl+C pauses instead,
	// as stopping the download used to (resume it in the TUI or rerun).
	intr := make(chan os.Signal, 1)
	signal.Notify(intr, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-intr
		mgr.PauseID(taskID)
		fmt.Fprintln(os.Stderr, "\n已暂停，可在 goquark tui 的下载中心继续")
		os.Exit(130)
	}()
	start := time.Now()
	for {
		var snap *download.Snapshot
		for _, s := range mgr.List() {
			if s.ID == taskID {
				s := s
				snap = &s
				break
			}
		}
		if snap != nil {
			if snap.Total > 0 {
				fmt.Fprintf(os.Stderr, "\rprogress %.1f%%  %.2f MiB/s", snap.Percent, snap.Speed/1024/1024)
			}
			if snap.Status == download.StatusDone {
				fmt.Fprintln(os.Stderr)
				fi, _ := os.Stat(local)
				sz := int64(0)
				if fi != nil {
					sz = fi.Size()
				}
				fmt.Printf("OK path=%s size=%d elapsed=%s id=%s\n", local, sz, time.Since(start).Round(time.Millisecond), snap.ID)
				return
			}
			if snap.Status == download.StatusError || snap.Status == download.StatusCancel {
				fmt.Fprintln(os.Stderr)
				fatal(fmt.Errorf("%s", snap.Error))
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func cmdDownloads(args []string) {
	cfg, rest, wantJSON := loadCfg(args)
	watch := false
	for _, a := range rest {
		if a == "--watch" || a == "-w" {
			watch = true
		}
	}
	// Only shows: asks a running download service, else reads downloads.json
	// without writing it.
	mgr := dlsvc.OpenViewer(cfg.Path())

	printOnce := func() {
		list := mgr.List()
		if wantJSON || output.IsAgentLike() {
			_ = output.PrintJSON(os.Stdout, map[string]any{
				"tasks":  list,
				"active": mgr.ActiveCount(),
			})
			return
		}
		if len(list) == 0 {
			fmt.Println("暂无下载任务")
			fmt.Println("提示: goquark download <远程> <本地>  或  goquark tui 里按 d")
			return
		}
		// header
		fmt.Printf("%-12s  %-8s  %8s  %-10s  %-10s  %-10s  %s\n",
			"ID", "状态", "进度", "速度", "已用", "剩余", "名称")
		for _, s := range list {
			pct := "-"
			if s.Total > 0 {
				pct = fmt.Sprintf("%.1f%%", s.Percent)
			}
			spd := "-"
			if s.Speed > 0 {
				spd = fmt.Sprintf("%.2f MiB/s", s.Speed/1024/1024)
			}
			el := "-"
			if s.Elapsed > 0 {
				el = download.FormatDuration(s.Elapsed)
			}
			eta := "-"
			if s.HasETA {
				eta = download.FormatDuration(s.ETA)
			} else if s.Status == download.StatusDone && s.Elapsed > 0 {
				eta = "完成"
			}
			st := string(s.Status)
			fmt.Printf("%-12s  %-8s  %8s  %-10s  %-10s  %-10s  %s\n  → %s\n",
				s.ID, st, pct, spd, el, eta, s.Name, s.Dest)
			if s.Error != "" && s.Status != download.StatusDone {
				fmt.Printf("  error: %s\n", s.Error)
			}
		}
		active, done := 0, 0
		for _, s := range list {
			if s.Status == download.StatusDone || s.Status == download.StatusCancel {
				done++
			} else {
				active++
			}
		}
		fmt.Printf("进行中=%d 已完成=%d 共=%d\n", active, done, len(list))
	}

	if !watch {
		printOnce()
		return
	}
	for {
		// clear-ish for human watch
		if !wantJSON && !output.IsAgentLike() {
			fmt.Fprint(os.Stderr, "\033[H\033[2J")
		}
		printOnce()
		if mgr.ActiveCount() == 0 {
			return
		}
		time.Sleep(time.Second)
	}
}

func cmdTUI(args []string) {
	cfg, _, _ := loadCfg(args)
	if err := tui.Run(cfg, tui.Options{Version: Version}); err != nil {
		fatal(err)
	}
}

// cmdUpdate: passive check / apply from public GitHub Release.
//
//	goquark update           → check only
//	goquark update --check   → check only
//	goquark update --yes     → download & replace if possible
func cmdUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "only check, do not apply")
	yes := fs.Bool("yes", false, "download and replace binary if update available")
	jsonOut := fs.Bool("json", false, "JSON output")
	allowCustomRepo := fs.Bool("allow-custom-repo", false,
		"permit installing a build from a repo other than "+update.DefaultRepo)
	_ = fs.Parse(args)
	// aliases: --check is default when no --yes
	if !*yes {
		*checkOnly = true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	res, err := update.Check(ctx, Version, update.RepoFromEnv())
	if err != nil {
		fatal(err)
	}

	wantJSON := *jsonOut || output.IsAgentLike() || os.Getenv("GOQUARK_OUTPUT") == "json"

	if !*checkOnly && res.UpdateAvailable {
		if !res.CanApply {
			if wantJSON {
				b, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(b))
			} else {
				fmt.Printf("发现新版本 %s（当前 %s）\n", res.Latest, res.Current)
				fmt.Println(res.Reason)
			}
			os.Exit(2)
		}
		if res.CustomRepo {
			fmt.Fprintf(os.Stderr,
				"警告: 将从 %s 安装（非官方仓库 %s），来源由 GOQUARK_UPDATE_REPO 指定\n",
				res.Repo, update.DefaultRepo)
		}
		fmt.Fprintf(os.Stderr, "正在下载 %s …\n", res.AssetName)
		if err := update.ApplyWithOptions(ctx, res, update.ApplyOptions{
			AllowCustomRepo: *allowCustomRepo,
		}); err != nil {
			if errors.Is(err, update.ErrUpdateInProgress) {
				fmt.Fprintln(os.Stderr, "已有一个 GoQuark 更新正在进行，请等它结束后重试。")
				os.Exit(3)
			}
			fatal(err)
		}
		if wantJSON {
			b, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Printf("已更新到 %s\n路径: %s\n请重新运行 goquark。\n", res.Latest, res.BinaryPath)
			if note := dlsvc.UpdateNote(config.DefaultPath()); note != "" {
				fmt.Println(note)
			}
			if res.PendingCleanup != "" {
				fmt.Printf("旧版本仍被当前进程占用，将在下次启动时清理：%s\n", res.PendingCleanup)
			}
		}
		return
	}

	if wantJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		return
	}
	if !res.UpdateAvailable {
		fmt.Printf("已是最新：%s\n", res.Current)
		return
	}
	fmt.Printf("发现新版本：%s → %s\n", res.Current, res.Latest)
	if res.ReleaseURL != "" {
		fmt.Printf("发布页：%s\n", res.ReleaseURL)
	}
	if res.Notes != "" {
		fmt.Println()
		fmt.Println("更新说明：")
		for _, line := range strings.Split(res.Notes, "\n") {
			fmt.Println("  " + line)
		}
		fmt.Println()
	}
	if res.CanApply {
		fmt.Printf("可自动更新资产：%s\n", res.AssetName)
		fmt.Println("执行: goquark update --yes")
	} else {
		fmt.Println(res.Reason)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
