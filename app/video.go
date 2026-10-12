package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const weChatPageError = "这个视频号分享链接只提供预览页，没有公开的视频播放地址，当前无法从该链接导出 MP4。请提供实际媒体直链；拾流不会再把此页面保存成视频。"
const videoFormat = "bv*+ba/b"

func activeStatus(s string) bool {
	return s == "queued" || s == "resolving" || s == "downloading" || s == "retrying" || s == "merging" || s == "validating"
}
func htmlMIME(mt string) bool {
	mt = strings.ToLower(strings.TrimSpace(strings.Split(mt, ";")[0]))
	return mt == "text/html" || mt == "application/xhtml+xml"
}
func hostIn(host string, domains ...string) bool {
	host = strings.ToLower(host)
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
func isWeChatPage(u *url.URL) bool {
	return u != nil && (hostIn(u.Hostname(), "channels.weixin.qq.com") || hostIn(u.Hostname(), "weixin.qq.com") && strings.HasPrefix(u.Path, "/sph/"))
}
func knownVideoPage(u *url.URL) bool {
	return hostIn(u.Hostname(), "bilibili.com", "b23.tv", "youtube.com", "youtu.be", "vimeo.com", "douyin.com", "iesdouyin.com", "kuaishou.com", "kwai.com", "xiaohongshu.com", "xhslink.com", "twitter.com", "x.com")
}

// Executables are shipped alongside the app; tests and source builds also use the project directory.
func (e *Engine) videoTools() (string, error) {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	for _, root := range []string{filepath.Dir(exe), e.root, cwd, filepath.Dir(cwd)} {
		dir := filepath.Join(root, "video-engine")
		found := true
		for _, name := range []string{"yt-dlp.exe", "ffmpeg.exe", "ffprobe.exe"} {
			if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.IsDir() {
				found = false
				break
			}
		}
		if found {
			return dir, nil
		}
	}
	return "", errors.New("视频引擎缺失，请把便携包中的 video-engine 文件夹与 ResourceLens.exe 放在一起")
}
func extractorArgs(dir string, headers http.Header) []string {
	a := []string{"--ignore-config", "--no-playlist", "--no-warnings", "--no-color", "--no-cache-dir", "--proxy", "", "--socket-timeout", "20", "--retries", "2", "--fragment-retries", "3", "--extractor-retries", "1", "--ffmpeg-location", dir, "--windows-filenames", "--trim-filenames", "120", "-f", videoFormat}
	// Only forward the same resource's required headers, never import browser credentials.
	for _, key := range []string{"User-Agent", "Referer", "Origin"} {
		if v := headers.Get(key); v != "" && !strings.ContainsAny(v, "\r\n") {
			a = append(a, "--add-header", key+":"+v)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "node.exe")); err == nil {
		a = append(a, "--js-runtimes", "node:"+filepath.Join(dir, "node.exe"))
	}
	return a
}

type videoFormatInfo struct {
	ID      string  `json:"format_id"`
	ACodec  string  `json:"acodec"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	FPS     float64 `json:"fps"`
	Bitrate float64 `json:"tbr"`
	Note    string  `json:"format_note"`
	URL     string  `json:"url"`
	Ext     string  `json:"ext"`
	VCodec  string  `json:"vcodec"`
	Size    float64 `json:"filesize"`
	Approx  float64 `json:"filesize_approx"`
}
type videoInfo struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Type      string            `json:"_type"`
	Extractor string            `json:"extractor_key"`
	Duration  float64           `json:"duration"`
	Thumbnail string            `json:"thumbnail"`
	Uploader  string            `json:"uploader"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	FPS       float64           `json:"fps"`
	Bitrate   float64           `json:"tbr"`
	DRM       bool              `json:"is_drm"`
	VCodec    string            `json:"vcodec"`
	Size      float64           `json:"filesize"`
	Approx    float64           `json:"filesize_approx"`
	Requested []videoFormatInfo `json:"requested_formats"`
	Formats   []videoFormatInfo `json:"formats"`
}

func (e *Engine) resolveVideo(raw string, headers http.Header) (*Resource, error) {
	u, _ := url.Parse(raw)
	if isPublicPlatformPage(u) {
		return e.resolvePublicPlatformVideo(raw, headers)
	}
	if isDouyinPage(u) {
		return e.resolveDouyinVideo(raw, headers)
	}
	return e.resolveVideoGeneric(raw, headers)
}

func (e *Engine) resolveVideoGeneric(raw string, headers http.Header) (*Resource, error) {
	ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
	defer cancel()
	return e.resolveVideoWithContext(ctx, raw, headers)
}

func (e *Engine) resolveVideoWithContext(ctx context.Context, raw string, headers http.Header) (*Resource, error) {
	dir, err := e.videoTools()
	if err != nil {
		return nil, err
	}
	args := append(extractorArgs(dir, headers), "--skip-download", "--dump-single-json", "--", raw)
	var out limitedBuffer
	out.limit = 16 << 20
	stderr := &tailBuffer{limit: 64 << 10}
	cmd := exec.Command(filepath.Join(dir, "yt-dlp.exe"), args...)
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err = runManagedCommand(ctx, cmd); err != nil {
		return nil, videoError(ctx, stderr.String(), err)
	}
	var info videoInfo
	if err = json.Unmarshal(out.Bytes(), &info); err != nil {
		return nil, errors.New("视频解析结果无效，未添加网页文件")
	}
	if info.DRM {
		return nil, errors.New("该视频受 DRM 保护，无法导出 MP4")
	}
	if info.Type == "playlist" || info.Type == "multi_video" {
		return nil, errors.New("请粘贴单个视频的播放链接")
	}
	hasVideo := info.VCodec != "" && info.VCodec != "none"
	for _, f := range info.Formats {
		hasVideo = hasVideo || f.VCodec != "none" && (f.VCodec != "" || f.Ext == "mp4" || f.Ext == "webm")
	}
	if !hasVideo {
		return nil, errors.New("页面没有可下载的视频流，未添加网页文件")
	}
	size := int64(max(info.Size, info.Approx))
	if len(info.Requested) > 0 {
		size = 0
		for _, f := range info.Requested {
			size += int64(max(f.Size, f.Approx))
		}
	}
	if size == 0 {
		size = -1
	}
	r := e.remember(raw, headers, "video/mp4", size, "", true)
	if r == nil {
		return nil, errors.New("资源列表已满，请清空后重试")
	}
	e.mu.Lock()
	r.Kind, r.Type, r.MIME = "page", "video", "video/mp4"
	r.Site, r.Duration, r.Size = info.Extractor, info.Duration, size
	r.Site = platformName(info.Extractor, raw)
	r.Thumbnail, r.Author, r.Width, r.Height, r.FPS, r.Bitrate = info.Thumbnail, info.Uploader, info.Width, info.Height, info.FPS, info.Bitrate
	r.Formats = formatsFromVideoInfo(info)
	if info.Title == "" {
		info.Title = info.ID
	}
	r.Name = safeFilename(info.Title + ".mp4")
	if r.NameOverride != "" {
		r.Name = r.NameOverride
	}
	copyResource := *r
	e.mu.Unlock()
	return &copyResource, nil
}

func videoError(ctx context.Context, details string, err error) error {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return errors.New("视频解析超时，请检查网络后重试")
	}
	lower := strings.ToLower(details)
	switch {
	case strings.Contains(lower, "unsupported url"):
		return errors.New("当前解析器不支持此播放页，未保存网页文件。请提供实际媒体直链或其他受支持的视频链接")
	case strings.Contains(lower, "sign in"), strings.Contains(lower, "login"), strings.Contains(lower, "cookies"), strings.Contains(lower, "http error 403"):
		return errors.New("平台要求登录或拒绝访问，当前未获取到可下载的视频；请使用可公开播放的链接或有效媒体直链")
	case strings.Contains(lower, "drm"):
		return errors.New("该视频受 DRM 保护，无法导出 MP4")
	}
	for _, line := range strings.Split(details, "\n") {
		if i := strings.Index(line, "ERROR:"); i >= 0 {
			text := strings.TrimSpace(line[i+6:])
			if len([]rune(text)) > 400 {
				text = string([]rune(text)[:400])
			}
			return fmt.Errorf("视频处理失败：%s", text)
		}
	}
	return fmt.Errorf("视频引擎执行失败：%w", err)
}

func (e *Engine) videoStage(j *Job, status string, percent float64) {
	e.mu.Lock()
	j.Status = status
	j.Percent = max(j.Percent, percent)
	e.mu.Unlock()
}
func pathInside(root, file string) bool {
	if file == "" || !filepath.IsAbs(file) {
		return false
	}
	rel, err := filepath.Rel(root, file)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func cleanupVideoStaging(downloadDir, staging string) {
	root := filepath.Join(downloadDir, ".resourcelens-temp")
	if !pathInside(root, staging) {
		return
	}
	for n := 0; n < 6; n++ {
		if os.RemoveAll(staging) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	os.Remove(root) // Only succeeds when all jobs have finished and the parent is empty.
}
func promoteVideo(from, dir, name string) (string, error) {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	for n := 0; n < 10000; n++ {
		candidate := name
		if n > 0 {
			candidate = fmt.Sprintf("%s (%d).mp4", stem, n)
		}
		final := filepath.Join(dir, candidate)
		if _, err := os.Stat(final); err == nil {
			continue
		}
		if err := commitDownload(from, final); err != nil {
			if _, exists := os.Stat(final); exists == nil {
				continue
			}
			return "", err
		}
		return final, nil
	}
	return "", errors.New("无法分配视频文件名")
}
func validateMP4(ctx context.Context, dir, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	err = rejectHTMLFile(f)
	f.Close()
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out := &limitedBuffer{limit: 2 << 20}
	stderr := &tailBuffer{limit: 8192}
	cmd := exec.Command(filepath.Join(dir, "ffprobe.exe"), "-v", "error", "-show_entries", "format=format_name,duration:stream=codec_type", "-of", "json", file)
	cmd.Stdout, cmd.Stderr = out, stderr
	if err = runManagedCommand(probeCtx, cmd); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("MP4 校验失败：文件不是可识别的视频，未标记为完成")
	}
	var probe struct {
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type string `json:"codec_type"`
		} `json:"streams"`
	}
	if json.Unmarshal(out.Bytes(), &probe) != nil {
		return errors.New("MP4 校验结果无效")
	}
	hasVideo := false
	for _, s := range probe.Streams {
		hasVideo = hasVideo || s.Type == "video"
	}
	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	if !strings.Contains(","+probe.Format.Name+",", ",mp4,") || !hasVideo || duration <= 0 {
		return errors.New("MP4 校验失败：容器、视频流或时长无效，未标记为完成")
	}
	return nil
}

func rejectHTMLFile(file *os.File) error {
	prefix := make([]byte, 8192)
	n, err := file.ReadAt(prefix, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	prefix = prefix[:n]
	if len(prefix) >= 2 && prefix[0] == 0x1f && prefix[1] == 0x8b {
		if gz, err := gzip.NewReader(io.NewSectionReader(file, 0, 1<<63-1)); err == nil {
			decoded, _ := io.ReadAll(io.LimitReader(gz, 8192))
			gz.Close()
			prefix = decoded
		}
	}
	text := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(prefix), "\ufeff")))
	if htmlMIME(http.DetectContentType(prefix)) || strings.Contains(text, "<!doctype html") || strings.Contains(text, "<html") {
		return errors.New("收到的是 HTML 网页或登录提示，已停止保存；请添加播放页进行视频解析")
	}
	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("视频解析输出超过限制")
	}
	return b.Buffer.Write(p)
}

type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}
func (b *tailBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

type lineWriter struct {
	pending []byte
	onLine  func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		w.onLine(strings.TrimSpace(string(w.pending[:i])))
		w.pending = w.pending[i+1:]
	}
	if len(w.pending) > 2<<20 {
		return 0, errors.New("视频进度输出超过限制")
	}
	return len(p), nil
}
func (w *lineWriter) Flush() {
	if len(w.pending) > 0 {
		w.onLine(strings.TrimSpace(string(w.pending)))
		w.pending = nil
	}
}
