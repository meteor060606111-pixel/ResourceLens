package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureFilterPreservesWholeMedia(t *testing.T) {
	e := &Engine{resources: map[string]*Resource{}}
	for _, tc := range []struct {
		path, mime string
		size       int64
		manual     bool
		keep       bool
	}{
		{"/a.M4S?range=0-999", "video/mp4", 4096, false, false},
		{"/track.cmfa", "audio/mp4", 4096, false, false},
		{"/part", "video/iso.segment", 4096, false, false},
		{"/segment-100.ts", "video/mp2t", 4096, false, false},
		{"/stream.flv", "video/x-flv", -1, false, false},
		{"/stream.flv?manual=1", "video/x-flv", -1, true, true},
		{"/recording.flv", "video/x-flv", 10000, false, true},
		{"/whole.mp4?range=1-99", "video/mp4", 10000, false, true},
		{"/camera.ts", "video/mp2t", 10000, false, true},
		{"/master.m3u8", "application/vnd.apple.mpegurl", 512, false, true},
		{"/manifest.mpd", "application/dash+xml", 512, false, true},
		{"/poster.webp", "image/webp", 1200, false, true},
	} {
		r := e.remember("https://cdn.example.com"+tc.path, http.Header{"Range": {"bytes=0-99"}}, tc.mime, tc.size, "", tc.manual)
		if (r != nil) != tc.keep {
			t.Errorf("%s keep=%v, expected %v", tc.path, r != nil, tc.keep)
		}
	}
	if _, err := e.addURL("https://cdn.example.com/track.m4s"); err == nil || !strings.Contains(err.Error(), "分段") {
		t.Fatal("manual segment did not explain how to use a complete source")
	}
}

func TestLoadSessionRemovesCaptureNoiseButRetainsJobs(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "data"), 0700)
	partID, fullID, liveID := strings.Repeat("1", 24), strings.Repeat("2", 24), strings.Repeat("3", 24)
	saved := storedSession{Resources: []Resource{
		{ID: partID, URL: "https://cdn.example.com/a.m4s", Name: "a.m4s", Type: "video", MIME: "video/mp4", Kind: "direct"},
		{ID: fullID, URL: "https://cdn.example.com/a.mp4", Name: "a.mp4", Type: "video", MIME: "video/mp4", Kind: "direct"},
		{ID: liveID, URL: "https://cdn.example.com/a.flv", Name: "a.flv", Type: "video", MIME: "video/x-flv", Kind: "direct", Manual: true, Size: -1},
	}, Jobs: []storedJob{{Job: Job{ID: "finished", ResourceID: partID, Status: "completed", File: "untouched.mp4"}}}}
	data, _ := json.Marshal(saved)
	os.WriteFile(filepath.Join(root, "data", "session.json"), data, 0600)
	e := &Engine{root: root, ctx: context.Background(), resources: map[string]*Resource{}, jobs: map[string]*Job{}}
	e.loadSession()
	if e.resources[partID] != nil || e.resources[fullID] == nil || e.resources[liveID] == nil || len(e.jobs) != 1 || e.jobs["finished"].File != "untouched.mp4" {
		t.Fatal("session cleanup removed a full resource, explicit live URL or download record")
	}
}
