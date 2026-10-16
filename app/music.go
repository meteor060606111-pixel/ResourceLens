package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed helpers/musicdl_bridge.py
var musicBridgeScript string

type MusicSource struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Playlist bool   `json:"playlist"`
}
type MusicStatus struct {
	Ready   bool          `json:"ready"`
	Version string        `json:"version,omitempty"`
	Sources []MusicSource `json:"sources"`
	Message string        `json:"message,omitempty"`
}
type MusicInfo struct {
	Source     string `json:"source"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album,omitempty"`
	Lyric      string `json:"lyric,omitempty"`
}
type musicTrack struct {
	MusicInfo
	SourceLabel string            `json:"sourceLabel"`
	Ext         string            `json:"ext"`
	URL         string            `json:"url"`
	Size        int64             `json:"size"`
	Duration    float64           `json:"duration"`
	Bitrate     float64           `json:"bitrate"`
	Cover       string            `json:"cover"`
	Headers     map[string]string `json:"headers"`
}
type musicSourceResult struct {
	Source string `json:"source"`
	Label  string `json:"label"`
	Count  int    `json:"count"`
	Note   string `json:"note,omitempty"`
}
type musicBridgeRequest struct {
	Action  string   `json:"action"`
	Keyword string   `json:"keyword,omitempty"`
	Sources []string `json:"sources,omitempty"`
	WorkDir string   `json:"workDir,omitempty"`
	URL     string   `json:"url,omitempty"`
}
type musicBridgeResponse struct {
	MusicStatus
	OK      bool                `json:"ok"`
	Error   string              `json:"error,omitempty"`
	Tracks  []musicTrack        `json:"tracks"`
	Results []musicSourceResult `json:"results"`
}
type MusicSearchResult struct {
	Tracks  []Resource          `json:"tracks"`
	Results []musicSourceResult `json:"results"`
}

func (e *Engine) musicPython() string {
	if configured := os.Getenv("RESOURCELENS_MUSIC_PYTHON"); configured != "" {
		if executableFile(configured) {
			return configured
		}
		return ""
	}
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	for _, candidate := range musicPythonCandidates(exe, e.root, cwd, runtime.GOOS) {
		if executableFile(candidate) {
			return candidate
		}
	}
	return ""
}

// Resolve packaged interpreters independently of the shell working directory.
// A macOS bundle keeps executable tools under Contents/Resources.
func musicPythonCandidates(executable, root, cwd, platform string) []string {
	bases := []string{}
	if platform == "darwin" {
		bases = append(bases, filepath.Join(filepath.Dir(executable), "..", "Resources"))
	}
	bases = append(bases, filepath.Dir(executable), root, cwd)
	paths := []string{}
	for _, base := range bases {
		engine := filepath.Join(base, "music-engine")
		if platform == "windows" {
			paths = append(paths, filepath.Join(engine, "runtime", "python.exe"), filepath.Join(engine, "venv", "Scripts", "python.exe"))
		} else {
			paths = append(paths, filepath.Join(engine, "runtime", "bin", "python3"), filepath.Join(engine, "runtime", "python"), filepath.Join(engine, "venv", "bin", "python3"))
		}
	}
	return paths
}

func (e *Engine) callMusicBridge(ctx context.Context, request musicBridgeRequest) (musicBridgeResponse, error) {
	if e.musicRunner != nil {
		return e.musicRunner(ctx, request)
	}
	var response musicBridgeResponse
	python := e.musicPython()
	if python == "" {
		return response, errors.New("音乐引擎尚未准备，请运行应用目录 music-engine 中的准备脚本")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	cmd := exec.CommandContext(ctx, python, "-I", "-B", "-X", "utf8", "-c", musicBridgeScript)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Dir = e.root
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONDONTWRITEBYTECODE=1")
	if tools, err := e.videoTools(); err == nil {
		// musicdl resolves Node with PATH; share the packaged runtime instead of
		// depending on shell tools or keeping a second Node distribution.
		cmd.Env = append(cmd.Env, "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	out := &limitedBuffer{limit: 4 << 20}
	cmd.Stdout = out
	cmd.Stderr = &tailBuffer{limit: 2048}
	if err = runManagedCommand(ctx, cmd); err != nil {
		if ctx.Err() != nil {
			return response, fmt.Errorf("音乐搜索已停止或等待超时，请缩小关键词或切换平台：%w", ctx.Err())
		}
		return response, errors.New("音乐引擎未能启动，请重新准备音乐引擎")
	}
	if err = json.Unmarshal(out.Bytes(), &response); err != nil {
		return response, errors.New("音乐引擎返回了无效结果，请重新准备音乐引擎")
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "音乐引擎未能返回结果"
		}
		return response, errors.New(response.Error)
	}
	return response, nil
}

func (e *Engine) musicStatus(ctx context.Context) MusicStatus {
	e.musicProbeMu.Lock()
	defer e.musicProbeMu.Unlock()
	if time.Now().Before(e.musicProbeUntil) {
		return e.musicProbe
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	response, err := e.callMusicBridge(ctx, musicBridgeRequest{Action: "probe"})
	e.musicProbe = response.MusicStatus
	if err != nil {
		e.musicProbe = MusicStatus{Message: err.Error(), Sources: []MusicSource{}}
	}
	e.musicProbeUntil = time.Now().Add(15 * time.Second)
	return e.musicProbe
}

func (e *Engine) searchMusic(ctx context.Context, keyword string, sources []string) (MusicSearchResult, error) {
	return e.queryMusic(ctx, "search", keyword, "", sources)
}

func (e *Engine) importMusicPlaylist(ctx context.Context, raw string, sources []string) (MusicSearchResult, error) {
	return e.queryMusic(ctx, "playlist", "", strings.TrimSpace(raw), sources)
}

func (e *Engine) queryMusic(ctx context.Context, action, keyword, raw string, sources []string) (MusicSearchResult, error) {
	result := MusicSearchResult{Tracks: []Resource{}, Results: []musicSourceResult{}}
	keyword = strings.TrimSpace(keyword)
	if action == "search" && (keyword == "" || len([]rune(keyword)) > 120) {
		return result, errors.New("请输入 1–120 字的歌名或歌手")
	}
	if len(sources) < 1 || len(sources) > 4 {
		return result, errors.New("每次请选择 1–4 个音乐平台")
	}
	if action == "playlist" && (len(sources) != 1 || len(raw) > 4096 || !publicMediaURL(raw)) {
		return result, errors.New("请选择一个平台并输入公开的 HTTP/HTTPS 歌单链接")
	}
	if !e.musicSearchMu.TryLock() {
		return result, errors.New("已有音乐搜索正在进行，请等待完成后重试")
	}
	defer e.musicSearchMu.Unlock()
	status := e.musicStatus(ctx)
	if !status.Ready {
		return result, errors.New(status.Message)
	}
	allowed := map[string]bool{}
	for _, source := range status.Sources {
		allowed[source.ID] = action != "playlist" || source.Playlist
	}
	for _, source := range sources {
		if !allowed[source] {
			return result, errors.New("音乐平台无效，请重新选择")
		}
	}
	dir, err := os.MkdirTemp(filepath.Join(e.root, "data"), "music-search-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	timeout := 150 * time.Second
	if action == "playlist" {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	response, err := e.callMusicBridge(ctx, musicBridgeRequest{Action: action, Keyword: keyword, URL: raw, Sources: sources, WorkDir: dir})
	if err != nil {
		return result, err
	}
	result.Results = response.Results
	seen := map[string]bool{}
	e.mu.Lock()
	for _, track := range response.Tracks {
		if len(result.Tracks) >= 100 {
			break
		}
		resource, err := musicResource(track)
		if err != nil {
			continue
		}
		if seen[resource.ID] {
			continue
		}
		seen[resource.ID] = true
		if e.resources[resource.ID] == nil && len(e.resources) >= 5000 {
			break
		}
		if old := e.resources[resource.ID]; old != nil {
			resource.CapturedAt = old.CapturedAt
			resource.NameOverride = old.NameOverride
			if old.NameOverride != "" {
				resource.Name = old.Name
			}
		}
		e.resources[resource.ID] = &resource
		result.Tracks = append(result.Tracks, resource)
	}
	e.mu.Unlock()
	e.saveSession()
	return result, nil
}

func musicResource(track musicTrack) (Resource, error) {
	var result Resource
	u, err := url.Parse(track.URL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(track.URL, "\r\n") {
		return result, errors.New("音频地址无效")
	}
	ext := strings.ToLower(strings.TrimPrefix(track.Ext, "."))
	validExt := map[string]bool{"mp3": true, "flac": true, "m4a": true, "aac": true, "wav": true, "ogg": true, "opus": true, "alac": true, "ape": true, "aiff": true, "wma": true, "mka": true, "webm": true, "mp4": true}
	if !validExt[ext] || strings.TrimSpace(track.Title) == "" {
		return result, errors.New("音频格式无效")
	}
	identity := track.Identifier
	if identity == "" {
		identity = track.Title + "\x00" + track.Artist + "\x00" + track.Album
	}
	digest := sha256.Sum256([]byte("music\x00" + track.Source + "\x00" + identity + "\x00" + ext))
	headers := http.Header{}
	for key, value := range track.Headers {
		if !strings.ContainsAny(key+value, "\r\n") {
			headers.Set(key, value)
		}
	}
	mt := mime.TypeByExtension("." + ext)
	if !strings.HasPrefix(mt, "audio/") {
		mt = "audio/" + ext
	}
	if ext == "mp3" {
		mt = "audio/mpeg"
	}
	if ext == "m4a" || ext == "mp4" {
		mt = "audio/mp4"
	}
	name := track.Title
	if track.Artist != "" {
		name += " - " + track.Artist
	}
	info := track.MusicInfo
	result = Resource{ID: hex.EncodeToString(digest[:12]), URL: track.URL, Name: safeFilename(name + "." + ext), Host: u.Hostname(), Type: "audio", MIME: mt, Size: track.Size, Kind: "music", Manual: true, Site: track.SourceLabel, Author: track.Artist, Duration: track.Duration, Bitrate: track.Bitrate, Thumbnail: track.Cover, Quality: strings.ToUpper(ext), CapturedAt: time.Now(), Headers: map[string]string{}, privateHeaders: headers, Music: &info}
	return result, nil
}

func writeMusicLyrics(file string, info *MusicInfo) error {
	if info == nil || info.Lyric == "" || info.Lyric == "NULL" {
		return nil
	}
	file = strings.TrimSuffix(file, filepath.Ext(file)) + ".lrc"
	if _, err := os.Stat(file); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".lyrics-*.tmp")
	if err != nil {
		return err
	}
	part := f.Name()
	defer os.Remove(part)
	text := strings.TrimSpace(strings.ReplaceAll(info.Lyric, "\r\n", "\n")) + "\n"
	if _, err = f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = commitDownload(part, file); err != nil {
		if _, exists := os.Stat(file); exists == nil {
			return nil
		}
		return err
	}
	return nil
}
