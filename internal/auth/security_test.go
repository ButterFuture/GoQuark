package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ButterFuture/GoQuark/internal/config"
)

// The QR payload is a live login token, so it must default into the config
// directory rather than the working directory, where it could be committed to a
// repo or read by other users on a shared machine.
func TestDefaultOptsKeepsQROutOfWorkingDir(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "goquark", "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	var opt Options
	defaultOpts(&opt, cfg)

	wantDir := filepath.Dir(cfgPath)
	for name, got := range map[string]string{
		"QRPngPath": opt.QRPngPath,
		"QRURLPath": opt.QRURLPath,
	} {
		if dir := filepath.Dir(got); dir != wantDir {
			t.Errorf("%s = %q, want it under %q", name, got, wantDir)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("%s = %q, want an absolute path (a relative one lands in cwd)", name, got)
		}
	}
}

func TestQRDirIsOwnerOnly(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "goquark", "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	dir := qrDir(cfg)
	if dir == "." {
		t.Fatal("qrDir fell back to the working directory")
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o700 {
		t.Errorf("qr dir mode = %o, want 700", perm)
	}
}

func TestWriteQRFilesAreNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "qrcode.png")
	txt := filepath.Join(dir, "qrcode.url.txt")
	const qrURL = "https://su.quark.cn/4_eMHBJ?token=secret-login-token"

	if err := writeQRFiles(qrURL, png, txt); err != nil {
		t.Fatalf("writeQRFiles: %v", err)
	}

	for _, p := range []string{png, txt} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), perm)
		}
		// Group and other must hold no bits at all — the token is in there.
		if st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is readable beyond its owner", filepath.Base(p))
		}
	}

	body, err := os.ReadFile(txt)
	if err != nil || !strings.Contains(string(body), qrURL) {
		t.Errorf("url file = %q (%v), want it to contain the QR url", body, err)
	}
	if st, _ := os.Stat(png); st != nil && st.Size() == 0 {
		t.Error("qrcode.png is empty")
	}
}

// The stored login reply carries the session credential; what MCP hands to an
// agent after login must not.
func TestUserInfoFromSessionDropsCredentials(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetSession(nil, "st", []byte(`{"code":"OK","success":true,"data":{"pus":"secret","uid":"u1","timestamp":1}}`), "wechat")
	got := UserInfoFromSession(cfg)
	if _, ok := got["pus"]; ok {
		t.Error("pus leaked")
	}
	if got["uid"] != "u1" {
		t.Errorf("uid = %v, want u1", got["uid"])
	}
	cfg.SetSession(nil, "st", []byte(`not json pus=secret`), "wechat")
	if got := UserInfoFromSession(cfg); got != nil {
		t.Errorf("unparsable blob returned %v", got)
	}
}
