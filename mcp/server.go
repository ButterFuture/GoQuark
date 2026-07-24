package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ButterFuture/GoQuark/internal/api"
	"github.com/ButterFuture/GoQuark/internal/auth"
	"github.com/ButterFuture/GoQuark/internal/client"
	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/device"
	"github.com/ButterFuture/GoQuark/internal/download"
	"github.com/ButterFuture/GoQuark/internal/update"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Run starts MCP server over stdio.
// version is the embedded app version (from main -ldflags).
func Run(version string) error {
	if version == "" {
		version = "dev"
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return err
	}
	if err := device.EnsureDevice(cfg, ""); err != nil {
		return err
	}
	c := client.New(cfg)
	mgr := download.Global()
	mgr.BindClient(c)
	mgr.SetDestDir(cfg.EffectiveDownloadDir())
	_, _ = mgr.LoadPersisted()

	// MCP login session (in-process; one active QR at a time)
	var loginSess *auth.QRSession

	s := server.NewMCPServer("goquark", version)

	// ---- status / identity (agent-friendly) ----

	s.AddTool(mcp.NewTool("goquark_status",
		mcp.WithDescription("Session/config status without calling Quark APIs. Reports logged_in, config path, version, download_dir, device summary."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out := map[string]any{
			"version":      version,
			"logged_in":    cfg.HasSessionCookie(),
			"config_path":  cfg.Path(),
			"download_dir": cfg.EffectiveDownloadDir(),
			"device": map[string]string{
				"device_name": cfg.Device.DeviceName,
				"model":       cfg.Device.Model,
				"machine_id":  cfg.Device.MachineID,
				"platform":    cfg.Device.Platform,
			},
			"session": map[string]any{
				"login_way":    cfg.Session.LoginWay,
				"logged_in_at": cfg.Session.LoggedInAt,
			},
			"login_session_active": loginSess != nil && loginSess.Token != "",
		}
		return textJSON(out)
	})

	// ---- login (QR for agents) ----

	s.AddTool(mcp.NewTool("goquark_login",
		mcp.WithDescription("Start Quark QR login for agents. Returns CLEARLY LABELED qr_url (open/scan link) and qr_ascii (terminal QR art). Does NOT wait for scan — call goquark_login_poll after showing QR to the user. If already logged in and force=false, returns logged_in without new QR."),
		mcp.WithBoolean("force", mcp.Description("If true, start a new QR even when already logged in. Default false."), mcp.DefaultBool(false)),
		mcp.WithBoolean("include_ascii", mcp.Description("Include qr_ascii terminal art (default true). Set false if only the link is needed."), mcp.DefaultBool(true)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		force := argBool(req, "force", false)
		includeASCII := argBool(req, "include_ascii", true)

		if cfg.HasSessionCookie() && !force {
			return textJSON(map[string]any{
				"ok":         true,
				"phase":      "already_logged_in",
				"logged_in":  true,
				"message":    "session already present; set force=true to re-login",
				"how_to_use": "User is logged in. No QR needed.",
			})
		}

		// Reload cfg path in case of concurrent CLI login (do not copy Config: contains Mutex)
		if reloaded, err := config.Load(cfg.Path()); err == nil {
			cfg = reloaded
			_ = device.EnsureDevice(cfg, "")
			c = client.New(cfg)
			mgr.BindClient(c)
		}

		dir := filepath.Dir(cfg.Path())
		_ = os.MkdirAll(dir, 0o700)
		pngPath := filepath.Join(dir, "mcp-qrcode.png")
		urlPath := filepath.Join(dir, "mcp-qrcode.url.txt")

		sess, err := auth.StartQRLogin(cfg, auth.Options{
			QRPngPath:  pngPath,
			QRURLPath:  urlPath,
			Timeout:    3 * time.Minute,
			PrintASCII: false,
			Quiet:      true,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		loginSess = sess

		out := map[string]any{
			// Explicit labels for agents — do not confuse these fields
			"ok":    true,
			"phase": "qr_ready",
			"logged_in": false,
			"message": "Show qr_url and/or qr_ascii to the user; then poll with goquark_login_poll until phase=logged_in",

			// --- QR payload (explicit names) ---
			"qr_url": map[string]any{
				"type":        "login_qr_link",
				"description": "Quark scan-login URL. User opens Quark APP → search-box camera → scan this link/QR.",
				"value":       sess.QRURL,
			},
			"qr_content": map[string]any{
				"type":        "login_qr_payload_string",
				"description": "Same string encoded inside the QR image (identical to qr_url.value).",
				"value":       sess.QRURL,
			},
			"qr_png_path": map[string]any{
				"type":        "local_png_file",
				"description": "Local PNG of the QR (host filesystem; agent may open if same machine).",
				"value":       sess.QRPngPath,
			},
			"how_to_scan": "夸克 APP → 顶部搜索框旁相机 → 扫码。扫描对象是 qr_url / qr_content，不是普通网页链接。",
			"next_step":  "Call goquark_login_poll every few seconds until phase is logged_in, expired, or timeout.",
		}
		if includeASCII && sess.QRASCII != "" {
			out["qr_ascii"] = map[string]any{
				"type":        "login_qr_terminal_art",
				"description": "Half-block ASCII QR for terminal display. Paste into chat/terminal for the user to scan from screen.",
				"value":       sess.QRASCII,
			}
		}
		return textJSON(out)
	})

	s.AddTool(mcp.NewTool("goquark_login_poll",
		mcp.WithDescription("Poll active QR login started by goquark_login. Returns phase: waiting_scan | logged_in | expired | timeout | error. On logged_in, session is saved and subsequent tools work."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if loginSess == nil || loginSess.Token == "" {
			// maybe already logged in via CLI
			if cfg.HasSessionCookie() {
				return textJSON(map[string]any{
					"phase":     "logged_in",
					"logged_in": true,
					"message":   "session present (no active QR session)",
				})
			}
			return mcp.NewToolResultError("no active login session; call goquark_login first"), nil
		}
		res, err := auth.PollQRLogin(cfg, loginSess)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// Refresh client after successful login
		if res.LoggedIn {
			if reloaded, err := config.Load(cfg.Path()); err == nil {
				cfg = reloaded
			}
			c = client.New(cfg)
			mgr.BindClient(c)
		}
		out := map[string]any{
			"phase":     res.Phase,
			"logged_in": res.LoggedIn,
			"message":   res.Message,
		}
		// Echo QR with labels so agent can re-show while waiting
		if res.QRURL != "" {
			out["qr_url"] = map[string]any{
				"type":        "login_qr_link",
				"description": "Still valid scan URL while phase=waiting_scan",
				"value":       res.QRURL,
			}
			out["qr_content"] = map[string]any{
				"type":        "login_qr_payload_string",
				"description": "QR payload string (same as qr_url.value)",
				"value":       res.QRURL,
			}
		}
		if res.QRASCII != "" {
			out["qr_ascii"] = map[string]any{
				"type":        "login_qr_terminal_art",
				"description": "Terminal QR art",
				"value":       res.QRASCII,
			}
		}
		if res.LoggedIn {
			out["user"] = auth.UserInfoFromSession(cfg)
			out["next_step"] = "Login complete. Use goquark_ls / goquark_download / goquark_whoami."
		}
		return textJSON(out)
	})

	s.AddTool(mcp.NewTool("goquark_device",
		mcp.WithDescription("Show local device profile used for Quark PC-compatible requests (machine_id, name, model). Does not call the network."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		b, _ := json.MarshalIndent(map[string]any{
			"device":      cfg.Device,
			"client_info": device.ClientInfoString(cfg.Device),
			"config":      cfg.Path(),
		}, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})

	s.AddTool(mcp.NewTool("goquark_whoami",
		mcp.WithDescription("Get logged-in Quark account info plus app version and update status. Requires prior `goquark login`."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := cfg.RequireLogin(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		info, err := api.UserInfo(c)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := map[string]any{
			"user":    info,
			"version": version,
			"device": map[string]string{
				"device_name": cfg.Device.DeviceName,
				"model":       cfg.Device.Model,
			},
			"session": map[string]string{
				"login_way":    cfg.Session.LoginWay,
				"logged_in_at": cfg.Session.LoggedInAt,
			},
		}
		cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		if res, err := update.Check(cctx, version, update.RepoFromEnv()); err == nil {
			out["update"] = res
			if res.UpdateAvailable && res.CanApply {
				out["apply_hint"] = "call goquark_update with apply=true, or CLI: goquark update --yes"
			} else if res.UpdateAvailable {
				out["apply_hint"] = "new version exists but no matching asset (self-built?); open release_url"
			}
		} else {
			out["update_error"] = err.Error()
		}
		return textJSON(out)
	})

	// ---- browse ----

	s.AddTool(mcp.NewTool("goquark_ls",
		mcp.WithDescription("List a drive directory. Returns {path,fid,list,count}. Use path like / or /docs."),
		mcp.WithString("path", mcp.Description("Remote path, default /"), mcp.DefaultString("/")),
		mcp.WithNumber("page", mcp.Description("Page number (1-based), default 1"), mcp.DefaultNumber(1)),
		mcp.WithNumber("size", mcp.Description("Page size (1-200), default 100"), mcp.DefaultNumber(100)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := cfg.RequireLogin(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		path := argString(req, "path", "/")
		if path == "" {
			path = "/"
		}
		page := argInt(req, "page", 1)
		size := argInt(req, "size", 100)
		if page < 1 {
			page = 1
		}
		if size < 1 {
			size = 100
		}
		if size > 200 {
			size = 200
		}
		fid, isDir, err := api.ResolvePath(c, path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !isDir {
			return textJSON(map[string]any{
				"path": path,
				"fid":  fid,
				"dir":  false,
				"list": []any{},
				"note": "path is a file; use goquark_stat for metadata",
			})
		}
		entries, err := api.ListDir(c, fid, page, size)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return textJSON(map[string]any{
			"path":  path,
			"fid":   fid,
			"dir":   true,
			"page":  page,
			"size":  size,
			"count": len(entries),
			"list":  entries,
		})
	})

	s.AddTool(mcp.NewTool("goquark_stat",
		mcp.WithDescription("Resolve a remote path or fid to metadata (fid, name, size, dir). Path examples: /a/b.pdf or bare fid."),
		mcp.WithString("path", mcp.Description("Remote path starting with / OR bare fid")),
		mcp.WithString("remote", mcp.Description("Alias of path (path or fid)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := cfg.RequireLogin(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		remote := argString(req, "path", "")
		if remote == "" {
			remote = argString(req, "remote", "")
		}
		if remote == "" {
			return mcp.NewToolResultError("path or remote required"), nil
		}
		info, err := resolveRemote(c, remote)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return textJSON(info)
	})

	// ---- download ----

	s.AddTool(mcp.NewTool("goquark_download",
		mcp.WithDescription("Download a remote file. remote=path or fid. local optional (defaults to download_dir/basename). wait=true (default) blocks until done; wait=false enqueues and returns task id."),
		mcp.WithString("remote", mcp.Required(), mcp.Description("Remote path (e.g. /docs/a.pdf) or file fid")),
		mcp.WithString("local", mcp.Description("Local destination file path; default under download_dir")),
		mcp.WithBoolean("wait", mcp.Description("If true (default), wait for completion. If false, enqueue and return task id."), mcp.DefaultBool(true)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := cfg.RequireLogin(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		remote, err := req.RequireString("remote")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		local := argString(req, "local", "")
		wait := argBool(req, "wait", true)

		info, err := resolveRemote(c, remote)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if info["dir"] == true {
			return mcp.NewToolResultError("remote is a directory; pick a file"), nil
		}
		fid, _ := info["fid"].(string)
		name, _ := info["name"].(string)
		if name == "" {
			name = fid
		}

		urlStr, size, err := api.GetDownloadURL(c, fid)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if local == "" || local == "." {
			dir := cfg.EffectiveDownloadDir()
			_ = os.MkdirAll(dir, 0o755)
			base := name
			if base == "" || base == "/" || base == "." {
				base = "file-" + shortID(fid)
			}
			local = filepath.Join(dir, filepath.Base(base))
		}
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil && !os.IsExist(err) {
			return mcp.NewToolResultError(err.Error()), nil
		}

		mgr.BindClient(c)
		mgr.SetDestDir(cfg.EffectiveDownloadDir())
		task, err := mgr.EnqueueEx(download.EnqueueOpts{
			Name: filepath.Base(local),
			URL:  urlStr,
			Dest: local,
			FID:  fid,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out := map[string]any{
			"task_id":    task.ID,
			"fid":        fid,
			"name":       name,
			"local":      local,
			"size_api":   size,
			"enqueued":   true,
			"wait":       wait,
			"status":     string(task.Status),
		}
		if !wait {
			return textJSON(out)
		}

		// Wait until terminal state or context cancel
		deadline := time.Now().Add(6 * time.Hour)
		for {
			if ctx.Err() != nil {
				return mcp.NewToolResultError("download cancelled: " + ctx.Err().Error()), nil
			}
			if time.Now().After(deadline) {
				return mcp.NewToolResultError("download wait timeout"), nil
			}
			snap := findTask(mgr, task.ID)
			if snap == nil {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			switch snap.Status {
			case download.StatusDone:
				fi, _ := os.Stat(local)
				sz := int64(0)
				if fi != nil {
					sz = fi.Size()
				}
				out["status"] = string(snap.Status)
				out["size_local"] = sz
				out["percent"] = snap.Percent
				out["ok"] = true
				return textJSON(out)
			case download.StatusError, download.StatusCancel:
				msg := snap.Error
				if msg == "" {
					msg = string(snap.Status)
				}
				return mcp.NewToolResultError(fmt.Sprintf("download %s: %s", snap.Status, msg)), nil
			}
			time.Sleep(350 * time.Millisecond)
		}
	})

	s.AddTool(mcp.NewTool("goquark_downloads",
		mcp.WithDescription("List download tasks (queue + history): id, name, status, percent, speed, error, dest."),
		mcp.WithBoolean("active_only", mcp.Description("If true, only queued/running/paused tasks"), mcp.DefaultBool(false)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		activeOnly := argBool(req, "active_only", false)
		list := mgr.List()
		items := make([]map[string]any, 0, len(list))
		for _, t := range list {
			if activeOnly {
				switch t.Status {
				case download.StatusQueued, download.StatusRunning, download.StatusPaused,
					download.StatusPausing, download.StatusFinalizing:
					// keep
				default:
					continue
				}
			}
			items = append(items, map[string]any{
				"id":      t.ID,
				"name":    t.Name,
				"status":  string(t.Status),
				"percent": t.Percent,
				"speed":   t.Speed,
				"done":    t.Done,
				"total":   t.Total,
				"error":   t.Error,
				"dest":    t.Dest,
				"fid":     t.FID,
			})
		}
		return textJSON(map[string]any{
			"count": len(items),
			"tasks": items,
		})
	})

	s.AddTool(mcp.NewTool("goquark_download_control",
		mcp.WithDescription("Control download tasks: action=pause|resume|cancel|clear_done. Optional id for single task; omit id to apply to all matching."),
		mcp.WithString("action", mcp.Required(), mcp.Description("pause | resume | cancel | clear_done")),
		mcp.WithString("id", mcp.Description("Task id from goquark_downloads / goquark_download")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		action, err := req.RequireString("action")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		action = strings.ToLower(strings.TrimSpace(action))
		id := strings.TrimSpace(argString(req, "id", ""))

		var n int
		var ok bool
		switch action {
		case "pause":
			if id != "" {
				ok = mgr.PauseID(id)
				if ok {
					n = 1
				}
			} else {
				n = mgr.PauseAll()
				ok = n > 0
			}
		case "resume":
			if id != "" {
				ok = mgr.ResumeID(id)
				if ok {
					n = 1
				}
			} else {
				// resume all paused/error via list
				for _, t := range mgr.List() {
					if t.Status == download.StatusPaused || t.Status == download.StatusError {
						if mgr.ResumeID(t.ID) {
							n++
							ok = true
						}
					}
				}
			}
		case "cancel":
			if id != "" {
				ok = mgr.CancelID(id)
				if ok {
					n = 1
				}
			} else {
				n = mgr.CancelAll()
				ok = n > 0
			}
		case "clear_done", "clear", "clear_completed":
			n = mgr.ClearFinished()
			ok = true
		default:
			return mcp.NewToolResultError("action must be pause|resume|cancel|clear_done"), nil
		}
		return textJSON(map[string]any{
			"action":  action,
			"id":      id,
			"ok":      ok,
			"affected": n,
		})
	})

	// ---- update (passive by default) ----

	s.AddTool(mcp.NewTool("goquark_check_update",
		mcp.WithDescription("Check GitHub Releases for a newer GoQuark version (passive; does not download)."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		res, err := update.Check(cctx, version, update.RepoFromEnv())
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return textJSON(res)
	})

	s.AddTool(mcp.NewTool("goquark_update",
		mcp.WithDescription("Apply self-update from latest GitHub Release if a matching binary exists. apply=false (default) checks only; apply=true downloads and replaces the binary."),
		mcp.WithBoolean("apply", mcp.Description("If true, download and replace the binary. Default false (check only)."), mcp.DefaultBool(false)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		apply := argBool(req, "apply", false)
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		res, err := update.Check(cctx, version, update.RepoFromEnv())
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if apply && res.UpdateAvailable {
			if !res.CanApply {
				b, _ := json.MarshalIndent(res, "", "  ")
				return mcp.NewToolResultError("update available but cannot apply: " + res.Reason + "\n" + string(b)), nil
			}
			if err := update.Apply(cctx, res); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}
		return textJSON(res)
	})

	return server.ServeStdio(s)
}

// --- helpers ---

func textJSON(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

func argString(req mcp.CallToolRequest, key, def string) string {
	switch args := req.Params.Arguments.(type) {
	case map[string]any:
		if v, ok := args[key]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	// Prefer typed helpers when available
	if s, err := req.RequireString(key); err == nil && s != "" {
		return s
	}
	return def
}

func argBool(req mcp.CallToolRequest, key string, def bool) bool {
	switch args := req.Params.Arguments.(type) {
	case map[string]any:
		switch v := args[key].(type) {
		case bool:
			return v
		case string:
			s := strings.ToLower(strings.TrimSpace(v))
			if s == "true" || s == "1" || s == "yes" {
				return true
			}
			if s == "false" || s == "0" || s == "no" {
				return false
			}
		case float64:
			return v != 0
		case int:
			return v != 0
		}
	}
	return def
}

func argInt(req mcp.CallToolRequest, key string, def int) int {
	switch args := req.Params.Arguments.(type) {
	case map[string]any:
		switch v := args[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case int64:
			return int(v)
		case json.Number:
			i, _ := v.Int64()
			return int(i)
		case string:
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
				return n
			}
		}
	}
	return def
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func findTask(mgr *download.Manager, id string) *download.Snapshot {
	for _, t := range mgr.List() {
		if t.ID == id {
			tt := t
			return &tt
		}
	}
	return nil
}

// resolveRemote accepts path (/a/b) or bare fid.
func resolveRemote(c *client.Client, remote string) (map[string]any, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return nil, fmt.Errorf("empty remote")
	}

	// Path form
	if strings.HasPrefix(remote, "/") {
		fid, isDir, err := api.ResolvePath(c, remote)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(remote)
		if name == "/" || name == "." {
			name = ""
		}
		out := map[string]any{
			"input": remote,
			"path":  remote,
			"fid":   fid,
			"dir":   isDir,
			"name":  name,
		}
		if !isDir {
			// try list parent for size
			parent := filepath.Dir(remote)
			if parent == "" {
				parent = "/"
			}
			pfid, pdir, err := api.ResolvePath(c, parent)
			if err == nil && pdir {
				entries, err := api.ListDir(c, pfid, 1, 200)
				if err == nil {
					base := filepath.Base(remote)
					for _, e := range entries {
						if e.Name == base || e.FID == fid {
							out["name"] = e.Name
							out["size"] = e.Size
							out["dir"] = e.Dir
							break
						}
					}
				}
			}
		}
		return out, nil
	}

	// Bare fid: look up name via... we only know fid; size needs download API or parent walk.
	// Return minimal metadata; agents can still download by fid.
	return map[string]any{
		"input": remote,
		"fid":   remote,
		"dir":   false,
		"name":  "",
		"note":  "resolved as bare fid (no path walk)",
	}, nil
}
