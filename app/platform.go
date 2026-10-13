package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func toolPath(dir, name string) string { return filepath.Join(dir, executableName(name)) }

// A selected base directory may itself be an OS alias or symlink. Owned
// subdirectories must remain physically inside it, without further links.
func ownedDirectory(base, dir string) bool {
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return false
	}
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	expected := filepath.Clean(filepath.Join(canonicalBase, rel))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(canonicalDir), expected)
	}
	return filepath.Clean(canonicalDir) == expected
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0111 != 0)
}

func defaultApplicationRoot() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "ResourceLens"), nil
	}
	exe, err := os.Executable()
	return filepath.Dir(exe), err
}

func defaultDownloadDirectory(root string) string {
	if runtime.GOOS == "darwin" {
		standard, err := defaultApplicationRoot()
		if resolved, resolveErr := filepath.EvalSymlinks(standard); resolveErr == nil {
			standard = resolved
		}
		if err == nil && filepath.Clean(root) == standard {
			home, err := os.UserHomeDir()
			if err == nil {
				return filepath.Join(home, "Downloads", "ResourceLens")
			}
		}
	}
	return filepath.Join(root, "downloads")
}

func mediaToolDirectories(root string) []string {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	dirs := []string{}
	if runtime.GOOS == "darwin" {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "..", "Resources", "video-engine"))
	}
	for _, base := range []string{filepath.Dir(exe), root, cwd, filepath.Dir(cwd)} {
		dirs = append(dirs, filepath.Join(base, "video-engine"))
	}
	return dirs
}

func (e *Engine) videoTools() (string, error) {
	for _, dir := range mediaToolDirectories(e.root) {
		found := true
		for _, name := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
			if !executableFile(toolPath(dir, name)) {
				found = false
				break
			}
		}
		if found {
			return filepath.Clean(dir), nil
		}
	}
	return "", errors.New("视频引擎缺失，请重新安装完整应用或恢复 video-engine 中的 yt-dlp、FFmpeg 和 FFprobe")
}

func nodeExecutable(dir string) string {
	if path := toolPath(dir, "node"); executableFile(path) {
		return path
	}
	if path, err := exec.LookPath(executableName("node")); err == nil {
		return path
	}
	return ""
}
