package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultMobilePort = 19090
const mobileCookie = "resourcelens_phone"

type MobileStatus struct {
	Enabled   bool      `json:"enabled"`
	Addresses []string  `json:"addresses"`
	Code      string    `json:"code,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	Clients   int       `json:"clients"`
	Port      int       `json:"port"`
}
type phoneSession struct{ Expires time.Time }
type pairAttempts struct {
	Count int
	Since time.Time
}
type MobileGateway struct {
	engine      *Engine
	ui          fs.FS
	server      *http.Server
	listener    net.Listener
	client      *http.Client
	mu          sync.Mutex
	code        string
	codeExpires time.Time
	sessions    map[string]phoneSession
	attempts    map[string]pairAttempts
	hosts       map[string]bool
	addresses   []string
	closed      bool
}

func mobileRandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func newPairCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%08d", n.Int64()), nil
}
func mobileAddresses(listener net.Listener) ([]string, map[string]bool) {
	port := listener.Addr().(*net.TCPAddr).Port
	hosts := map[string]bool{"127.0.0.1": true, "localhost": true}
	if listener.Addr().(*net.TCPAddr).IP.IsLoopback() {
		return []string{fmt.Sprintf("http://127.0.0.1:%d/", port)}, hosts
	}
	addresses := []string{}
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		items, _ := iface.Addrs()
		for _, item := range items {
			ip, _, err := net.ParseCIDR(item.String())
			if err != nil || ip.To4() == nil || !ip.IsPrivate() {
				continue
			}
			if hosts[ip.String()] {
				continue
			}
			hosts[ip.String()] = true
			addresses = append(addresses, "http://"+net.JoinHostPort(ip.String(), strconv.Itoa(port))+"/")
		}
	}
	sort.Strings(addresses)
	return addresses, hosts
}
func newMobileGateway(e *Engine, ui fs.FS, listener net.Listener) (*MobileGateway, error) {
	code, err := newPairCode()
	if err != nil {
		listener.Close()
		return nil, err
	}
	addresses, hosts := mobileAddresses(listener)
	m := &MobileGateway{engine: e, ui: ui, listener: listener, code: code, codeExpires: time.Now().Add(5 * time.Minute), sessions: map[string]phoneSession{}, attempts: map[string]pairAttempts{}, addresses: addresses, hosts: hosts, client: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 30 * time.Second}}}
	m.server = &http.Server{Handler: http.HandlerFunc(m.serve), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go m.server.Serve(listener)
	return m, nil
}
func (e *Engine) startMobile() error {
	cfg := e.configSnapshot()
	port := cfg.MobilePort
	if port == 0 {
		port = defaultMobilePort
	}
	e.mobileMu.Lock()
	defer e.mobileMu.Unlock()
	if e.ctx.Err() != nil {
		return errors.New("应用正在退出")
	}
	if e.mobile != nil {
		return nil
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("手机连接端口 %d 无法使用：%w", port, err)
	}
	m, err := newMobileGateway(e, e.ui, listener)
	if err != nil {
		return err
	}
	e.mobile = m
	return nil
}
func (e *Engine) stopMobile() {
	e.mobileMu.Lock()
	m := e.mobile
	e.mobile = nil
	e.mobileMu.Unlock()
	if m != nil {
		m.Close()
	}
}
func (m *MobileGateway) Close() {
	m.mu.Lock()
	m.closed = true
	m.sessions = map[string]phoneSession{}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.server.Shutdown(ctx)
	m.server.Close()
	if t, ok := m.client.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}
func (m *MobileGateway) status() MobileStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for token, s := range m.sessions {
		if now.After(s.Expires) {
			delete(m.sessions, token)
		}
	}
	if now.After(m.codeExpires) {
		if code, err := newPairCode(); err == nil {
			m.code = code
			m.codeExpires = now.Add(5 * time.Minute)
		}
	}
	return MobileStatus{Enabled: !m.closed, Addresses: append([]string{}, m.addresses...), Code: m.code, ExpiresAt: m.codeExpires, Clients: len(m.sessions), Port: m.listener.Addr().(*net.TCPAddr).Port}
}
func (e *Engine) mobileStatus() MobileStatus {
	port := e.configSnapshot().MobilePort
	if port == 0 {
		port = defaultMobilePort
	}
	e.mobileMu.Lock()
	defer e.mobileMu.Unlock()
	if e.mobile == nil {
		return MobileStatus{Addresses: []string{}, Port: port}
	}
	return e.mobile.status()
}
func (e *Engine) rotateMobileCode() (MobileStatus, error) {
	e.mobileMu.Lock()
	defer e.mobileMu.Unlock()
	if e.mobile == nil {
		return MobileStatus{}, errors.New("请先开启手机连接")
	}
	code, err := newPairCode()
	if err != nil {
		return MobileStatus{}, err
	}
	e.mobile.mu.Lock()
	e.mobile.code = code
	e.mobile.codeExpires = time.Now().Add(5 * time.Minute)
	e.mobile.mu.Unlock()
	return e.mobile.status(), nil
}
func (m *MobileGateway) authorized(r *http.Request) bool {
	c, err := r.Cookie(mobileCookie)
	if err != nil || len(c.Value) != 64 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[c.Value]
	return !m.closed && ok && time.Now().Before(s.Expires)
}
func mobileJSON(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
}
func (m *MobileGateway) validRequest(r *http.Request) bool {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != strconv.Itoa(m.listener.Addr().(*net.TCPAddr).Port) || !m.hosts[host] {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		return false
	}
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(remote)
	return err == nil && ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}
func (m *MobileGateway) pair(w http.ResponseWriter, r *http.Request, code string) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	m.mu.Lock()
	now := time.Now()
	for key, a := range m.attempts {
		if now.Sub(a.Since) > 5*time.Minute {
			delete(m.attempts, key)
		}
	}
	// Bound global attempts before inserting a new per-address record.
	for _, key := range []string{"*", ip} {
		a := m.attempts[key]
		if a.Since.IsZero() {
			a.Since = now
		}
		a.Count++
		m.attempts[key] = a
		limit := 8
		if key == "*" {
			limit = 40
		}
		if a.Count > limit {
			m.mu.Unlock()
			w.WriteHeader(429)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "尝试次数过多，请稍后重试"})
			return
		}
	}
	if m.closed || len(code) != 8 || now.After(m.codeExpires) || subtle.ConstantTimeCompare([]byte(code), []byte(m.code)) != 1 {
		m.mu.Unlock()
		mobileJSON(w, nil, errors.New("配对码无效或已过期，请查看电脑上的配对码"))
		return
	}
	for token, s := range m.sessions {
		if now.After(s.Expires) {
			delete(m.sessions, token)
		}
	}
	if old, err := r.Cookie(mobileCookie); err == nil {
		delete(m.sessions, old.Value)
	}
	if len(m.sessions) >= 6 {
		m.mu.Unlock()
		mobileJSON(w, nil, errors.New("连接设备已达上限，请在电脑上停止并重新开启手机连接"))
		return
	}
	token, err := mobileRandomToken()
	if err != nil {
		m.mu.Unlock()
		mobileJSON(w, nil, err)
		return
	}
	m.sessions[token] = phoneSession{Expires: now.Add(8 * time.Hour)}
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: mobileCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
	mobileJSON(w, map[string]any{"paired": true, "version": appVersion}, nil)
}
func (m *MobileGateway) serve(w http.ResponseWriter, r *http.Request) {
	if !m.validRequest(r) {
		http.Error(w, "Connection rejected", 403)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
	if r.Method == "GET" && (r.URL.Path == "/" || r.URL.Path == "/phone.html") {
		data, err := fs.ReadFile(m.ui, "phone.html")
		if err != nil {
			http.Error(w, "手机界面尚未构建", 503)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(data)
		return
	}
	if (r.Method == "GET" || r.Method == "HEAD") && (strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/phone-icon.png" || r.URL.Path == "/phone.webmanifest" || r.URL.Path == "/favicon.svg") {
		http.FileServer(http.FS(m.ui)).ServeHTTP(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/mobile/bootstrap" {
		mobileJSON(w, map[string]any{"version": appVersion, "paired": m.authorized(r)}, nil)
		return
	}
	var in struct {
		Code    string   `json:"code"`
		URL     string   `json:"url"`
		URLs    []string `json:"urls"`
		ID      string   `json:"id"`
		Quality string   `json:"quality"`
		Force   bool     `json:"force"`
		Paused  *bool    `json:"paused"`
		Details bool     `json:"details"`
	}
	if r.Method == "POST" {
		typeName, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if typeName != "application/json" {
			http.Error(w, "JSON required", 415)
			return
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			mobileJSON(w, nil, errors.New("请求格式无效"))
			return
		}
	}
	if r.URL.Path == "/api/mobile/pair" && r.Method == "POST" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		m.pair(w, r, in.Code)
		return
	}
	if !m.authorized(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "请先连接电脑"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/files/") && (r.Method == "GET" || r.Method == "HEAD") {
		m.serveFile(w, r, strings.TrimPrefix(r.URL.Path, "/files/"))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/poster/") && (r.Method == "GET" || r.Method == "HEAD") {
		m.servePreview(w, r, strings.TrimPrefix(r.URL.Path, "/poster/"), "poster")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/preview/") && (r.Method == "GET" || r.Method == "HEAD") {
		m.servePreview(w, r, strings.TrimPrefix(r.URL.Path, "/preview/"), "job")
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/mobile/state" {
		mobileJSON(w, m.state(r.URL.Query()), nil)
		return
	}
	if r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	var result any
	var err error
	switch r.URL.Path {
	case "/api/mobile/add":
		if len(in.URLs) == 0 && in.URL != "" {
			in.URLs = []string{in.URL}
		}
		if len(in.URLs) > 20 {
			err = errors.New("手机端每次最多添加 20 条链接")
		} else {
			result, err = m.engine.startParseBatch(in.URLs)
		}
	case "/api/mobile/download":
		result, err = m.engine.startDownloadOptions(in.ID, in.Quality, in.Force)
		if err == nil && in.Details {
			m.engine.mu.RLock()
			if job := m.engine.jobs[result.(string)]; job != nil {
				result = map[string]any{"id": job.ID, "status": job.Status}
			}
			m.engine.mu.RUnlock()
		}
	case "/api/mobile/queue-control":
		if in.Paused == nil {
			err = errors.New("请选择暂停或继续队列")
		} else {
			err = m.engine.setQueuePaused(*in.Paused)
		}
	case "/api/mobile/pause":
		err = m.engine.pauseJob(in.ID)
	case "/api/mobile/resume":
		result, err = m.engine.resumeJob(in.ID)
	case "/api/mobile/cancel":
		err = m.engine.cancelJob(in.ID)
	case "/api/mobile/formats":
		var formats []MediaFormat
		formats, err = m.engine.resourceFormats(in.ID)
		for i := range formats {
			formats[i].Selector = ""
			formats[i].Reason = ""
		}
		result = formats
	case "/api/mobile/disconnect":
		c, _ := r.Cookie(mobileCookie)
		m.mu.Lock()
		delete(m.sessions, c.Value)
		m.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: mobileCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	default:
		http.NotFound(w, r)
		return
	}
	if err == nil {
		m.engine.saveSession()
	} else if strings.HasPrefix(err.Error(), "DUPLICATE:") {
		err = errors.New("DUPLICATE: 内容已经下载过，是否重新下载？")
	} else {
		err = errors.New("操作未完成，请在电脑上查看详情并重试")
	}
	mobileJSON(w, result, err)
}
func (m *MobileGateway) serveFile(w http.ResponseWriter, r *http.Request, id string) {
	m.engine.mu.RLock()
	j := m.engine.jobs[id]
	if j == nil || j.Status != "completed" {
		m.engine.mu.RUnlock()
		http.Error(w, "文件尚未下载完成", 404)
		return
	}
	file, name, dir := j.File, j.Name, j.OutputDir
	if dir == "" {
		dir = m.engine.config.DownloadDir
	}
	m.engine.mu.RUnlock()
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || !pathInside(dir, file) || !ownedDirectory(dir, filepath.Dir(file)) {
		http.Error(w, "文件不可用，请在电脑中检查", 404)
		return
	}
	f, err := os.Open(file)
	if err != nil {
		http.Error(w, "文件无法读取", 404)
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", mediaFileType(file))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(file)}))
	http.ServeContent(w, r, name, info.ModTime(), f)
}
func (m *MobileGateway) servePreview(w http.ResponseWriter, r *http.Request, id, mode string) {
	data, err := m.engine.createMediaTicket(id, mode)
	if err != nil {
		http.Error(w, "预览暂不可用，请在电脑上查看详情", 404)
		return
	}
	address, _ := data.(map[string]any)["url"].(string)
	u, err := url.Parse(address)
	base, _ := url.Parse(m.engine.baseURL)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || !strings.HasPrefix(u.Path, "/media/") {
		http.Error(w, "预览不可用", 500)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), nil)
	if err != nil {
		http.Error(w, "预览不可用", 500)
		return
	}
	if value := r.Header.Get("Range"); value != "" {
		req.Header.Set("Range", value)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		http.Error(w, "预览读取失败", 502)
		return
	}
	defer resp.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Accept-Ranges", "Content-Range"} {
		if value := resp.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
