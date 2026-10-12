package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWechatOriginalMasksAndCapturedMP4(t *testing.T) {
	// Expected full-mask digests come from the original shipped WxIsaac64 WASM.
	vectors := map[string]string{
		"0":                   "e1662af3b7e59867c919ad19055fc8cecea2b154d37e1459b2f96d1da14cef1f",
		"1":                   "39d98f5b25cc52f0996f7ef9e1022156cb029901be6a11ebbdd7469c6ffd5839",
		"1234567890123456789": "7a760c59b0fddef2c9d212bdca8671b9f293e3a1b0a4e39699a6cb1f5d363572",
	}
	for seed, expected := range vectors {
		mask, err := wechatDecodeMask(seed)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(mask)
		if hex.EncodeToString(digest[:]) != expected {
			t.Fatalf("mask disagrees with original for seed %s", seed)
		}
	}
	e := testEngine(t, 1)
	testDir := t.TempDir()
	plainFile := filepath.Join(testDir, "fixture.mp4")
	tools, err := e.videoTools()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(tools, "ffmpeg.exe"), "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=16x16:d=0.2", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", plainFile)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("local fixture: %v %s", err, output)
	}
	plain, err := os.ReadFile(plainFile)
	if err != nil {
		t.Fatal(err)
	}
	mask, _ := wechatDecodeMask("1")
	encrypted := bytes.Clone(plain)
	for i := 0; i < len(encrypted) && i < len(mask); i++ {
		encrypted[i] ^= mask[i]
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"data":{"object":{"objectDesc":{"description":"本地视频号管线检查","media":[{"mediaType":4,"url":"http://finder.video.qq.com/fixture.mp4?encfilekey=test-fixture","urlToken":"&token=fixture-only","decodeKey":"1","fileSize":"%d"}]}}}}`, len(encrypted))
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "fixture.mp4", time.Unix(1700000000, 0), bytes.NewReader(encrypted))
	}))
	defer origin.Close()
	u, _ := url.Parse(origin.URL)
	e.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, u.Host)
	}
	e.mu.Lock()
	e.capture = true
	e.mu.Unlock()
	// Seeing only an encrypted CDN URL must not enable a corrupt MP4 download.
	pending := e.remember("http://finder.video.qq.com/fixture.mp4?encfilekey=test-fixture&token=fixture-only", nil, "video/mp4", int64(len(encrypted)), "", false)
	if pending == nil || !pending.CaptureRequired {
		t.Fatal("encrypted CDN response did not wait for metadata")
	}
	if _, err = e.startDownload(pending.ID); err == nil {
		t.Fatal("encrypted CDN URL downloaded without video metadata")
	}
	proxy, _ := url.Parse("http://" + e.proxyListener.Addr().String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	response, err := client.Get("http://channels.weixin.qq.com/metadata")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	e.mu.RLock()
	var resourceID string
	for id, r := range e.resources {
		if r.WechatVideo {
			resourceID = id
			if r.CaptureRequired || id != pending.ID {
				t.Fatal("metadata did not upgrade the existing pending media")
			}
			if r.wechatSeed != "1" {
				t.Fatal("lost captured seed")
			}
		}
	}
	e.mu.RUnlock()
	if resourceID == "" {
		t.Fatal("proxy did not capture video metadata")
	}
	jobID, err := e.startDownload(resourceID)
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, e, jobID)
	if job.Status != "completed" {
		t.Fatalf("video pipeline failed: %s", job.Error)
	}
	result, err := os.ReadFile(job.File)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, plain) {
		t.Fatal("final MP4 differs from original plaintext fixture")
	}
	t.Log("3 original WASM vectors matched; proxy metadata -> encrypted download -> ISAAC-64 -> ffprobe -> committed MP4 passed")
}
