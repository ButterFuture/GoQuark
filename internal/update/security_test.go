package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidRepoRejectsInjection(t *testing.T) {
	for _, r := range []string{
		"../../etc/passwd", "https://evil.com/x", "owner/name/extra",
		"owner name", "owner/na?me", "", "a/../b",
	} {
		if ValidRepo(r) {
			t.Errorf("ValidRepo(%q) = true, want false", r)
		}
	}
	for _, r := range []string{"ButterFuture/GoQuark", "owner/name-1.2_x"} {
		if !ValidRepo(r) {
			t.Errorf("ValidRepo(%q) = false, want true", r)
		}
	}
}

func TestRepoFromEnvFallsBackOnGarbage(t *testing.T) {
	t.Setenv("GOQUARK_UPDATE_REPO", "https://evil.com/pwn")
	if got := RepoFromEnv(); got != DefaultRepo {
		t.Errorf("RepoFromEnv() = %q, want %q", got, DefaultRepo)
	}
}

func TestRequireGitHubURLRejectsForeignHosts(t *testing.T) {
	for _, u := range []string{
		"https://evil.com/goquark", "http://github.com/x", // plaintext
		"https://github.com.evil.com/x", "https://raw.githubusercontent.com.evil.com/x",
	} {
		if _, err := requireGitHubURL(u); err == nil {
			t.Errorf("requireGitHubURL(%q) accepted, want rejection", u)
		}
	}
	for _, u := range []string{
		"https://github.com/o/r/releases/download/v1/goquark",
		"https://objects.githubusercontent.com/x",
	} {
		if _, err := requireGitHubURL(u); err != nil {
			t.Errorf("requireGitHubURL(%q) = %v, want accept", u, err)
		}
	}
}

// An env-var-redirected repo must not be able to drive a binary replacement.
func TestApplyRefusesCustomRepoWithoutOptIn(t *testing.T) {
	res := &Result{
		UpdateAvailable: true, CanApply: true, Latest: "9.9.9",
		AssetName: "goquark_9.9.9_linux_amd64",
		AssetURL:  "https://github.com/attacker/evil/releases/download/v9.9.9/goquark_9.9.9_linux_amd64",
		SumsURL:   "https://github.com/attacker/evil/releases/download/v9.9.9/SHA256SUMS",
		Repo:      "attacker/evil", CustomRepo: true,
	}
	err := Apply(context.Background(), res)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("Apply() = %v, want refusal", err)
	}
	if res.Applied {
		t.Fatal("Apply() marked Applied on a refused custom repo")
	}
}

// A release with no SHA256SUMS must not be self-applied.
func TestCheckResultWithoutSumsCannotApply(t *testing.T) {
	res := &Result{
		UpdateAvailable: true, CanApply: false, Latest: "9.9.9",
		Reason:   "publishes no SHA256SUMS",
		AssetURL: "https://github.com/o/r/releases/download/v9.9.9/goquark",
	}
	if err := Apply(context.Background(), res); err == nil {
		t.Fatal("Apply() on unverifiable release succeeded, want error")
	}
}

func TestApplyRejectsNonGitHubAsset(t *testing.T) {
	res := &Result{
		UpdateAvailable: true, CanApply: true, Latest: "9.9.9",
		AssetName: "goquark", Repo: DefaultRepo,
		AssetURL: "https://evil.com/goquark",
		SumsURL:  "https://evil.com/SHA256SUMS",
	}
	if err := Apply(context.Background(), res); err == nil ||
		!strings.Contains(err.Error(), "non-GitHub") {
		t.Fatalf("Apply() = %v, want non-GitHub rejection", err)
	}
}

// --- SHA256SUMS parsing: the core of the self-update integrity check ---

const (
	sumA = "1111111111111111111111111111111111111111111111111111111111111111"
	sumB = "2222222222222222222222222222222222222222222222222222222222222222"
)

func TestSumForAssetMatchesNameExactly(t *testing.T) {
	// Real sha256sum output: two spaces for text mode, " *" for binary mode.
	manifest := []byte(
		sumA + "  goquark_1.0.2_linux_amd64\n" +
			sumB + " *goquark_1.0.2_linux_amd64.exe\n")

	got, err := sumForAsset(manifest, "goquark_1.0.2_linux_amd64")
	if err != nil || got != sumA {
		t.Errorf("linux asset: got (%q, %v), want %q", got, err, sumA)
	}
	// The .exe line contains the linux name as a prefix; a match must not leak
	// across, in either direction.
	got, err = sumForAsset(manifest, "goquark_1.0.2_linux_amd64.exe")
	if err != nil || got != sumB {
		t.Errorf("windows asset: got (%q, %v), want %q", got, err, sumB)
	}
}

func TestSumForAssetRejectsUnlistedAndMalformed(t *testing.T) {
	listed := []byte(sumA + "  goquark_1.0.2_linux_amd64\n")

	if _, err := sumForAsset(listed, "goquark_1.0.2_linux_arm64"); err == nil {
		t.Error("unlisted asset accepted, want refusal")
	}
	if _, err := sumForAsset(nil, "goquark_1.0.2_linux_amd64"); err == nil {
		t.Error("empty manifest accepted, want refusal")
	}
	for _, bad := range []string{
		"deadbeef  goquark\n",                   // too short
		strings.Repeat("z", 64) + "  goquark\n", // not hex
	} {
		if _, err := sumForAsset([]byte(bad), "goquark"); err == nil {
			t.Errorf("malformed digest %q accepted, want refusal", bad)
		}
	}
}

// --- download digest + the abort that protects the running binary ---

// testServer serves a fake release over TLS, opening the host and client seams
// for the duration of the test.
func testServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	prevHost, prevClient := allowHost, newHTTPClient
	allowHost = func(string) bool { return true }
	newHTTPClient = func(d time.Duration) *http.Client {
		c := srv.Client()
		c.Timeout = d
		return c
	}
	t.Cleanup(func() {
		allowHost, newHTTPClient = prevHost, prevClient
		srv.Close()
	})
	return srv
}

func TestDownloadFileReportsRealDigest(t *testing.T) {
	payload := []byte("goquark-binary-bytes")
	srv := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	dest := filepath.Join(t.TempDir(), "asset")

	got, err := downloadFile(context.Background(), srv.URL+"/asset", dest)
	if err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(payload)); got != want {
		t.Errorf("digest = %s, want %s", got, want)
	}
	onDisk, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(onDisk, payload) {
		t.Errorf("dest contents = %q (%v), want %q", onDisk, err, payload)
	}
}

// A tampered asset must be refused before the running binary is touched.
func TestApplyAbortsOnChecksumMismatch(t *testing.T) {
	const asset = "goquark_9.9.9_test"
	srv := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, SumsAssetName) {
			// The manifest promises sumA ...
			_, _ = w.Write([]byte(sumA + "  " + asset + "\n"))
			return
		}
		// ... but the served bytes hash to something else entirely.
		_, _ = w.Write([]byte("tampered payload"))
	}))

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	before, err := os.ReadFile(exe)
	if err != nil {
		t.Skipf("cannot read test binary: %v", err)
	}

	res := &Result{
		UpdateAvailable: true, CanApply: true, Latest: "9.9.9",
		AssetName: asset, Repo: DefaultRepo,
		AssetURL: srv.URL + "/" + asset,
		SumsURL:  srv.URL + "/" + SumsAssetName,
	}
	err = Apply(context.Background(), res)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Apply() = %v, want checksum mismatch", err)
	}
	if res.Applied {
		t.Error("Applied set despite mismatch")
	}
	// The executable must be untouched and the staged file cleaned up.
	after, err := os.ReadFile(exe)
	if err != nil || !bytes.Equal(before, after) {
		t.Error("running binary was modified by a failed update")
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Error("staged .new file was left behind after abort")
	}
	// The staged download now gets a unique name, so check the whole directory
	// rather than one path.
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagePrefix) {
			t.Errorf("staging file %s left behind after abort", e.Name())
		}
		if strings.HasSuffix(e.Name(), lockName) {
			t.Errorf("update lock %s left behind after abort", e.Name())
		}
	}
}
