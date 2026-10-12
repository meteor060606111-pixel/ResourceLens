package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestHTMLNeverCompletesAsMedia(t *testing.T) {
	for _, variant := range []string{"html-mime", "mislabelled", "gzip-mislabelled"} {
		t.Run(variant, func(t *testing.T) {
			e := testEngine(t, 1)
			payload := []byte("<!DOCTYPE html><html><body>Login required</body></html>")
			if variant == "gzip-mislabelled" {
				var b bytes.Buffer
				z := gzip.NewWriter(&b)
				z.Write(payload)
				z.Close()
				payload = b.Bytes()
			}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mt := "video/mp4"
				if variant == "html-mime" {
					mt = "text/html"
				}
				w.Header().Set("Content-Type", mt)
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				if r.Method != "HEAD" {
					w.Write(payload)
				}
			}))
			defer s.Close()
			r := e.remember(s.URL+"/fake.mp4", http.Header{}, "video/mp4", int64(len(payload)), "", true)
			id, err := e.startDownload(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			j := waitJob(t, e, id)
			if j.Status != "failed" || !strings.Contains(j.Error, "网页") {
				t.Fatalf("false media completion: %s %s", j.Status, j.Error)
			}
			files, _ := os.ReadDir(e.config.DownloadDir)
			if len(files) != 0 {
				t.Fatal("HTML or partial file was retained")
			}
		})
	}
}

func fixtureVideo(t *testing.T, e *Engine, dash bool) string {
	t.Helper()
	dir, err := e.videoTools()
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	output := filepath.Join(fixture, "clip.mp4")
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=teal:s=320x180:r=15", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "2", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac"}
	if dash {
		output = filepath.Join(fixture, "manifest.mpd")
		args = append(args, "-f", "dash", "-seg_duration", "1")
	} else {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, filepath.ToSlash(output))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.Command(filepath.Join(dir, "ffmpeg.exe"), args...)
	cmd.Dir = fixture
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = runManagedCommand(ctx, cmd); err != nil {
		t.Fatalf("fixture generation: %v %s", err, stderr.String())
	}
	return fixture
}
func TestPageDownloadAndDASHMergeProduceValidatedMP4(t *testing.T) {
	for _, dash := range []bool{false, true} {
		t.Run(fmt.Sprintf("dash-%v", dash), func(t *testing.T) {
			e := testEngine(t, 1)
			fixture := fixtureVideo(t, e, dash)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if dash && r.URL.Path != "/manifest.mpd" {
					if _, err := os.Stat(filepath.Join(fixture, filepath.FromSlash(r.URL.Path))); err != nil {
						t.Logf("missing DASH fragment: %s", r.URL.Path)
					}
				}
				if r.URL.Path == "/watch" {
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, `<html><head><title>ResourceLens flow test</title></head><body><video src="/clip.mp4" controls></video></body></html>`)
					return
				}
				http.FileServer(http.Dir(fixture)).ServeHTTP(w, r)
			}))
			defer s.Close()
			page := s.URL + "/watch"
			if dash {
				page = s.URL + "/manifest.mpd"
			}
			r, err := e.addURL(page)
			if err != nil {
				t.Fatal(err)
			}
			if r.Kind != "page" || r.MIME != "video/mp4" || !strings.HasSuffix(r.Name, ".mp4") {
				t.Fatalf("not resolved: %+v", r)
			}
			for attempt := 0; attempt < 2; attempt++ {
				id, err := e.startDownload(r.ID)
				if err != nil {
					t.Fatal(err)
				}
				j := waitJob(t, e, id)
				if j.Status != "completed" {
					t.Fatalf("pipeline failed: %s", j.Error)
				}
				if j.Percent != 100 || j.done.Load() != j.Total {
					t.Fatal("completion progress mismatch")
				}
				dir, _ := e.videoTools()
				if err = validateMP4(context.Background(), dir, j.File); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(filepath.Join(dir, "ffprobe.exe"), "-v", "error", "-show_entries", "stream=codec_type", "-of", "json", j.File)
				var out bytes.Buffer
				cmd.Stdout = &out
				if err = runManagedCommand(context.Background(), cmd); err != nil {
					t.Fatal(err)
				}
				var streams struct {
					Streams []struct {
						Type string `json:"codec_type"`
					} `json:"streams"`
				}
				json.Unmarshal(out.Bytes(), &streams)
				video, audio := false, false
				for _, stream := range streams.Streams {
					video = video || stream.Type == "video"
					audio = audio || stream.Type == "audio"
				}
				if !video || !audio {
					t.Fatalf("merged MP4 missing stream: %s", out.String())
				}
			}
			files, _ := os.ReadDir(e.config.DownloadDir)
			if len(files) != 2 {
				t.Fatalf("unexpected files or staging leak: %v", files)
			}
		})
	}
}
func TestManagedCancellationStopsChildProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestManagedProcessHelper$")
	cmd.Env = append(os.Environ(), "RESOURCE_LENS_TEST_CHILD=1", "RESOURCE_LENS_TEST_CHILD_PID_FILE="+pidFile)
	finished := make(chan error, 1)
	go func() { finished <- runManagedCommand(ctx, cmd) }()
	var child windows.Handle
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		select {
		case err := <-finished:
			t.Fatalf("parent exited before starting child: %v", err)
		default:
		}
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			child, err = windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("child did not start")
	}
	defer windows.CloseHandle(child)
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("managed command did not stop")
	}
	if status, err := windows.WaitForSingleObject(child, 3000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("child survived: %v %v", status, err)
	}
}
func TestManagedProcessHelper(t *testing.T) {
	if os.Getenv("RESOURCE_LENS_TEST_CHILD") != "1" {
		return
	}
	child := exec.Command("ping.exe", "127.0.0.1", "-n", "60")
	hideCommand(child)
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("RESOURCE_LENS_TEST_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		child.Process.Kill()
		os.Exit(3)
	}
	child.Wait()
	os.Exit(0)
}
func TestWeChatPreviewWaitsForCaptureAndCannotDownloadHTML(t *testing.T) {
	e := testEngine(t, 1)
	r, err := e.addURL("https://weixin.qq.com/sph/AKhlBHhT4R")
	if err != nil || r == nil || !r.CaptureRequired || r.Kind != "wechat-page" {
		t.Fatal("WeChat preview did not become a capture placeholder")
	}
	if _, err = e.startDownload(r.ID); err == nil || len(e.jobs) != 0 {
		t.Fatal("WeChat preview created an HTML download")
	}
}

func TestDirectMP4ValidatedAndFakeBinaryRejected(t *testing.T) {
	e := testEngine(t, 1)
	fixture := fixtureVideo(t, e, false)
	payload, err := os.ReadFile(filepath.Join(fixture, "clip.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		b := payload
		if r.URL.Path == "/fake.mp4" {
			b = []byte(`{"error":"unauthorized"}`)
		}
		http.ServeContent(w, r, "clip.mp4", time.Now(), bytes.NewReader(b))
	}))
	defer s.Close()
	for _, name := range []string{"clip.mp4", "fake.mp4"} {
		r, err := e.addURL(s.URL + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := e.startDownload(r.ID)
		j := waitJob(t, e, id)
		if name == "fake.mp4" {
			if j.Status != "failed" || !strings.Contains(j.Error, "MP4 校验失败") {
				t.Fatalf("fake binary completed: %s %s", j.Status, j.Error)
			}
			if _, err = os.Stat(j.File); !os.IsNotExist(err) {
				t.Fatal("fake MP4 was committed")
			}
		} else {
			if j.Status != "completed" {
				t.Fatal(j.Error)
			}
			actual, _ := os.ReadFile(j.File)
			if sha256.Sum256(actual) != sha256.Sum256(payload) {
				t.Fatal("direct MP4 content changed")
			}
		}
	}
}
