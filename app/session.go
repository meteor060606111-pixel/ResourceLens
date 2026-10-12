package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// Captured Cookie/Authorization headers stay in memory. Video decode seeds are
// media metadata and are retained locally so interrupted WeChat jobs can resume.
type storedJob struct {
	Job
	Done int64 `json:"done"`
}
type storedSession struct {
	Resources         []Resource        `json:"resources"`
	Jobs              []storedJob       `json:"jobs"`
	WechatDecodeSeeds map[string]string `json:"wechatDecodeSeeds,omitempty"`
}

func (e *Engine) saveSession() {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	e.mu.RLock()
	saved := storedSession{}
	for _, r := range e.resources {
		saved.Resources = append(saved.Resources, *r)
		if r.wechatSeed != "" {
			if saved.WechatDecodeSeeds == nil {
				saved.WechatDecodeSeeds = map[string]string{}
			}
			saved.WechatDecodeSeeds[r.ID] = r.wechatSeed
		}
	}
	for _, j := range e.jobs {
		saved.Jobs = append(saved.Jobs, storedJob{Job: Job{ID: j.ID, ResourceID: j.ResourceID, Name: j.Name, URL: j.URL, Status: j.Status, Total: j.Total, File: j.File, Error: j.Error, CreatedAt: j.CreatedAt, Percent: j.Percent, Notice: j.Notice, ResumeAvailable: j.ResumeAvailable, Quality: j.Quality, RequestedQuality: j.RequestedQuality, OutputDir: j.OutputDir}, Done: j.done.Load()})
	}
	e.mu.RUnlock()
	b, err := json.Marshal(saved)
	if err == nil {
		err = atomicWrite(filepath.Join(e.root, "data", "session.json"), b, 0600)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "保存下载记录失败：%v\n", err)
	}
}
func (e *Engine) loadSession() {
	f, err := os.Open(filepath.Join(e.root, "data", "session.json"))
	if err != nil {
		return
	}
	defer f.Close()
	var saved storedSession
	if err = json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&saved); err != nil {
		fmt.Fprintf(os.Stderr, "读取下载记录失败：%v\n", err)
		return
	}
	for _, r := range saved.Resources {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || !validResourceID(r.ID) {
			continue
		}
		r.privateHeaders = http.Header{}
		for _, key := range []string{"User-Agent", "Referer", "Origin", "Accept", "Range"} {
			if value := r.Headers[key]; value != "" {
				r.privateHeaders.Set(key, value)
			}
		}
		r.Name = safeFilename(r.Name)
		if r.WechatVideo && r.WechatPlaybackURL == "" {
			r.WechatPlaybackURL = r.URL
			r.WechatSourceSize = r.Size
			r.Size = -1
			r.Quality = "画质待确认"
		}
		if !r.WechatVideo && r.Type == "video" && wechatEncryptedURL(r.URL) {
			r.Site, r.Kind, r.CaptureRequired = "微信视频号", "wechat-media", true
		}
		if seed := saved.WechatDecodeSeeds[r.ID]; seed != "" {
			if _, err := wechatDecodeMask(seed); err == nil {
				r.wechatSeed = seed
			}
		}
		e.resources[r.ID] = &r
		if len(e.resources) >= 5000 {
			break
		}
	}
	for _, savedJob := range saved.Jobs {
		j := &savedJob.Job
		if j.ID == "" || !validResourceID(j.ResourceID) {
			continue
		}
		j.done.Store(max(0, savedJob.Done))
		if activeStatus(j.Status) {
			j.Status = "paused"
			j.Error = ""
			j.Notice = "上次关闭时已保留进度，点击继续下载"
		}
		if j.Status != "completed" {
			cfg := e.config
			if j.OutputDir != "" {
				cfg.DownloadDir = j.OutputDir
			}
			j.ResumeAvailable = cacheHasData(downloadCacheDir(cfg, j.ResourceID, j.RequestedQuality))
			if j.Total > 0 {
				j.Percent = min(99, float64(j.done.Load())/float64(j.Total)*100)
			}
		}
		e.jobs[j.ID] = j
	}
	e.mergeWechatResourceDuplicates()
}

// The player changes range and signature parameters while reading one file.
// Keep one resource for that media and retain every job's file and status.
func (e *Engine) mergeWechatResourceDuplicates() {
	byMedia := map[string]*Resource{}
	aliases := map[string]string{}
	for _, resource := range e.resources {
		if resource.Type != "video" || !wechatEncryptedURL(resource.URL) {
			continue
		}
		identity := wechatMediaIdentity(resource.URL)
		previous := byMedia[identity]
		if previous == nil {
			byMedia[identity] = resource
			continue
		}
		preferResource := (resource.WechatVideo && !previous.WechatVideo) || (resource.WechatVideo == previous.WechatVideo && resource.CapturedAt.After(previous.CapturedAt))
		if preferResource {
			aliases[previous.ID] = resource.ID
			byMedia[identity] = resource
		} else {
			aliases[resource.ID] = previous.ID
		}
	}
	for id := range aliases {
		delete(e.resources, id)
	}
	for _, job := range e.jobs {
		for attempts := 0; attempts < len(aliases); attempts++ {
			next, ok := aliases[job.ResourceID]
			if !ok {
				break
			}
			job.ResourceID = next
		}
	}
}
func validResourceID(id string) bool {
	if len(id) != 24 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
