package main

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const phonePageSize = 30

type phonePage struct {
	Page  int `json:"page"`
	Pages int `json:"pages"`
	Total int `json:"total"`
}

func phonePageBounds(raw string, total int) (phonePage, int, int) {
	page, _ := strconv.Atoi(raw)
	pages := max(1, (total+phonePageSize-1)/phonePageSize)
	page = max(1, min(page, pages))
	start := (page - 1) * phonePageSize
	return phonePage{page, pages, total}, start, min(start+phonePageSize, total)
}

func phoneMatches(query string, values ...string) bool {
	text := strings.ToLower(strings.Join(values, " "))
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

// Project only phone-safe fields, only for the selected pages. Do not call
// Engine.state here: it builds every desktop row, including secrets and paths.
func (m *MobileGateway) state(query url.Values) map[string]any {
	e := m.engine
	e.mu.Lock()
	defer e.mu.Unlock()
	rs := make([]*Resource, 0, len(e.resources))
	for _, r := range e.resources {
		kind := query.Get("resourceType")
		matchesType := kind == "" || kind == "all" || kind == r.Type || kind == "video" && r.Type == "playlist" || kind == "file" && r.Type == "other"
		if matchesType && phoneMatches(query.Get("resourceQuery"), r.Name, r.Site, r.Author) {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].CapturedAt.Equal(rs[j].CapturedAt) {
			return rs[i].ID < rs[j].ID
		}
		return rs[i].CapturedAt.After(rs[j].CapturedAt)
	})
	resourcePage, start, end := phonePageBounds(query.Get("resourcePage"), len(rs))
	js := make([]*Job, 0, len(e.jobs))
	counts := map[string]int{"all": len(e.jobs), "active": 0, "completed": 0, "failed": 0}
	queued := make(map[string]string, end-start)
	for _, r := range rs[start:end] {
		queued[r.ID] = ""
	}
	active := false
	for _, j := range e.jobs {
		inProgress := activeStatus(j.Status)
		active = active || inProgress && (j.Status != "queued" || !e.config.QueuePaused && !j.ScheduledAt.After(time.Now()))
		if inProgress || j.Status == "paused" {
			counts["active"]++
			if current, visible := queued[j.ResourceID]; visible && current != "queued" {
				queued[j.ResourceID] = "paused"
				if inProgress {
					queued[j.ResourceID] = "queued"
				}
			}
		} else if j.Status == "completed" {
			counts["completed"]++
		} else if j.Status == "failed" || j.Status == "cancelled" {
			counts["failed"]++
		}
		filter := query.Get("jobFilter")
		matchesFilter := filter == "" || filter == "all" || filter == "active" && (inProgress || j.Status == "paused") || filter == "completed" && j.Status == "completed" || filter == "failed" && (j.Status == "failed" || j.Status == "cancelled")
		if matchesFilter && phoneMatches(query.Get("jobQuery"), j.Name) {
			js = append(js, j)
		}
	}
	sort.Slice(js, func(i, j int) bool {
		if js[i].CreatedAt.Equal(js[j].CreatedAt) {
			return js[i].ID < js[j].ID
		}
		return js[i].CreatedAt.After(js[j].CreatedAt)
	})
	jobPage, jobStart, jobEnd := phonePageBounds(query.Get("jobPage"), len(js))
	resources := make([]map[string]any, 0, end-start)
	for _, r := range rs[start:end] {
		resources = append(resources, map[string]any{"id": r.ID, "name": r.Name, "type": r.Type, "size": r.Size, "site": r.Site, "author": r.Author, "width": r.Width, "height": r.Height, "duration": r.Duration, "captureRequired": r.CaptureRequired, "hasPoster": r.Thumbnail != "" || r.Type == "image", "quality": r.Quality, "downloadStatus": queued[r.ID]})
	}
	jobs := make([]map[string]any, 0, jobEnd-jobStart)
	for _, j := range js[jobStart:jobEnd] {
		done, speed, eta := jobProgressLocked(j, time.Now())
		item := map[string]any{"id": j.ID, "resourceId": j.ResourceID, "name": j.Name, "status": j.Status, "total": j.Total, "done": done, "speed": speed, "eta": eta, "percent": j.Percent, "quality": j.Quality, "requestedQuality": j.RequestedQuality, "resumeAvailable": j.ResumeAvailable, "createdAt": j.CreatedAt, "priority": j.Priority, "scheduledAt": j.ScheduledAt}
		if r := e.resources[j.ResourceID]; r != nil {
			item["resource"] = map[string]any{"id": r.ID, "type": r.Type, "duration": r.Duration, "hasPoster": r.Thumbnail != "" || r.Type == "image"}
			item["type"] = r.Type
		}
		if j.Error != "" {
			item["error"] = "下载未完成，请重试或在电脑查看详情"
		}
		jobs = append(jobs, item)
	}
	var latest *ParseBatch
	parsing := false
	for _, batch := range e.batches {
		parsing = parsing || batch.Status == "parsing"
		if latest == nil || batch.CreatedAt.After(latest.CreatedAt) {
			latest = batch
		}
	}
	batches := []map[string]any{}
	if latest != nil {
		failed := false
		for _, item := range latest.Items {
			failed = failed || item.Status == "failed"
		}
		batches = append(batches, map[string]any{"id": latest.ID, "status": latest.Status, "createdAt": latest.CreatedAt, "hasFailed": failed})
	}
	return map[string]any{"version": appVersion, "resources": resources, "jobs": jobs, "batches": batches, "capture": e.capture, "queuePaused": e.config.QueuePaused, "activity": active || parsing, "jobCounts": counts, "paging": map[string]phonePage{"resources": resourcePage, "jobs": jobPage}}
}
