package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type ProxySnapshot struct {
	Enabled      uint64 `json:"enabled"`
	Server       string `json:"server"`
	HasEnabled   bool   `json:"hasEnabled"`
	HasServer    bool   `json:"hasServer"`
	OwnedAddress string `json:"ownedAddress"`
}

const proxyKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func hideCommand(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

// Use the Windows shell directly: hidden subprocess flags must not be applied to Explorer.
func openInExplorer(location string, selectFile bool) error {
	if !filepath.IsAbs(location) {
		return errors.New("保存位置必须是完整路径")
	}
	location = filepath.Clean(location)
	info, err := os.Stat(location)
	if err != nil {
		return fmt.Errorf("保存位置不存在或无法访问：%s", location)
	}
	if selectFile && info.IsDir() || !selectFile && !info.IsDir() {
		return errors.New("保存位置类型无效，请重新选择下载目录")
	}
	target, err := windows.UTF16PtrFromString(location)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err = windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && !errors.Is(err, syscall.Errno(1)) {
		return fmt.Errorf("初始化资源管理器失败：%w", err)
	}
	defer windows.CoUninitialize()
	if !selectFile {
		verb, _ := windows.UTF16PtrFromString("open")
		if err = windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL); err != nil {
			return fmt.Errorf("打开下载目录失败：%w", err)
		}
		return nil
	}
	shell := windows.NewLazySystemDLL("shell32.dll")
	var item unsafe.Pointer
	hr, _, _ := shell.NewProc("SHParseDisplayName").Call(uintptr(unsafe.Pointer(target)), 0, uintptr(unsafe.Pointer(&item)), 0, 0)
	if int32(hr) < 0 {
		return fmt.Errorf("定位下载文件失败：0x%08X", uint32(hr))
	}
	defer windows.CoTaskMemFree(item)
	// With zero selection count, the absolute item opens its parent folder and is selected.
	hr, _, _ = shell.NewProc("SHOpenFolderAndSelectItems").Call(uintptr(item), 0, 0, 0)
	if int32(hr) < 0 {
		return fmt.Errorf("打开并选中下载文件失败：0x%08X", uint32(hr))
	}
	return nil
}

// A Windows Job owns yt-dlp and every FFmpeg child, so cancellation/exit cannot orphan a merge.
func runManagedCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return err
	}
	hideCommand(cmd)
	if err = cmd.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		windows.CloseHandle(process)
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("无法管理视频引擎进程：%w", err)
	}
	finished := make(chan struct{})
	cancelled := make(chan struct{})
	go func() {
		defer close(cancelled)
		select {
		case <-ctx.Done():
			windows.TerminateJobObject(job, 1)
		case <-finished:
		}
	}()
	err = cmd.Wait()
	close(finished)
	<-cancelled
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func startupError(err error) {
	title, _ := windows.UTF16PtrFromString("拾流无法启动")
	text, _ := windows.UTF16PtrFromString(err.Error() + "\n\n请检查 data\\app.log，或关闭已经运行的拾流窗口后重试。")
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}
func commitDownload(from, to string) error {
	a, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	b, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(a, b, windows.MOVEFILE_WRITE_THROUGH)
}
func notifyProxy() {
	dll := windows.NewLazySystemDLL("wininet.dll")
	p := dll.NewProc("InternetSetOptionW")
	p.Call(0, 39, 0, 0)
	p.Call(0, 37, 0, 0)
}
func proxyState() (ProxySnapshot, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, proxyKey, registry.QUERY_VALUE)
	if err != nil {
		return ProxySnapshot{}, err
	}
	defer k.Close()
	s := ProxySnapshot{}
	s.Enabled, _, err = k.GetIntegerValue("ProxyEnable")
	s.HasEnabled = err == nil
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return s, err
	}
	s.Server, _, err = k.GetStringValue("ProxyServer")
	s.HasServer = err == nil
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return s, err
	}
	return s, nil
}
func applyProxy(s ProxySnapshot) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, proxyKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if s.HasServer {
		err = k.SetStringValue("ProxyServer", s.Server)
	} else {
		err = k.DeleteValue("ProxyServer")
		if errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if s.HasEnabled {
		err = k.SetDWordValue("ProxyEnable", uint32(s.Enabled))
	} else {
		err = k.DeleteValue("ProxyEnable")
		if errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
	}
	notifyProxy()
	return err
}
func (e *Engine) recoveryPath() string { return filepath.Join(e.root, "data", "proxy-recovery.json") }
func (e *Engine) recoverProxy() error {
	b, err := os.ReadFile(e.recoveryPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var old ProxySnapshot
	if err = json.Unmarshal(b, &old); err != nil {
		return fmt.Errorf("代理恢复记录损坏：%w", err)
	}
	current, err := e.readProxy()
	if err != nil {
		return err
	}
	if current.Server == old.OwnedAddress && current.Enabled == 1 {
		if err = e.writeProxy(old); err != nil {
			return err
		}
	}
	return os.Remove(e.recoveryPath())
}
func (e *Engine) enableSystemProxyLocked() error {
	old, err := e.readProxy()
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", e.config.ProxyPort)
	old.OwnedAddress = addr
	b, _ := json.MarshalIndent(old, "", "  ")
	if err = atomicWrite(e.recoveryPath(), b, 0600); err != nil {
		return err
	}
	next := ProxySnapshot{Enabled: 1, HasEnabled: true, Server: addr, HasServer: true}
	if err = e.writeProxy(next); err != nil {
		e.writeProxy(old)
		os.Remove(e.recoveryPath())
		return err
	}
	e.proxySnapshot = &old
	return nil
}
func (e *Engine) restoreProxyLocked() error {
	if e.proxySnapshot == nil {
		return nil
	}
	current, err := e.readProxy()
	if err != nil {
		return err
	}
	if current.Server == e.proxySnapshot.OwnedAddress && current.Enabled == 1 {
		if err = e.writeProxy(*e.proxySnapshot); err != nil {
			return err
		}
	}
	if err = os.Remove(e.recoveryPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	e.proxySnapshot = nil
	return nil
}
func (e *Engine) isCertificateInstalled() bool {
	cmd := exec.Command("certutil.exe", "-user", "-store", "Root", e.ca.Thumbprint)
	hideCommand(cmd)
	return cmd.Run() == nil
}
func (e *Engine) installCertificate() error {
	cmd := exec.Command("certutil.exe", "-user", "-addstore", "Root", e.ca.certPath)
	hideCommand(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("安装证书失败：%w", err)
	}
	if !e.isCertificateInstalled() {
		return errors.New("证书安装后未能确认，请检查系统证书库")
	}
	return nil
}
func (e *Engine) removeCertificate() error {
	cmd := exec.Command("certutil.exe", "-user", "-delstore", "Root", e.ca.Thumbprint)
	hideCommand(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("移除证书失败：%w", err)
	}
	return nil
}
