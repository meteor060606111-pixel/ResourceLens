package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestQueuePriorityFIFOAndPause(t *testing.T) {
	e := testEngine(t, 1)
	if err := e.setQueuePaused(true); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []string
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if !seen[r.URL.Path] {
			order = append(order, r.URL.Path)
			seen[r.URL.Path] = true
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "data.bin", time.Time{}, bytes.NewReader([]byte("queue-order-data")))
	}))
	defer server.Close()
	var ids []string
	for _, item := range []struct {
		name     string
		priority int
	}{{"normal-a", 0}, {"low", -1}, {"high", 1}, {"normal-b", 0}} {
		resource := e.remember(server.URL+"/"+item.name, nil, "application/octet-stream", 16, "", true)
		id, err := e.startDownloadPlanned(resource.ID, "best", true, DownloadPlan{Priority: item.priority})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	time.Sleep(70 * time.Millisecond)
	mu.Lock()
	count := len(order)
	mu.Unlock()
	if count != 0 {
		t.Fatal("paused queue contacted source")
	}
	if err := e.setQueuePaused(false); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if job := waitJob(t, e, id); job.Status != "completed" {
			t.Fatal(job.Status, job.Error)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/high", "/normal-a", "/normal-b", "/low"}
	if len(order) != len(want) {
		t.Fatalf("order = %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestScheduledJobDoesNotReserveDownloadSlot(t *testing.T) {
	e := testEngine(t, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, "planned download")
	}))
	defer server.Close()
	resource := e.remember(server.URL+"/future.bin", nil, "application/octet-stream", 16, "", true)
	when := time.Now().Add(400 * time.Millisecond)
	id, err := e.startDownloadPlanned(resource.ID, "best", true, DownloadPlan{Priority: 1, ScheduledAt: when})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(90 * time.Millisecond)
	e.mu.RLock()
	status, running := e.jobs[id].Status, e.runningDownloads
	e.mu.RUnlock()
	if status != "queued" || running != 0 {
		t.Fatalf("scheduled job started early: %s/%d", status, running)
	}
	if job := waitJob(t, e, id); job.Status != "completed" {
		t.Fatal(job.Status, job.Error)
	}
	if time.Now().Before(when) {
		t.Fatal("completed before schedule")
	}
}

func TestQueuePlanPersistsAndCancelledReservationReleases(t *testing.T) {
	e := testEngine(t, 1)
	if err := e.setQueuePaused(true); err != nil {
		t.Fatal(err)
	}
	r := e.remember("https://example.com/scheduled.mp4", nil, "video/mp4", 1, "", true)
	when := time.Now().Add(time.Hour).Truncate(time.Second)
	id, err := e.startDownloadPlanned(r.ID, "best", true, DownloadPlan{Priority: 1, ScheduledAt: when})
	if err != nil {
		t.Fatal(err)
	}
	e.saveSession()
	loaded := &Engine{root: e.root, config: e.configSnapshot(), jobs: map[string]*Job{}, resources: map[string]*Resource{}}
	loaded.loadSession()
	j := loaded.jobs[id]
	if j == nil || j.Priority != 1 || !j.ScheduledAt.Equal(when) || j.Status != "paused" {
		t.Fatalf("lost plan after restart: %#v", j)
	}
	if err := e.cancelJob(id); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, e, id); j.Status != "cancelled" {
		t.Fatal(j.Status)
	}
}

func TestSegmentedDownloadSharesTaskRateLimit(t *testing.T) {
	e := testEngine(t, 1)
	e.mu.Lock()
	e.config.SpeedLimitKB = 512
	e.mu.Unlock()
	payload := bytes.Repeat([]byte("bandwidth"), 65536)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "limit.bin", time.Time{}, bytes.NewReader(payload))
	}))
	defer server.Close()
	r := e.remember(server.URL+"/limit.bin", nil, "application/octet-stream", int64(len(payload)), "", true)
	started := time.Now()
	id, err := e.startDownload(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, e, id)
	if j.Status != "completed" {
		t.Fatal(j.Error)
	}
	if elapsed := time.Since(started); elapsed < time.Second {
		t.Fatalf("segments bypassed rate limit: %s", elapsed)
	}
	data, err := os.ReadFile(j.File)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("rate limiter changed downloaded data", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	limiter := &downloadLimiter{bytesPerSecond: 1}
	started = time.Now()
	if !errors.Is(limiter.wait(ctx, 100000), context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("rate wait prevented pause")
	}
}

func TestUpdateCachePrunesOnlyOwnedOlderPackages(t *testing.T) {
	e := &Engine{root: t.TempDir()}
	os.MkdirAll(e.updateDir(), 0700)
	owned := "ResourceLens-1.0.0-0123456789ab.dmg"
	keep := "ResourceLens-1.0.1-0123456789ab.dmg"
	for _, name := range []string{owned, keep, "holiday.mp4", "ResourceLens-custom.dmg"} {
		os.WriteFile(filepath.Join(e.updateDir(), name), []byte("test"), 0600)
	}
	e.pruneUpdatePackages(filepath.Join(e.updateDir(), keep))
	if _, err := os.Stat(filepath.Join(e.updateDir(), owned)); !os.IsNotExist(err) {
		t.Fatal("owned old installer retained")
	}
	for _, name := range []string{keep, "holiday.mp4", "ResourceLens-custom.dmg"} {
		if _, err := os.Stat(filepath.Join(e.updateDir(), name)); err != nil {
			t.Fatal("unrelated or selected file removed", name)
		}
	}
}
