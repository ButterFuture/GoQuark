// Package update checks GitHub Releases and applies self-updates.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultRepo is the public release repository (owner/name).
	DefaultRepo = "ButterFuture/GoQuark"
	// UserAgent for GitHub API.
	UserAgent = "GoQuark-Updater"
)

// Result of a check / apply attempt.
type Result struct {
	Current       string `json:"current"`
	Latest        string `json:"latest"`
	UpdateAvailable bool `json:"update_available"`
	ReleaseURL    string `json:"release_url,omitempty"`
	AssetName     string `json:"asset_name,omitempty"`
	AssetURL      string `json:"asset_url,omitempty"`
	CanApply      bool   `json:"can_apply"` // matching binary asset exists
	Reason        string `json:"reason,omitempty"`
	Applied       bool   `json:"applied,omitempty"`
	BinaryPath    string `json:"binary_path,omitempty"`
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// NormalizeVersion strips leading "v" and spaces.
func NormalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	return v
}

// Compare returns -1 if a<b, 0 if equal, 1 if a>b (semver-ish major.minor.patch).
// Non-numeric / "dev" / empty are treated as 0.0.0-dev (always older than real releases).
func Compare(a, b string) int {
	ap := parseVer(NormalizeVersion(a))
	bp := parseVer(NormalizeVersion(b))
	for i := 0; i < 3; i++ {
		if ap[i] < bp[i] {
			return -1
		}
		if ap[i] > bp[i] {
			return 1
		}
	}
	// equal numbers: prefer pure release over "dev"
	aDev := isDevLike(a)
	bDev := isDevLike(b)
	if aDev && !bDev {
		return -1
	}
	if !aDev && bDev {
		return 1
	}
	return 0
}

func isDevLike(v string) bool {
	n := strings.ToLower(NormalizeVersion(v))
	return n == "" || n == "dev" || n == "unknown" || strings.Contains(n, "dev") || strings.Contains(n, "dirty")
}

func parseVer(v string) [3]int {
	var out [3]int
	if isDevLike(v) && !strings.ContainsAny(v, "0123456789") {
		return out
	}
	// take major.minor.patch prefix only
	v = strings.Split(v, "-")[0]
	v = strings.Split(v, "+")[0]
	parts := strings.Split(v, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}

// ExpectedAssetName matches release naming: goquark_1.0.0_linux_amd64[.exe]
func ExpectedAssetName(version string) string {
	ver := NormalizeVersion(version)
	name := fmt.Sprintf("goquark_%s_%s_%s", ver, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Check queries GitHub for the latest release and compares to current.
func Check(ctx context.Context, current, repo string) (*Result, error) {
	if repo == "" {
		repo = DefaultRepo
	}
	if current == "" {
		current = "dev"
	}
	rel, err := fetchLatest(ctx, repo)
	if err != nil {
		return nil, err
	}
	latest := NormalizeVersion(rel.TagName)
	cur := NormalizeVersion(current)
	res := &Result{
		Current:    cur,
		Latest:     latest,
		ReleaseURL: rel.HTMLURL,
	}
	if Compare(cur, latest) >= 0 {
		res.UpdateAvailable = false
		res.Reason = "already up to date"
		return res, nil
	}
	res.UpdateAvailable = true
	want := ExpectedAssetName(latest)
	// also accept goquark_{os}_{arch} without version, or with .exe variants
	for _, a := range rel.Assets {
		if matchAsset(a.Name, want, latest) {
			res.AssetName = a.Name
			res.AssetURL = a.BrowserDownloadURL
			res.CanApply = true
			break
		}
	}
	if !res.CanApply {
		res.Reason = fmt.Sprintf(
			"new version %s is available, but no release binary for %s/%s (self-built or unsupported platform); download manually: %s",
			latest, runtime.GOOS, runtime.GOARCH, rel.HTMLURL,
		)
	}
	return res, nil
}

func matchAsset(name, want, latest string) bool {
	if name == want {
		return true
	}
	// case-insensitive
	if strings.EqualFold(name, want) {
		return true
	}
	// alternate: goquark-linux-amd64
	alt := fmt.Sprintf("goquark-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		alt += ".exe"
	}
	if strings.EqualFold(name, alt) {
		return true
	}
	// contains goos/goarch and version
	ln := strings.ToLower(name)
	if strings.Contains(ln, strings.ToLower(runtime.GOOS)) &&
		strings.Contains(ln, strings.ToLower(runtime.GOARCH)) &&
		(strings.Contains(ln, strings.ToLower(NormalizeVersion(latest))) || strings.Contains(ln, "goquark")) {
		// prefer names that look like our convention
		if strings.HasPrefix(ln, "goquark") {
			return true
		}
	}
	return false
}

func fetchLatest(ctx context.Context, repo string) (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", UserAgent)
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	} else if t := os.Getenv("GH_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github api %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("no releases found for %s", repo)
	}
	return &rel, nil
}

// Apply downloads the asset and replaces the current executable.
// On success the process should typically exit so the new binary is used next start.
func Apply(ctx context.Context, res *Result) error {
	if res == nil || !res.UpdateAvailable {
		return fmt.Errorf("no update available")
	}
	if !res.CanApply || res.AssetURL == "" {
		return fmt.Errorf("%s", res.Reason)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	res.BinaryPath = exe

	tmp := exe + ".new"
	if err := downloadFile(ctx, res.AssetURL, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// replace: try rename over; on Windows may need remove first
	bak := exe + ".bak"
	_ = os.Remove(bak)
	if err := os.Rename(exe, bak); err != nil {
		// if can't move running binary, try direct overwrite on unix via rename new→exe after unlink
		if err2 := os.Remove(exe); err2 != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("replace binary: %v (backup: %v)", err2, err)
		}
	}
	if err := os.Rename(tmp, exe); err != nil {
		// restore bak
		_ = os.Rename(bak, exe)
		_ = os.Remove(tmp)
		return fmt.Errorf("install new binary: %w", err)
	}
	_ = os.Remove(bak) // best-effort; linux allows deleting running bak
	res.Applied = true
	return nil
}

func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("download %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// RepoFromEnv returns GOQUARK_UPDATE_REPO or DefaultRepo.
func RepoFromEnv() string {
	if r := strings.TrimSpace(os.Getenv("GOQUARK_UPDATE_REPO")); r != "" {
		return r
	}
	return DefaultRepo
}
