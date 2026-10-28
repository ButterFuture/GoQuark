package dlsvc

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ButterFuture/GoQuark/internal/demobuild"
	"github.com/ButterFuture/GoQuark/internal/download"
)

// InProcess reports whether downloads run inside each process instead of in
// the service: demo builds (no network) and GOQUARK_DOWNLOAD_SERVICE=off.
func InProcess() bool {
	if demobuild.Enabled {
		return true
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GOQUARK_DOWNLOAD_SERVICE")))
	return v == "off" || v == "0" || v == "false"
}

// Open returns the download controller for the config at cfgPath: a client of
// the download service, started on first use, or an in-process manager when
// InProcess.
func Open(cfgPath string) download.Controller {
	if InProcess() {
		return local(cfgPath)
	}
	return newClient(cfgPath, true)
}

// OpenViewer is Open for a process that only shows the downloads: it talks
// to a running service but does not start one, and otherwise reads
// downloads.json without ever writing it.
func OpenViewer(cfgPath string) download.Controller {
	if !InProcess() {
		c := newClient(cfgPath, false)
		if _, err := c.Info(); err == nil {
			return c
		}
	}
	m := download.NewManager(nil, "")
	m.SetStatePath(statePath(filepath.Dir(cfgPath)))
	m.SetReadOnly()
	_, _ = m.LoadPersisted()
	return m
}

// UpdateNote tells the user what an installed update means for downloads in
// progress: none ("") when nothing is downloading or downloads run in-process.
func UpdateNote(cfgPath string) string {
	if InProcess() {
		return ""
	}
	c := newClient(cfgPath, false)
	defer c.Close()
	if in, err := c.Info(); err != nil || in.Active == 0 {
		return ""
	}
	return "正在进行的下载不受影响；下载服务会在它们结束后自动换成新版本。"
}

func local(cfgPath string) *download.Manager {
	m := download.Global()
	m.SetStatePath(statePath(filepath.Dir(cfgPath)))
	logf := log.New(os.Stderr, "", 0).Printf
	m.OnReloadSession = func() { bindSession(m, cfgPath, logf) }
	bindSession(m, cfgPath, logf)
	_, _ = m.LoadPersisted()
	return m
}
