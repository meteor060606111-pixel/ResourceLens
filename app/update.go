package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
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

func (e *Engine) updateDir() string       { return filepath.Join(e.root, "data", "updates") }
func (e *Engine) updateStatePath() string { return filepath.Join(e.updateDir(), "state.json") }
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
	info := UpdateInfo{CurrentVersion: appVersion, Status: "idle", Message: "可检查新版，或选择本地安装包升级"}
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
	if info.Version != "" && compareVersions(info.Version, appVersion) <= 0 {
		info.Available, info.CanInstall = false, false
	}
	info.CanRollback = e.hasUpdateBackup()
	e.pruneUpdatePackages(info.Path)
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
	if feedURL == "" && runtime.GOOS == "darwin" {
		info, err := e.checkGitHubMacUpdate()
		if err != nil {
			return nil, err
		}
		if err := e.setUpdateInfo(info); err != nil {
			return nil, err
		}
		return info, nil
	}
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
	dest := filepath.Join(e.updateDir(), "ResourceLens-"+info.Version+"-"+info.SHA256[:12]+updatePackageExtension())
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
	if (info.Source != "online" && info.Source != "github") || !info.Available || info.URL == "" || !shaPattern.MatchString(info.SHA256) {
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
	f, err := os.CreateTemp(e.updateDir(), "package-*"+updatePackageExtension())
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
	final.Source = info.Source
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

// Only remove installers created by prepareLocalUpdate in our private cache.
// User downloads, apps elsewhere and a currently prepared package are untouched.
func (e *Engine) pruneUpdatePackages(keep string) {
	owned := regexp.MustCompile(`^ResourceLens-(\d+\.\d+\.\d+(?:\.\d+)?)-[a-f0-9]{12}\.(dmg|exe)$`)
	entries, _ := os.ReadDir(e.updateDir())
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		match := owned.FindStringSubmatch(entry.Name())
		path := filepath.Join(e.updateDir(), entry.Name())
		if match != nil && path != keep && compareVersions(match[1], appVersion) <= 0 {
			_ = os.Remove(path)
		}
	}
}
