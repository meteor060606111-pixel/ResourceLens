package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type desktopInstance struct {
	lock     *os.File
	listener net.Listener
	socket   string
	once     sync.Once
}

func acquireDesktopInstance() (*desktopInstance, bool, error) {
	return acquireNamedInstance("ResourceLens.Desktop.v2")
}

func acquireNamedInstance(name string) (*desktopInstance, bool, error) {
	// A per-user directory prevents another user's launch from activating this app.
	dir := os.Getenv("RESOURCELENS_INSTANCE_DIR")
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, false, err
		}
		dir = filepath.Join(cache, "ResourceLens", "instance")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, false, err
	}
	hash := sha256.Sum256([]byte(name))
	stem := filepath.Join(dir, fmt.Sprintf("%x", hash[:8]))
	lock, err := os.OpenFile(stem+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, err
		}
		for attempt := 0; attempt < 20; attempt++ {
			conn, dialErr := net.DialTimeout("unix", stem+".sock", 100*time.Millisecond)
			if dialErr == nil {
				conn.Close()
				return nil, true, nil
			}
			time.Sleep(25 * time.Millisecond)
		}
		return nil, true, errors.New("已运行的拾流未能响应，请稍后重试")
	}
	os.Remove(stem + ".sock")
	listener, err := net.Listen("unix", stem+".sock")
	if err != nil {
		lock.Close()
		return nil, false, err
	}
	os.Chmod(stem+".sock", 0600)
	return &desktopInstance{lock: lock, listener: listener, socket: stem + ".sock"}, false, nil
}

func (i *desktopInstance) listen(activate func()) {
	go func() {
		for {
			conn, err := i.listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
			activate()
		}
	}()
}

func activateCurrentWindow() {
	// Launch Services brings this bundle forward without invoking another copy.
	exe, _ := os.Executable()
	if filepath.Base(filepath.Dir(exe)) == "MacOS" {
		exec.Command("/usr/bin/open", filepath.Dir(filepath.Dir(filepath.Dir(exe)))).Run()
	}
}

func (i *desktopInstance) Close() {
	if i == nil {
		return
	}
	i.once.Do(func() {
		i.listener.Close()
		os.Remove(i.socket)
		syscall.Flock(int(i.lock.Fd()), syscall.LOCK_UN)
		i.lock.Close()
	})
}
