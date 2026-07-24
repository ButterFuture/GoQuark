package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Device holds persistent device fingerprint. Generated once, never regenerated.
type Device struct {
	MachineID  string `json:"machine_id"`  // OpenUTDID-style 24-char id
	DeviceName string `json:"device_name"` // e.g. 陈的MacBook Air
	Model      string `json:"model"`       // e.g. MacBookAir10,1
	OSVersion  string `json:"os_version"`  // e.g. 14.5
	Arch       string `json:"arch"`        // arm64 / x86_64
	Platform   string `json:"platform"`    // always darwin for Mac client sim
	CreatedAt  string `json:"created_at"`
}

// Session holds login state after QR scan.
type Session struct {
	Cookies       map[string]string `json:"cookies,omitempty"`
	ServiceTicket string            `json:"service_ticket,omitempty"`
	UserInfo      json.RawMessage   `json:"user_info,omitempty"`
	LoginWay      string            `json:"login_way,omitempty"`
	LoggedInAt    string            `json:"logged_in_at,omitempty"`
}

// Config is the on-disk config file.
type Config struct {
	Device  Device  `json:"device"`
	Session Session `json:"session"`
	// Download defaults (VIP-like official fallback)
	PartSize    int `json:"part_size"`    // bytes, default 4MiB
	Concurrency int `json:"concurrency"` // default 12
	// DownloadDir is where files are saved. Empty → system Downloads/GoQuark.
	// Example: "/home/chen/Downloads/GoQuark"
	DownloadDir string `json:"download_dir,omitempty"`

	// Update: TUI startup auto-check. When true, skip automatic prompt
	// (manual check via key U / CLI / MCP still works).
	DisableUpdateAutoCheck bool `json:"disable_update_auto_check,omitempty"`

	mu   sync.Mutex `json:"-"`
	path string     `json:"-"`
}

func DefaultPath() string {
	if p := os.Getenv("GOQUARK_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "goquark", "config.json")
}

// DefaultDownloadDir returns ~/Downloads/GoQuark (or XDG / Windows equivalents).
func DefaultDownloadDir() string {
	if p := os.Getenv("GOQUARK_DOWNLOAD_DIR"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "GoQuark")
	}
	// Linux XDG user-dirs may override Downloads
	if xdg := os.Getenv("XDG_DOWNLOAD_DIR"); xdg != "" {
		return filepath.Join(xdg, "GoQuark")
	}
	// Common layouts
	for _, name := range []string{"Downloads", "downloads", "Download"} {
		p := filepath.Join(home, name)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return filepath.Join(p, "GoQuark")
		}
	}
	// Fallback even if Downloads doesn't exist yet
	return filepath.Join(home, "Downloads", "GoQuark")
}

// EffectiveDownloadDir returns configured dir or the system default.
func (c *Config) EffectiveDownloadDir() string {
	if c != nil {
		if d := strings.TrimSpace(c.DownloadDir); d != "" {
			// expand ~
			if strings.HasPrefix(d, "~/") {
				if home, err := os.UserHomeDir(); err == nil {
					return filepath.Join(home, d[2:])
				}
			}
			return d
		}
	}
	return DefaultDownloadDir()
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			c := &Config{
				PartSize:    4 << 20,
				Concurrency: 12,
				path:        path,
			}
			return c, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	c.path = path
	if c.PartSize <= 0 {
		c.PartSize = 4 << 20
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 12
	}
	if c.Session.Cookies == nil {
		c.Session.Cookies = map[string]string{}
	}
	return &c, nil
}

func (c *Config) Path() string { return c.path }

func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		c.path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *Config) HasDevice() bool {
	return c.Device.MachineID != "" && c.Device.DeviceName != "" && c.Device.Model != ""
}

func (c *Config) CookieHeader() string {
	if len(c.Session.Cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.Session.Cookies))
	for k, v := range c.Session.Cookies {
		if k == "" || v == "" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	// stable-ish order not required for Cookie header
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "; "
		}
		out += p
	}
	return out
}

func (c *Config) SetSession(cookies map[string]string, st string, user json.RawMessage, way string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cookies == nil {
		cookies = map[string]string{}
	}
	c.Session = Session{
		Cookies:       cookies,
		ServiceTicket: st,
		UserInfo:      user,
		LoginWay:      way,
		LoggedInAt:    time.Now().Format(time.RFC3339),
	}
}

// ClearSession removes login cookies / tickets but keeps device binding.
func (c *Config) ClearSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Session = Session{Cookies: map[string]string{}}
}

// UpsertCookie updates a single cookie value in the session map.
func (c *Config) UpsertCookie(name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Session.Cookies == nil {
		c.Session.Cookies = map[string]string{}
	}
	if name == "" {
		return
	}
	c.Session.Cookies[name] = value
}

// HasSessionCookie reports whether a known auth cookie is present.
func (c *Config) HasSessionCookie() bool {
	ck := c.Session.Cookies
	if len(ck) == 0 {
		return false
	}
	// Official PC session typically has __pus and/or __uid / __kps.
	for _, k := range []string{"__pus", "__puus", "__uid", "__kps", "__kp"} {
		if v := ck[k]; v != "" {
			return true
		}
	}
	// fallback: any cookie
	for _, v := range ck {
		if v != "" {
			return true
		}
	}
	return false
}

func (c *Config) RequireLogin() error {
	if !c.HasSessionCookie() {
		return errors.New("not logged in; run: goquark login")
	}
	return nil
}
