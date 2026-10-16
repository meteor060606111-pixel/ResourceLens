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
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const macBundleIdentifier = "com.resourcelens.desktop"

func updatePackageExtension() string    { return ".dmg" }
func (e *Engine) hasUpdateBackup() bool { return false }
func (e *Engine) rollbackUpdate() error {
	return errors.New("macOS 请从 GitHub Releases 下载上一版 DMG，退出拾流后替换应用；配置和下载文件会保留")
}
func replaceFile(from, to string) error { return os.Rename(from, to) }

func macBundleVersion(bundle string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	if output, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", bundle).CombinedOutput(); err != nil {
		return "", fmt.Errorf("应用签名完整性校验失败：%s", strings.TrimSpace(string(output)))
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
	return UpdateInfo{CurrentVersion: appVersion, Version: version, Status: "ready", Source: "local", Path: path, FileName: filepath.Base(path), SHA256: sha, Size: size, Available: true, CanInstall: true, Message: "已核验应用标识、芯片、版本、签名完整性和 SHA-256。安装后替换现有应用，成功后清理旧程序与暂存包；配置和下载文件保留。签名完整性校验不等同 Apple 公证"}, nil
}

func (e *Engine) launchUpdateWorker(mode string, info UpdateInfo) error {
	if mode != "install" {
		return errors.New("macOS 仅支持安装已准备的更新")
	}
	quitMu.RLock()
	quit := requestApplicationQuit
	quitMu.RUnlock()
	if quit == nil {
		return errors.New("请在桌面版拾流中安装更新")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	if err := validateMacInstallTarget(bundle); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(e.updateDir(), "worker-*.noindex")
	if err != nil {
		return err
	}
	helper := filepath.Join(dir, "ResourceLens-update")
	if err := copyUpdateFile(executable, helper); err != nil {
		os.RemoveAll(dir)
		return err
	}
	if err := os.Chmod(helper, 0700); err != nil {
		os.RemoveAll(dir)
		return err
	}
	task := macUpdateTask{Root: e.root, Bundle: bundle, ParentPID: os.Getpid(), Package: info}
	data, _ := json.Marshal(task)
	taskPath := filepath.Join(dir, "task.json")
	if err := os.WriteFile(taskPath, data, 0600); err != nil {
		os.RemoveAll(dir)
		return err
	}
	info.Status, info.CanInstall = "installing", false
	info.Message = "正在退出并替换现有应用，更新成功后自动打开"
	if err := e.setUpdateInfo(info); err != nil {
		os.RemoveAll(dir)
		return err
	}
	cmd := exec.Command(helper, "--update-worker", taskPath)
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		info.Status, info.CanInstall, info.Message = "ready", true, "无法启动升级助手，请重试"
		e.setUpdateInfo(info)
		return err
	}
	go cmd.Wait()
	go func() { time.Sleep(time.Second); quit() }()
	return nil
}

type macUpdateTask struct {
	Root      string     `json:"root"`
	Bundle    string     `json:"bundle"`
	ParentPID int        `json:"parentPid"`
	Package   UpdateInfo `json:"package"`
}

func validateMacInstallTarget(bundle string) error {
	user, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if bundle != "/Applications/ResourceLens.app" && bundle != filepath.Join(user, "Applications", "ResourceLens.app") {
		return errors.New("请先将拾流安装到 Applications，再使用应用内更新")
	}
	real, err := filepath.EvalSymlinks(bundle)
	if err != nil || real != bundle {
		return errors.New("应用安装路径异常，请从 Applications 中运行拾流")
	}
	return nil
}

// A single APFS/HFS+ exchange keeps the installed path present even if the
// machine loses power. Verification failure exchanges the original back.
func replaceMacBundle(staged, target, previous string, verify func(string) error) error {
	if err := unix.RenamexNp(staged, target, unix.RENAME_SWAP); err != nil {
		return err
	}
	if err := verify(target); err != nil {
		if restoreErr := unix.RenamexNp(staged, target, unix.RENAME_SWAP); restoreErr != nil {
			moveErr := os.Rename(staged, previous)
			return errors.Join(err, restoreErr, moveErr)
		}
		return err
	}
	return os.RemoveAll(staged)
}

func macSigningTeam(bundle string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, _ := exec.CommandContext(ctx, "/usr/bin/codesign", "-dv", "--verbose=4", bundle).CombinedOutput()
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "TeamIdentifier=") {
			team := strings.TrimPrefix(line, "TeamIdentifier=")
			if team != "not set" {
				return team
			}
		}
	}
	return ""
}

func runUpdateWorker(taskPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	workerDir := filepath.Dir(executable)
	if filepath.Clean(taskPath) != filepath.Join(workerDir, "task.json") || !strings.HasPrefix(filepath.Base(workerDir), "worker-") || !strings.HasSuffix(workerDir, ".noindex") {
		return errors.New("升级任务路径无效")
	}
	data, err := os.ReadFile(taskPath)
	if err != nil || len(data) > 16384 {
		return errors.New("升级任务无法读取")
	}
	var task macUpdateTask
	if json.Unmarshal(data, &task) != nil || task.ParentPID <= 1 || !filepath.IsAbs(task.Root) || !pathInside(filepath.Join(task.Root, "data", "updates"), workerDir) || !pathInside(filepath.Join(task.Root, "data", "updates"), task.Package.Path) {
		return errors.New("升级任务格式无效")
	}
	if err := validateMacInstallTarget(task.Bundle); err != nil {
		return err
	}
	defer os.RemoveAll(workerDir)
	statePath := filepath.Join(task.Root, "data", "updates", "state.json")
	state := task.Package
	writeState := func(status, message string) {
		state.Status, state.Message, state.CanInstall = status, message, false
		data, _ := json.MarshalIndent(state, "", "  ")
		_ = atomicWrite(statePath, data, 0600)
	}
	defer func() {
		if state.Status == "installing" || state.Status == "ready" {
			writeState("failed", "自动更新未完成，现有应用和下载文件已保留，请重新准备安装包")
		}
	}()
	checked, err := inspectUpdatePackage(task.Package.Path)
	if err != nil || checked.SHA256 != task.Package.SHA256 || checked.Version != task.Package.Version {
		return errors.New("升级包校验失败，已保留现有应用")
	}
	current, err := macBundleVersion(task.Bundle)
	if err != nil || compareVersions(checked.Version, current) <= 0 {
		return errors.New("更新版本不高于已安装版本")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	mount, err := os.MkdirTemp("", "resourcelens-install-*.noindex")
	if err != nil {
		return err
	}
	defer os.Remove(mount)
	if output, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-quiet", "-mountpoint", mount, checked.Path).CombinedOutput(); err != nil {
		return fmt.Errorf("无法挂载更新包：%s", strings.TrimSpace(string(output)))
	}
	defer exec.Command("/usr/bin/hdiutil", "detach", "-force", "-quiet", mount).Run()
	stage, err := os.MkdirTemp(filepath.Dir(task.Bundle), ".ResourceLens-update-*.noindex")
	if err != nil {
		return errors.New("无法写入 Applications，请检查当前用户的目录权限")
	}
	keepRecovery := false
	defer func() {
		if !keepRecovery {
			os.RemoveAll(stage)
		}
	}()
	staged, previous := filepath.Join(stage, "ResourceLens.app"), filepath.Join(stage, "previous")
	if output, err := exec.CommandContext(ctx, "/usr/bin/ditto", filepath.Join(mount, "ResourceLens.app"), staged).CombinedOutput(); err != nil {
		return fmt.Errorf("暂存应用失败：%s", strings.TrimSpace(string(output)))
	}
	team := macSigningTeam(task.Bundle)
	verify := func(bundle string) error {
		version, err := macBundleVersion(bundle)
		if err != nil || version != checked.Version {
			return errors.New("新应用验证失败")
		}
		if team != "" && macSigningTeam(bundle) != team {
			return errors.New("新应用开发团队与已安装应用不同，拒绝替换")
		}
		return nil
	}
	if err := verify(staged); err != nil {
		return err
	}
	until := time.Now().Add(90 * time.Second)
	for syscall.Kill(task.ParentPID, 0) == nil {
		if time.Now().After(until) {
			return errors.New("原程序尚未退出，已取消更新")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := replaceMacBundle(staged, task.Bundle, previous, verify); err != nil {
		restored, _ := macBundleVersion(task.Bundle)
		if restored != current {
			keepRecovery = true
			writeState("failed", "应用替换失败，恢复文件保留在 "+stage+"；请手动恢复")
		} else {
			writeState("failed", "更新未完成，已保留原应用，请重新尝试："+err.Error())
			_ = exec.Command("/usr/bin/open", task.Bundle).Run()
		}
		return err
	}
	state.CurrentVersion, state.Path = checked.Version, ""
	writeState("installed", "已更新至 "+checked.Version+"；旧程序与暂存安装包已清理")
	_ = os.Remove(task.Package.Path)
	register := "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	_ = exec.Command(register, "-f", task.Bundle).Run()
	return exec.Command("/usr/bin/open", task.Bundle).Run()
}
