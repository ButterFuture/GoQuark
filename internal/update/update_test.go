package update

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A release body is whatever its author wrote, and it ends up on a terminal.
func TestSanitizeNotes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"crlf", "hello\r\nworld", "hello\nworld"},
		{"lone cr", "a\rb", "a\nb"},
		{"sgr colour", "\x1b[31mred\x1b[0m text", "red text"},
		{"cursor move", "top\x1b[2Joverwritten", "topoverwritten"},
		{"bel and nul", "a\x07b\x00c", "abc"},
		{"keeps tab and newline", "keep\ttab\nand line", "keep\ttab\nand line"},
		{"trims", "  padded  ", "padded"},
		{"markdown survives", "### Security\n- `utls` 1.6.7 → 1.8.2", "### Security\n- `utls` 1.6.7 → 1.8.2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeNotes(c.in); got != c.want {
				t.Errorf("sanitizeNotes(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitizeNotesCapsLength(t *testing.T) {
	got := sanitizeNotes(strings.Repeat("x", MaxNotesBytes+5000))
	if len(got) > MaxNotesBytes+8 {
		t.Errorf("notes not capped: %d bytes", len(got))
	}
}

// Two updaters must not stage into the same directory at once: that race is
// what produced "chmod ...goquark.new: no such file or directory" when the
// winner renamed the staged file away before the loser chmod'ed it.
func TestLockUpdateIsExclusive(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockUpdate(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if _, err := lockUpdate(dir); err == nil {
		t.Fatal("second lock succeeded while the first was held")
	} else if !errors.Is(err, ErrUpdateInProgress) {
		t.Errorf("second lock returned %v, want ErrUpdateInProgress", err)
	}
	unlock()
	unlock2, err := lockUpdate(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock2()
	if _, err := os.Stat(filepath.Join(dir, lockName)); !os.IsNotExist(err) {
		t.Error("lock file left behind after release")
	}
}

// A lock from an updater that died must not block every later run forever.
func TestLockUpdateStealsStaleLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lockName)
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * lockStale)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockUpdate(dir)
	if err != nil {
		t.Fatalf("stale lock was not reclaimed: %v", err)
	}
	unlock()
}

// Windows cannot delete the image of a running process, so the replaced
// binary is still on disk at the next start and has to be cleared then.
func TestCleanupLeftovers(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path: %v", err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dir := filepath.Dir(exe)

	bak := exe + bakSuffix
	legacy := exe + ".new"
	staleStage := filepath.Join(dir, stagePrefix+"stale")
	freshStage := filepath.Join(dir, stagePrefix+"fresh")
	for _, p := range []string{bak, legacy, staleStage, freshStage} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Skipf("cannot write beside the test binary: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(p) })
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(staleStage, old, old); err != nil {
		t.Fatal(err)
	}

	CleanupLeftovers()

	for _, p := range []string{bak, legacy, staleStage} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s was not cleaned up", filepath.Base(p))
		}
	}
	// A staging file this recent may belong to an update running right now.
	if _, err := os.Stat(freshStage); err != nil {
		t.Error("a fresh staging file was removed; a running update would break")
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("cleanup touched the running binary: %v", err)
	}
}

// The happy path on Unix: the old image is gone and nothing is left pending.
func TestSwapBinaryReplacesAndCleans(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "goquark")
	tmp := filepath.Join(dir, stagePrefix+"abc")
	mustWrite(t, exe, "old")
	mustWrite(t, tmp, "new")

	pending, err := swapBinary(tmp, exe)
	if err != nil {
		t.Fatalf("swapBinary: %v", err)
	}
	if pending != "" {
		t.Errorf("pending = %q, want empty when the old image can be deleted", pending)
	}
	if got := mustRead(t, exe); got != "new" {
		t.Errorf("binary content = %q, want %q", got, "new")
	}
	if _, err := os.Stat(exe + bakSuffix); !os.IsNotExist(err) {
		t.Error("backup left behind")
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("staging file still present after the rename")
	}
}

// On Windows the replaced image cannot be deleted while the old process still
// runs it. That must not fail the update: the new binary is in place, and the
// leftover is reported for the next start to clear.
func TestSwapBinaryReportsUndeletableOldImage(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "goquark.exe")
	tmp := filepath.Join(dir, stagePrefix+"abc")
	mustWrite(t, exe, "old")
	mustWrite(t, tmp, "new")

	orig := removeFile
	removeFile = func(name string) error {
		if strings.HasSuffix(name, bakSuffix) {
			return os.ErrPermission // what Windows does with a running image
		}
		return orig(name)
	}
	t.Cleanup(func() { removeFile = orig })

	pending, err := swapBinary(tmp, exe)
	if err != nil {
		t.Fatalf("swapBinary failed although the new binary was installable: %v", err)
	}
	if pending != exe+bakSuffix {
		t.Errorf("pending = %q, want %q", pending, exe+bakSuffix)
	}
	if got := mustRead(t, exe); got != "new" {
		t.Errorf("binary content = %q, want %q", got, "new")
	}
	if got := mustRead(t, exe+bakSuffix); got != "old" {
		t.Error("the old image was not preserved for later cleanup")
	}
}

// If the new binary cannot be moved into place, the old one must come back —
// the user is never left without a working executable.
func TestSwapBinaryRestoresOnFailedInstall(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "goquark")
	tmp := filepath.Join(dir, stagePrefix+"abc")
	mustWrite(t, exe, "old")
	mustWrite(t, tmp, "new")

	orig := renameFile
	renameFile = func(from, to string) error {
		if from == tmp {
			return os.ErrPermission
		}
		return orig(from, to)
	}
	t.Cleanup(func() { renameFile = orig })

	if _, err := swapBinary(tmp, exe); err == nil {
		t.Fatal("swapBinary reported success although the install failed")
	}
	if got := mustRead(t, exe); got != "old" {
		t.Errorf("binary content = %q, want the original %q restored", got, "old")
	}
	if _, err := os.Stat(exe + bakSuffix); !os.IsNotExist(err) {
		t.Error("backup left behind after the rollback")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// Startup cleanup must not delete the backup an update in flight still needs
// to roll back to.
func TestCleanupLeftoversDefersToRunningUpdate(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path: %v", err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dir := filepath.Dir(exe)

	unlock, err := lockUpdate(dir)
	if err != nil {
		t.Skipf("cannot lock beside the test binary: %v", err)
	}
	defer unlock()

	bak := exe + bakSuffix
	if err := os.WriteFile(bak, []byte("the only copy"), 0o600); err != nil {
		t.Skipf("cannot write beside the test binary: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(bak) })

	CleanupLeftovers()

	if got, err := os.ReadFile(bak); err != nil || string(got) != "the only copy" {
		t.Error("cleanup removed the backup of an update that is still running")
	}
}

// mismatchResult points at srv and always fails verification, so an apply
// that gets past the guards stops before touching the running binary.
func mismatchResult(srvURL string) *Result {
	const asset = "goquark_9.9.9_test"
	return &Result{
		UpdateAvailable: true, CanApply: true, Latest: "9.9.9",
		AssetName: asset, Repo: DefaultRepo,
		AssetURL: srvURL + "/" + asset,
		SumsURL:  srvURL + "/" + SumsAssetName,
	}
}

// One process must run one update at a time. The on-disk lock covers other
// processes; this covers a caller that fires several update goroutines — the
// TUI's auto-check racing a manual "u", say — which would otherwise download
// the release once per goroutine.
func TestApplyRefusesASecondGoroutineInThisProcess(t *testing.T) {
	const asset = "goquark_9.9.9_test"
	inside := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	srv := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, SumsAssetName) {
			// Hold the first apply open so the second one is genuinely
			// concurrent rather than merely sequential.
			once.Do(func() { close(inside); <-release })
			_, _ = w.Write([]byte(sumA + "  " + asset + "\n"))
			return
		}
		_, _ = w.Write([]byte("tampered payload"))
	}))

	first := make(chan error, 1)
	go func() { first <- Apply(context.Background(), mismatchResult(srv.URL)) }()

	<-inside // the first apply now holds the in-process claim
	err := Apply(context.Background(), mismatchResult(srv.URL))
	if !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("second apply returned %v, want ErrUpdateInProgress", err)
	}

	close(release)
	if err := <-first; !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("first apply returned %v, want it to run to the checksum check", err)
	}

	// The claim must be released even though the apply failed, or one bad
	// download would block every later update for the life of the process.
	if applyInFlight.Load() {
		t.Fatal("in-process claim still held after the apply finished")
	}
	if err := Apply(context.Background(), mismatchResult(srv.URL)); errors.Is(err, ErrUpdateInProgress) {
		t.Error("a later apply was refused; the claim leaked")
	}
}

// Many goroutines at once: exactly one gets to do the work.
func TestApplyAdmitsOnlyOneOfManyGoroutines(t *testing.T) {
	const asset = "goquark_9.9.9_test"
	inside := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	srv := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, SumsAssetName) {
			once.Do(func() { close(inside); <-release })
			_, _ = w.Write([]byte(sumA + "  " + asset + "\n"))
			return
		}
		_, _ = w.Write([]byte("tampered payload"))
	}))

	winner := make(chan error, 1)
	go func() { winner <- Apply(context.Background(), mismatchResult(srv.URL)) }()
	<-inside

	const others = 8
	var wg sync.WaitGroup
	refused := make(chan error, others)
	for i := 0; i < others; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refused <- Apply(context.Background(), mismatchResult(srv.URL))
		}()
	}
	wg.Wait()
	close(refused)

	for err := range refused {
		if !errors.Is(err, ErrUpdateInProgress) {
			t.Errorf("a concurrent apply got through: %v", err)
		}
	}
	close(release)
	<-winner
}

// Startup cleanup must not run while this process is applying an update.
func TestCleanupLeftoversDefersToThisProcess(t *testing.T) {
	applyInFlight.Store(true)
	t.Cleanup(func() { applyInFlight.Store(false) })

	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path: %v", err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	bak := exe + bakSuffix
	if err := os.WriteFile(bak, []byte("the only copy"), 0o600); err != nil {
		t.Skipf("cannot write beside the test binary: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(bak) })

	CleanupLeftovers()

	if _, err := os.Stat(bak); err != nil {
		t.Error("cleanup removed the backup of an update running in this process")
	}
}
