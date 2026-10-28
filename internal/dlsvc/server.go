package dlsvc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ButterFuture/GoQuark/internal/api"
	"github.com/ButterFuture/GoQuark/internal/client"
	"github.com/ButterFuture/GoQuark/internal/config"
	"github.com/ButterFuture/GoQuark/internal/device"
	"github.com/ButterFuture/GoQuark/internal/download"
)

// Tunables; tests shorten them.
var (
	tickEvery  = 2 * time.Second
	idleExit   = 30 * time.Second
	executable = os.Executable
)

type server struct {
	dir, cfgPath, version string
	mgr                   *download.Manager
	logf                  func(format string, args ...any)

	exe     string
	exeMod  time.Time
	exeSize int64
	cfgMod  time.Time

	clients atomic.Int32
	restart atomic.Bool
	ln      net.Listener
	stop    sync.Once
	done    chan struct{}
}

// Serve runs the download service for the config at cfgPath until it goes
// idle, a newer binary is installed and nothing is downloading, or it is
// signalled. It returns nil at once if another service already owns the
// config directory.
func Serve(cfgPath, version string) error {
	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, ok, err := tryLock(lockPath(dir))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer lock.Close()

	// We own the directory, so a socket file left here is a dead one.
	sock := sockPath(dir)
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	defer os.Remove(sock)

	s := &server{
		dir: dir, cfgPath: cfgPath, version: version, ln: ln,
		logf: log.New(os.Stderr, "", log.LstdFlags).Printf,
		done: make(chan struct{}),
	}
	s.mgr = download.NewManager(nil, "")
	s.mgr.SetStatePath(statePath(dir))
	s.mgr.OnReloadSession = s.bindSession
	s.bindSession()
	_, _ = s.mgr.LoadPersisted()
	if exe, err := executable(); err == nil {
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			exe = p
		}
		s.exe = exe
		s.exeMod, s.exeSize = fileStamp(exe)
	}
	s.logf("download service %s pid %d serving %s", version, os.Getpid(), dir)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			s.logf("signalled, pausing downloads")
			s.shutdown()
		case <-s.done:
		}
	}()
	go s.monitor()

	for {
		conn, err := ln.Accept()
		if err != nil {
			break // listener closed by shutdown
		}
		go s.handle(conn)
	}
	<-s.done
	s.mgr.PauseAllWait()
	s.mgr.WaitSaves()
	s.logf("download service stopped")
	return nil
}

func (s *server) shutdown() {
	s.stop.Do(func() {
		close(s.done)
		_ = s.ln.Close()
	})
}

// bindSession points the manager at the login saved in the config.
func (s *server) bindSession() {
	bindSession(s.mgr, s.cfgPath, s.logf)
	s.cfgMod, _ = fileStamp(s.cfgPath)
}

func bindSession(mgr *download.Manager, cfgPath string, logf func(string, ...any)) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logf("load config: %v", err)
		return
	}
	_ = device.EnsureDevice(cfg, "")
	c := client.New(cfg)
	mgr.BindClient(c)
	// CDN URLs expire: queued and resumed tasks fetch a fresh one by fid.
	mgr.ResolveURL = func(fid string) (string, int64, error) {
		return api.GetDownloadURL(c, fid)
	}
	mgr.SetMaxLive(cfg.MaxDownloads)
	mgr.SetDestDir(cfg.EffectiveDownloadDir())
}

func (s *server) monitor() {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	var idleSince time.Time
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		active := s.mgr.ActiveCount()
		if !s.restart.Load() && s.exeReplaced() {
			s.restart.Store(true)
			s.logf("new binary installed; restarting once %d download(s) finish", active)
		}
		if s.restart.Load() && active == 0 {
			s.logf("restarting on the new binary")
			s.shutdown()
			return
		}
		if s.clients.Load() == 0 && active == 0 {
			if idleSince.IsZero() {
				idleSince = time.Now()
			} else if time.Since(idleSince) >= idleExit {
				s.logf("idle, exiting")
				s.shutdown()
				return
			}
		} else {
			idleSince = time.Time{}
		}
		// A login or logout in any GoQuark process lands in config.json.
		if mod, _ := fileStamp(s.cfgPath); mod.After(s.cfgMod) {
			s.bindSession()
		}
	}
}

func (s *server) exeReplaced() bool {
	if s.exe == "" {
		return false
	}
	mod, size := fileStamp(s.exe)
	if mod.IsZero() {
		return false // gone, e.g. mid-update: look again next tick
	}
	return !mod.Equal(s.exeMod) || size != s.exeSize
}

func fileStamp(path string) (time.Time, int64) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, 0
	}
	return fi.ModTime(), fi.Size()
}

func (s *server) info() Info {
	return Info{Version: s.version, PID: os.Getpid(), RestartPending: s.restart.Load(), Active: s.mgr.ActiveCount()}
}

func (s *server) handle(conn net.Conn) {
	s.clients.Add(1)
	defer s.clients.Add(-1)
	defer conn.Close()
	rd := bufio.NewReaderSize(conn, 64<<10)
	enc := json.NewEncoder(conn)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req request
		resp := response{}
		if err := json.Unmarshal(line, &req); err != nil {
			resp.Error = "bad request"
		} else {
			resp.ID = req.ID
			res, err := s.call(req.Method, req.Params)
			if err != nil {
				resp.Error = err.Error()
			} else if resp.Result, err = json.Marshal(res); err != nil {
				resp.Error = err.Error()
			}
		}
		if enc.Encode(resp) != nil {
			return
		}
	}
}

func (s *server) call(method string, p params) (any, error) {
	m := s.mgr
	switch method {
	case "Hello", "Info":
		return s.info(), nil
	case "List":
		return m.List(), nil
	case "LiveCount":
		l, q, lim := m.LiveCount()
		return [3]int{l, q, lim}, nil
	case "ActiveCount":
		return m.ActiveCount(), nil
	case "IncompleteCount":
		return m.IncompleteCount(), nil
	case "DestDir":
		return m.DestDir(), nil
	case "SetDestDir":
		m.SetDestDir(p.Dir)
		return nil, nil
	case "SetMaxLive":
		return m.SetMaxLive(p.N), nil
	case "Add":
		if p.Opt == nil {
			return nil, fmt.Errorf("add: no task")
		}
		return m.Add(*p.Opt)
	case "PauseAll":
		return m.PauseAll(), nil
	case "PauseAllWait":
		return m.PauseAllWait(), nil
	case "PauseID":
		return m.PauseID(p.ID), nil
	case "PauseIDs":
		return m.PauseIDs(p.IDs), nil
	case "CancelID":
		return m.CancelID(p.ID), nil
	case "CancelIDs":
		return m.CancelIDs(p.IDs), nil
	case "CancelAll":
		return m.CancelAll(), nil
	case "ResumeID":
		return m.ResumeID(p.ID), nil
	case "ResumeIDs":
		return m.ResumeIDs(p.IDs), nil
	case "ResumePaused":
		return m.ResumePaused(), nil
	case "ResumeQuitPaused":
		return m.ResumeQuitPaused(), nil
	case "RemoveID":
		return m.RemoveID(p.ID), nil
	case "ClearFinished":
		return m.ClearFinished(), nil
	case "ClearStaleDone":
		return m.ClearStaleDone(), nil
	case "ClearQuitResumeFlags":
		m.ClearQuitResumeFlags()
		return nil, nil
	case "FindAtDest":
		return m.FindAtDest(p.FID, p.Dest), nil
	case "BusyAtDest":
		return m.BusyAtDest(p.Dest), nil
	case "ReloadSession":
		s.bindSession()
		return nil, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}
