package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func testEngine(t *testing.T, concurrent int) *Engine {
	t.Helper()
	root := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cfg := Config{DownloadDir: filepath.Join(root, "downloads"), ProxyPort: port, Segments: 4, Concurrent: concurrent, HTTPS: true}
	os.MkdirAll(filepath.Join(root, "data"), 0700)
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(root, "data", "config.json"), b, 0600)
	e, err := NewEngine(root, fstest.MapFS{"index.html": {Data: []byte("test UI")}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func waitJob(t *testing.T, e *Engine, id string) *Job {
	t.Helper()
	// Bundled yt-dlp can take over 25 seconds to start on a busy macOS host.
	// Keep the output and byte validation strict while bounding that startup.
	until := time.Now().Add(60 * time.Second)
	for time.Now().Before(until) {
		e.mu.RLock()
		j := e.jobs[id]
		status := j.Status
		e.mu.RUnlock()
		if status == "completed" || status == "failed" || status == "cancelled" {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.mu.RLock()
	j := e.jobs[id]
	status, detail, notice, done, total := j.Status, j.Error, j.Notice, j.done.Load(), j.Total
	e.mu.RUnlock()
	t.Fatalf("job timeout: status=%s done=%d total=%d error=%s notice=%s", status, done, total, detail, notice)
	return nil
}
func TestProxyCaptureAndRangeDownloadIdentity(t *testing.T) {
	e := testEngine(t, 2)
	payload := bytes.Repeat([]byte("ResourceLens-byte-exact-test!"), 120000)
	var ranges atomic.Int32
	var cookies atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
		}
		if r.Header.Get("Cookie") == "session=test-only" {
			cookies.Add(1)
		}
		w.Header().Set("Content-Type", "application/zip")
		http.ServeContent(w, r, "demo.zip", time.Unix(1700000000, 0), bytes.NewReader(payload))
	}))
	defer s.Close()
	proxy, _ := url.Parse("http://" + e.proxyListener.Addr().String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	e.mu.Lock()
	e.capture = true
	e.mu.Unlock()
	req, _ := http.NewRequest("GET", s.URL+"/demo.zip", nil)
	req.Header.Set("Cookie", "session=test-only")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	e.mu.RLock()
	var res *Resource
	for _, r := range e.resources {
		res = r
	}
	e.mu.RUnlock()
	if res == nil || res.Type != "file" || res.Size != int64(len(payload)) {
		t.Fatal("capture metadata mismatch")
	}
	if _, ok := res.Headers["Cookie"]; ok {
		t.Fatal("credentials exposed to UI")
	}
	id, err := e.startDownload(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, e, id)
	if j.Status != "completed" {
		t.Fatalf("download failed: %s", j.Error)
	}
	actual, err := os.ReadFile(j.File)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(actual) != sha256.Sum256(payload) {
		t.Fatal("download hash mismatch")
	}
	if ranges.Load() != 5 {
		t.Fatalf("expected one identity probe and 4 download ranges, got %d", ranges.Load())
	}
	if cookies.Load() < 5 {
		t.Fatal("captured headers not used by download")
	}
	if j.done.Load() != int64(len(payload)) {
		t.Fatal("progress byte count mismatch")
	}
	id2, err := e.startDownload(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	j2 := waitJob(t, e, id2)
	if j2.Status != "completed" || j2.File == j.File {
		t.Fatal("filename collision not handled")
	}
}
func TestHTTPSCaptureWithLocalCA(t *testing.T) {
	e := testEngine(t, 1)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("png-test-bytes"))
	}))
	defer s.Close()
	upRoots := x509.NewCertPool()
	upRoots.AddCert(s.Certificate())
	e.transport.TLSClientConfig = &tls.Config{RootCAs: upRoots}
	roots := x509.NewCertPool()
	roots.AddCert(e.ca.cert)
	proxy, _ := url.Parse("http://" + e.proxyListener.Addr().String())
	tr := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	e.mu.Lock()
	e.capture = true
	e.mu.Unlock()
	resp, err := client.Get(s.URL + "/cover.png")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "png-test-bytes" {
		t.Fatal("HTTPS forwarding corrupted")
	}
	if len(e.resources) != 1 {
		t.Fatal("HTTPS capture not recorded")
	}
}
func TestCancelRunningAndQueuedDownloads(t *testing.T) {
	e := testEngine(t, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "8388608")
		if r.Method == "HEAD" {
			return
		}
		for n := 0; n < 128; n++ {
			if _, err := w.Write(make([]byte, 65536)); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer s.Close()
	r1, err := e.addURL(s.URL + "/slow.mp4")
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := e.addURL(s.URL + "/queued.mp4")
	id1, _ := e.startDownload(r1.ID)
	id2, _ := e.startDownload(r2.ID)
	time.Sleep(80 * time.Millisecond)
	e.mu.Lock()
	e.jobs[id2].cancel()
	e.jobs[id1].cancel()
	e.mu.Unlock()
	for _, id := range []string{id1, id2} {
		j := waitJob(t, e, id)
		if j.Status != "cancelled" {
			t.Fatalf("expected cancelled, got %s", j.Status)
		}
	}
	files, _ := os.ReadDir(e.config.DownloadDir)
	if len(files) != 0 {
		var names []string
		for _, f := range files {
			names = append(names, f.Name())
		}
		t.Fatalf("cancelled downloads left files: %v", names)
	}
}
func TestAPIAuthorizationAndOrigin(t *testing.T) {
	e := testEngine(t, 1)
	resp, err := http.Get(e.baseURL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("unauthenticated state accepted")
	}
	req, _ := http.NewRequest("POST", e.baseURL+"/api/clear", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Origin", "https://unrelated.invalid")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
}
func TestBadContentRangeFailsWithoutFinalFile(t *testing.T) {
	e := testEngine(t, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "video/mp4")
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", "1048576")
			return
		}
		w.Header().Set("Content-Range", "bytes 0-1/2")
		w.WriteHeader(206)
		w.Write([]byte{1, 2})
	}))
	defer s.Close()
	r, _ := e.addURL(s.URL + "/bad.mp4")
	id, _ := e.startDownload(r.ID)
	j := waitJob(t, e, id)
	if j.Status != "failed" || !strings.Contains(j.Error, "Content-Range") {
		t.Fatalf("expected range error: %+v", j)
	}
	if _, err := os.Stat(j.File); !os.IsNotExist(err) {
		t.Fatal("bad range produced final file")
	}
}
func TestNonRangeAndHeadFallback(t *testing.T) {
	e := testEngine(t, 1)
	payload := []byte("a server that disallows HEAD and ignores Range")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(payload)
	}))
	defer s.Close()
	r, err := e.addURL(s.URL + "/audio.mp3")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := e.startDownload(r.ID)
	j := waitJob(t, e, id)
	if j.Status != "completed" {
		t.Fatal(j.Error)
	}
	b, _ := os.ReadFile(j.File)
	if !bytes.Equal(b, payload) {
		t.Fatal("single-stream fallback mismatch")
	}
}
func TestFilenameSanitization(t *testing.T) {
	for _, name := range []string{"CON.mp4", "../../bad:name?.mp4", "NUL", "a\x00b", ""} {
		safe := safeFilename(name)
		if strings.ContainsAny(safe, `<>:"/\|?*`) {
			t.Fatal(safe)
		}
		if strings.HasPrefix(safe, "CON.") || safe == "NUL" || safe == "" {
			t.Fatal(safe)
		}
	}
}

var _ = context.Background
var _ = fmt.Sprintf

func TestProxyRecoveryPreservesUserSettings(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "data"), 0700)
	original := ProxySnapshot{Enabled: 1, Server: "127.0.0.1:7890", HasEnabled: true, HasServer: true}
	current := original
	e := &Engine{root: root, config: Config{ProxyPort: 18999}, readProxy: func() (ProxySnapshot, error) { return current, nil }, writeProxy: func(s ProxySnapshot) error { current = s; return nil }}
	if err := e.enableSystemProxyLocked(); err != nil {
		t.Fatal(err)
	}
	if current.Server != "127.0.0.1:18999" {
		t.Fatal("proxy not set")
	}
	if _, err := os.Stat(e.recoveryPath()); err != nil {
		t.Fatal("durable recovery record missing")
	}
	if err := e.restoreProxyLocked(); err != nil {
		t.Fatal(err)
	}
	if current.Server != original.Server || current.Enabled != original.Enabled {
		t.Fatal("original proxy not restored")
	}
	if err := e.enableSystemProxyLocked(); err != nil {
		t.Fatal(err)
	}
	current.Server = "user-changed-proxy:8080"
	if err := e.restoreProxyLocked(); err != nil {
		t.Fatal(err)
	}
	if current.Server != "user-changed-proxy:8080" {
		t.Fatal("user override overwritten")
	}
	current = original
	if err := e.enableSystemProxyLocked(); err != nil {
		t.Fatal(err)
	}
	// Emulate process restart against a persisted recovery record; no registry writes.
	e.proxySnapshot = nil
	if err := e.recoverProxy(); err != nil {
		t.Fatal(err)
	}
	if current.Server != original.Server {
		t.Fatal("crash recovery did not restore original")
	}
	if _, err := os.Stat(e.recoveryPath()); !os.IsNotExist(err) {
		t.Fatal("recovery file not removed")
	}
}
