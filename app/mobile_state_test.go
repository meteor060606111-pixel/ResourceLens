package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPhonePagingSearchAndQueueAcrossPages(t *testing.T) {
	e, m, base := phoneTestGateway(t)
	cookie := pairPhone(t, m, base)
	e.mu.Lock()
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("r%04d", i)
		e.resources[id] = &Resource{ID: id, Name: fmt.Sprintf("视频 %04d", i), Type: "video", CapturedAt: time.Unix(int64(i), 0), Headers: map[string]string{"Cookie": "never-send-this"}}
		e.jobs[id] = &Job{ID: id, ResourceID: id, Name: fmt.Sprintf("下载 %04d", i), Status: "completed", CreatedAt: time.Unix(int64(i), 0)}
	}
	e.jobs["r0000"].Status = "queued"
	e.config.QueuePaused = true
	e.mu.Unlock()
	snapshot := m.state(url.Values{})
	rs := snapshot["resources"].([]map[string]any)
	if len(rs) != phonePageSize || rs[0]["id"] != "r0999" || len(snapshot["jobs"].([]map[string]any)) != phonePageSize {
		t.Fatal("phone received unbounded or unordered history")
	}
	paging := snapshot["paging"].(map[string]phonePage)
	if paging["resources"].Total != 1000 || paging["resources"].Pages != 34 || snapshot["activity"] != false {
		t.Fatal("wrong total or polling hint", snapshot["paging"])
	}
	status, body, _ := phoneRequest(t, base, "/api/mobile/state?resourceQuery=视频+0000&jobFilter=active", nil, cookie, nil)
	if status != 200 || strings.Contains(string(body), "never-send-this") {
		t.Fatalf("state: %d %s", status, body)
	}
	var result struct {
		Data struct {
			Resources []struct{ ID, DownloadStatus string }
			Jobs      []struct{ ID string }
		}
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data.Resources) != 1 || result.Data.Resources[0].ID != "r0000" || result.Data.Resources[0].DownloadStatus != "queued" || len(result.Data.Jobs) != 1 {
		t.Fatalf("search must include records outside the first page: %s", body)
	}
	snapshot = m.state(url.Values{"resourcePage": {"999999"}, "jobPage": {"-2"}})
	paging = snapshot["paging"].(map[string]phonePage)
	if paging["resources"].Page != 34 || paging["jobs"].Page != 1 {
		t.Fatal("invalid page bounds", paging)
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/queue-control", map[string]bool{"paused": false}, nil, nil)
	if status != 401 {
		t.Fatal("unpaired phone controlled queue")
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/queue-control", map[string]bool{"paused": false}, cookie, nil)
	if status != 200 || e.configSnapshot().QueuePaused {
		t.Fatal("paired phone cannot resume queue")
	}
	if m.state(nil)["activity"] != true {
		t.Fatal("active task on another page must keep phone up to date")
	}
	file := filepath.Join(e.configSnapshot().DownloadDir, "already-downloaded.mp4")
	if err := os.WriteFile(file, []byte("existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.jobs["r0001"].File = file
	e.config.DuplicatePolicy = "skip"
	e.mu.Unlock()
	status, body, _ = phoneRequest(t, base, "/api/mobile/download", map[string]any{"id": "r0001", "details": true}, cookie, nil)
	if status != 200 || !strings.Contains(string(body), `"status":"completed"`) {
		t.Fatalf("batch must recognize an existing file outside the visible page: %d %s", status, body)
	}
}

func BenchmarkPhoneSnapshot1000(b *testing.B) {
	e := &Engine{resources: map[string]*Resource{}, jobs: map[string]*Job{}, batches: map[string]*ParseBatch{}}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprint(i)
		e.resources[id] = &Resource{ID: id, Name: "测试资源", Type: "video"}
		e.jobs[id] = &Job{ID: id, ResourceID: id, Name: "测试下载", Status: "completed"}
	}
	m := &MobileGateway{engine: e}
	b.ReportAllocs()
	for b.Loop() {
		m.state(nil)
	}
}
