package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMediaLocalTicketAndGuards(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(fixtureVideo(t, &Engine{root: root}, false), "clip.mp4")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{ctx: context.Background(), resources: map[string]*Resource{"r": {ID: "r", Name: "作品.mp4", Type: "video"}}, jobs: map[string]*Job{"j": {ID: "j", ResourceID: "r", Name: "作品.mp4", Status: "completed", File: file, CreatedAt: time.Now()}}}
	server := httptest.NewServer(http.HandlerFunc(e.serveMedia))
	defer server.Close()
	e.baseURL = server.URL
	result, err := e.createMediaTicket("r", "video")
	if err != nil {
		t.Fatal(err)
	}
	ticket := result.(map[string]any)
	preview := ticket["url"].(string)
	if strings.Contains(preview, file) || strings.Contains(preview, "token=") {
		t.Fatal("ticket leaked original identity")
	}
	req, _ := http.NewRequest("GET", preview, nil)
	req.Header.Set("Range", "bytes=17-31")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || !bytes.Equal(body, data[17:32]) {
		t.Fatalf("local range got %d, %d bytes", resp.StatusCode, len(body))
	}
	for _, tc := range []struct {
		name, origin, referer, host string
		status                      int
	}{{"origin", "https://evil.example", "", "", 403}, {"referer", "", "https://evil.example/page", "", 403}, {"host", "", "", "evil.example", 403}} {
		t.Run(tc.name, func(t *testing.T) {
			request, _ := http.NewRequest("GET", preview, nil)
			request.Header.Set("Origin", tc.origin)
			request.Header.Set("Referer", tc.referer)
			if tc.host != "" {
				request.Host = tc.host
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatal(response.StatusCode)
			}
		})
	}
	response, _ := server.Client().Get(server.URL + "/media/unknown")
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal(response.StatusCode)
	}
	path, _ := url.Parse(preview)
	key := strings.TrimPrefix(path.Path, "/media/")
	e.mu.Lock()
	old := e.mediaTickets[key]
	old.Expires = time.Now().Add(-time.Minute)
	e.mediaTickets[key] = old
	e.mu.Unlock()
	response, _ = server.Client().Get(preview)
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("expired ticket accepted")
	}
	request := httptest.NewRequest("GET", preview, nil)
	request.RemoteAddr = "192.168.1.10:4321"
	record := httptest.NewRecorder()
	e.serveMedia(record, request)
	if record.Code != 403 {
		t.Fatal("non-local request accepted")
	}
}

func TestMediaWechatRangesDecryptAbsoluteOffset(t *testing.T) {
	mask, err := wechatDecodeMask("1234567890")
	if err != nil {
		t.Fatal(err)
	}
	plain := append([]byte("0000ftypisom0000"), bytes.Repeat([]byte("offset-sensitive-stream"), 8000)...)
	encrypted := append([]byte(nil), plain...)
	for i := 0; i < len(mask) && i < len(encrypted); i++ {
		encrypted[i] ^= mask[i]
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "clip.mp4", time.Time{}, bytes.NewReader(encrypted))
	}))
	defer source.Close()
	e := &Engine{ctx: context.Background(), transport: &http.Transport{}, mediaTickets: map[string]mediaTicket{"ticket": {URL: source.URL, Type: "video/mp4", Mask: mask, Expires: time.Now().Add(time.Minute)}}}
	server := httptest.NewServer(http.HandlerFunc(e.serveMedia))
	defer server.Close()
	e.baseURL = server.URL
	for _, span := range [][2]int{{0, 15}, {3, 4097}, {131060, 131090}, {131090, 131120}} {
		request, _ := http.NewRequest("GET", server.URL+"/media/ticket", nil)
		request.Header.Set("Range", "bytes="+strconv.Itoa(span[0])+"-"+strconv.Itoa(span[1]))
		resp, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 206 || !bytes.Equal(body, plain[span[0]:span[1]+1]) {
			t.Fatalf("range %v decrypted incorrectly", span)
		}
	}
	// Prefix probing detects a already-decoded file, even if a seed is present.
	plainSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "clip.mp4", time.Time{}, bytes.NewReader(plain))
	}))
	defer plainSource.Close()
	got, err := e.previewWechatMask(&Resource{wechatSeed: "1234567890"}, plainSource.URL)
	if err != nil || len(got) != 0 {
		t.Fatal("plaintext must not be XORed", err)
	}
	got, err = e.previewWechatMask(&Resource{wechatSeed: "1234567890"}, source.URL)
	if err != nil || !bytes.Equal(got, mask) {
		t.Fatal("encrypted file must use matched mask", err)
	}
}

func TestMediaSourceValidation(t *testing.T) {
	for _, raw := range []string{"http://localhost/a", "http://127.0.0.1/a", "http://10.0.0.1/a", "file:///D:/secret", "https://user:password@example.com/a", "http://169.254.1.1/a"} {
		if publicMediaURL(raw) {
			t.Fatal("private source accepted", raw)
		}
	}
	if !publicMediaURL("https://cdn.example.com/a.mp4?sign=opaque") {
		t.Fatal("public URL rejected")
	}
}
