// Package update checks GitHub Releases and applies self-updates.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"sync/atomic"
	"time"
	"unicode"
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

	// Notes is the release body, sanitized for display on a terminal. It is
	// remote, attacker-influenced text: never print it without going through
	// sanitizeNotes first.
	Notes string `json:"notes,omitempty"`

	// PendingCleanup is the replaced binary that could not be deleted yet —
	// Windows holds the image of a running process open. CleanupLeftovers
	// removes it on the next start.
	PendingCleanup string `json:"pending_cleanup,omitempty"`
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
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

// MaxNotesBytes caps how much release prose is kept.
const MaxNotesBytes = 16 << 10

// ansiEscape matches ANSI escape sequences, including the CSI forms used for
// colour and cursor movement.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b[@-Z\\\\-_]|\x1b.")

// sanitizeNotes makes a GitHub release body safe to print on a terminal. The
// body is whatever the release author wrote, so it is treated as untrusted:
// escape sequences could otherwise move the cursor, repaint the screen or
// hide text, and an oversized body could bury the dialog around it.
func sanitizeNotes(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = ansiEscape.ReplaceAllString(s, "")

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > MaxNotesBytes {
		out = out[:MaxNotesBytes]
		if i := strings.LastIndexByte(out, '\n'); i > 0 {
			out = out[:i]
		}
		out = strings.TrimSpace(out) + "\n…"
	}
	return out
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
		Notes:      sanitizeNotes(rel.Body),
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

	// Claim the in-process slot before any network work, so a duplicate
	// goroutine stops here rather than downloading the release a second time.
	if !applyInFlight.CompareAndSwap(false, true) {
		return fmt.Errorf("%w: this process is already updating", ErrUpdateInProgress)
	}
	defer applyInFlight.Store(false)

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

	dir := filepath.Dir(exe)

	// One updater at a time. Two concurrent Apply calls — the TUI and the MCP
	// server, say — both staged to the same exe+".new" path: whichever renamed
	// it into place first left the other chmod'ing a file that was no longer
	// there, which surfaced as "chmod ...goquark.new: no such file or
	// directory" even though the update had in fact succeeded.
	unlock, err := lockUpdate(dir)
	if err != nil {
		return err
	}
	defer unlock()

	// Stage under a unique name beside the target, so the install is a rename
	// within one filesystem and no two runs can collide on the same path.
	stage, err := os.CreateTemp(dir, stagePrefix+"*")
	if err != nil {
		return fmt.Errorf("stage the download in %s: %w", dir, err)
	}
	tmp := stage.Name()
	_ = stage.Close()
	installed := false
	defer func() {
		if !installed {
			_ = os.Remove(tmp)
		}
	}()

	gotSum, err := downloadFile(ctx, res.AssetURL, tmp)
	if err != nil {
		return err
	}
	if !strings.EqualFold(gotSum, wantSum) {
		return fmt.Errorf(
			"checksum mismatch for %s: %s lists %s, download is %s — update aborted",
			res.AssetName, SumsAssetName, wantSum, gotSum,
		)
	}
	res.SHA256 = gotSum

	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf("make the staged binary executable: %w", err)
	}

	pending, err := swapBinary(tmp, exe)
	if err != nil {
		return err
	}
	installed = true
	res.Applied = true
	res.PendingCleanup = pending
	return nil
}

// removeFile and renameFile are os.Remove and os.Rename, replaced in tests so
// the platform-specific failures this function exists to handle — Windows
// refusing to delete the image of a running process, an install that cannot
// be moved into place — can be exercised anywhere.
var (
	removeFile = os.Remove
	renameFile = os.Rename
)

// swapBinary installs the staged binary at exe, moving the current one aside
// first.
//
// Windows refuses to delete or write over the image of a running process, but
// it does allow renaming it — so the old binary is moved out of the way and
// the new one takes its place. The running binary is never unlinked: if the
// install then failed, the user would be left with nothing to run at all.
//
// The returned path is the old image when it could not be deleted. On Windows
// that is the normal outcome rather than a failure, and CleanupLeftovers
// removes it the next time GoQuark starts.
func swapBinary(tmp, exe string) (pending string, err error) {
	bak := exe + bakSuffix
	_ = removeFile(bak)
	if err := renameFile(exe, bak); err != nil {
		return "", fmt.Errorf("move the running binary aside (%s): %w", exe, err)
	}
	if err := renameFile(tmp, exe); err != nil {
		_ = renameFile(bak, exe) // put ourselves back
		return "", fmt.Errorf("install the new binary at %s: %w", exe, err)
	}
	if err := removeFile(bak); err != nil {
		return bak, nil
	}
	return "", nil
}

// ErrUpdateInProgress reports that an update is already running: another
// goroutine in this process, or another GoQuark process on this machine.
// Callers can treat it as "wait and retry" rather than as a failure.
var ErrUpdateInProgress = errors.New("an update is already in progress")

// applyInFlight guards an apply within this process.
//
// The on-disk lock below covers other processes, but it is the wrong tool for
// our own goroutines: two of them would serialise on it only by one failing,
// and a goroutine arriving just as the first released the lock would download
// and install the whole release a second time. A process-local claim, taken
// before any network work, keeps that to one attempt.
var applyInFlight atomic.Bool

const (
	stagePrefix = ".goquark-update-"
	bakSuffix   = ".bak"
	lockName    = ".goquark-update.lock"
	// lockStale is how long a lock file may sit before it is treated as
	// abandoned by a crashed updater rather than held by a live one.
	lockStale = 15 * time.Minute
)

// lockUpdate takes a best-effort exclusive lock on updates in dir, so two
// processes cannot stage and swap the binary at the same time. The returned
// function releases it.
func lockUpdate(dir string) (func(), error) {
	path := filepath.Join(dir, lockName)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("lock updates in %s: %w", dir, err)
		}
		st, statErr := os.Stat(path)
		if statErr != nil || time.Since(st.ModTime()) < lockStale {
			return nil, fmt.Errorf(
				"%w: another GoQuark process holds %s; try again once it has finished",
				ErrUpdateInProgress, path,
			)
		}
		_ = os.Remove(path) // abandoned by an updater that died mid-run
	}
	return nil, fmt.Errorf("%w: lock %s could not be claimed", ErrUpdateInProgress, path)
}

// CleanupLeftovers removes what a previous update could not. On Windows the
// replaced image cannot be deleted while the old process still runs it, and a
// run that died mid-download leaves a staged file behind. Safe to call on
// every start: it never touches the current executable, and it leaves recent
// staging files alone in case an update is in flight.
func CleanupLeftovers() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)

	// Another GoQuark may be mid-update right now, in which case the backup it
	// just moved aside is the only copy it can roll back to. Leave everything
	// alone until that run has finished — ours included, since the on-disk
	// lock only appears partway into an apply.
	if applyInFlight.Load() {
		return
	}
	if st, err := os.Stat(filepath.Join(dir, lockName)); err == nil {
		if time.Since(st.ModTime()) < lockStale {
			return
		}
		_ = os.Remove(filepath.Join(dir, lockName))
	}

	_ = os.Remove(exe + bakSuffix)
	_ = os.Remove(exe + ".new") // staging path used up to v1.0.4

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), stagePrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < time.Hour {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
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
