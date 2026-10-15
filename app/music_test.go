package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMusicSearchFeedsExistingDownloadAndKeepsCredentialsPrivate(t *testing.T) {
	e := testEngine(t, 1)
	payload := musicFixture(t, e, "mp3")
	var authorized atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "session=private-music-cookie" {
			w.WriteHeader(403)
			return
		}
		authorized.Add(1)
		w.Header().Set("Content-Type", "audio/mpeg")
		http.ServeContent(w, r, "test.mp3", time.Unix(1700000000, 0), bytes.NewReader(payload))
	}))
	defer server.Close()
	e.musicRunner = func(ctx context.Context, input musicBridgeRequest) (musicBridgeResponse, error) {
		if input.Action == "probe" {
			return musicBridgeResponse{OK: true, MusicStatus: MusicStatus{Ready: true, Version: "fixture", Sources: []MusicSource{{ID: "MiguMusicClient", Label: "咪咕音乐"}}}}, nil
		}
		return musicBridgeResponse{OK: true, Tracks: []musicTrack{{MusicInfo: MusicInfo{Source: "MiguMusicClient", Identifier: "123", Title: "测试歌曲", Artist: "测试作者", Album: "测试专辑", Lyric: "[00:00.00]本地验证歌词"}, SourceLabel: "咪咕音乐", Ext: "mp3", Duration: 180, URL: server.URL + "/test.mp3", Size: int64(len(payload)), Headers: map[string]string{"Cookie": "session=private-music-cookie"}}}}, nil
	}
	result, err := e.searchMusic(context.Background(), "测试", []string{"MiguMusicClient"})
	if err != nil || len(result.Tracks) != 1 {
		t.Fatalf("search: %+v %v", result, err)
	}
	serialized, _ := json.Marshal(result)
	if strings.Contains(string(serialized), "private-music-cookie") {
		t.Fatal("credential exposed to UI")
	}
	session, _ := os.ReadFile(filepath.Join(e.root, "data", "session.json"))
	if strings.Contains(string(session), "private-music-cookie") {
		t.Fatal("credential persisted")
	}
	resource := result.Tracks[0]
	if resource.Type != "audio" || resource.Music.Album != "测试专辑" || !validResourceID(resource.ID) {
		t.Fatalf("metadata lost: %+v", resource)
	}
	id, err := e.startDownloadOptions(resource.ID, "best", false)
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, e, id)
	if job.Status != "completed" {
		t.Fatalf("download: %s %s", job.Status, job.Error)
	}
	if !strings.Contains(job.Notice, "可能是试听片段") {
		t.Fatal("short source warning missing from completed job")
	}
	data, err := os.ReadFile(job.File)
	if err != nil || sha256.Sum256(data) != sha256.Sum256(payload) || authorized.Load() == 0 {
		t.Fatal("download identity or private request headers lost")
	}
	lyrics, err := os.ReadFile(strings.TrimSuffix(job.File, filepath.Ext(job.File)) + ".lrc")
	if err != nil || !strings.Contains(string(lyrics), "本地验证歌词") {
		t.Fatal("lyrics not saved")
	}
	writeMusicLyrics(job.File, &MusicInfo{Lyric: "overwrite"})
	again, _ := os.ReadFile(strings.TrimSuffix(job.File, filepath.Ext(job.File)) + ".lrc")
	if !bytes.Equal(lyrics, again) {
		t.Fatal("existing lyrics overwritten")
	}
}

func musicFixture(t *testing.T, e *Engine, ext string) []byte {
	t.Helper()
	tools, err := e.videoTools()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "fixture."+ext)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "sine=frequency=440:duration=8"}
	if ext == "wma" {
		args = append(args, "-c:a", "wmav2")
	} else {
		args = append(args, "-c:a", "libmp3lame", "-b:a", "192k")
	}
	cmd := exec.Command(toolPath(tools, "ffmpeg"), append(args, file)...)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = runManagedCommand(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMusicPlaylistValidationAndBridgeRequest(t *testing.T) {
	e := testEngine(t, 1)
	var received musicBridgeRequest
	e.musicRunner = func(ctx context.Context, input musicBridgeRequest) (musicBridgeResponse, error) {
		if input.Action == "probe" {
			return musicBridgeResponse{MusicStatus: MusicStatus{Ready: true, Sources: []MusicSource{{ID: "MiguMusicClient", Playlist: true}, {ID: "OtherMusicClient"}}}}, nil
		}
		received = input
		return musicBridgeResponse{Tracks: []musicTrack{{MusicInfo: MusicInfo{Source: "MiguMusicClient", Title: "歌单测试", Identifier: "456"}, Ext: "mp3", URL: "https://example.com/playlist.mp3"}}}, nil
	}
	for _, input := range []struct {
		link    string
		sources []string
	}{
		{"", []string{"MiguMusicClient"}}, {"file:///C:/private", []string{"MiguMusicClient"}},
		{"http://127.0.0.1/private", []string{"MiguMusicClient"}},
		{"https://example.com/playlist", []string{"MiguMusicClient", "OtherMusicClient"}},
		{"https://example.com/playlist", []string{"OtherMusicClient"}},
	} {
		if _, err := e.importMusicPlaylist(context.Background(), input.link, input.sources); err == nil {
			t.Fatalf("invalid playlist accepted: %+v", input)
		}
	}
	if received.Action != "" {
		t.Fatal("invalid playlist reached music engine")
	}
	link := "https://music.migu.cn/v3/music/playlist/123"
	result, err := e.importMusicPlaylist(context.Background(), link, []string{"MiguMusicClient"})
	if err != nil || len(result.Tracks) != 1 || received.Action != "playlist" || received.URL != link || received.WorkDir == "" {
		t.Fatalf("playlist bridge: %+v %v %+v", result, err, received)
	}
	if _, err = os.Stat(received.WorkDir); !os.IsNotExist(err) {
		t.Fatal("playlist temporary data retained")
	}
}

func TestMusicAudioValidationAndCompatiblePreviewPreserveOriginal(t *testing.T) {
	e := testEngine(t, 1)
	tools, err := e.videoTools()
	if err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(t.TempDir(), "invalid.mp3")
	os.WriteFile(invalid, []byte("This is not an audio file."), 0600)
	if _, err = validateMusicAudio(context.Background(), tools, invalid); err == nil {
		t.Fatal("non-audio accepted")
	}
	file := filepath.Join(e.root, "downloads", "fixture.wma")
	os.MkdirAll(filepath.Dir(file), 0700)
	original := musicFixture(t, e, "wma")
	os.WriteFile(file, original, 0600)
	actual, err := validateMusicAudio(context.Background(), tools, file)
	if err != nil || actual < 7 {
		t.Fatalf("valid WMA rejected: %v %.2f", err, actual)
	}
	if musicDurationNotice(180, actual) == "" || musicDurationNotice(8, actual) != "" {
		t.Fatal("preview duration notice is incorrect")
	}
	preview, err := e.prepareLocalPreview(file, "audio/x-ms-wma")
	if err != nil || !preview.Compatible || preview.MIME != "audio/mp4" || preview.File == file {
		t.Fatalf("WMA preview: %+v %v", preview, err)
	}
	probe, err := probeAudio(context.Background(), tools, preview.File)
	if err != nil || probe.Streams[0].Codec != "aac" {
		t.Fatal("compatible preview is not playable AAC")
	}
	data, _ := os.ReadFile(file)
	if sha256.Sum256(data) != sha256.Sum256(original) {
		t.Fatal("original WMA modified")
	}
	// An interrupted cache must be refreshed rather than blocking future playback.
	os.WriteFile(preview.File, []byte("broken cache"), 0600)
	if refreshed, err := e.prepareLocalPreview(file, "audio/x-ms-wma"); err != nil || refreshed.File != preview.File {
		t.Fatalf("cache repair: %+v %v", refreshed, err)
	}
}

func TestMusicDownloadRejectsInvalidAudioBeforePublishing(t *testing.T) {
	e := testEngine(t, 1)
	payload := bytes.Repeat([]byte("not-a-valid-audio-file"), 1000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		http.ServeContent(w, r, "bad.mp3", time.Unix(1700000000, 0), bytes.NewReader(payload))
	}))
	defer server.Close()
	r, err := musicResource(musicTrack{MusicInfo: MusicInfo{Source: "LocalFixture", Identifier: "invalid", Title: "无效音频"}, Ext: "mp3", URL: server.URL + "/bad.mp3", Size: int64(len(payload))})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.resources[r.ID] = &r
	e.mu.Unlock()
	id, err := e.startDownloadOptions(r.ID, "best", false)
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, e, id)
	if job.Status != "failed" || !strings.Contains(job.Error, "音频校验失败") {
		t.Fatalf("bad audio published: %+v", job)
	}
	entries, _ := os.ReadDir(filepath.Join(e.root, "downloads"))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".mp3") {
			t.Fatal("invalid music exposed as finished download")
		}
	}
}

func TestMusicLyricsFailureDoesNotLeavePartialFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "song.mp3")
	os.Mkdir(filepath.Join(dir, "song.lrc"), 0700)
	// An existing item is never overwritten, including a folder with the same name.
	if err := writeMusicLyrics(file, &MusicInfo{Lyric: "[00:00]歌词"}); err != nil {
		t.Fatal(err)
	}
	if err := writeMusicLyrics(filepath.Join(dir, "missing", "song.mp3"), &MusicInfo{Lyric: "text"}); err == nil {
		t.Fatal("lyrics write failure was hidden")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".lyrics-") {
			t.Fatal("lyrics temporary file leaked")
		}
	}
}

func TestMusicSourceValidationAndStableIdentity(t *testing.T) {
	e := testEngine(t, 1)
	searches := 0
	e.musicRunner = func(ctx context.Context, input musicBridgeRequest) (musicBridgeResponse, error) {
		if input.Action == "search" {
			searches++
		}
		return musicBridgeResponse{MusicStatus: MusicStatus{Ready: true, Sources: []MusicSource{{ID: "MiguMusicClient"}}}}, nil
	}
	for _, input := range []struct {
		keyword string
		sources []string
	}{{"", []string{"MiguMusicClient"}}, {"query", nil}, {"query", []string{"InvalidClient"}}} {
		if _, err := e.searchMusic(context.Background(), input.keyword, input.sources); err == nil {
			t.Fatal("invalid search accepted")
		}
	}
	if searches != 0 {
		t.Fatal("unvalidated sources reached engine")
	}
	track := musicTrack{MusicInfo: MusicInfo{Source: "MiguMusicClient", Identifier: "123", Title: "name"}, Ext: "mp3", URL: "https://example.com/music.mp3?token=one"}
	a, err := musicResource(track)
	if err != nil {
		t.Fatal(err)
	}
	track.URL = "https://example.com/music.mp3?token=two"
	b, err := musicResource(track)
	if err != nil || a.ID != b.ID {
		t.Fatal("expiring query changed song identity")
	}
	track.Ext = "../exe"
	if _, err = musicResource(track); err == nil {
		t.Fatal("unsafe extension accepted")
	}
	track.Ext = "mp3"
	track.URL = "file:///C:/private"
	if _, err = musicResource(track); err == nil {
		t.Fatal("non HTTP source accepted")
	}
}
