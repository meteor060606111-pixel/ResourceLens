package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"
)

func updatePackageExtension() string { return ".exe" }

func readWindowsVersion(path string) (version, product, description string, err error) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return "", "", "", errors.New("文件缺少 Windows 版本信息，不是可识别的拾流安装包")
	}
	if size > 4<<20 {
		return "", "", "", errors.New("文件版本信息异常")
	}
	data := make([]byte, size)
	if err = windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&data[0])); err != nil {
		return
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var length uint32
	if err = windows.VerQueryValue(unsafe.Pointer(&data[0]), `\`, unsafe.Pointer(&fixed), &length); err != nil {
		return
	}
	if fixed == nil || length < uint32(unsafe.Sizeof(*fixed)) || fixed.Signature != 0xFEEF04BD {
		err = errors.New("安装包版本信息损坏")
		return
	}
	version, err = normalizedVersion(fmt.Sprintf("%d.%d.%d.%d", fixed.FileVersionMS>>16, fixed.FileVersionMS&0xffff, fixed.FileVersionLS>>16, fixed.FileVersionLS&0xffff))
	if err != nil {
		return
	}
	var translation *byte
	if queryErr := windows.VerQueryValue(unsafe.Pointer(&data[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&translation), &length); queryErr != nil || translation == nil || length < 4 {
		err = errors.New("安装包缺少产品信息")
		return
	}
	translations := unsafe.Slice(translation, int(length))
	if len(translations) > 128 {
		translations = translations[:128]
	}
	for i := 0; i+4 <= len(translations); i += 4 {
		language := binary.LittleEndian.Uint16(translations[i:])
		page := binary.LittleEndian.Uint16(translations[i+2:])
		prefix := fmt.Sprintf(`\StringFileInfo\%04x%04x\`, language, page)
		read := func(name string) string {
			var text *uint16
			var size uint32
			if windows.VerQueryValue(unsafe.Pointer(&data[0]), prefix+name, unsafe.Pointer(&text), &size) != nil || text == nil || size == 0 || size > 4096 {
				return ""
			}
			return windows.UTF16ToString(unsafe.Slice(text, int(size)))
		}
		if product == "" {
			product = read("ProductName")
		}
		if description == "" {
			description = read("FileDescription")
		}
	}
	return
}

func inspectUpdatePackage(path string) (UpdateInfo, error) {
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return UpdateInfo{}, errors.New("请选择完整路径的拾流 .exe 安装程序")
	}
	info, err := os.Stat(path)
	if err != nil {
		return UpdateInfo{}, errors.New("安装文件不存在或无法读取")
	}
	if info.IsDir() || info.Size() < 1<<20 || info.Size() > maxUpdateBytes {
		return UpdateInfo{}, errors.New("安装文件大小无效")
	}
	version, product, description, err := readWindowsVersion(path)
	if err != nil {
		return UpdateInfo{}, err
	}
	if !containsFold(product, "ResourceLens") || !(containsFold(description, "安装") || containsFold(description, "setup")) {
		return UpdateInfo{}, errors.New("这不是拾流 ResourceLens 安装程序，请选择正式新版安装包")
	}
	f, err := os.Open(path)
	if err != nil {
		return UpdateInfo{}, err
	}
	defer f.Close()
	first := make([]byte, min(info.Size(), 2<<20))
	n, _ := io.ReadFull(f, first)
	first = first[:n]
	if len(first) < 2 || string(first[:2]) != "MZ" || !(bytes.Contains(first, []byte("Inno Setup"))) {
		return UpdateInfo{}, errors.New("安装程序格式不匹配，当前仅支持拾流 Inno Setup 安装包")
	}
	sha, size, err := fileSHA256(path)
	if err != nil {
		return UpdateInfo{}, err
	}
	return UpdateInfo{CurrentVersion: appVersion, Version: version, Status: "ready", Source: "local", Path: path, FileName: filepath.Base(path), SHA256: sha, Size: size, Available: true, CanInstall: true, Message: "已识别安装版本并计算 SHA-256；点击安装后会备份当前程序，保留配置和下载文件"}, nil
}

func (e *Engine) hasUpdateBackup() bool {
	b, err := os.ReadFile(filepath.Join(e.updateDir(), "previous.json"))
	if err != nil {
		return false
	}
	var metadata updateBackup
	if json.Unmarshal(b, &metadata) != nil || !shaPattern.MatchString(metadata.SHA256) {
		return false
	}
	info, err := os.Stat(filepath.Join(e.updateDir(), "ResourceLens.previous.exe"))
	return err == nil && !info.IsDir()
}
func (e *Engine) rollbackUpdate() error {
	unlock, err := e.beginUpdateOperation()
	if err != nil {
		return err
	}
	defer unlock()
	if !e.hasUpdateBackup() {
		return errors.New("没有可回退的程序备份")
	}
	b, err := os.ReadFile(filepath.Join(e.updateDir(), "previous.json"))
	if err != nil {
		return err
	}
	var metadata updateBackup
	if json.Unmarshal(b, &metadata) != nil {
		return errors.New("上一版程序记录损坏")
	}
	sha, _, err := fileSHA256(filepath.Join(e.updateDir(), "ResourceLens.previous.exe"))
	if err != nil || sha != metadata.SHA256 {
		return errors.New("上一版程序备份校验失败，未关闭软件或执行回退")
	}
	e.mu.RLock()
	for _, j := range e.jobs {
		if activeStatus(j.Status) {
			e.mu.RUnlock()
			return errors.New("请先暂停或完成下载，再回退版本")
		}
	}
	e.mu.RUnlock()
	return e.launchUpdateWorker("rollback", UpdateInfo{})
}
func (e *Engine) launchUpdateWorker(mode string, info UpdateInfo) error {
	quitMu.RLock()
	quit := requestApplicationQuit
	quitMu.RUnlock()
	if quit == nil {
		return errors.New("请在桌面版拾流中执行升级或回退")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(filepath.Dir(exe)), filepath.Clean(e.root)) {
		return errors.New("程序必须在其安装目录中运行才能升级，请从桌面快捷方式启动")
	}
	if err = os.MkdirAll(e.updateDir(), 0700); err != nil {
		return err
	}
	helper := filepath.Join(e.updateDir(), fmt.Sprintf("update-worker-%d.exe", time.Now().UnixNano()))
	if err = copyUpdateFile(exe, helper); err != nil {
		return err
	}
	task := updateTask{Root: e.root, Exe: exe, ParentPID: uint32(os.Getpid()), Mode: mode, Package: info}
	taskBytes, _ := json.Marshal(task)
	taskFile := helper + ".json"
	if err = atomicWrite(taskFile, taskBytes, 0600); err != nil {
		return err
	}
	cmd := exec.Command(helper, "--update-worker", taskFile)
	hideCommand(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("启动升级助手失败：%w", err)
	}
	cmd.Process.Release()
	info.Status = "installing"
	info.CanInstall = false
	info.Message = "正在关闭拾流并安装；配置与下载文件将保留"
	if mode == "rollback" {
		info.Message = "正在关闭拾流并恢复上一版程序"
	}
	e.setUpdateInfo(info)
	go func() { time.Sleep(time.Second); quit() }()
	return nil
}

func runUpdateWorker(taskPath string) error {
	b, err := os.ReadFile(taskPath)
	if err != nil {
		return err
	}
	var task updateTask
	if err = json.Unmarshal(b, &task); err != nil {
		return err
	}
	if !filepath.IsAbs(task.Root) || task.ParentPID == 0 || !strings.EqualFold(filepath.Clean(task.Exe), filepath.Join(filepath.Clean(task.Root), "ResourceLens.exe")) || !pathInside(filepath.Join(task.Root, "data", "updates"), taskPath) || (task.Mode != "install" && task.Mode != "rollback") {
		return errors.New("升级任务路径或操作无效")
	}
	dir := filepath.Join(task.Root, "data", "updates")
	statePath := filepath.Join(dir, "state.json")
	writeState := func(status, message string) {
		info := task.Package
		info.Status = status
		info.CanInstall = false
		info.CanRollback = true
		info.Message = message
		data, _ := json.MarshalIndent(info, "", "  ")
		atomicWrite(statePath, data, 0600)
	}
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE, false, task.ParentPID)
	if err == nil {
		result, waitErr := windows.WaitForSingleObject(parent, 30000)
		windows.CloseHandle(parent)
		if waitErr != nil || result != windows.WAIT_OBJECT_0 {
			writeState("failed", "拾流未退出，升级已停止；请关闭软件后重试")
			return errors.New("等待主程序退出失败")
		}
	}
	if err != nil && !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		writeState("failed", "无法确认拾流已退出，未执行升级")
		return errors.New("无法检查主程序状态")
	}
	// Even failed verification/installation returns the user to the preserved app.
	defer func() {
		cmd := exec.Command(task.Exe)
		cmd.Dir = task.Root
		if cmd.Start() == nil {
			cmd.Process.Release()
		}
	}()
	backup := filepath.Join(dir, "ResourceLens.previous.exe")
	metaPath := filepath.Join(dir, "previous.json")
	if task.Mode == "rollback" {
		metadataBytes, err := os.ReadFile(metaPath)
		if err != nil {
			return err
		}
		var meta updateBackup
		if json.Unmarshal(metadataBytes, &meta) != nil {
			return errors.New("回退记录损坏")
		}
		sha, _, err := fileSHA256(backup)
		if err != nil || sha != meta.SHA256 {
			writeState("failed", "上一版程序备份校验失败，未执行回退")
			return errors.New("备份校验失败")
		}
		if err = replaceFromBackup(backup, task.Exe); err != nil {
			writeState("failed", "无法恢复上一版程序，请检查目录权限")
			return err
		}
		writeState("rolled-back", "已恢复上一版程序，配置与下载文件已保留")
	} else {
		info, err := inspectUpdatePackage(task.Package.Path)
		if err != nil || info.SHA256 != task.Package.SHA256 || info.Version != task.Package.Version || !pathInside(dir, task.Package.Path) {
			writeState("failed", "安装包校验失败，未执行升级")
			return errors.New("安装包校验失败")
		}
		sha, size, err := fileSHA256(task.Exe)
		if err != nil {
			return err
		}
		tempBackup := backup + ".new"
		os.Remove(tempBackup)
		if err = copyUpdateFile(task.Exe, tempBackup); err != nil {
			return err
		}
		if err = verifyUpdateBackup(tempBackup, sha, size); err != nil {
			os.Remove(tempBackup)
			writeState("failed", "当前程序备份校验失败，未启动安装程序")
			return err
		}
		if err = replaceFile(tempBackup, backup); err != nil {
			return err
		}
		metadataBytes, _ := json.Marshal(updateBackup{Version: task.Package.CurrentVersion, SHA256: sha})
		if err = atomicWrite(metaPath, metadataBytes, 0600); err != nil {
			return err
		}
		cmd := exec.Command(task.Package.Path, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-", "/DIR="+task.Root)
		hideCommand(cmd)
		err = cmd.Run()
		if err != nil {
			return restoreFailedUpdate(backup, task.Exe, "安装程序执行失败", err, writeState)
		}
		if info, err := os.Stat(task.Exe); err != nil || info.IsDir() {
			return restoreFailedUpdate(backup, task.Exe, "安装后主程序缺失", errors.New("主程序缺失"), writeState)
		}
		version, product, _, err := readWindowsVersion(task.Exe)
		if err != nil || version != task.Package.Version || !containsFold(product, "ResourceLens") {
			return restoreFailedUpdate(backup, task.Exe, "安装后的程序版本未匹配", errors.New("安装结果版本校验失败"), writeState)
		}
		writeState("installed", "更新安装完成，配置与下载文件已保留")
	}
	os.Remove(taskPath)
	return nil
}
func replaceFile(from, to string) error {
	f, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(f, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
