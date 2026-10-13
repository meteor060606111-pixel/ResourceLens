package main

import (
	"context"
	"debug/macho"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const macBundleIdentifier = "com.resourcelens.desktop"

func updatePackageExtension() string    { return ".dmg" }
func (e *Engine) hasUpdateBackup() bool { return false }
func (e *Engine) rollbackUpdate() error {
	return errors.New("macOS 请从 GitHub Releases 下载上一版 DMG，退出拾流后替换应用；配置和下载文件会保留")
}
func runUpdateWorker(string) error {
	return errors.New("macOS 使用 DMG 安装，无需 Windows 升级助手")
}
func replaceFile(from, to string) error { return os.Rename(from, to) }

func macBundleVersion(bundle string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", filepath.Join(bundle, "Contents", "Info.plist")).Output()
	if err != nil {
		return "", errors.New("磁盘映像中缺少有效的应用信息")
	}
	var metadata struct {
		Identifier string `json:"CFBundleIdentifier"`
		Version    string `json:"CFBundleShortVersionString"`
		Executable string `json:"CFBundleExecutable"`
	}
	if json.Unmarshal(output, &metadata) != nil || metadata.Identifier != macBundleIdentifier || metadata.Executable != "ResourceLens" {
		return "", errors.New("这不是拾流 ResourceLens 的 macOS 应用")
	}
	exe := filepath.Join(bundle, "Contents", "MacOS", "ResourceLens")
	want := macho.CpuArm64
	if runtime.GOARCH == "amd64" {
		want = macho.CpuAmd64
	}
	if fat, err := macho.OpenFat(exe); err == nil {
		defer fat.Close()
		found := false
		for _, arch := range fat.Arches {
			found = found || arch.Cpu == want
		}
		if !found {
			return "", errors.New("安装包不支持当前 Mac 芯片，请下载匹配版本")
		}
	} else {
		binary, err := macho.Open(exe)
		if err != nil {
			return "", errors.New("安装包中缺少有效的 macOS 主程序")
		}
		defer binary.Close()
		if binary.Cpu != want {
			return "", errors.New("安装包不支持当前 Mac 芯片，请下载 Apple 芯片或 Intel 对应版本")
		}
	}
	return normalizedVersion(metadata.Version)
}

func inspectUpdatePackage(path string) (UpdateInfo, error) {
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".dmg") {
		return UpdateInfo{}, errors.New("请选择完整路径的拾流 .dmg 安装包")
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() < 1<<20 || info.Size() > maxUpdateBytes {
		return UpdateInfo{}, errors.New("安装文件不存在、无法读取或大小无效")
	}
	sha, size, err := fileSHA256(path)
	if err != nil {
		return UpdateInfo{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "verify", "-quiet", path).CombinedOutput(); err != nil {
		return UpdateInfo{}, fmt.Errorf("磁盘映像校验失败：%s", strings.TrimSpace(string(output)))
	}
	mount, err := os.MkdirTemp("", "resourcelens-update-*")
	if err != nil {
		return UpdateInfo{}, err
	}
	defer os.Remove(mount)
	if output, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-quiet", "-mountpoint", mount, path).CombinedOutput(); err != nil {
		return UpdateInfo{}, fmt.Errorf("无法只读检查安装映像：%s", strings.TrimSpace(string(output)))
	}
	defer func() {
		detachCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		exec.CommandContext(detachCtx, "/usr/bin/hdiutil", "detach", "-force", "-quiet", mount).Run()
	}()
	version, err := macBundleVersion(filepath.Join(mount, "ResourceLens.app"))
	if err != nil {
		return UpdateInfo{}, err
	}
	return UpdateInfo{CurrentVersion: appVersion, Version: version, Status: "ready", Source: "local", Path: path, FileName: filepath.Base(path), SHA256: sha, Size: size, Available: true, CanInstall: true, Message: "已核验应用标识、芯片、版本和 SHA-256。打开 DMG 后拾流会退出，将应用拖入 Applications 完成更新；配置和下载文件保留"}, nil
}

func (e *Engine) launchUpdateWorker(mode string, info UpdateInfo) error {
	if mode != "install" {
		return errors.New("macOS 使用 DMG 手动替换应用")
	}
	quitMu.RLock()
	quit := requestApplicationQuit
	quitMu.RUnlock()
	if quit == nil {
		return errors.New("请在桌面版拾流中打开安装映像")
	}
	if err := exec.Command("/usr/bin/open", info.Path).Run(); err != nil {
		return fmt.Errorf("打开安装映像失败：%w", err)
	}
	info.Status, info.CanInstall = "opened", false
	info.Message = "安装映像已打开；将 ResourceLens.app 拖入 Applications 替换，配置和下载文件保留"
	if err := e.setUpdateInfo(info); err != nil {
		return err
	}
	go func() { time.Sleep(time.Second); quit() }()
	return nil
}
