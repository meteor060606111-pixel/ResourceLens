package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func qualityFixture(size, width, height int) []byte {
	data := make([]byte, size)
	binary.BigEndian.PutUint32(data, 24)
	copy(data[4:8], "ftyp")
	copy(data[8:12], "isom")
	binary.BigEndian.PutUint32(data[24:28], 108)
	copy(data[28:32], "moov")
	binary.BigEndian.PutUint32(data[32:36], 100)
	copy(data[36:40], "trak")
	binary.BigEndian.PutUint32(data[40:44], 92)
	copy(data[44:48], "tkhd")
	binary.BigEndian.PutUint32(data[124:128], uint32(width)<<16)
	binary.BigEndian.PutUint32(data[128:132], uint32(height)<<16)
	mask, _ := wechatDecodeMask("1")
	for index := range data {
		if index < len(mask) {
			data[index] ^= mask[index]
		}
	}
	return data
}

func TestWechatQualityDoesNotSilentlySubstitutePlayback(t *testing.T) {
	e := testEngine(t, 1)
	low, high := qualityFixture(2048, 576, 576), qualityFixture(8192, 1920, 1080)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("basedata") == "" {
			w.WriteHeader(400)
			return
		}
		data := low
		if r.URL.Query().Get("X-snsvideoflag") == "hd" {
			data = high
		}
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "fixture.mp4", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer origin.Close()
	u, _ := url.Parse(origin.URL)
	e.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, u.Host)
	}
	resource := &Resource{ID: "0123456789abcdef01234567", URL: "http://finder.video.qq.com/stodownload?encfilekey=fixture&token=ok&basedata=fixture-only", WechatVideo: true, WechatSourceSize: 16384, WechatFormats: []WechatFormat{{ID: "hd", Width: 1920, Height: 1080}}, wechatSeed: "1", privateHeaders: http.Header{}, Type: "video"}
	resource.WechatPlaybackURL = resource.URL
	e.resources[resource.ID] = resource
	job := &Job{ID: "quality-fixture", ResourceID: resource.ID}
	e.jobs[job.ID] = job
	copy := *resource
	err := e.prepareWechatDownload(context.Background(), job, &copy, "source")
	if err == nil || !strings.Contains(err.Error(), "未换成低清文件") || job.File != "" {
		t.Fatalf("source request silently downgraded: %v", err)
	}
	copy = *resource
	if err = e.prepareWechatDownload(context.Background(), job, &copy, "best"); err != nil {
		t.Fatal(err)
	}
	if copy.Size != 8192 || copy.Width != 1920 || copy.Height != 1080 || !strings.Contains(job.Quality, "平台标注版本未取得") {
		t.Fatal("did not expose highest actual accessible variant and actual size")
	}
	if !strings.Contains(copy.URL, "X-snsvideoflag=hd") || !strings.Contains(copy.URL, "basedata=fixture-only") {
		t.Fatal("lost required parameters or quality selector")
	}
	copy = *resource
	if err = e.prepareWechatDownload(context.Background(), job, &copy, "playback"); err != nil {
		t.Fatal(err)
	}
	if copy.Size != 2048 || copy.Width != 576 || !strings.Contains(job.Quality, "播放版本") {
		t.Fatal("explicit playback did not report its own size and resolution")
	}
}
