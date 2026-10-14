package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func phoneTestGateway(t *testing.T) (*Engine, *MobileGateway, string) {
	t.Helper()
	e := testEngine(t, 1)
	if e.mobileStatus().Enabled {
		t.Fatal("phone access must start disabled")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m, err := newMobileGateway(e, fstest.MapFS{"phone.html": {Data: []byte("phone entry")}}, ln)
	if err != nil {
		t.Fatal(err)
	}
	e.mobileMu.Lock()
	e.mobile = m
	e.mobileMu.Unlock()
	return e, m, "http://" + ln.Addr().String()
}

func phoneRequest(t *testing.T, base, endpoint string, data any, cookie *http.Cookie, mutate func(*http.Request)) (int, []byte, []*http.Cookie) {
	t.Helper()
	method := "GET"
	var body io.Reader
	if data != nil {
		method = "POST"
		b, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, base+endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if mutate != nil {
		mutate(r)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b, resp.Cookies()
}

func pairPhone(t *testing.T, m *MobileGateway, base string) *http.Cookie {
	t.Helper()
	status, body, cookies := phoneRequest(t, base, "/api/mobile/pair", map[string]any{"code": m.status().Code}, nil, nil)
	if status != 200 || len(cookies) != 1 {
		t.Fatalf("pair: %d %s", status, body)
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || len(c.Value) != 64 {
		t.Fatalf("cookie properties: %#v", c)
	}
	return c
}

func TestPhonePairingAndCapabilityBoundary(t *testing.T) {
	e, m, base := phoneTestGateway(t)
	status, b, _ := phoneRequest(t, base, "/", nil, nil, nil)
	if status != 200 || string(b) != "phone entry" {
		t.Fatalf("entry: %d %s", status, b)
	}
	_, b, _ = phoneRequest(t, base, "/api/mobile/bootstrap", nil, nil, nil)
	if strings.Contains(string(b), m.status().Code) || strings.Contains(string(b), e.token) {
		t.Fatal("public bootstrap exposed credential")
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/state", nil, nil, nil)
	if status != 401 {
		t.Fatalf("unpaired state: %d", status)
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/pair", map[string]string{"code": "bad"}, nil, nil)
	if status != 400 {
		t.Fatalf("bad code: %d", status)
	}
	cookie := pairPhone(t, m, base)
	for _, endpoint := range []string{"/api/bootstrap", "/api/state", "/api/config", "/api/install-certificate", "/api/mobile/config", "/api/mobile/open-folder"} {
		status, _, _ = phoneRequest(t, base, endpoint, map[string]any{}, cookie, nil)
		if status != 404 {
			t.Fatalf("desktop capability %s exposed: %d", endpoint, status)
		}
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/state", nil, cookie, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	if status != 403 {
		t.Fatalf("cross origin: %d", status)
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/state", nil, cookie, func(r *http.Request) { r.Host = "evil.example:" + strings.Split(base, ":")[2] })
	if status != 403 {
		t.Fatalf("untrusted host: %d", status)
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/state", nil, cookie, func(r *http.Request) { r.Header.Set("Origin", base) })
	if status != 200 {
		t.Fatalf("same origin: %d", status)
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/disconnect", map[string]any{}, cookie, nil)
	if status != 200 {
		t.Fatalf("disconnect: %d", status)
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/state", nil, cookie, nil)
	if status != 401 {
		t.Fatalf("revoked session: %d", status)
	}
	pairPhone(t, m, base)
	e.stopMobile()
	if m.authorized(&http.Request{Header: http.Header{"Cookie": []string{cookie.String()}}}) {
		t.Fatal("session survived stop")
	}
}

func TestPhonePairCodeExpiryAndThrottle(t *testing.T) {
	_, m, base := phoneTestGateway(t)
	code := m.status().Code
	m.mu.Lock()
	m.codeExpires = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	status, _, _ := phoneRequest(t, base, "/api/mobile/pair", map[string]string{"code": code}, nil, nil)
	if status != 400 {
		t.Fatalf("expired code: %d", status)
	}
	m.status()
	for i := 0; i < 8; i++ {
		phoneRequest(t, base, "/api/mobile/pair", map[string]string{"code": "bad"}, nil, nil)
	}
	status, _, _ = phoneRequest(t, base, "/api/mobile/pair", map[string]string{"code": m.status().Code}, nil, nil)
	if status != 429 {
		t.Fatalf("throttle: %d", status)
	}
}

func TestPhoneStateDoesNotExposeDesktopSecrets(t *testing.T) {
	e, m, base := phoneTestGateway(t)
	cookie := pairPhone(t, m, base)
	e.mu.Lock()
	e.resources["r"] = &Resource{ID: "r", Name: "公开标题", Type: "video", URL: "https://media.example/private-token", Headers: map[string]string{"Cookie": "account-cookie"}}
	e.jobs["j"] = &Job{ID: "j", ResourceID: "r", Name: "公开标题", Status: "failed", URL: "https://media.example/private-token", File: "/secret/download-path", Error: "/secret/download-path account-cookie"}
	e.batches = map[string]*ParseBatch{"b": {ID: "b", Items: []ParseItem{{URL: "https://media.example/private-token", Status: "failed", Error: "account-cookie"}}}}
	e.mu.Unlock()
	status, b, _ := phoneRequest(t, base, "/api/mobile/state", nil, cookie, nil)
	if status != 200 {
		t.Fatalf("state: %d %s", status, b)
	}
	for _, secret := range []string{"private-token", "account-cookie", "/secret/download-path", e.token, e.ca.certPath, m.status().Code} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("state leaked %q", secret)
		}
	}
	if !strings.Contains(string(b), "公开标题") {
		t.Fatal("sanitization removed public title")
	}
}

func TestPhoneFileTransferRangeAndPreview(t *testing.T) {
	e, m, base := phoneTestGateway(t)
	cookie := pairPhone(t, m, base)
	var payload bytes.Buffer
	if err := png.Encode(&payload, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	dir := e.configSnapshot().DownloadDir
	file := filepath.Join(dir, "测试图片.png")
	if err := os.WriteFile(file, payload.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.jobs["image"] = &Job{ID: "image", Name: "测试图片.png", Status: "completed", File: file, OutputDir: dir, Total: int64(payload.Len())}
	e.mu.Unlock()
	status, _, _ := phoneRequest(t, base, "/files/image", nil, nil, nil)
	if status != 401 {
		t.Fatalf("unpaired file: %d", status)
	}
	status, b, _ := phoneRequest(t, base, "/files/image", nil, cookie, nil)
	if status != 200 || !bytes.Equal(b, payload.Bytes()) {
		t.Fatalf("transfer: %d", status)
	}
	status, b, _ = phoneRequest(t, base, "/files/image", nil, cookie, func(r *http.Request) { r.Header.Set("Range", "bytes=10-17") })
	if status != 206 || !bytes.Equal(b, payload.Bytes()[10:18]) {
		t.Fatalf("range: %d %x", status, b)
	}
	status, b, _ = phoneRequest(t, base, "/preview/image", nil, cookie, nil)
	if status != 200 || !bytes.Equal(b, payload.Bytes()) {
		t.Fatalf("preview: %d %s", status, b)
	}
	outside := filepath.Join(e.root, "private-file")
	os.WriteFile(outside, []byte("not shared"), 0600)
	e.mu.Lock()
	e.jobs["outside"] = &Job{ID: "outside", Status: "completed", File: outside, OutputDir: dir}
	e.jobs["working"] = &Job{ID: "working", Status: "downloading", File: file, OutputDir: dir}
	e.mu.Unlock()
	for _, id := range []string{"outside", "working", "../private-file"} {
		status, _, _ = phoneRequest(t, base, "/files/"+id, nil, cookie, nil)
		if status != 404 {
			t.Fatalf("non-download file %s exposed: %d", id, status)
		}
	}
	link := filepath.Join(dir, "linked.png")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic link privilege unavailable; file and range checks completed")
		}
		t.Fatal(err)
	}
	e.mu.Lock()
	e.jobs["linked"] = &Job{ID: "linked", Status: "completed", File: link, OutputDir: dir}
	e.mu.Unlock()
	status, _, _ = phoneRequest(t, base, "/files/linked", nil, cookie, nil)
	if status != 404 {
		t.Fatalf("symlink exposed: %d", status)
	}
}
