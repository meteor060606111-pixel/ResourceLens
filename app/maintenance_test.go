package main

import (
	"archive/zip"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceDiagnosticsExcludePrivateData(t *testing.T) {
	root := t.TempDir()
	download := filepath.Join(root, "private-downloads")
	for _, dir := range []string{download, filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{root: root, config: Config{DownloadDir: download, ProxyPort: 19888, Segments: 8, Concurrent: 3, HTTPS: true, ClassifyBy: "none", DuplicatePolicy: "ask", NameTemplate: "private-template-secret", UpdateURL: "https://updates.example.com/feed?secret=update-feed-secret"}, resources: map[string]*Resource{}, jobs: map[string]*Job{}, ca: &Authority{cert: &x509.Certificate{NotAfter: time.Now().Add(time.Hour)}}, readProxy: func() (ProxySnapshot, error) { return ProxySnapshot{}, nil }}
	e.resources["resource-secret"] = &Resource{ID: "resource-secret", URL: "https://video.example/secret-signed-path?token=signed-url-secret", Name: "private-personal-video-title", Author: "private-author-name", Headers: map[string]string{"Cookie": "secret-cookie"}, privateHeaders: http.Header{"Cookie": []string{"private-cookie"}}, wechatSeed: "private-decode-seed", Type: "video", Site: "private-site-title"}
	e.jobs["job-secret"] = &Job{ID: "job-secret", Name: "private-personal-video-title", URL: "https://x.example/?sign=private-signature", File: filepath.Join(download, "private-personal-video-title.mp4"), Error: "403 https://secret.example/?token=private-error-token", Status: "failed"}
	result, err := e.exportDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	path := result.(map[string]any)["file"].(string)
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if len(z.File) != 3 {
		t.Fatalf("unexpected diagnostic entries: %d", len(z.File))
	}
	for _, entry := range z.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"resource-secret", "job-secret", "signed-url-secret", "private-personal-video-title", "private-author-name", "private-cookie", "secret-cookie", "private-decode-seed", "private-error-token", "update-feed-secret", "private-template-secret", "private-site-title", root, "secret.example", "video.example"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("%s leaked %q", entry.Name, secret)
			}
		}
		if entry.Name == "diagnostics.json" {
			var value map[string]any
			if err = json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if value["version"] != appVersion {
				t.Fatal("version missing")
			}
		}
	}
}
