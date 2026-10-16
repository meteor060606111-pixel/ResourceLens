package main

import (
	"context"
	"errors"
	"fmt"

	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"time"
)

func parseContentRange(v string) (int64, int64, int64, bool) {
	m := regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+)$`).FindStringSubmatch(v)
	if len(m) != 4 {
		return 0, 0, 0, false
	}
	a, _ := strconv.ParseInt(m[1], 10, 64)
	b, _ := strconv.ParseInt(m[2], 10, 64)
	total, _ := strconv.ParseInt(m[3], 10, 64)
	return a, b, total, a <= b && b < total
}
func (e *Engine) startDownload(id string, requestedQuality ...string) (string, error) {
	quality := "best"
	if len(requestedQuality) > 0 && requestedQuality[0] != "" {
		quality = requestedQuality[0]
	}
	return e.startDownloadOptions(id, quality, true)
}
func (e *Engine) startDownloadOptions(id, quality string, force bool) (string, error) {
	return e.startDownloadPlanned(id, quality, force, DownloadPlan{})
}

func (e *Engine) startDownloadPlanned(id, quality string, force bool, plan DownloadPlan) (string, error) {
	if err := validateDownloadPlan(plan); err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.resources[id]
	if r == nil {
		return "", errors.New("资源不存在，请重新捕获或添加链接")
	}
	if r.CaptureRequired || r.Kind == "wechat-page" {
		return "", errors.New("先点击开始捕获，再在电脑版微信打开并播放目标视频；出现实际视频标题后即可下载 MP4")
	}
	for _, j := range e.jobs {
		if j.ResourceID == id && activeStatus(j.Status) {
			return "", errors.New("这个资源已经在下载队列中")
		}
	}
	cfg := e.config
	if quality == "" {
		quality = "best"
	}
	for _, old := range e.jobs {
		if old.ResourceID == id && old.Status == "paused" && (old.RequestedQuality == quality || old.RequestedQuality == "" && quality == "best") {
			return "", errors.New("此资源已有暂停任务，请在下载任务中点击继续，保留原任务进度")
		}
	}
	if err := validateFormatChoice(r, quality); err != nil {
		return "", err
	}
	if !force && cfg.DuplicatePolicy != "allow" {
		for _, old := range e.jobs {
			if old.ResourceID == id && old.Status == "completed" {
				if _, err := os.Stat(old.File); err == nil {
					if cfg.DuplicatePolicy == "skip" {
						return old.ID, nil
					}
					return "", errors.New("DUPLICATE:此视频已经下载过，可以定位已有文件或仍然下载")
				}
			}
		}
	}
	jobID := fmt.Sprintf("%x-%x", time.Now().UnixNano(), e.jobSequence.Add(1))
	ctx, cancel := context.WithCancel(e.ctx)
	outputDir, name, err := downloadDestination(cfg, r)
	if err != nil {
		cancel()
		return "", err
	}
	j := &Job{ID: jobID, ResourceID: id, Name: name, URL: r.URL, Status: "queued", Total: r.Size, CreatedAt: time.Now(), cancel: cancel, RequestedQuality: quality, OutputDir: outputDir, Priority: plan.Priority, ScheduledAt: plan.ScheduledAt}
	j.Quality = r.Quality
	if r.WechatVideo {
		j.Quality = wechatQualityLabel(quality)
		if r.WechatResolvedQuality != quality {
			j.Total = -1
		}
	}
	e.jobs[jobID] = j
	copyResource := *r
	copyResource.Name, copyResource.SelectedFormat = name, quality
	copyResource.privateHeaders = r.privateHeaders.Clone()
	for _, format := range r.Formats {
		if format.ID == quality {
			j.Quality = format.Label
			j.Total = format.Size
			copyResource.Size = format.Size
			break
		}
	}
	cfg.DownloadDir = outputDir
	e.launchDownloadLocked(ctx, cancel, j, copyResource, cfg, quality)
	return jobID, nil
}
func (e *Engine) launchDownloadLocked(ctx context.Context, cancel context.CancelFunc, j *Job, copyResource Resource, cfg Config, quality string) {
	e.workers.Add(1)
	go func() {
		defer e.workers.Done()
		defer cancel()
		if err := e.awaitDownloadSlot(ctx, j); err != nil {
			e.finishJob(j, err)
			return
		}
		defer func() {
			e.mu.Lock()
			e.runningDownloads--
			e.wakeQueueLocked()
			e.mu.Unlock()
		}()
		cfg.SpeedLimitKB = int(j.limiter.bytesPerSecond / 1024)
		var err error
		if copyResource.Kind == "page" || copyResource.Kind == "playlist" || copyResource.Type == "playlist" {
			err = e.downloadVideo(ctx, j, &copyResource, cfg)
		} else {
			if copyResource.WechatVideo {
				e.videoStage(j, "resolving", 0)
				err = e.prepareWechatDownload(ctx, j, &copyResource, quality)
			}
			if err == nil {
				err = e.download(ctx, j, &copyResource, cfg)
			}
		}
		if err == nil && copyResource.Music != nil {
			if lyricErr := writeMusicLyrics(j.File, copyResource.Music); lyricErr != nil {
				e.mu.Lock()
				j.completionNotice += " 音频已保存，歌词文件写入失败，请检查目录权限"
				e.mu.Unlock()
			}
		}
		e.finishJob(j, err)
	}()
}
func (e *Engine) finishJob(j *Job, err error) {
	e.mu.Lock()
	if err == nil {
		j.Status = "completed"
		j.Percent = 100
		j.Notice = strings.TrimSpace(j.completionNotice)
		j.ResumeAvailable = false
	} else if errors.Is(err, context.Canceled) || e.ctx.Err() != nil {
		j.Status = "cancelled"
		if j.pauseRequested || e.ctx.Err() != nil {
			j.Status = "paused"
			j.Notice = "下载已暂停，进度保存在本机"
		}
		j.Error = ""
	} else {
		j.Status = "failed"
		j.Error = err.Error()
	}
	j.cancel = nil
	cfg := e.config
	if j.OutputDir != "" {
		cfg.DownloadDir = j.OutputDir
	}
	cache := downloadCacheDir(cfg, j.ResourceID, j.RequestedQuality)
	if j.Status != "completed" {
		j.ResumeAvailable = cacheHasData(cache)
	}
	if j.Status == "cancelled" {
		if !e.cacheOwnedByOtherLocked(j) {
			cleanupVideoStaging(cfg.DownloadDir, cache)
		}
		j.ResumeAvailable = false
	}
	for _, other := range e.jobs {
		if other.ID != j.ID && !activeStatus(other.Status) && other.Status != "completed" {
			otherCfg := e.config
			if other.OutputDir != "" {
				otherCfg.DownloadDir = other.OutputDir
			}
			other.ResumeAvailable = cacheHasData(downloadCacheDir(otherCfg, other.ResourceID, other.RequestedQuality))
		}
	}
	e.mu.Unlock()
	e.saveSession()
}
func downloadRequest(ctx context.Context, method string, r *Resource) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header = r.privateHeaders.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	stripHop(req.Header)
	req.Header.Del("Range")
	req.Header.Del("If-Range")
	req.Header.Set("Accept-Encoding", "identity")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "ResourceLens/1.0")
	}
	return req, nil
}
