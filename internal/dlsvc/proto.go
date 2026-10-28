// Package dlsvc runs one download manager per config directory as a small
// background service, so the TUI, MCP and CLI of the same user share one queue
// and one downloads.json instead of overwriting each other's.
//
// The first process that needs downloads starts `goquark download-service`;
// the service holds downloads.lock for its lifetime and listens on a unix
// socket next to the config (see sockPath). Clients send one JSON request per line and read
// one JSON response per line. The service exits once nothing is downloading
// and no client is connected, and restarts on a new binary as soon as no
// download is running, so an update never cuts a download short.
package dlsvc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ButterFuture/GoQuark/internal/download"
)

const (
	sockName  = "download-service.sock"
	lockName  = "downloads.lock"
	logName   = "download-service.log"
	stateName = "downloads.json"
)

// maxSockPath keeps under the smallest unix socket path limit (104 bytes on
// macOS, 108 on Linux).
const maxSockPath = 100

// sockPath is the service socket for the config in dir: next to the config,
// or, when that path is too long for a socket, in a directory only this user
// can enter, named after dir.
func sockPath(dir string) string {
	if p := filepath.Join(dir, sockName); len(p) <= maxSockPath {
		return p
	}
	sum := sha256.Sum256([]byte(dir))
	name := "goquark-" + hex.EncodeToString(sum[:6]) + ".sock"
	base := os.Getenv("XDG_RUNTIME_DIR") // per-user, 0700
	if base == "" {
		base = filepath.Join(os.TempDir(), fmt.Sprintf("goquark-%d", os.Getuid()))
		_ = os.Mkdir(base, 0o700)
		// Shared /tmp: use the directory only if it is ours and closed.
		if fi, err := os.Lstat(base); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 || !ownedByMe(fi) {
			return filepath.Join(dir, sockName)
		}
	}
	return filepath.Join(base, name)
}

func lockPath(dir string) string  { return filepath.Join(dir, lockName) }
func logPath(dir string) string   { return filepath.Join(dir, logName) }
func statePath(dir string) string { return filepath.Join(dir, stateName) }

type request struct {
	ID     uint64 `json:"id"`
	Method string `json:"method"`
	Params params `json:"params"`
}

// params carries the arguments of every method; each uses the fields it needs.
type params struct {
	ID      string                `json:"id,omitempty"`
	IDs     []string              `json:"ids,omitempty"`
	N       int                   `json:"n,omitempty"`
	Dir     string                `json:"dir,omitempty"`
	FID     string                `json:"fid,omitempty"`
	Dest    string                `json:"dest,omitempty"`
	Opt     *download.EnqueueOpts `json:"opt,omitempty"`
	Version string                `json:"version,omitempty"`
}

type response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Info describes the running service.
type Info struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
	// RestartPending: a newer binary is installed and the service switches to
	// it once the running downloads finish.
	RestartPending bool `json:"restart_pending"`
	Active         int  `json:"active"`
}
