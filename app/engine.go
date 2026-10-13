package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	DownloadDir     string `json:"downloadDir"`
	ProxyPort       int    `json:"proxyPort"`
	Segments        int    `json:"segments"`
	Concurrent      int    `json:"concurrent"`
	HTTPS           bool   `json:"https"`
	HostFilter      string `json:"hostFilter"`
	ClassifyBy      string `json:"classifyBy"`
	NameTemplate    string `json:"nameTemplate"`
	DuplicatePolicy string `json:"duplicatePolicy"`
	UpdateURL       string `json:"updateUrl"`
	OnboardingDone  bool   `json:"onboardingDone"`
}
type Resource struct {
	ID                    string            `json:"id"`
	URL                   string            `json:"url"`
	Name                  string            `json:"name"`
	NameOverride          string            `json:"nameOverride,omitempty"`
	Host                  string            `json:"host"`
	Type                  string            `json:"type"`
	MIME                  string            `json:"mime"`
	Size                  int64             `json:"size"`
	Kind                  string            `json:"kind"`
	Manual                bool              `json:"manual,omitempty"`
	Site                  string            `json:"site,omitempty"`
	Duration              float64           `json:"duration,omitempty"`
	Thumbnail             string            `json:"thumbnail,omitempty"`
	Author                string            `json:"author,omitempty"`
	GroupID               string            `json:"groupId,omitempty"`
	Formats               []MediaFormat     `json:"formats,omitempty"`
	FPS                   float64           `json:"fps,omitempty"`
	Bitrate               float64           `json:"bitrate,omitempty"`
	SelectedFormat        string            `json:"-"`
	WechatVideo           bool              `json:"wechatVideo,omitempty"`
	WechatSourceSize      int64             `json:"wechatSourceSize,omitempty"`
	WechatPlaybackURL     string            `json:"wechatPlaybackUrl,omitempty"`
	WechatFormats         []WechatFormat    `json:"wechatFormats,omitempty"`
	WechatResolvedQuality string            `json:"wechatResolvedQuality,omitempty"`
	Quality               string            `json:"quality,omitempty"`
	Width                 int               `json:"width,omitempty"`
	Height                int               `json:"height,omitempty"`
	CaptureRequired       bool              `json:"captureRequired,omitempty"`
	CapturedAt            time.Time         `json:"capturedAt"`
	Headers               map[string]string `json:"headers"`
	privateHeaders        http.Header
	wechatSeed            string
}
type Job struct {
	ID               string    `json:"id"`
	ResourceID       string    `json:"resourceId"`
	Name             string    `json:"name"`
	URL              string    `json:"url"`
	Status           string    `json:"status"`
	Total            int64     `json:"total"`
	File             string    `json:"file"`
	Error            string    `json:"error"`
	CreatedAt        time.Time `json:"createdAt"`
	Percent          float64   `json:"percent"`
	Notice           string    `json:"notice,omitempty"`
	ResumeAvailable  bool      `json:"resumeAvailable"`
	Quality          string    `json:"quality,omitempty"`
	RequestedQuality string    `json:"requestedQuality,omitempty"`
	OutputDir        string    `json:"outputDir,omitempty"`
	pauseRequested   bool
	done             atomic.Int64
	cancel           context.CancelFunc
	started          time.Time
	speedSampleAt    time.Time
	speedSampleBytes int64
	speed            float64
}
type Engine struct {
	mu                     sync.RWMutex
	root, token, baseURL   string
	config                 Config
	resources              map[string]*Resource
	jobs                   map[string]*Job
	capture                bool
	ca                     *Authority
	transport              *http.Transport
	apiServer, proxyServer *http.Server
	proxyListener          net.Listener
	connsMu                sync.Mutex
	conns                  map[net.Conn]bool
	ctx                    context.Context
	cancel                 context.CancelFunc
	closeOnce              sync.Once
	workers                sync.WaitGroup
	sessionMu              sync.Mutex
	downloadSem            chan struct{}
	proxySnapshot          *ProxySnapshot
	certInstalled          atomic.Bool
	jobSequence            atomic.Uint64
	wechatRequests         atomic.Uint64
	wechatScriptPatches    atomic.Uint64
	wechatMetadataCount    atomic.Uint64
	wechatTLSFailures      atomic.Uint64
	wechatHTMLPatches      atomic.Uint64
	wechatGetterPatches    atomic.Uint64
	wechatDetailPatches    atomic.Uint64
	wechatResponses        map[string]wechatResponseSummary
	wechatBridgeKey        string
	wechatCaptureSequence  atomic.Uint64
	readProxy              func() (ProxySnapshot, error)
	writeProxy             func(ProxySnapshot) error
	mediaTickets           map[string]mediaTicket
	previewMu              sync.Mutex
	batches                map[string]*ParseBatch
	update                 UpdateInfo
	douyinSources          map[string]cachedDouyinSource
}

func NewEngine(root string, ui fs.FS, port int) (*Engine, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Join(root, "data"), 0700); err != nil {
		return nil, err
	}
	// macOS aliases /var and /tmp to /private. Normalize user-selected base
	// directories once so owned-cache checks and yt-dlp's real paths agree.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	cfg := Config{DownloadDir: defaultDownloadDirectory(root), ProxyPort: 18999, Segments: 8, Concurrent: 3, HTTPS: true, ClassifyBy: "none", NameTemplate: "{title}", DuplicatePolicy: "ask"}
	if data, err := os.ReadFile(filepath.Join(root, "data", "config.json")); err == nil {
		if err = json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("读取配置失败：%w", err)
		}
	}
	if err = validateConfig(cfg); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(cfg.DownloadDir, 0755); err != nil {
		return nil, err
	}
	cfg.DownloadDir, err = filepath.EvalSymlinks(cfg.DownloadDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		cancel()
		return nil, err
	}
	e := &Engine{root: root, config: cfg, token: hex.EncodeToString(token), resources: map[string]*Resource{}, jobs: map[string]*Job{}, ctx: ctx, cancel: cancel, conns: map[net.Conn]bool{}, downloadSem: make(chan struct{}, cfg.Concurrent), transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: false, DisableCompression: true, MaxIdleConns: 100, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext}}
	e.readProxy = proxyState
	e.writeProxy = applyProxy
	if e.wechatBridgeKey, err = loadWechatBridgeKey(filepath.Join(root, "data")); err != nil {
		cancel()
		return nil, err
	}
	if err = e.recoverProxy(); err != nil {
		cancel()
		return nil, err
	}
	e.ca, err = LoadAuthority(filepath.Join(root, "data"))
	if err != nil {
		cancel()
		return nil, err
	}
	e.loadSession()
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.ProxyPort))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("代理端口 %d 已被占用，请关闭其他拾流实例：%w", cfg.ProxyPort, err)
	}
	e.proxyListener = listener
	e.proxyServer = &http.Server{Handler: http.HandlerFunc(e.serveProxy), ReadHeaderTimeout: 15 * time.Second}
	go e.proxyServer.Serve(listener)
	apiListener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		listener.Close()
		cancel()
		return nil, err
	}
	e.baseURL = "http://" + apiListener.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", e.serveAPI)
	mux.HandleFunc("/media/", e.serveMedia)
	mux.Handle("/", http.FileServer(http.FS(ui)))
	e.apiServer = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go e.apiServer.Serve(apiListener)
	e.certInstalled.Store(e.isCertificateInstalled())
	e.workers.Add(1)
	go func() {
		defer e.workers.Done()
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-tick.C:
				e.saveSession()
			}
		}
	}()
	return e, nil
}
func validateConfig(c Config) error {
	if !filepath.IsAbs(c.DownloadDir) || c.Segments < 1 || c.Segments > 16 || c.Concurrent < 1 || c.Concurrent > 6 || c.ProxyPort < 1024 || c.ProxyPort > 65535 {
		return errors.New("设置无效：目录须为绝对路径，分段 1–16，并行任务 1–6，端口 1024–65535")
	}
	if c.ClassifyBy != "" && c.ClassifyBy != "none" && c.ClassifyBy != "platform" && c.ClassifyBy != "author" {
		return errors.New("分类方式无效")
	}
	if c.DuplicatePolicy != "" && c.DuplicatePolicy != "ask" && c.DuplicatePolicy != "skip" && c.DuplicatePolicy != "allow" {
		return errors.New("重复下载选项无效")
	}
	if len(c.NameTemplate) > 240 {
		return errors.New("命名模板过长")
	}
	if c.UpdateURL != "" {
		if err := validateUpdateURL(c.UpdateURL); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) configSnapshot() Config { e.mu.RLock(); defer e.mu.RUnlock(); return e.config }
func (e *Engine) bootstrap() map[string]any {
	return map[string]any{"baseURL": e.baseURL, "token": e.token}
}
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		e.cancel()
		e.mu.Lock()
		e.capture = false
		err := e.restoreProxyLocked()
		e.mu.Unlock()
		if err != nil {
			fmt.Fprintf(os.Stderr, "恢复代理失败：%v\n", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if e.apiServer != nil {
			e.apiServer.Shutdown(ctx)
		}
		if e.proxyServer != nil {
			e.proxyServer.Close()
		}
		e.connsMu.Lock()
		for c := range e.conns {
			c.Close()
		}
		e.connsMu.Unlock()
		e.transport.CloseIdleConnections()
		e.workers.Wait()
		e.saveSession()
	})
}
func (e *Engine) state() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	rs := make([]Resource, 0, len(e.resources))
	for _, r := range e.resources {
		rs = append(rs, *r)
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].CapturedAt.After(rs[j].CapturedAt) })
	js := make([]map[string]any, 0, len(e.jobs))
	for _, j := range e.jobs {
		done := j.done.Load()
		speed := float64(0)
		if j.Status == "downloading" {
			now := time.Now()
			if j.speedSampleAt.IsZero() {
				j.speedSampleAt, j.speedSampleBytes = now, done
			} else if elapsed := now.Sub(j.speedSampleAt).Seconds(); elapsed >= 1 {
				j.speed = max(0, float64(done-j.speedSampleBytes)/elapsed)
				j.speedSampleAt, j.speedSampleBytes = now, done
			}
			speed = j.speed
		} else {
			j.speed, j.speedSampleBytes, j.speedSampleAt = 0, done, time.Now()
		}
		eta := float64(0)
		if speed > 0 && j.Total > done {
			eta = float64(j.Total-done) / speed
		}
		js = append(js, map[string]any{"id": j.ID, "resourceId": j.ResourceID, "name": j.Name, "url": j.URL, "status": j.Status, "total": j.Total, "done": done, "speed": speed, "eta": eta, "file": j.File, "error": j.Error, "createdAt": j.CreatedAt, "percent": j.Percent, "notice": j.Notice, "resumeAvailable": j.ResumeAvailable, "quality": j.Quality, "requestedQuality": j.RequestedQuality})
	}
	sort.Slice(js, func(i, j int) bool { return js[i]["createdAt"].(time.Time).After(js[j]["createdAt"].(time.Time)) })
	bs := make([]ParseBatch, 0, len(e.batches))
	for _, b := range e.batches {
		copy := *b
		copy.Items = append([]ParseItem(nil), b.Items...)
		bs = append(bs, copy)
	}
	return map[string]any{"resources": rs, "jobs": js, "batches": bs, "update": e.update, "config": e.config, "capture": e.capture, "certificateInstalled": e.certInstalled.Load(), "certificatePath": e.ca.certPath, "proxyAddress": fmt.Sprintf("127.0.0.1:%d", e.config.ProxyPort), "version": appVersion, "platform": runtime.GOOS, "arch": runtime.GOARCH}
}
func (e *Engine) allowedOrigin(origin string) bool {
	return origin == "" || origin == e.baseURL || origin == "http://wails.localhost" || origin == "https://wails.localhost" || runtime.GOOS == "darwin" && origin == "wails://wails"
}
func (e *Engine) serveAPI(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !e.allowedOrigin(origin) {
		http.Error(w, "Origin rejected", http.StatusForbidden)
		return
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/api/bootstrap" && r.Method == "GET" {
		e.writeJSON(w, e.bootstrap(), nil)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+e.token {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/state" {
		e.writeJSON(w, e.state(), nil)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/wechat-status" {
		e.writeJSON(w, e.wechatStatus(), nil)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var in struct {
		URL      string   `json:"url"`
		ID       string   `json:"id"`
		IDs      []string `json:"ids"`
		Enabled  bool     `json:"enabled"`
		Quality  string   `json:"quality"`
		URLs     []string `json:"urls"`
		Template string   `json:"template"`
		Force    bool     `json:"force"`
		Path     string   `json:"path"`
		Mode     string   `json:"mode"`
		Config   Config   `json:"config"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		e.writeJSON(w, nil, errors.New("请求格式无效"))
		return
	}
	var result any
	var err error
	switch r.URL.Path {
	case "/api/capture":
		e.mu.Lock()
		if in.Enabled {
			if e.config.HTTPS && !e.certInstalled.Load() {
				err = errors.New("请先安装 HTTPS 证书，或在设置中关闭 HTTPS 捕获")
			} else if e.proxySnapshot == nil {
				err = e.enableSystemProxyLocked()
			}
			if err == nil {
				if !e.capture {
					e.wechatCaptureSequence.Add(1)
					e.wechatRequests.Store(0)
					e.wechatScriptPatches.Store(0)
					e.wechatMetadataCount.Store(0)
					e.wechatTLSFailures.Store(0)
					e.wechatHTMLPatches.Store(0)
					e.wechatGetterPatches.Store(0)
					e.wechatDetailPatches.Store(0)
					e.wechatResponses = nil
				}
				e.capture = true
			}
		} else {
			e.capture = false
			err = e.restoreProxyLocked()
		}
		e.mu.Unlock()
	case "/api/add":
		result, err = e.addURL(in.URL)
	case "/api/batch-add":
		result, err = e.startParseBatch(in.URLs)
	case "/api/batch-cancel":
		e.mu.Lock()
		if b := e.batches[in.ID]; b != nil && b.cancel != nil {
			b.cancel()
		}
		e.mu.Unlock()
	case "/api/formats":
		result, err = e.resourceFormats(in.ID)
	case "/api/preview":
		result, err = e.createMediaTicket(in.ID, in.Mode)
	case "/api/download":
		result, err = e.startDownloadOptions(in.ID, in.Quality, in.Force)
	case "/api/pause":
		err = e.pauseJob(in.ID)
	case "/api/resume":
		result, err = e.resumeJob(in.ID)
	case "/api/cancel":
		err = e.cancelJob(in.ID)
	case "/api/clear":
		e.mu.Lock()
		e.resources = map[string]*Resource{}
		e.mu.Unlock()
	case "/api/delete":
		e.mu.Lock()
		for _, id := range in.IDs {
			delete(e.resources, id)
		}
		e.mu.Unlock()
	case "/api/rename":
		result, err = e.renameItems(in.IDs, in.Template, in.Mode)
	case "/api/clear-jobs":
		err = e.clearJobHistory(in.IDs)
	case "/api/cache":
		result, err = e.cleanCache(in.Enabled)
	case "/api/diagnostics":
		result, err = e.exportDiagnostics()
	case "/api/self-check":
		result = e.selfCheck()
	case "/api/onboarding":
		e.mu.Lock()
		e.config.OnboardingDone = true
		c := e.config
		data, _ := json.MarshalIndent(c, "", "  ")
		err = atomicWrite(filepath.Join(e.root, "data", "config.json"), data, 0600)
		e.mu.Unlock()
	case "/api/update-check":
		result, err = e.checkUpdate()
	case "/api/update-local":
		result, err = e.prepareLocalUpdate(in.Path)
	case "/api/update-download":
		result, err = e.downloadUpdate()
	case "/api/update-install":
		err = e.installPreparedUpdate()
	case "/api/update-rollback":
		err = e.rollbackUpdate()
	case "/api/config":
		err = validateConfig(in.Config)
		if err == nil {
			e.mu.Lock()
			if in.Config.ProxyPort != e.config.ProxyPort {
				err = errors.New("当前版本的代理端口固定，不能在运行中修改")
			} else {
				for _, j := range e.jobs {
					if activeStatus(j.Status) {
						err = errors.New("请等待下载完成或取消任务后再保存设置")
						break
					}
				}
			}
			if err == nil {
				err = os.MkdirAll(in.Config.DownloadDir, 0755)
			}
			if err == nil {
				in.Config.DownloadDir, err = filepath.EvalSymlinks(in.Config.DownloadDir)
			}
			if err == nil {
				data, _ := json.MarshalIndent(in.Config, "", "  ")
				err = atomicWrite(filepath.Join(e.root, "data", "config.json"), data, 0600)
			}
			if err == nil {
				e.config = in.Config
				e.downloadSem = make(chan struct{}, in.Config.Concurrent)
			}
			e.mu.Unlock()
		}
	case "/api/install-certificate":
		err = e.installCertificate()
		if err == nil {
			e.certInstalled.Store(true)
		}
	case "/api/remove-certificate":
		e.mu.RLock()
		active := e.capture
		e.mu.RUnlock()
		if active {
			err = errors.New("请先停止捕获再移除证书")
		} else {
			err = e.removeCertificate()
			if err == nil {
				e.certInstalled.Store(false)
			}
		}
	case "/api/open-folder":
		location := e.configSnapshot().DownloadDir
		selectFile := in.ID != ""
		if selectFile {
			e.mu.RLock()
			job := e.jobs[in.ID]
			if job == nil || job.Status != "completed" || job.File == "" {
				err = errors.New("该任务没有已完成的下载文件")
			} else {
				location = job.File
			}
			e.mu.RUnlock()
		}
		if err == nil {
			err = openInExplorer(location, selectFile)
		}
	default:
		http.Error(w, "Not found", 404)
		return
	}
	if err == nil || r.URL.Path == "/api/rename" {
		e.saveSession()
	}
	e.writeJSON(w, result, err)
}
func (e *Engine) writeJSON(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
}
func atomicWrite(file string, b []byte, mode os.FileMode) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
func resourceType(mt, raw string) string {
	mt = strings.ToLower(strings.Split(mt, ";")[0])
	switch {
	case strings.Contains(mt, "mpegurl"), mt == "application/dash+xml":
		return "playlist"
	case strings.HasPrefix(mt, "video/"):
		return "video"
	case strings.HasPrefix(mt, "audio/"):
		return "audio"
	case strings.HasPrefix(mt, "image/"):
		return "image"
	}
	u, _ := url.Parse(raw)
	if u == nil {
		return "other"
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".mp4", ".webm", ".mov", ".mkv":
		return "video"
	case ".mp3", ".aac", ".wav", ".m4a", ".ogg":
		return "audio"
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".svg", ".avif":
		return "image"
	case ".m3u8", ".mpd":
		return "playlist"
	case ".pdf", ".zip", ".rar", ".7z":
		return "file"
	}
	return "other"
}
func safeFilename(name string) string {
	name = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`).ReplaceAllString(name, "_")
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	if name == "" {
		name = "resource"
	}
	stem := strings.ToUpper(strings.Split(name, ".")[0])
	if regexp.MustCompile(`^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$`).MatchString(stem) {
		name = "_" + name
	}
	r := []rune(name)
	if len(r) > 160 {
		ext := filepath.Ext(name)
		if len([]rune(ext)) > 20 {
			ext = ""
		}
		name = string(r[:130]) + ext
	}
	return name
}
func (e *Engine) remember(raw string, h http.Header, mt string, size int64, disposition string, manual bool) *Resource {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	if mediaFragment(raw, mt) || !manual && captureNoise(raw, mt, size) {
		return nil
	}
	cfg := e.configSnapshot()
	if !manual && cfg.HostFilter != "" && !strings.Contains(strings.ToLower(u.Hostname()), strings.ToLower(cfg.HostFilter)) {
		return nil
	}
	typ := resourceType(mt, raw)
	if !manual && typ == "other" {
		return nil
	}
	identity := raw
	if typ == "video" && wechatEncryptedURL(raw) {
		identity = wechatMediaIdentity(raw)
	}
	sum := sha256.Sum256([]byte(identity))
	id := hex.EncodeToString(sum[:12])
	name, _ := url.PathUnescape(path.Base(u.Path))
	if name == "." || name == "/" || name == "" {
		name = "resource"
	}
	if _, p, err := mime.ParseMediaType(disposition); err == nil && p["filename"] != "" {
		name = p["filename"]
	}
	name = safeFilename(name)
	pub := map[string]string{}
	for _, key := range []string{"User-Agent", "Referer", "Origin", "Accept", "Range"} {
		if v := h.Get(key); v != "" {
			pub[key] = v
		}
	}
	resource := &Resource{ID: id, URL: raw, Name: name, Host: u.Host, Type: typ, MIME: mt, Size: size, Kind: "direct", CapturedAt: time.Now(), Headers: pub, privateHeaders: h.Clone(), GroupID: mediaGroup(raw, h.Get("Referer")), Site: platformName("", raw)}
	resource.Manual = manual
	if typ == "video" && wechatEncryptedURL(raw) {
		resource.Site, resource.Kind, resource.CaptureRequired = "微信视频号", "wechat-media", true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	prev := e.resources[id]
	if prev == nil && typ == "video" && wechatEncryptedURL(raw) {
		// Older sessions used the full request URL as their ID. Reuse that row
		// when a newer player URL refers to the same encrypted media.
		for _, candidate := range e.resources {
			if candidate.Type == "video" && wechatMediaURL(candidate.URL) && wechatMediaIdentity(candidate.URL) == identity {
				prev = candidate
				break
			}
		}
	}
	if prev != nil {
		prev.Manual = prev.Manual || manual
		if size > 0 {
			prev.Size = size
		}
		if prev.CaptureRequired && wechatEncryptedURL(raw) {
			prev.URL, prev.privateHeaders = raw, h.Clone()
		}
		return prev
	}
	if len(e.resources) >= 5000 {
		return nil
	}
	e.resources[id] = resource
	return resource
}
func (e *Engine) addURL(raw string) (*Resource, error) {
	raw, err := normalizeVideoInput(raw)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("请输入有效的 http:// 或 https:// 链接")
	}
	if isWeChatPage(u) {
		return e.addWechatPage(raw)
	}
	if mediaFragment(raw, "") {
		return nil, errors.New("这是播放器的分段地址，请添加完整视频页面或 M3U8 / MPD 播放清单")
	}
	if knownVideoPage(u) || resourceType("", raw) == "playlist" {
		return e.resolveVideo(raw, nil)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "HEAD", raw, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 ResourceLens/1.1")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := (&http.Client{Transport: e.transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("探测链接失败：%w", err)
	}
	resp.Body.Close()
	if isWeChatPage(resp.Request.URL) {
		return e.addWechatPage(resp.Request.URL.String())
	}
	if htmlMIME(resp.Header.Get("Content-Type")) || resourceType(resp.Header.Get("Content-Type"), raw) == "playlist" {
		return e.resolveVideo(raw, nil)
	}
	if resp.StatusCode >= 400 && resp.StatusCode != 405 {
		return nil, fmt.Errorf("链接返回 HTTP %d", resp.StatusCode)
	}
	if mediaFragment(raw, resp.Header.Get("Content-Type")) {
		return nil, errors.New("这是播放器的分段地址，请添加完整视频页面或 M3U8 / MPD 播放清单")
	}
	r := e.remember(raw, req.Header, resp.Header.Get("Content-Type"), resp.ContentLength, resp.Header.Get("Content-Disposition"), true)
	if r == nil {
		return nil, errors.New("资源列表已满，请清空后重试")
	}
	return r, nil
}

var _ = io.EOF
