package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDouyinPerRenditionMetadataAndQualityOrder(t *testing.T) {
	addr := func(raw string, width, height int) map[string]any {
		return map[string]any{"url_list": []any{raw}, "width": width, "height": height, "data_size": int64(99999999)}
	}
	item := map[string]any{
		"aweme_id": "7688110714506022163", "desc": "quality fixture",
		"video": map[string]any{
			"width": 720, "height": 1280, "duration": 70000,
			"play_addr_h264": addr("https://media.example/default", 576, 1024),
			"bit_rate": []any{
				map[string]any{"play_addr": addr("https://media.example/default", 576, 1024), "FPS": 30, "bit_rate": 9000000},
				map[string]any{"play_addr": addr("https://media.example/1080-avc", 1080, 1920), "FPS": 30, "bit_rate": 12000000},
				map[string]any{"play_addr": addr("https://media.example/1080-hevc", 1080, 1920), "FPS": 60, "bit_rate": 4000000, "is_h265": 1},
				map[string]any{"play_addr": addr("https://media.example/1080-hevc-best", 1080, 1920), "FPS": 60, "bit_rate": 6000000, "is_h265": 1},
				map[string]any{"play_addr": map[string]any{"url_list": []any{"https://media.example/unknown"}}, "FPS": 30, "bit_rate": 15000000},
			},
		},
	}
	v := publicDouyinItem(item, "7688110714506022163", "https://www.douyin.com/video/7688110714506022163")
	if v == nil || len(v.Formats) != 5 {
		t.Fatalf("duplicate URL was not enriched in place: %+v", v)
	}
	low := v.Formats[0]
	if douyinNumber(low["width"]) != 576 || douyinNumber(low["height"]) != 1024 || douyinNumber(low["fps"]) != 30 || douyinNumber(low["tbr"]) != 9000 {
		t.Fatalf("default metadata overwrote the candidate's actual dimensions or discarded its ladder data: %+v", low)
	}
	if unknown := v.Formats[4]; douyinPixels(unknown) != 0 {
		t.Fatalf("unreported ladder dimensions were invented from the default video: %+v", unknown)
	}
	ordered := orderedDouyinFormats(v.Formats)
	if ordered[0]["url"] != "https://media.example/1080-hevc-best" || ordered[1]["url"] != "https://media.example/1080-hevc" || ordered[2]["url"] != "https://media.example/1080-avc" {
		t.Fatalf("codec preference, bitrate or input order beat resolution/FPS: %+v", ordered)
	}
	avc := map[string]any{"width": 1080, "height": 1920, "fps": 60, "vcodec": "h264", "tbr": 12000}
	hevc := ordered[0]
	if betterDouyinQuality(avc, hevc) || strings.Contains(douyinQualityLabel(hevc, []map[string]any{avc}), "回退") {
		t.Fatal("a lower bitrate in HEVC was incorrectly treated as degraded AVC quality")
	}
}

func TestDouyinHighestAccessibleSourceAndActualSize(t *testing.T) {
	prefix := bytes.Repeat([]byte{0x36}, 4096)
	copy(prefix[:12], []byte{0, 0, 0, 32, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		if r.Header.Get("Range") != "bytes=0-4095" || r.Header.Get("Cookie") != "" {
			t.Error("quality check exceeded its unauthenticated prefix-only request")
		}
		if strings.HasPrefix(r.URL.Path, "/blocked") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		if r.URL.Path != "/unknown-total" {
			w.Header().Set("Content-Range", "bytes 0-4095/12000")
		}
		w.WriteHeader(http.StatusPartialContent)
		w.Write(prefix)
	}))
	defer server.Close()
	e := &Engine{transport: &http.Transport{Proxy: nil}}
	defer e.transport.CloseIdleConnections()
	format := func(path, codec string, width, height, fps, bitrate int) map[string]any {
		return map[string]any{"url": server.URL + path, "ext": "mp4", "vcodec": codec, "acodec": "aac", "width": width, "height": height, "fps": fps, "tbr": bitrate, "filesize": int64(99999999), "filesize_approx": int64(88888888)}
	}
	tests := []struct {
		name, expected, notice string
		formats                []map[string]any
		size                   int64
	}{
		{"resolution-before-codec", "/hevc-1080", "", []map[string]any{format("/avc-720", "h264", 720, 1280, 60, 9000), format("/hevc-1080", "h265", 1080, 1920, 30, 4000)}, 12000},
		{"fps-before-bitrate", "/avc-60fps", "", []map[string]any{format("/avc-30fps", "h264", 1080, 1920, 30, 9000), format("/avc-60fps", "h264", 1080, 1920, 60, 4000)}, 12000},
		{"same-codec-bitrate", "/avc-best", "", []map[string]any{format("/avc-default", "h264", 720, 1280, 30, 3000), format("/avc-best", "h264", 720, 1280, 30, 6000)}, 12000},
		{"blocked-higher-source", "/avc-fallback", "回退", []map[string]any{format("/avc-fallback", "h264", 720, 1280, 30, 3000), format("/blocked-high", "h264", 1080, 1920, 60, 6000)}, 12000},
		{"no-invented-total", "/unknown-total", "", []map[string]any{format("/unknown-total", "h264", 720, 1280, 30, 3000)}, -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requested = nil
			v := &douyinPublicVideo{ID: "7688110714506022163", Source: "https://www.douyin.com/video/7688110714506022163", UserAgent: douyinUserAgent, Size: 99999999, Formats: test.formats}
			if err := e.verifyDouyinMedia(context.Background(), v); err != nil {
				t.Fatal(err)
			}
			selected := v.Formats[0]
			if len(v.Formats) != 1 || selected["url"] != server.URL+test.expected || v.Size != test.size {
				t.Fatalf("wrong selected media/response size: %+v", v)
			}
			if _, ok := selected["filesize_approx"]; ok {
				t.Fatal("unselected page estimate survived media verification")
			}
			if test.size < 0 {
				if _, ok := selected["filesize"]; ok {
					t.Fatal("unknown HTTP total retained a stale page filesize")
				}
			} else if douyinNumber(selected["filesize"]) != float64(test.size) {
				t.Fatalf("metadata total differs from the selected HTTP address: %+v", selected)
			}
			if test.notice != "" && !strings.Contains(v.Quality, test.notice) {
				t.Fatalf("lower-quality fallback was not reported: %q", v.Quality)
			}
			if test.notice == "" && len(requested) != 1 {
				t.Fatalf("an accessible lower source was probed ahead of the best one: %v", requested)
			}
		})
	}
}

func TestDouyinDoesNotPretendDASHVideoTrackHasAudio(t *testing.T) {
	prefix := bytes.Repeat([]byte{0x36}, 4096)
	copy(prefix[:12], []byte{0, 0, 0, 32, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/muxed" {
			t.Errorf("a DASH video-only URL was requested as a muxed MP4: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-4095/12000")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(prefix)
	}))
	defer server.Close()
	item := map[string]any{
		"aweme_id": "7688110714506022163",
		"video": map[string]any{
			"width": 720, "height": 1280, "format": "mp4",
			"play_addr": map[string]any{"url_list": []any{server.URL + "/muxed"}, "width": 720, "height": 1280},
			"bit_rate":  []any{map[string]any{"format": "dash", "FPS": 60, "bit_rate": 9000000, "play_addr": map[string]any{"url_list": []any{server.URL + "/video-only"}, "width": 1080, "height": 1920}}},
		},
	}
	v := publicDouyinItem(item, "7688110714506022163", "https://www.douyin.com/video/7688110714506022163")
	e := &Engine{transport: &http.Transport{Proxy: nil}}
	defer e.transport.CloseIdleConnections()
	if v == nil {
		t.Fatal("complete MP4 candidate was discarded")
	}
	if err := e.verifyDouyinMedia(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if v.Formats[0]["acodec"] != "aac" || !strings.Contains(v.Quality, "DASH") || !strings.Contains(v.Quality, "较高档无法使用") {
		t.Fatalf("DASH limitation was hidden or audio metadata is wrong: %+v", v)
	}
}

func TestDouyinRefreshedSizeAndQualityReachResourceAndActiveJob(t *testing.T) {
	r := &Resource{ID: "fixture", Size: 99999999, Quality: "obsolete"}
	e := &Engine{
		resources: map[string]*Resource{r.ID: {ID: r.ID, Size: 99999999}},
		jobs: map[string]*Job{
			"active":    {ResourceID: r.ID, Status: "resolving", Total: 99999999},
			"completed": {ResourceID: r.ID, Status: "completed", Total: 777, Quality: "old completed download"},
		},
	}
	e.updateDouyinSource(r, 12000, 1080, 1920, "最高可用原流 · 1080×1920 · 60 fps · H265")
	if r.Size != 12000 || e.resources[r.ID].Size != 12000 || e.jobs["active"].Total != 12000 || e.jobs["active"].Quality != r.Quality || e.resources[r.ID].Width != 1080 {
		t.Fatal("resource copy, list or active task retained the stale rendition size/quality")
	}
	if e.jobs["completed"].Total != 777 || e.jobs["completed"].Quality != "old completed download" {
		t.Fatal("refresh rewrote the details of an older completed download")
	}
}

func TestDouyinPrefixIdentitySurvivesCDNRefreshAndSeparatesChangedMedia(t *testing.T) {
	// Only the prefix-reader runs. This fixture is not submitted as a complete
	// video, and the test opens no real platform or user browser profile.
	prefix := bytes.Repeat([]byte{0x36}, 4096)
	copy(prefix[:12], []byte{0, 0, 0, 32, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-4095" || r.Header.Get("Cookie") != "" {
			t.Error("prefix probe did not stay scoped to an unauthenticated Range request")
		}
		body := bytes.Clone(prefix)
		size := 12000
		if r.URL.Path == "/changed" {
			body[1024]++
		}
		if r.URL.Path == "/resized" {
			size++
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-4095/%d", size))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body)
	}))
	defer server.Close()
	e := &Engine{transport: &http.Transport{Proxy: nil}}
	defer e.transport.CloseIdleConnections()
	check := func(path, ordinal string) string {
		t.Helper()
		v := &douyinPublicVideo{ID: "7688110714506022163", Source: "https://www.douyin.com/video/7688110714506022163", UserAgent: douyinUserAgent, Size: -1, Formats: []map[string]any{{"format_id": ordinal, "url": server.URL + path, "vcodec": "h264", "ext": "mp4"}}}
		if err := e.verifyDouyinMedia(context.Background(), v); err != nil {
			t.Fatal(err)
		}
		id, _ := v.Formats[0]["format_id"].(string)
		if len(v.Formats) != 1 || len(id) != len("public-")+20 || v.Size < 12000 {
			t.Fatalf("unexpected verified format: %+v", v)
		}
		return id
	}
	first := check("/origin?signature=first", "public-0")
	if mirrored := check("/mirror?signature=refreshed", "public-7"); first != mirrored {
		t.Fatal("same media changed cache identity after URL signature or mirror order changed")
	}
	if check("/changed", "public-0") == first || check("/resized", "public-0") == first {
		t.Fatal("changed media would reuse the existing partial-file cache")
	}
}

func TestDouyinCancelledAddressRefreshStartsNoBrowser(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &Engine{transport: &http.Transport{Proxy: nil}}
	defer e.transport.CloseIdleConnections()
	_, err := e.videoDownloadInput(ctx, &Resource{URL: "https://www.douyin.com/video/7688110714506022163"}, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled refresh continued instead of stopping: %v", err)
	}
}
