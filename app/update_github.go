package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const officialRepository = "meteor060606111-pixel/ResourceLens"
const officialReleasePage = "https://github.com/" + officialRepository + "/releases"

type githubReleaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type githubRelease struct {
	Tag        string               `json:"tag_name"`
	Draft      bool                 `json:"draft"`
	Prerelease bool                 `json:"prerelease"`
	Assets     []githubReleaseAsset `json:"assets"`
}

func selectMacRelease(releases []githubRelease, arch, current string) (UpdateInfo, error) {
	if arch != "arm64" && arch != "amd64" {
		return UpdateInfo{}, errors.New("没有适用于当前 Mac 芯片的更新")
	}
	info := UpdateInfo{CurrentVersion: current, Version: current, Status: "latest", Source: "github", Message: "当前没有适用于这台 Mac 的可用更新"}
	for _, release := range releases {
		if release.Draft || release.Prerelease || !strings.HasSuffix(release.Tag, "-macos") {
			continue
		}
		version, err := normalizedVersion(strings.TrimSuffix(release.Tag, "-macos"))
		if err != nil || compareVersions(version, info.Version) <= 0 {
			continue
		}
		name := fmt.Sprintf("ResourceLens-%s-macos-%s.dmg", version, arch)
		for _, asset := range release.Assets {
			wantURL := "https://github.com/" + officialRepository + "/releases/download/" + release.Tag + "/" + name
			if asset.Name != name || asset.URL != wantURL || asset.Size < 1<<20 || asset.Size > maxUpdateBytes {
				continue
			}
			digest := strings.TrimPrefix(asset.Digest, "sha256:")
			if !shaPattern.MatchString(digest) {
				// Old releases without GitHub's digest can still be downloaded
				// manually; never turn an absent digest into an installable update.
				continue
			}
			info = UpdateInfo{CurrentVersion: current, Version: version, Status: "available", Source: "github", URL: asset.URL, FileName: name, SHA256: strings.ToLower(digest), Size: asset.Size, Available: true, Message: "GitHub 有适用于此 Mac 的新版；下载后会与 GitHub SHA-256 核对，安装时替换现有应用"}
		}
	}
	return info, nil
}

func (e *Engine) checkGitHubMacUpdate() (UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+officialRepository+"/releases?per_page=100", nil)
	if err != nil {
		return UpdateInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ResourceLens/"+appVersion)
	response, err := updateHTTPClient().Do(req)
	if err != nil {
		return UpdateInfo{}, errors.New("无法连接 GitHub，请稍后重试或选择本地安装包")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return UpdateInfo{}, fmt.Errorf("GitHub 更新检查返回 HTTP %d，请稍后重试", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return UpdateInfo{}, errors.New("GitHub 更新信息读取失败或过大")
	}
	var releases []githubRelease
	if json.Unmarshal(data, &releases) != nil {
		return UpdateInfo{}, errors.New("GitHub 更新信息格式无效")
	}
	return selectMacRelease(releases, runtime.GOARCH, appVersion)
}

// Background checks never install, interrupt downloads, or replace a package
// already prepared by the user. Only the desktop's explicit install action exits.
func (e *Engine) startUpdateChecks() {
	if runtime.GOOS != "darwin" {
		return
	}
	e.workers.Add(1)
	go func() {
		defer e.workers.Done()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-timer.C:
				e.mu.RLock()
				check := e.config.AutoCheckUpdates && e.update.Status != "ready" && e.update.Status != "downloading" && e.update.Status != "installing"
				e.mu.RUnlock()
				if check {
					_, _ = e.checkUpdate()
				}
				timer.Reset(12 * time.Hour)
			}
		}
	}()
}
