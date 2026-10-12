package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitUpgradeStatus(t *testing.T, e *Engine, id, status string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.RLock()
		j := e.jobs[id]
		got, detail := j.Status, j.Error
		e.mu.RUnlock()
		if got == status {
			return
		}
		if got == "failed" && status != "failed" {
			t.Fatalf("task failed: %s", detail)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task did not reach %s", status)
}
func rangedUpgradeServer(payload func() []byte, slow *atomic.Bool, starts *[]int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := payload()
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(data)))
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			return
		}
		var a, b int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &a, &b); err != nil {
			http.ServeContent(w, r, "fixture.zip", time.Time{}, bytes.NewReader(data))
			return
		}
		b = min(b, int64(len(data)-1))
		if a > b {
			w.WriteHeader(416)
			return
		}
		if starts != nil {
			*starts = append(*starts, a)
		}
		w.Header().Set("Content-Length", fmt.Sprint(b-a+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(data)))
		w.WriteHeader(206)
		for offset := a; offset <= b; offset += 8192 {
			end := min(offset+8192, b+1)
			if _, err := w.Write(data[offset:end]); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if slow != nil && slow.Load() && b-a > 4096 {
				time.Sleep(8 * time.Millisecond)
			}
		}
	}))
}
func TestUpgradePauseRestartAndResumeAreByteExact(t *testing.T) {
	e := testEngine(t, 1)
	e.mu.Lock()
	e.config.Segments = 1
	e.mu.Unlock()
	payload := bytes.Repeat([]byte("durable-range-resume-"), 160000)
	var slow atomic.Bool
	slow.Store(true)
	s := rangedUpgradeServer(func() []byte { return payload }, &slow, nil)
	defer s.Close()
	r, err := e.addURL(s.URL + "/archive.zip")
	if err != nil {
		t.Fatal(err)
	}
	id, err := e.startDownload(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.jobs[id].done.Load() < 32768 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err = e.pauseJob(id); err != nil {
		t.Fatal(err)
	}
	waitUpgradeStatus(t, e, id, "paused")
	e.mu.RLock()
	cached := e.jobs[id].done.Load()
	dir := e.jobs[id].OutputDir
	cfg := e.config
	e.mu.RUnlock()
	if cached <= 0 || cached >= int64(len(payload)) {
		t.Fatalf("invalid paused progress %d", cached)
	}
	if !cacheHasData(downloadCacheDir(cfg, r.ID, "best")) {
		t.Fatal("pause discarded bytes")
	}
	e.saveSession()
	// Rebuild only persisted download state, without changing the real system proxy.
	restored := &Engine{root: e.root, resources: map[string]*Resource{}, jobs: map[string]*Job{}, config: cfg}
	restored.loadSession()
	j := restored.jobs[id]
	if j == nil || j.Status != "paused" || j.OutputDir != dir || !j.ResumeAvailable || j.done.Load() != cached {
		t.Fatal("paused task was not restored")
	}
	slow.Store(false)
	if resumed, err := e.resumeJob(id); err != nil || resumed != id {
		t.Fatalf("resume must reuse same task: %s %v", resumed, err)
	}
	waitUpgradeStatus(t, e, id, "completed")
	actual, err := os.ReadFile(e.jobs[id].File)
	if err != nil || sha256.Sum256(actual) != sha256.Sum256(payload) {
		t.Fatal("resumed file does not match original")
	}
	if len(e.jobs) != 1 {
		t.Fatal("resume created another history entry")
	}
	if cacheHasData(downloadCacheDir(cfg, r.ID, "best")) {
		t.Fatal("completed chunks were not cleaned")
	}
}
func TestUpgradeChangedMediaDoesNotAppendStaleBytes(t *testing.T) {
	e := testEngine(t, 1)
	e.mu.Lock()
	e.config.Segments = 1
	e.mu.Unlock()
	first := bytes.Repeat([]byte("old-media-data-"), 120000)
	next := bytes.Repeat([]byte("new-media-data-"), 120000)
	var switched, slow atomic.Bool
	slow.Store(true)
	s := rangedUpgradeServer(func() []byte {
		if switched.Load() {
			return next
		}
		return first
	}, &slow, nil)
	defer s.Close()
	r, _ := e.addURL(s.URL + "/changing.zip")
	id, _ := e.startDownload(r.ID)
	deadline := time.Now().Add(5 * time.Second)
	for e.jobs[id].done.Load() < 16384 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := e.pauseJob(id); err != nil {
		t.Fatal(err)
	}
	waitUpgradeStatus(t, e, id, "paused")
	switched.Store(true)
	slow.Store(false)
	if _, err := e.resumeJob(id); err != nil {
		t.Fatal(err)
	}
	waitUpgradeStatus(t, e, id, "completed")
	actual, _ := os.ReadFile(e.jobs[id].File)
	if !bytes.Equal(actual, next) {
		t.Fatal("stale bytes appended to new rendition")
	}
}
func TestUpgradePausedTaskCancelAndCacheProtection(t *testing.T) {
	e := testEngine(t, 1)
	cfg := e.config
	id := strings.Repeat("a", 24)
	cache := downloadCacheDir(cfg, id, "best")
	os.MkdirAll(cache, 0700)
	os.WriteFile(filepath.Join(cache, "chunk-00.part"), []byte("keep-progress"), 0600)
	e.mu.Lock()
	e.jobs["paused"] = &Job{ID: "paused", ResourceID: id, Status: "paused", OutputDir: cfg.DownloadDir, ResumeAvailable: true}
	e.mu.Unlock()
	orphan := downloadCacheDir(cfg, strings.Repeat("b", 24), "best")
	os.MkdirAll(orphan, 0700)
	os.WriteFile(filepath.Join(orphan, "chunk-00.part"), []byte("orphan"), 0600)
	result, err := e.cleanCache(true)
	if err != nil || result["folders"] != 1 {
		t.Fatal(result, err)
	}
	if !cacheHasData(cache) {
		t.Fatal("cleanup deleted paused progress")
	}
	if err = e.cancelJob("paused"); err != nil {
		t.Fatal(err)
	}
	if cacheHasData(cache) || e.jobs["paused"].Status != "cancelled" {
		t.Fatal("cancel did not release paused cache")
	}
}
func TestUpgradeQualitySelectionAndFolderNaming(t *testing.T) {
	cfg := Config{DownloadDir: t.TempDir(), ClassifyBy: "author", NameTemplate: "{date}-{author}-{title}"}
	r := &Resource{URL: "https://www.bilibili.com/video/example", Site: "哔哩哔哩", Author: "作者/测试", Name: "片名.mp4", Formats: []MediaFormat{{ID: "fmt:80", Label: "1080p", Available: true, Selector: "80+bestaudio"}}, SelectedFormat: "fmt:80"}
	dir, name, err := downloadDestination(cfg, r)
	if err != nil || !strings.Contains(dir, "作者_测试") || !strings.Contains(name, "片名.mp4") {
		t.Fatal(dir, name, err)
	}
	if selectedVideoSelector(r) != "80+bestaudio" {
		t.Fatal("explicit video tier lost audio")
	}
	if validateFormatChoice(r, "fmt:invalid") == nil {
		t.Fatal("invalid tier silently used best")
	}
	r.Formats[0].Available = false
	r.Formats[0].Reason = "not public"
	if validateFormatChoice(r, "fmt:80") == nil {
		t.Fatal("unavailable tier accepted")
	}
	if downloadCacheDir(cfg, r.ID, "fmt:80") == downloadCacheDir(cfg, r.ID, "fmt:64") {
		t.Fatal("quality caches collide")
	}
}
func TestUpgradeDuplicatePromptAndSkipKeepHistory(t *testing.T) {
	e := testEngine(t, 1)
	data := []byte("duplicate-fixture")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		http.ServeContent(w, r, "clip.mp3", time.Time{}, bytes.NewReader(data))
	}))
	defer s.Close()
	r, _ := e.addURL(s.URL + "/clip.mp3")
	id, _ := e.startDownload(r.ID)
	waitUpgradeStatus(t, e, id, "completed")
	e.mu.Lock()
	e.config.DuplicatePolicy = "ask"
	e.mu.Unlock()
	if _, err := e.startDownloadOptions(r.ID, "best", false); err == nil || !strings.HasPrefix(err.Error(), "DUPLICATE:") {
		t.Fatal("completed file was not detected")
	}
	e.mu.Lock()
	e.config.DuplicatePolicy = "skip"
	e.mu.Unlock()
	next, err := e.startDownloadOptions(r.ID, "best", false)
	if err != nil || next != id || len(e.jobs) != 1 {
		t.Fatal("skip created duplicate task")
	}
}
func TestUpgradeBatchParserShowsPerLinkSuccessAndFailure(t *testing.T) {
	e := testEngine(t, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "5")
		if r.Method != "HEAD" {
			io.WriteString(w, "image")
		}
	}))
	defer s.Close()
	id, err := e.startParseBatch([]string{s.URL + "/image.png", "invalid", s.URL + "/image.png"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.RLock()
		b := e.batches[id]
		status := b.Status
		e.mu.RUnlock()
		if status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	state := e.state()
	bs := state["batches"].([]ParseBatch)
	if len(bs) != 1 || len(bs[0].Items) != 2 || bs[0].Items[0].Status != "ready" || bs[0].Items[1].Status != "failed" {
		t.Fatal("batch progress lost per-link result")
	}
	raw, _ := json.Marshal(state)
	if bytes.Contains(raw, []byte("pauseRequested")) {
		t.Fatal("internal fields exposed")
	}
}
func TestUpgradeCancelOldFailureDoesNotDeleteActiveCache(t *testing.T) {

	e := testEngine(t, 1)
	id := strings.Repeat("e", 24)
	cache := downloadCacheDir(e.config, id, "best")
	os.MkdirAll(cache, 0700)
	os.WriteFile(filepath.Join(cache, "chunk-00.part"), []byte("active-data"), 0600)
	e.mu.Lock()
	e.jobs["old"] = &Job{ID: "old", ResourceID: id, Status: "failed", OutputDir: e.config.DownloadDir, ResumeAvailable: true}
	e.jobs["new"] = &Job{ID: "new", ResourceID: id, Status: "downloading", RequestedQuality: "best", OutputDir: e.config.DownloadDir}
	e.mu.Unlock()
	if err := e.cancelJob("old"); err != nil {
		t.Fatal(err)
	}
	if !cacheHasData(cache) {
		t.Fatal("old failure removed new task's progress")
	}
}
func TestUpgradeRenamePreservesExtensionsAndAvoidsExistingFiles(t *testing.T) {
	e := testEngine(t, 1)
	id := strings.Repeat("f", 24)
	e.resources[id] = &Resource{ID: id, Name: "标题.mp4", Author: "作者", Site: "平台", URL: "https://cdn.example/video.mp4"}
	original := filepath.Join(e.config.DownloadDir, "标题.mp4")
	os.WriteFile(original, []byte("original"), 0600)
	existing := filepath.Join(e.config.DownloadDir, "平台-作者-001.mp4")
	os.WriteFile(existing, []byte("keep-existing"), 0600)
	e.jobs["complete"] = &Job{ID: "complete", ResourceID: id, Name: "标题.mp4", File: original, Status: "completed"}
	n, err := e.renameItems([]string{"complete"}, "{platform}-{author}-{index}", "jobs")
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	renamed := e.jobs["complete"].File
	if filepath.Base(renamed) != "平台-作者-001 (1).mp4" {
		t.Fatal(renamed)
	}
	data, _ := os.ReadFile(existing)
	if string(data) != "keep-existing" {
		t.Fatal("existing file overwritten")
	}
	data, _ = os.ReadFile(renamed)
	if string(data) != "original" {
		t.Fatal("original file bytes changed")
	}
	n, err = e.renameItems([]string{id}, "自定义-{title}", "resources")
	if err != nil || n != 1 || e.resources[id].NameOverride != "自定义-标题.mp4" {
		t.Fatal("resource name override missing", err)
	}
}
func TestUpgradeDirectTimeoutRemainsCancellable(t *testing.T) {
	e := testEngine(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j := &Job{RequestedQuality: "best"}
	r := &Resource{ID: strings.Repeat("d", 24), URL: "http://127.0.0.1:1/file", Name: "file.zip"}
	if err := e.download(ctx, j, r, e.config); err != context.Canceled {
		t.Fatal(err)
	}
}
