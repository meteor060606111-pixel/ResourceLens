package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestHEVCPreviewCachePreservesOriginalAndPlaysVideo(t *testing.T) {
	root := t.TempDir()
	e := &Engine{root: root, ctx: context.Background(), jobs: map[string]*Job{}, config: Config{DownloadDir: filepath.Join(root, "downloads")}, mediaTickets: map[string]mediaTicket{}}
	tools, err := e.videoTools()
	if err != nil {
		t.Fatal(err)
	}
	avc := filepath.Join(fixtureVideo(t, e, false), "clip.mp4")
	direct, err := e.prepareLocalPreview(avc, "video/mp4")
	if err != nil || direct.File != avc || direct.Compatible {
		t.Fatalf("compatible original must play directly: %v", err)
	}
	hevc := filepath.Join(root, "original-hevc.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.Command(toolPath(tools, "ffmpeg"), "-hide_banner", "-loglevel", "error", "-i", avc, "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "pools=1:frame-threads=1:log-level=error", "-c:a", "copy", hevc)
	if err := runManagedCommand(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(hevc)
	preview, err := e.prepareLocalPreview(hevc, "video/mp4")
	if err != nil || !preview.Compatible || preview.File == hevc || !pathInside(filepath.Join(root, "data", "preview-cache"), preview.File) {
		t.Fatalf("HEVC did not produce an isolated preview: %+v %v", preview, err)
	}
	probe, err := probePreview(ctx, tools, preview.File)
	if err != nil {
		t.Fatal(err)
	}
	video, audio := false, false
	for _, stream := range probe.Streams {
		video = video || stream.Type == "video" && stream.Codec == "h264" && stream.Pixels == "yuv420p" && stream.Width > 0 && stream.Height > 0
		audio = audio || stream.Type == "audio" && stream.Codec == "aac"
	}
	if !video || !audio {
		t.Fatal("preview lost video or audio")
	}
	decode := exec.Command(toolPath(tools, "ffmpeg"), "-v", "error", "-xerror", "-i", preview.File, "-f", "null", "-")
	if err := runManagedCommand(ctx, decode); err != nil {
		t.Fatal("preview did not fully decode", err)
	}
	cacheBefore, _ := os.ReadFile(preview.File)
	again, err := e.prepareLocalPreview(hevc, "video/mp4")
	cacheAfter, _ := os.ReadFile(preview.File)
	if err != nil || again.File != preview.File || !bytes.Equal(cacheBefore, cacheAfter) {
		t.Fatal("preview cache was not reused", err)
	}
	after, _ := os.ReadFile(hevc)
	if !bytes.Equal(before, after) {
		t.Fatal("preview modified the downloaded original")
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(preview.File, old, old)
	e.mediaTickets["playing"] = mediaTicket{File: preview.File, Expires: time.Now().Add(time.Minute)}
	if _, err := e.cleanCache(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(preview.File); err != nil {
		t.Fatal("cache cleanup removed an active preview")
	}
	delete(e.mediaTickets, "playing")
	if _, err := e.cleanCache(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(preview.File); !os.IsNotExist(err) {
		t.Fatal("expired preview cache was not cleaned")
	}
	after, _ = os.ReadFile(hevc)
	if !bytes.Equal(before, after) {
		t.Fatal("cache cleanup changed the original")
	}
}
