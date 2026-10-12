package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func containsFold(s, part string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(part))
}

type CheckItem struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	Message string `json:"message"`
}
type SelfCheckResult struct {
	Version      string      `json:"version"`
	Items        []CheckItem `json:"items"`
	Ready        bool        `json:"ready"`
	CaptureReady bool        `json:"captureReady"`
}

func writableDirectory(dir string) bool {
	f, err := os.CreateTemp(dir, ".resourcelens-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	return closeErr == nil && removeErr == nil
}

func (e *Engine) selfCheck() any {
	cfg := e.configSnapshot()
	r := SelfCheckResult{Version: appVersion, Ready: true, CaptureReady: true, Items: []CheckItem{}}
	add := func(id, label, status, message string) {
		r.Items = append(r.Items, CheckItem{id, label, status, message})
		if status == "error" {
			r.Ready = false
		}
	}
	for _, d := range []struct{ id, label, path string }{{"data-directory", "本机配置目录", filepath.Join(e.root, "data")}, {"download-directory", "下载目录", cfg.DownloadDir}} {
		if writableDirectory(d.path) {
			add(d.id, d.label, "ok", "目录可读写")
		} else {
			add(d.id, d.label, "error", "目录不可写，请在设置中选择可写目录并检查磁盘空间")
		}
	}
	if _, err := e.videoTools(); err != nil {
		add("video-engine", "视频解析和处理引擎", "error", "yt-dlp、FFmpeg 或 FFprobe 缺失，请恢复 video-engine 文件夹")
	} else {
		add("video-engine", "视频解析和处理引擎", "ok", "yt-dlp、FFmpeg 和 FFprobe 已就绪")
	}
	if !cfg.HTTPS {
		add("certificate", "HTTPS 捕获证书", "warning", "HTTPS 捕获已关闭；开启并安装证书后可捕获视频号")
		r.CaptureReady = false
	} else if !e.certInstalled.Load() {
		add("certificate", "HTTPS 捕获证书", "warning", "尚未安装本机捕获证书，请点击安装 HTTPS 证书")
		r.CaptureReady = false
	} else if e.ca == nil || e.ca.cert == nil || time.Now().After(e.ca.cert.NotAfter) {
		add("certificate", "HTTPS 捕获证书", "error", "捕获证书无效或已过期")
		r.CaptureReady = false
	} else {
		add("certificate", "HTTPS 捕获证书", "ok", "本机捕获证书已安装且有效")
	}
	e.mu.RLock()
	capture := e.capture
	listener := e.proxyListener
	e.mu.RUnlock()
	if listener == nil {
		add("proxy", "本地捕获代理", "error", "代理未启动")
		r.CaptureReady = false
	} else if !capture {
		add("proxy", "本地捕获代理", "ok", "代理已就绪，点击开始捕获即可使用")
	} else {
		ws := e.wechatStatus()
		if ready, ok := ws["proxyReady"].(bool); ok && ready {
			add("proxy", "本地捕获代理", "ok", "捕获已开启，系统代理连接正常")
		} else {
			add("proxy", "本地捕获代理", "warning", "系统代理已改变，请停止后重新开始捕获")
			r.CaptureReady = false
		}
	}
	ws := e.wechatStatus()
	code, _ := ws["code"].(string)
	message, _ := ws["message"].(string)
	status := "warning"
	if code == "captured" || code == "connected" {
		status = "ok"
	}
	add("wechat", "微信视频号连接", status, message)
	return r
}

// Build diagnostics from a strict allowlist. Raw logs/session/config are not
// copied because they may contain cookies, signed URLs, keys or video titles.
func (e *Engine) diagnosticSnapshot() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	kinds, sites, statuses, errors := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for _, r := range e.resources {
		switch r.Type {
		case "video", "audio", "image", "playlist", "file":
			kinds[r.Type]++
		default:
			kinds["other"]++
		}
		switch platformName(r.Site, r.URL) {
		case "哔哩哔哩":
			sites["bilibili"]++
		case "抖音":
			sites["douyin"]++
		case "微信视频号":
			sites["wechat"]++
		case "快手":
			sites["kuaishou"]++
		case "小红书":
			sites["xiaohongshu"]++
		default:
			sites["other"]++
		}
	}
	for _, j := range e.jobs {
		switch j.Status {
		case "queued", "resolving", "downloading", "retrying", "merging", "validating", "paused", "completed", "failed", "cancelled":
			statuses[j.Status]++
		default:
			statuses["other"]++
		}
		if j.Error != "" {
			errors[diagnosticErrorCode(j.Error)]++
		}
	}
	return map[string]any{
		"schema": 1, "createdAt": time.Now().UTC().Format(time.RFC3339), "version": appVersion, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"settings":  map[string]any{"segments": e.config.Segments, "concurrent": e.config.Concurrent, "https": e.config.HTTPS, "classification": e.config.ClassifyBy, "duplicatePolicy": e.config.DuplicatePolicy, "onlineUpdateConfigured": e.config.UpdateURL != ""},
		"capture":   map[string]any{"enabled": e.capture, "certificateInstalled": e.certInstalled.Load(), "wechatRequests": e.wechatRequests.Load(), "wechatMetadata": e.wechatMetadataCount.Load(), "wechatTLSFailures": e.wechatTLSFailures.Load(), "htmlPatches": e.wechatHTMLPatches.Load(), "scriptPatches": e.wechatScriptPatches.Load()},
		"resources": map[string]any{"count": len(e.resources), "types": kinds, "platforms": sites}, "jobs": map[string]any{"count": len(e.jobs), "statuses": statuses, "errorCategories": errors},
	}
}

func diagnosticErrorCode(message string) string {
	// Categories are fixed literals; never return any fragment of the input.
	for _, pair := range [][2]string{{"context canceled", "cancelled"}, {"取消", "cancelled"}, {"timeout", "timeout"}, {"超时", "timeout"}, {"403", "access-denied"}, {"401", "access-denied"}, {"权限", "access-denied"}, {"证书", "certificate"}, {"画质", "quality"}, {"版本", "quality"}, {"解析", "parse"}, {"空间", "storage"}, {"目录", "storage"}, {"404", "not-found"}} {
		if containsFold(message, pair[0]) {
			return pair[1]
		}
	}
	return "other"
}

func (e *Engine) exportDiagnostics() (any, error) {
	dir := filepath.Join(e.root, "diagnostics")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("无法创建诊断目录：%w", err)
	}
	f, err := os.CreateTemp(dir, "拾流-诊断-"+time.Now().Format("20060102-150405")+"-*.zip")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	z := zip.NewWriter(f)
	entries := map[string]any{"diagnostics.json": e.diagnosticSnapshot(), "self-check.json": e.selfCheck()}
	for name, value := range entries {
		w, err := z.Create(name)
		if err != nil {
			return nil, err
		}
		if err = json.NewEncoder(w).Encode(value); err != nil {
			return nil, err
		}
	}
	w, err := z.Create("说明.txt")
	if err != nil {
		return nil, err
	}
	if _, err = fmt.Fprint(w, "拾流诊断包仅包含版本、自检结果、匿名汇总和固定错误分类。\n不包含日志、完整标题、媒体地址、Cookie、令牌、解码密钥、证书私钥或下载视频。\n"); err != nil {
		return nil, err
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	ok = true
	return map[string]any{"file": name, "files": 3, "message": "诊断包已导出，仅包含匿名汇总和自检结果"}, nil
}
