// Package update checks GitHub Releases and applies self-updates.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	// SumsAssetName is the checksum manifest CI publishes next to the binaries.
	// A release without it cannot be self-applied.
	SumsAssetName = "SHA256SUMS"
)

// Result of a check / apply attempt.
type Result struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url,omitempty"`
	AssetName       string `json:"asset_name,omitempty"`
	AssetURL        string `json:"asset_url,omitempty"`
	CanApply        bool   `json:"can_apply"` // matching binary asset + checksum manifest exist
	Reason          string `json:"reason,omitempty"`
	Applied         bool   `json:"applied,omitempty"`
	BinaryPath      string `json:"binary_path,omitempty"`

	// Repo the release was read from. CustomRepo is true when it is not
	// DefaultRepo; Apply refuses those unless the caller opts in explicitly, so
	// GOQUARK_UPDATE_REPO alone can never redirect a binary replacement.
	Repo       string `json:"repo,omitempty"`
	CustomRepo bool   `json:"custom_repo,omitempty"`

	// SumsURL is the SHA256SUMS asset used for verification; SHA256 is the
	// digest of the binary that was actually installed.
	SumsURL string `json:"sums_url,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
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

// repoPattern matches "owner/name" and nothing else. The repo is interpolated
// into an api.github.com path, so it must not carry a scheme, host, traversal
// or query string.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ValidRepo reports whether r is a safe "owner/name" repository reference.
func ValidRepo(r string) bool {
	return repoPattern.MatchString(r) && !strings.Contains(r, "..")
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
	if !ValidRepo(repo) {
		return nil, fmt.Errorf("invalid update repo %q (want owner/name)", repo)
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
		Repo:       repo,
		CustomRepo: repo != DefaultRepo,
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
		if a.Name == SumsAssetName {
			if res.SumsURL == "" {
				res.SumsURL = a.BrowserDownloadURL
			}
			continue
		}
		if res.AssetURL == "" && matchAsset(a.Name, want, latest) {
			res.AssetName = a.Name
			res.AssetURL = a.BrowserDownloadURL
		}
	}
	switch {
	case res.AssetURL == "":
		res.Reason = fmt.Sprintf(
			"new version %s is available, but no release binary for %s/%s (self-built or unsupported platform); download manually: %s",
			latest, runtime.GOOS, runtime.GOARCH, rel.HTMLURL,
		)
	case res.SumsURL == "":
		// Fail closed: replacing our own executable with an unverified
		// download is exactly what the checksum manifest exists to prevent.
		res.Reason = fmt.Sprintf(
			"new version %s publishes no %s; refusing to self-update without an integrity check — download and verify manually: %s",
			latest, SumsAssetName, rel.HTMLURL,
		)
	default:
		res.CanApply = true
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

// githubHost reports whether h is a host GitHub serves the API and releases
// from. Update downloads and the API token are both restricted to these: a
// redirected repo must not be able to hand us a binary from somewhere else, or
// collect GITHUB_TOKEN on the way.
func githubHost(h string) bool {
	h = strings.ToLower(h)
	switch h {
	case "github.com", "www.github.com", "api.github.com", "codeload.github.com":
		return true
	}
	return strings.HasSuffix(h, ".githubusercontent.com")
}

// allowHost gates which hosts the updater will fetch from. It is a var only so
// tests can point the updater at a local server; production always uses
// githubHost. setAuth deliberately does NOT consult it, so a token still only
// ever reaches real GitHub.
var allowHost = githubHost

// newHTTPClient builds the client used for update fetches. Also a var only so
// tests can supply one that trusts a local test server's certificate.
var newHTTPClient = func(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// requireGitHubURL parses raw and rejects anything that is not https on a
// GitHub host.
func requireGitHubURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("bad update url: %w", err)
	}
	if u.Scheme != "https" || !allowHost(u.Hostname()) {
		return nil, fmt.Errorf("refusing update download from non-GitHub url: %s", raw)
	}
	return u, nil
}

// setAuth attaches a GitHub token, but only when the request actually goes to
// GitHub.
func setAuth(req *http.Request) {
	if !githubHost(req.URL.Hostname()) {
		return
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
		return
	}
	if t := os.Getenv("GH_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
}

func fetchLatest(ctx context.Context, repo string) (*ghRelease, error) {
	u, err := requireGitHubURL(fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", UserAgent)
	setAuth(req)
	client := newHTTPClient(30 * time.Second)
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

// ApplyOptions carries opt-ins that must originate from an explicit user
// action rather than from the environment.
type ApplyOptions struct {
	// AllowCustomRepo permits installing a binary built from a repository
	// other than DefaultRepo (goquark update --allow-custom-repo).
	AllowCustomRepo bool
}

// Apply downloads the asset, verifies it against the release SHA256SUMS and
// replaces the current executable.
// On success the process should typically exit so the new binary is used next start.
func Apply(ctx context.Context, res *Result) error {
	return ApplyWithOptions(ctx, res, ApplyOptions{})
}

// ApplyWithOptions is Apply with explicit opt-ins. It refuses to install from a
// non-default repository, and never writes the new binary into place before its
// SHA-256 matches the release manifest.
func ApplyWithOptions(ctx context.Context, res *Result, opt ApplyOptions) error {
	if res == nil || !res.UpdateAvailable {
		return fmt.Errorf("no update available")
	}
	if !res.CanApply || res.AssetURL == "" {
		return fmt.Errorf("%s", res.Reason)
	}
	if res.CustomRepo && !opt.AllowCustomRepo {
		return fmt.Errorf(
			"refusing to replace the binary with a build from %q instead of %s "+
				"(GOQUARK_UPDATE_REPO is set); re-run with --allow-custom-repo if that is intended",
			res.Repo, DefaultRepo,
		)
	}
	if res.SumsURL == "" {
		return fmt.Errorf("no %s for release %s; refusing to self-update unverified", SumsAssetName, res.Latest)
	}

	// Fetch the expected digest first — no point downloading if the manifest
	// does not cover our asset.
	wantSum, err := fetchExpectedSum(ctx, res.SumsURL, res.AssetName)
	if err != nil {
		return err
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
	gotSum, err := downloadFile(ctx, res.AssetURL, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if !strings.EqualFold(gotSum, wantSum) {
		_ = os.Remove(tmp)
		return fmt.Errorf(
			"checksum mismatch for %s: %s lists %s, download is %s — update aborted",
			res.AssetName, SumsAssetName, wantSum, gotSum,
		)
	}
	res.SHA256 = gotSum

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

// fetchExpectedSum reads the SHA256SUMS manifest and returns the digest listed
// for asset. Lines are "<hex>  <name>" or "<hex> *<name>" (sha256sum output).
// An asset the manifest does not cover is an error, not a skipped check.
func fetchExpectedSum(ctx context.Context, sumsURL, asset string) (string, error) {
	u, err := requireGitHubURL(sumsURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	setAuth(req)
	client := newHTTPClient(60 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", SumsAssetName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch %s: %s", SumsAssetName, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return sumForAsset(body, asset)
}

// sumForAsset returns the digest the manifest lists for asset. Lines are
// "<hex>  <name>" or "<hex> *<name>" (sha256sum output). The name is compared
// exactly, so a request for goquark_1.0.2_linux_amd64 can never be satisfied by
// the goquark_1.0.2_linux_amd64.exe line that contains it as a prefix.
func sumForAsset(manifest []byte, asset string) (string, error) {
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if filepath.Base(strings.TrimPrefix(fields[1], "*")) != asset {
			continue
		}
		sum := strings.ToLower(fields[0])
		if len(sum) != 64 {
			return "", fmt.Errorf("%s: malformed digest for %s", SumsAssetName, asset)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return "", fmt.Errorf("%s: malformed digest for %s", SumsAssetName, asset)
		}
		return sum, nil
	}
	return "", fmt.Errorf("%s does not list %s; refusing to self-update unverified", SumsAssetName, asset)
}

// downloadFile fetches url into dest and returns the hex SHA-256 of the bytes
// it wrote, so the caller can verify before installing.
func downloadFile(ctx context.Context, rawURL, dest string) (string, error) {
	u, err := requireGitHubURL(rawURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	setAuth(req)
	client := newHTTPClient(10 * time.Minute)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("download %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RepoFromEnv returns GOQUARK_UPDATE_REPO or DefaultRepo. A malformed value
// falls back to DefaultRepo rather than being interpolated into an API URL;
// a well-formed override still cannot drive Apply on its own (see ApplyOptions).
func RepoFromEnv() string {
	r := strings.TrimSpace(os.Getenv("GOQUARK_UPDATE_REPO"))
	if r == "" || !ValidRepo(r) {
		return DefaultRepo
	}
	return r
}
