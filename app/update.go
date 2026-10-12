package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxUpdateBytes int64 = 1 << 30

type UpdateInfo struct {
	CurrentVersion string `json:"currentVersion"`
	Version        string `json:"version,omitempty"`
	Status         string `json:"status"`
	Source         string `json:"source,omitempty"`
	Path           string `json:"path,omitempty"`
	FileName       string `json:"fileName,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Size           int64  `json:"size,omitempty"`
	Available      bool   `json:"available"`
	CanInstall     bool   `json:"canInstall"`
	CanRollback    bool   `json:"canRollback"`
	Message        string `json:"message"`
	URL            string `json:"url,omitempty"`
}
type updateFeed struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}
type updateTask struct {
	Root      string     `json:"root"`
	Exe       string     `json:"exe"`
	ParentPID uint32     `json:"parentPid"`
	Mode      string     `json:"mode"`
	Package   UpdateInfo `json:"package"`
}
type updateBackup struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

var quitMu sync.RWMutex
var requestApplicationQuit func()
var updateOperationMu sync.Mutex
var versionPattern = regexp.MustCompile(`^\d{1,5}\.\d{1,5}\.\d{1,5}(?:\.\d{1,5})?$`)
var shaPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

func validateUpdateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("更新源须为不含账号信息和片段的 HTTPS 地址")
	}
	return nil
}
func normalizedVersion(v string) (string, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if !versionPattern.MatchString(v) {
		return "", errors.New("版本号须为数字形式，例如 1.2.1")
	}
	parts := strings.Split(v, ".")
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n > 65535 {
			return "", errors.New("版本号范围无效")
		}
		parts[i] = strconv.Itoa(n)
	}
	if len(parts) == 4 && parts[3] == "0" {
		parts = parts[:3]
	}
	return strings.Join(parts, "."), nil
}
func compareVersions(a, b string) int {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 4; i++ {
		av, bv := 0, 0
		if i < len(aa) {
			av, _ = strconv.Atoi(aa[i])
		}
		if i < len(bb) {
			bv, _ = strconv.Atoi(bb[i])
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return 0
}
func (e *Engine) beginUpdateOperation() (func(), error) {
	if !updateOperationMu.TryLock() {
		return nil, errors.New("另一个更新操作正在进行，请稍后再试")
	}
	e.mu.RLock()
	installing := e.update.Status == "installing"
	e.mu.RUnlock()
	if installing {
		updateOperationMu.Unlock()
		return nil, errors.New("正在关闭软件并执行升级，请等待完成")
	}
	return updateOperationMu.Unlock, nil
}
func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func copyUpdateFile(from, to string) error {
	f, err := os.Open(from)
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(to)
		}
	}()
	if _, err = io.Copy(out, io.LimitReader(f, maxUpdateBytes+1)); err != nil {
		return err
	}
	if info, err := out.Stat(); err != nil || info.Size() > maxUpdateBytes {
		return errors.New("更新文件超过 1 GB 上限")
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

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

func (e *Engine) updateDir() string       { return filepath.Join(e.root, "data", "updates") }
func (e *Engine) updateStatePath() string { return filepath.Join(e.updateDir(), "state.json") }
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
func (e *Engine) setUpdateInfo(info UpdateInfo) error {
	info.CurrentVersion = appVersion
	info.CanRollback = e.hasUpdateBackup()
	if err := os.MkdirAll(e.updateDir(), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(e.updateStatePath(), b, 0600); err != nil {
		return err
	}
	e.mu.Lock()
	e.update = info
	e.mu.Unlock()
	return nil
}
func (e *Engine) loadUpdateInfo() {
	info := UpdateInfo{CurrentVersion: appVersion, Status: "idle", Message: "支持选择本地新版安装程序；在线更新源可在设置中预留"}
	if b, err := os.ReadFile(e.updateStatePath()); err == nil {
		var saved UpdateInfo
		if json.Unmarshal(b, &saved) == nil {
			info = saved
			info.CurrentVersion = appVersion
			if info.Status == "downloading" || info.Status == "installing" {
				info.Status = "failed"
				info.CanInstall = false
				info.Message = "上次更新未正常完成，请重新选择安装包"
			}
			if info.Status == "ready" {
				sha, _, err := fileSHA256(info.Path)
				info.CanInstall = err == nil && sha == info.SHA256 && compareVersions(info.Version, appVersion) > 0
				if !info.CanInstall {
					info.Status = "idle"
					info.Message = "之前准备的安装包已失效或当前已安装该版本，请选择新版安装包"
				}
			}
		}
	}
	info.CanRollback = e.hasUpdateBackup()
	e.mu.Lock()
	e.update = info
	e.mu.Unlock()
}

func updateHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("更新源重定向过多")
		}
		return validateUpdateURL(req.URL.String())
	}}
}
func (e *Engine) checkUpdate() (any, error) {
	unlock, err := e.beginUpdateOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	feedURL := e.configSnapshot().UpdateURL
	if feedURL == "" {
		info := UpdateInfo{Status: "idle", Message: "尚未配置在线更新源，请选择本地新版安装程序"}
		if err := e.setUpdateInfo(info); err != nil {
			return nil, err
		}
		e.mu.RLock()
		info = e.update
		e.mu.RUnlock()
		return info, nil
	}
	if err := validateUpdateURL(feedURL); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := updateHTTPClient().Do(req)
	if err != nil {
		return nil, errors.New("无法连接在线更新源，请检查地址和网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("更新源返回 HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(b) > 65536 {
		return nil, errors.New("更新信息读取失败或超过大小上限")
	}
	var feed updateFeed
	if err = json.Unmarshal(b, &feed); err != nil {
		return nil, errors.New("更新源须返回包含 version、url 和 sha256 的 JSON")
	}
	if feed.Version, err = normalizedVersion(feed.Version); err != nil {
		return nil, err
	}
	if err = validateUpdateURL(feed.URL); err != nil {
		return nil, err
	}
	if !shaPattern.MatchString(feed.SHA256) {
		return nil, errors.New("更新源缺少有效的 SHA-256 校验值")
	}
	info := UpdateInfo{Version: feed.Version, Status: "available", Source: "online", URL: feed.URL, SHA256: strings.ToLower(feed.SHA256), Available: compareVersions(feed.Version, appVersion) > 0, Message: "发现新版，可下载并核验安装包"}
	if !info.Available {
		info.Status = "latest"
		info.Message = "当前已是该更新源提供的最新版本"
	}
	if err = e.setUpdateInfo(info); err != nil {
		return nil, err
	}
	e.mu.RLock()
	info = e.update
	e.mu.RUnlock()
	return info, nil
}

func (e *Engine) prepareLocalUpdate(path string) (any, error) {
	unlock, err := e.beginUpdateOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return e.prepareLocalUpdateUnlocked(path)
}
func (e *Engine) prepareLocalUpdateUnlocked(path string) (any, error) {
	info, err := inspectUpdatePackage(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	if compareVersions(info.Version, appVersion) <= 0 {
		return nil, fmt.Errorf("安装包版本 %s 不高于当前 %s，请选择新版安装程序", info.Version, appVersion)
	}
	if err = os.MkdirAll(e.updateDir(), 0700); err != nil {
		return nil, err
	}
	dest := filepath.Join(e.updateDir(), "ResourceLens-"+info.Version+"-"+info.SHA256[:12]+".exe")
	if existing, _, err := fileSHA256(dest); err == nil {
		if existing != info.SHA256 {
			return nil, errors.New("已有暂存文件校验不一致，请重新选择安装包")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else if err = copyUpdateFile(path, dest); err != nil {
		return nil, err
	}
	sha, size, err := fileSHA256(dest)
	if err != nil || sha != info.SHA256 || size != info.Size {
		return nil, errors.New("安装包暂存校验失败，未准备升级")
	}
	info.Path = dest
	if err = e.setUpdateInfo(info); err != nil {
		return nil, err
	}
	e.mu.RLock()
	info = e.update
	e.mu.RUnlock()
	return info, nil
}

func (e *Engine) downloadUpdate() (any, error) {
	unlock, err := e.beginUpdateOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	e.mu.RLock()
	info := e.update
	e.mu.RUnlock()
	if info.Source != "online" || !info.Available || info.URL == "" || !shaPattern.MatchString(info.SHA256) {
		return nil, errors.New("请先检查更新并选择可用新版")
	}
	if err := validateUpdateURL(info.URL); err != nil {
		return nil, err
	}
	info.Status = "downloading"
	info.CanInstall = false
	info.Message = "正在下载并核验更新包"
	if err := e.setUpdateInfo(info); err != nil {
		return nil, err
	}
	fail := func(err error) (any, error) {
		info.Status = "failed"
		info.Message = err.Error()
		e.setUpdateInfo(info)
		return nil, err
	}
	if err := os.MkdirAll(e.updateDir(), 0700); err != nil {
		return fail(err)
	}
	f, err := os.CreateTemp(e.updateDir(), "package-*.exe")
	if err != nil {
		return fail(err)
	}
	path := f.Name()
	defer os.Remove(path)
	req, err := http.NewRequestWithContext(e.ctx, "GET", info.URL, nil)
	if err != nil {
		f.Close()
		return fail(err)
	}
	resp, err := updateHTTPClient().Do(req)
	if err != nil {
		f.Close()
		return fail(errors.New("更新包下载失败，请检查网络"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength > maxUpdateBytes {
		f.Close()
		return fail(errors.New("更新包响应无效或超过 1 GB 上限"))
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxUpdateBytes+1))
	f.Close()
	if err != nil || n > maxUpdateBytes {
		return fail(errors.New("更新包下载未完成或超出大小上限"))
	}
	sha, _, err := fileSHA256(path)
	if err != nil || sha != info.SHA256 {
		return fail(errors.New("更新包 SHA-256 与更新源不一致，已拒绝安装"))
	}
	checked, err := inspectUpdatePackage(path)
	if err != nil {
		return fail(err)
	}
	if checked.Version != info.Version {
		return fail(errors.New("更新包版本与更新源声明不一致"))
	}
	prepared, err := e.prepareLocalUpdateUnlocked(path)
	if err != nil {
		return fail(err)
	}
	final := prepared.(UpdateInfo)
	final.Source = "online"
	final.URL = info.URL
	final.Message = "安装包版本和 SHA-256 已与更新源匹配，点击安装后升级"
	if err = e.setUpdateInfo(final); err != nil {
		return fail(err)
	}
	e.mu.RLock()
	final = e.update
	e.mu.RUnlock()
	return final, nil
}

func (e *Engine) installPreparedUpdate() error {
	unlock, err := e.beginUpdateOperation()
	if err != nil {
		return err
	}
	defer unlock()
	e.mu.RLock()
	info := e.update
	for _, j := range e.jobs {
		if activeStatus(j.Status) {
			e.mu.RUnlock()
			return errors.New("请先暂停或完成下载，再安装更新")
		}
	}
	e.mu.RUnlock()
	if info.Status != "ready" || !info.CanInstall || !pathInside(e.updateDir(), info.Path) {
		return errors.New("请先选择并核验新版安装程序")
	}
	checked, err := inspectUpdatePackage(info.Path)
	if err != nil {
		return err
	}
	if checked.SHA256 != info.SHA256 || checked.Version != info.Version || compareVersions(checked.Version, appVersion) <= 0 {
		return errors.New("暂存安装包已改变或版本无效，请重新选择")
	}
	return e.launchUpdateWorker("install", info)
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
func verifyUpdateBackup(path, expectedSHA string, expectedSize int64) error {
	sha, size, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("读取程序备份失败：%w", err)
	}
	if sha != expectedSHA || size != expectedSize {
		return errors.New("程序备份内容校验失败")
	}
	return nil
}

func restoreFailedUpdate(backup, target, reason string, cause error, writeState func(string, string)) error {
	if restoreErr := replaceFromBackup(backup, target); restoreErr != nil {
		writeState("failed", reason+"；恢复之前的主程序失败，备份保留在 data\\updates，请检查目录权限并手动恢复")
		return errors.Join(cause, fmt.Errorf("恢复程序失败：%w", restoreErr))
	}
	writeState("failed", reason+"；已恢复之前的主程序，请检查安装日志后重试")
	return cause
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
func replaceFromBackup(backup, target string) error {
	sha, size, err := fileSHA256(backup)
	if err != nil {
		return err
	}
	temp := target + ".restore"
	os.Remove(temp)
	if err := copyUpdateFile(backup, temp); err != nil {
		return err
	}
	if err := verifyUpdateBackup(temp, sha, size); err != nil {
		os.Remove(temp)
		return err
	}
	return replaceFile(temp, target)
}
