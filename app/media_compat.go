package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type localPreview struct {
	File, Quality, Note string
	MIME                string
	Width, Height       int
	Compatible          bool
}

type previewProbe struct {
	Streams []struct {
		Type   string `json:"codec_type"`
		Codec  string `json:"codec_name"`
		Pixels string `json:"pix_fmt"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"streams"`
	Format struct {
		Name string `json:"format_name"`
	} `json:"format"`
}

func probePreview(ctx context.Context, tools, file string) (previewProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out := &limitedBuffer{limit: 64 << 10}
	cmd := exec.Command(toolPath(tools, "ffprobe"), "-v", "error", "-show_entries", "stream=codec_type,codec_name,pix_fmt,width,height:format=format_name", "-of", "json", file)
	cmd.Stdout, cmd.Stderr = out, &tailBuffer{limit: 2048}
	var result previewProbe
	if runManagedCommand(ctx, cmd) != nil || json.Unmarshal(out.Bytes(), &result) != nil {
		return result, errors.New("无法读取视频信息，请检查文件是否完整")
	}
	return result, nil
}

func (e *Engine) prepareLocalPreview(file, mediaType string) (localPreview, error) {
	result := localPreview{File: file, Quality: "已下载文件", Note: "直接播放原文件。"}
	if strings.HasPrefix(mediaType, "audio/") {
		return e.prepareAudioPreview(file)
	}
	if !strings.HasPrefix(mediaType, "video/") {
		return result, nil
	}
	tools, err := e.videoTools()
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Minute)
	defer cancel()
	probe, err := probePreview(ctx, tools, file)
	if err != nil {
		return result, err
	}
	videoOK, audioOK := false, true
	for _, s := range probe.Streams {
		switch s.Type {
		case "video":
			if result.Width > 0 {
				continue
			}
			result.Width, result.Height = s.Width, s.Height
			videoOK = s.Codec == "h264" && s.Pixels == "yuv420p" || s.Codec == "vp8" || s.Codec == "vp9" || s.Codec == "av1"
		case "audio":
			audioOK = audioOK && (s.Codec == "aac" || s.Codec == "mp3" || s.Codec == "opus" || s.Codec == "vorbis")
		}
	}
	if result.Width == 0 || result.Height == 0 {
		return result, errors.New("该文件没有可播放的视频画面")
	}
	containerOK := strings.Contains(","+probe.Format.Name+",", ",mp4,") || probe.Format.Name == "matroska,webm" && strings.EqualFold(filepath.Ext(file), ".webm")
	if videoOK && audioOK && containerOK {
		return result, nil
	}
	// This disposable playback copy never replaces or writes to the download.
	e.previewMu.Lock()
	defer e.previewMu.Unlock()
	if ctx.Err() != nil {
		return result, errors.New("兼容预览准备超时，请重新打开预览")
	}
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("原视频文件不可读取")
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("preview-h264-v1:%s:%d:%d", filepath.Clean(file), info.Size(), info.ModTime().UnixNano())))
	dir := filepath.Join(e.root, "data", "preview-cache")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return result, errors.New("无法创建预览缓存目录")
	}
	if !ownedDirectory(e.root, dir) {
		return result, errors.New("预览缓存目录无效")
	}
	target := filepath.Join(dir, hex.EncodeToString(key[:])+".mp4")
	if cached, err := os.Lstat(target); err == nil && cached.Mode().IsRegular() && cached.Size() > 0 {
		cachedProbe, err := probePreview(ctx, tools, target)
		if err == nil {
			for _, s := range cachedProbe.Streams {
				if s.Type == "video" && s.Codec == "h264" && s.Pixels == "yuv420p" {
					result.File, result.Compatible, result.Width, result.Height = target, true, s.Width, s.Height
					result.Quality, result.Note = "兼容预览", "仅转换预览缓存，原下载文件的画质和大小保持不变。"
					os.Chtimes(target, time.Now(), time.Now())
					return result, nil
				}
			}
		}
		if err := os.Remove(target); err != nil {
			return result, errors.New("无法刷新预览缓存")
		}
	}
	tmp, err := os.CreateTemp(dir, "preview-*.part")
	if err != nil {
		return result, errors.New("无法写入预览缓存")
	}
	part := tmp.Name()
	tmp.Close()
	defer os.Remove(part)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", file, "-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn", "-map_metadata", "-1", "-vf", "scale=w='min(1280,iw)':h='min(1280,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2:reset_sar=1", "-c:v", "libx264", "-preset", "veryfast", "-crf", "22", "-pix_fmt", "yuv420p", "-threads", "4", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-f", "mp4", part}
	cmd := exec.Command(toolPath(tools, "ffmpeg"), args...)
	cmd.Stderr = &tailBuffer{limit: 4096}
	if err := runManagedCommand(ctx, cmd); err != nil {
		if ctx.Err() != nil {
			return result, errors.New("兼容预览准备超时，请用本机播放器打开原文件")
		}
		return result, errors.New("兼容预览生成失败，请用本机播放器打开原文件")
	}
	converted, err := probePreview(ctx, tools, part)
	if err != nil {
		return result, err
	}
	for _, s := range converted.Streams {
		if s.Type == "video" && s.Codec == "h264" && s.Pixels == "yuv420p" {
			result.Width, result.Height, result.Compatible = s.Width, s.Height, true
			break
		}
	}
	if !result.Compatible {
		return result, errors.New("兼容预览没有生成有效画面")
	}
	if err := os.Rename(part, target); err != nil {
		return result, errors.New("无法保存预览缓存")
	}
	result.File, result.Quality, result.Note = target, "兼容预览", "仅转换预览缓存，原下载文件的画质和大小保持不变。"
	e.trimPreviewCache(target)
	return result, nil
}

var previewCacheName = regexp.MustCompile(`^[a-f0-9]{64}\.(mp4|m4a)$`)

// Called with previewMu held. Only generated regular files in the fixed cache
// directory qualify; symlinks and other files are never removed.
func (e *Engine) previewCacheEntries() []os.FileInfo {
	dir := filepath.Join(e.root, "data", "preview-cache")
	if !ownedDirectory(e.root, dir) {
		return nil
	}
	entries, _ := os.ReadDir(dir)
	files := []os.FileInfo{}
	for _, entry := range entries {
		if !previewCacheName.MatchString(entry.Name()) {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err == nil && info.Mode().IsRegular() {
			files = append(files, info)
		}
	}
	return files
}

func (e *Engine) trimPreviewCache(current string) {
	files := e.previewCacheEntries()
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime().Before(files[j].ModTime()) })
	protected := map[string]bool{current: true}
	e.mu.RLock()
	for _, ticket := range e.mediaTickets {
		if time.Now().Before(ticket.Expires) {
			protected[ticket.File] = true
		}
	}
	e.mu.RUnlock()
	var total int64
	for _, file := range files {
		total += file.Size()
	}
	dir := filepath.Join(e.root, "data", "preview-cache")
	for _, file := range files {
		target := filepath.Join(dir, file.Name())
		if total <= 512<<20 || protected[target] || time.Since(file.ModTime()) < 15*time.Minute {
			continue
		}
		if os.Remove(target) == nil {
			total -= file.Size()
		}
	}
}
