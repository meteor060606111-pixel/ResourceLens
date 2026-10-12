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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func downloadCacheDir(cfg Config, id, quality string) string {
	key := id
	if quality != "" && quality != "best" {
		hash := sha256.Sum256([]byte(quality))
		key += "-" + hex.EncodeToString(hash[:6])
	}
	return filepath.Join(cfg.DownloadDir, ".resourcelens-temp", key)
}
func cacheHasData(dir string) bool {
	if videoCacheHasData(dir) {
		return true
	}
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if !f.IsDir() && strings.HasPrefix(f.Name(), "chunk-") {
			if info, err := f.Info(); err == nil && info.Size() > 0 {
				return true
			}
		}
	}
	return false
}
func (e *Engine) cacheOwnedByOtherLocked(j *Job) bool {
	cfg := e.config
	if j.OutputDir != "" {
		cfg.DownloadDir = j.OutputDir
	}
	cache := downloadCacheDir(cfg, j.ResourceID, j.RequestedQuality)
	for _, other := range e.jobs {
		if other.ID == j.ID || (!activeStatus(other.Status) && other.Status != "paused") {
			continue
		}
		otherCfg := e.config
		if other.OutputDir != "" {
			otherCfg.DownloadDir = other.OutputDir
		}
		if strings.EqualFold(cache, downloadCacheDir(otherCfg, other.ResourceID, other.RequestedQuality)) {
			return true
		}
	}
	return false
}
func (e *Engine) pauseJob(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.jobs[id]
	if j == nil || !activeStatus(j.Status) || j.cancel == nil {
		return errors.New("该任务当前不能暂停")
	}
	j.pauseRequested = true
	j.Notice = "正在保存进度并暂停"
	j.cancel()
	return nil
}
func (e *Engine) cancelJob(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.jobs[id]
	if j == nil {
		return errors.New("任务不存在")
	}
	j.pauseRequested = false
	if j.cancel != nil {
		j.cancel()
	} else if j.Status != "completed" {
		cfg := e.config
		if j.OutputDir != "" {
			cfg.DownloadDir = j.OutputDir
		}
		if !e.cacheOwnedByOtherLocked(j) {
			cleanupVideoStaging(cfg.DownloadDir, downloadCacheDir(cfg, j.ResourceID, j.RequestedQuality))
		}
		j.Status = "cancelled"
		j.Notice = ""
		j.Error = ""
		j.ResumeAvailable = false
	}
	return nil
}
func (e *Engine) resumeJob(id string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.jobs[id]
	if j == nil {
		return "", errors.New("任务不存在")
	}
	if activeStatus(j.Status) || j.Status == "completed" {
		return "", errors.New("该任务无需续传")
	}
	for _, other := range e.jobs {
		if other.ID != id && other.ResourceID == j.ResourceID && activeStatus(other.Status) {
			return "", errors.New("同一资源已有任务正在下载，请等待或暂停后再继续此任务")
		}
	}
	r := e.resources[j.ResourceID]
	if r == nil || r.CaptureRequired {
		return "", errors.New("请重新添加或播放原视频，补全下载信息")
	}
	quality := j.RequestedQuality
	if quality == "" {
		quality = "best"
	}
	if err := validateFormatChoice(r, quality); err != nil {
		return "", err
	}
	cfg := e.config
	if j.OutputDir != "" {
		cfg.DownloadDir = j.OutputDir
	} else {
		dir, _, err := downloadDestination(cfg, r)
		if err != nil {
			return "", err
		}
		cfg.DownloadDir = dir
		j.OutputDir = dir
	}
	ctx, cancel := context.WithCancel(e.ctx)
	j.cancel = cancel
	j.pauseRequested = false
	j.Status = "queued"
	j.Error = ""
	j.Notice = "准备继续下载"
	j.Percent = 0
	j.RequestedQuality = quality
	copy := *r
	copy.Name = j.Name
	copy.SelectedFormat = quality
	copy.privateHeaders = r.privateHeaders.Clone()
	for _, format := range r.Formats {
		if format.ID == quality {
			copy.Size = format.Size
			j.Total = format.Size
			j.Quality = format.Label
			break
		}
	}
	e.launchDownloadLocked(ctx, cancel, j, copy, cfg, quality)
	return j.ID, nil
}

type directManifest struct {
	Total    int64  `json:"total"`
	ETag     string `json:"etag"`
	Prefix   string `json:"prefix"`
	Tail     string `json:"tail,omitempty"`
	Ranged   bool   `json:"ranged"`
	Segments int    `json:"segments"`
}

func (e *Engine) probeDirect(ctx context.Context, client *http.Client, r *Resource) (directManifest, error) {
	probeCtx, cancelProbe := context.WithTimeout(ctx, 40*time.Second)
	defer cancelProbe()
	ctx = probeCtx
	m := directManifest{Total: -1}
	headSize := int64(-1)
	headCtx, headCancel := context.WithTimeout(ctx, 20*time.Second)
	head, headErr := downloadRequest(headCtx, "HEAD", r)
	if headErr == nil {
		if response, err := client.Do(head); err == nil {
			if response.StatusCode == 200 {
				headSize = response.ContentLength
			}
			response.Body.Close()
		}
	}
	headCancel()
	request, err := downloadRequest(ctx, "GET", r)
	if err != nil {
		return m, err
	}
	request.Header.Set("Range", "bytes=0-4095")
	resp, err := client.Do(request)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return m, fmt.Errorf("媒体地址返回 HTTP %d；地址可能已过期，请重新播放或解析，已有进度会保留", resp.StatusCode)
	}
	if htmlMIME(resp.Header.Get("Content-Type")) {
		return m, errors.New("服务器返回网页或登录提示，未保存为媒体")
	}
	m.Total = resp.ContentLength
	m.Ranged = resp.StatusCode == 206
	if m.Ranged {
		a, b, t, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || a != 0 || b != min(int64(4095), t-1) || headSize > 0 && headSize != t {
			return m, errors.New("分段 Content-Range 与请求不符")
		}
		m.Total = t
	}
	prefix, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return m, err
	}
	sum := sha256.Sum256(prefix)
	m.Prefix = hex.EncodeToString(sum[:])
	m.ETag = resp.Header.Get("ETag")
	if strings.HasPrefix(m.ETag, "W/") {
		m.ETag = ""
	}
	if m.ETag == "" {
		m.ETag = resp.Header.Get("Last-Modified")
	}
	if m.Ranged && m.ETag == "" && m.Total > 4096 {
		tailReq, err := downloadRequest(ctx, "GET", r)
		if err != nil {
			return m, err
		}
		start := max(int64(0), m.Total-16384)
		tailReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, m.Total-1))
		tailResp, err := client.Do(tailReq)
		if err != nil {
			return m, err
		}
		defer tailResp.Body.Close()
		a, b, t, ok := parseContentRange(tailResp.Header.Get("Content-Range"))
		if tailResp.StatusCode != 206 || !ok || a != start || b != m.Total-1 || t != m.Total {
			return m, errors.New("末尾 Content-Range 与请求不符")
		}
		tail, err := io.ReadAll(io.LimitReader(tailResp.Body, 16385))
		if err != nil || int64(len(tail)) != m.Total-start {
			return m, errors.New("末尾文件内容校验失败")
		}
		sum := sha256.Sum256(tail)
		m.Tail = hex.EncodeToString(sum[:])
	}
	return m, nil
}
func (e *Engine) download(ctx context.Context, j *Job, r *Resource, cfg Config) error {
	cache := downloadCacheDir(cfg, r.ID, j.RequestedQuality)
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	success := false
	invalid := false
	defer func() {
		if success || invalid {
			cleanupVideoStaging(cfg.DownloadDir, cache)
		}
	}()
	client := &http.Client{Transport: e.transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("重定向过多")
		}
		if len(via) > 0 && req.URL.Host != via[len(via)-1].URL.Host {
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
		}
		return nil
	}}
	var last error
	for attempt := 1; attempt <= videoConnectionAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 1 {
			e.videoNotice(j, "retrying", fmt.Sprintf("重新连接 %d/%d，保留已下载的数据", attempt, videoConnectionAttempts))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
			e.mu.RLock()
			fresh := e.resources[r.ID]
			if fresh != nil && !r.WechatVideo {
				r.URL = fresh.URL
				r.privateHeaders = fresh.privateHeaders.Clone()
			}
			e.mu.RUnlock()
		}
		attemptCtx, cancel := context.WithCancel(ctx)
		stop, reason, exited := make(chan struct{}), make(chan string, 1), make(chan struct{})
		go e.watchVideoConnection(attemptCtx, cancel, j, attempt, stop, reason, exited)
		last = e.transferDirect(attemptCtx, client, j, r, cfg, cache)
		close(stop)
		<-exited
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if last == nil {
			success = true
			return nil
		}
		select {
		case why := <-reason:
			last = errors.New(why)
		default:
		}
		if strings.Contains(last.Error(), "Content-Range") || strings.Contains(last.Error(), "网页") || strings.Contains(last.Error(), "校验") || strings.Contains(last.Error(), "超出") {
			invalid = true
			break
		}
	}
	if last == nil {
		last = errors.New("连接失败")
	}
	return fmt.Errorf("%w；可点击续传重试", last)
}
func (e *Engine) transferDirect(ctx context.Context, client *http.Client, j *Job, r *Resource, cfg Config, cache string) error {
	e.videoNotice(j, "resolving", "确认文件及可续传范围")
	m, err := e.probeDirect(ctx, client, r)
	if err != nil {
		return err
	}
	m.Segments = 1
	if m.Ranged && m.Total >= 256*1024 {
		m.Segments = min(cfg.Segments, int(m.Total/65536))
	}
	manifestFile := filepath.Join(cache, "direct.json")
	var old directManifest
	data, _ := os.ReadFile(manifestFile)
	same := json.Unmarshal(data, &old) == nil && m.Total == old.Total && m.Prefix == old.Prefix && m.Tail == old.Tail && m.ETag == old.ETag && m.Ranged == old.Ranged
	if same {
		m.Segments = old.Segments
		if m.Segments < 1 || m.Segments > 16 {
			return errors.New("续传分段信息无效")
		}
	} else {
		files, _ := os.ReadDir(cache)
		for _, f := range files {
			if !f.IsDir() && strings.HasPrefix(f.Name(), "chunk-") {
				if err = os.Remove(filepath.Join(cache, f.Name())); err != nil {
					return err
				}
			}
		}
		j.done.Store(0)
	}
	data, _ = json.Marshal(m)
	if err = atomicWrite(manifestFile, data, 0600); err != nil {
		return err
	}
	var cached int64
	for n := 0; n < m.Segments; n++ {
		if s, err := os.Stat(filepath.Join(cache, fmt.Sprintf("chunk-%02d.part", n))); err == nil {
			cached += s.Size()
		}
	}
	if !m.Ranged {
		cached = 0
	}
	j.done.Store(cached)
	e.mu.Lock()
	j.Status = "downloading"
	j.Total = m.Total
	r.Size = m.Total
	if original := e.resources[r.ID]; original != nil {
		original.Size = m.Total
	}
	j.Percent = 0
	j.Notice = ""
	if !m.Ranged && same {
		j.Notice = "来源不支持断点续传，此文件需要重新传输；暂停任务和其他文件的进度已保留"
	}
	j.speedSampleAt = time.Now()
	j.speedSampleBytes = cached
	j.ResumeAvailable = cached > 0
	e.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, m.Segments)
	chunk := int64(0)
	if m.Total > 0 {
		chunk = (m.Total + int64(m.Segments) - 1) / int64(m.Segments)
	}
	for n := 0; n < m.Segments; n++ {
		a, b := int64(n)*chunk, min(m.Total-1, int64(n+1)*chunk-1)
		wg.Add(1)
		go func(n int, a, b int64) {
			defer wg.Done()
			if err := e.transferChunk(child, client, j, r, m, filepath.Join(cache, fmt.Sprintf("chunk-%02d.part", n)), a, b); err != nil {
				errs <- err
				cancel()
			}
		}(n, a, b)
	}
	wg.Wait()
	close(errs)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var first error
	for v := range errs {
		if first == nil || errors.Is(first, context.Canceled) {
			first = v
		}
	}
	if first != nil {
		return first
	}
	assembly := filepath.Join(cache, "assembled.part")
	out, err := os.OpenFile(assembly, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	var bytes int64
	for n := 0; n < m.Segments; n++ {
		if ctx.Err() != nil {
			out.Close()
			return ctx.Err()
		}
		f, err := os.Open(filepath.Join(cache, fmt.Sprintf("chunk-%02d.part", n)))
		if err != nil {
			out.Close()
			return err
		}
		written, copyErr := io.Copy(out, f)
		f.Close()
		bytes += written
		if copyErr != nil {
			out.Close()
			return copyErr
		}
	}
	syncErr := out.Sync()
	closeErr := out.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if m.Total >= 0 && bytes != m.Total {
		return errors.New("下载文件长度校验失败")
	}
	f, err := os.Open(assembly)
	if err != nil {
		return err
	}
	err = rejectHTMLFile(f)
	f.Close()
	if err != nil {
		return err
	}
	if r.WechatVideo && r.wechatSeed != "" && !wechatFileHasMP4Header(assembly) {
		e.videoStage(j, "validating", 97)
		if err = decryptWechatPrefix(ctx, assembly, r.wechatSeed); err != nil {
			return err
		}
	}
	if r.Type == "video" && (strings.EqualFold(filepath.Ext(r.Name), ".mp4") || strings.HasPrefix(r.MIME, "video/mp4")) {
		dir, err := e.videoTools()
		if err != nil {
			return err
		}
		e.videoStage(j, "validating", 98)
		if err = validateMP4(ctx, dir, assembly); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	final, err := promoteFile(assembly, cfg.DownloadDir, r.Name)
	if err != nil {
		return err
	}
	e.mu.Lock()
	j.File = final
	j.Name = filepath.Base(final)
	j.Total = bytes
	j.done.Store(bytes)
	e.mu.Unlock()
	return nil
}
func (e *Engine) transferChunk(ctx context.Context, client *http.Client, j *Job, r *Resource, m directManifest, file string, a, b int64) error {
	flags := os.O_CREATE | os.O_WRONLY
	if m.Ranged {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(file, flags, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	info, _ := f.Stat()
	offset := info.Size()
	expected := b - a + 1
	if m.Ranged && offset == expected {
		return nil
	}
	if m.Ranged && offset > expected {
		return errors.New("续传文件超出请求范围")
	}
	request, err := downloadRequest(ctx, "GET", r)
	if err != nil {
		return err
	}
	if m.Ranged {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", a+offset, b))
		if m.ETag != "" {
			request.Header.Set("If-Range", m.ETag)
		}
	}
	resp, err := client.Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if m.Ranged {
		start, end, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if resp.StatusCode != 206 || !ok || start != a+offset || end != b || total != m.Total {
			return errors.New("分段 Content-Range 与请求不符，媒体可能已改变")
		}
	} else if resp.StatusCode != 200 {
		return fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}
	if htmlMIME(resp.Header.Get("Content-Type")) {
		return errors.New("服务器返回网页或登录提示")
	}
	buf := make([]byte, 128*1024)
	var received int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if m.Ranged && offset+received+int64(n) > expected {
				return errors.New("分段长度超出请求范围")
			}
			written, writeErr := f.Write(buf[:n])
			received += int64(written)
			j.done.Add(int64(written))
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if m.Ranged && offset+received != expected || !m.Ranged && resp.ContentLength >= 0 && received != resp.ContentLength {
		return io.ErrUnexpectedEOF
	}
	return f.Sync()
}
