package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ButterFuture/GoQuark/internal/api"
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

	s := server.NewMCPServer("goquark", version)

	s.AddTool(mcp.NewTool("goquark_device",
		mcp.WithDescription("Show simulated Mac device fingerprint (machine_id, name, model)"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		b, _ := json.MarshalIndent(map[string]any{
			"device":      cfg.Device,
			"client_info": device.ClientInfoString(cfg.Device),
			"config":      cfg.Path(),
		}, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})

	s.AddTool(mcp.NewTool("goquark_whoami",
		mcp.WithDescription("Get logged-in Quark user info (includes app version + update status)"),
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
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})

	s.AddTool(mcp.NewTool("goquark_ls",
		mcp.WithDescription("List a drive path"),
		mcp.WithString("path", mcp.Description("Remote path, default /"), mcp.DefaultString("/")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := cfg.RequireLogin(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		path, _ := req.RequireString("path")
		if path == "" {
			path = "/"
		}
		fid, isDir, err := api.ResolvePath(c, path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !isDir {
			return mcp.NewToolResultText(fmt.Sprintf("file fid=%s path=%s", fid, path)), nil
		}
		entries, err := api.ListDir(c, fid, 1, 100)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		b, _ := json.MarshalIndent(entries, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})

	s.AddTool(mcp.NewTool("goquark_download",
		mcp.WithDescription("High-speed download a remote file to local path"),
		mcp.WithString("remote", mcp.Required(), mcp.Description("Remote path or fid")),
		mcp.WithString("local", mcp.Required(), mcp.Description("Local destination path")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := cfg.RequireLogin(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		remote, err := req.RequireString("remote")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		local, err := req.RequireString("local")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		fid := remote
		if len(remote) > 0 && (remote[0] == '/' || remote == "") {
			var isDir bool
			fid, isDir, err = api.ResolvePath(c, remote)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if isDir {
				return mcp.NewToolResultError("remote is directory"), nil
			}
		}
		urlStr, size, err := api.GetDownloadURL(c, fid)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := download.File(ctx, c, urlStr, local, download.Options{}); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		fi, _ := os.Stat(local)
		return mcp.NewToolResultText(fmt.Sprintf("ok size_api=%d size_local=%d path=%s", size, fi.Size(), local)), nil
	})

	// Passive update tools — never auto-run on MCP start.
	s.AddTool(mcp.NewTool("goquark_check_update",
		mcp.WithDescription("Check GitHub Releases for a newer GoQuark version (passive; does not download)"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		res, err := update.Check(cctx, version, update.RepoFromEnv())
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})

	s.AddTool(mcp.NewTool("goquark_update",
		mcp.WithDescription("Apply self-update from latest GitHub Release if a matching binary exists. If only a newer tag exists without an asset for this platform (e.g. self-built), reports cannot_apply but still lists the new version."),
		mcp.WithBoolean("apply", mcp.Description("If true, download and replace the binary. Default false (check only)."), mcp.DefaultBool(false)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		apply := false
		switch args := req.Params.Arguments.(type) {
		case map[string]any:
			switch v := args["apply"].(type) {
			case bool:
				apply = v
			case string:
				apply = v == "true" || v == "1" || v == "yes"
			}
		}
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
		b, _ := json.MarshalIndent(res, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})

	return server.ServeStdio(s)
}
