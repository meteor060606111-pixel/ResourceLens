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
	"strconv"
	"strings"
	"time"
)

type audioProbe struct {
	Streams []struct {
		Type  string `json:"codec_type"`
		Codec string `json:"codec_name"`
	} `json:"streams"`
	Format struct {
		Name     string `json:"format_name"`
		Duration string `json:"duration"`
	} `json:"format"`
}

func probeAudio(ctx context.Context, tools, file string) (audioProbe, error) {
	var probe audioProbe
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out := &limitedBuffer{limit: 64 << 10}
	cmd := exec.Command(toolPath(tools, "ffprobe"), "-v", "error", "-show_entries", "stream=codec_type,codec_name:format=format_name,duration", "-of", "json", file)
	cmd.Stdout, cmd.Stderr = out, &tailBuffer{limit: 2048}
	if runManagedCommand(ctx, cmd) != nil || json.Unmarshal(out.Bytes(), &probe) != nil {
		return probe, errors.New("音频校验失败：文件不可读取或格式无效")
	}
	for _, stream := range probe.Streams {
		if stream.Type == "audio" && stream.Codec != "" {
			return probe, nil
		}
	}
	return probe, errors.New("音频校验失败：文件中没有有效音轨")
}

func validateMusicAudio(ctx context.Context, tools, file string) (float64, error) {
	probe, err := probeAudio(ctx, tools, file)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.Command(toolPath(tools, "ffmpeg"), "-hide_banner", "-loglevel", "error", "-nostdin", "-xerror", "-err_detect", "explode", "-i", file, "-map", "0:a:0", "-vn", "-sn", "-f", "null", "-")
	cmd.Stderr = &tailBuffer{limit: 2048}
	if err = runManagedCommand(ctx, cmd); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("音频校验失败：文件损坏、受保护或无法完整解码")
	}
	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	return duration, nil
}

func musicDurationNotice(expected, actual float64) string {
	if expected > 10 && actual > 0 && actual+5 < expected*.9 {
		return fmt.Sprintf("当前音源约 %.0f 秒，短于平台标注的 %.0f 秒，可能是试听片段", actual, expected)
	}
	return ""
}

func (e *Engine) prepareAudioPreview(file string) (localPreview, error) {
	result := localPreview{File: file, Quality: "已下载音频", Note: "直接播放原音频文件。"}
	tools, err := e.videoTools()
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Minute)
	defer cancel()
	probe, err := probeAudio(ctx, tools, file)
	if err != nil {
		return result, err
	}
	codec := ""
	for _, s := range probe.Streams {
		if s.Type == "audio" {
			codec = s.Codec
			break
		}
	}
	supported := map[string]bool{"mp3": true, "aac": true, "opus": true, "vorbis": true, "flac": true, "pcm_s16le": true, "pcm_s24le": true, "pcm_f32le": true}
	ext := strings.ToLower(filepath.Ext(file))
	if supported[codec] && map[string]bool{".mp3": true, ".m4a": true, ".mp4": true, ".aac": true, ".ogg": true, ".opus": true, ".flac": true, ".wav": true, ".webm": true}[ext] {
		return result, nil
	}
	e.previewMu.Lock()
	defer e.previewMu.Unlock()
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("原音频文件不可读取")
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("audio-preview-v1:%s:%d:%d", filepath.Clean(file), info.Size(), info.ModTime().UnixNano())))
	dir := filepath.Join(e.root, "data", "preview-cache")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return result, err
	}
	if !ownedDirectory(e.root, dir) {
		return result, errors.New("音频预览缓存目录无效")
	}
	target := filepath.Join(dir, hex.EncodeToString(key[:])+".m4a")
	result.File, result.MIME, result.Compatible = target, "audio/mp4", true
	result.Quality, result.Note = "兼容音频预览", "仅生成播放缓存，原下载音频的格式、质量和标签保持不变。"
	if existing, err := os.Lstat(target); err == nil {
		if !existing.Mode().IsRegular() {
			return result, errors.New("音频预览缓存文件无效")
		}
		if existing.Size() > 0 {
			if _, err = probeAudio(ctx, tools, target); err == nil {
				os.Chtimes(target, time.Now(), time.Now())
				return result, nil
			}
		}
		if err = os.Remove(target); err != nil {
			return result, errors.New("无法刷新音频预览缓存")
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}
	part, err := os.CreateTemp(dir, "audio-*.part")
	if err != nil {
		return result, err
	}
	temp := part.Name()
	part.Close()
	defer os.Remove(temp)
	cmd := exec.Command(toolPath(tools, "ffmpeg"), "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", file, "-map", "0:a:0", "-vn", "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", "-f", "mp4", temp)
	cmd.Stderr = &tailBuffer{limit: 2048}
	if err = runManagedCommand(ctx, cmd); err != nil {
		return result, errors.New("兼容音频预览生成失败，请用本机播放器打开原文件")
	}
	if _, err = probeAudio(ctx, tools, temp); err != nil {
		return result, err
	}
	if err = os.Rename(temp, target); err != nil {
		return result, err
	}
	e.trimPreviewCache(target)
	return result, nil
}
