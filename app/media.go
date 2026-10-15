package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A ticket grants access to one known media object, never to arbitrary URLs or
// paths supplied by a browser. Credentials remain inside the engine.
type mediaTicket struct {
	Name, Type, URL, File string
	Headers               http.Header
	Expires               time.Time
	Mask                  []byte
}

func publicMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(raw, "\r\n") {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return false
	}
	if ip := net.ParseIP(strings.Split(h, "%")[0]); ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return false
	}
	return true
}

func (e *Engine) createMediaTicket(id, mode string) (any, error) {
	if mode != "poster" && mode != "video" && mode != "job" {
		return nil, errors.New("预览类型无效")
	}
	ticket := mediaTicket{Expires: time.Now().Add(10 * time.Minute)}
	quality, note := "播放版本", "下载仍按资源选择的画质执行。"
	width, height := 0, 0
	var resource Resource
	e.mu.RLock()
	if mode == "job" {
		j := e.jobs[id]
		if j == nil || j.Status != "completed" {
			e.mu.RUnlock()
			return nil, errors.New("下载完成后即可预览")
		}
		ticket.File, ticket.Name, ticket.Type = j.File, j.Name, mediaFileType(j.File)
	} else {
		r := e.resources[id]
		if r == nil {
			e.mu.RUnlock()
			return nil, errors.New("资源不存在")
		}
		resource = *r
		resource.privateHeaders = r.privateHeaders.Clone()
		ticket.Name = r.Name
		ticket.Type = r.MIME
		if mode == "video" {
			var newest *Job
			for _, j := range e.jobs {
				if j.ResourceID == id && j.Status == "completed" && (newest == nil || j.CreatedAt.After(newest.CreatedAt)) {
					newest = j
				}
			}
			if newest != nil {
				if info, err := os.Stat(newest.File); err == nil && info.Mode().IsRegular() {
					ticket.File, ticket.Name, ticket.Type = newest.File, newest.Name, mediaFileType(newest.File)
				}
			}
		}
	}
	e.mu.RUnlock()
	if ticket.File != "" {
		if info, err := os.Stat(ticket.File); err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("预览文件已被移动或删除")
		}
		local, err := e.prepareLocalPreview(ticket.File, ticket.Type)
		if err != nil {
			return nil, err
		}
		ticket.File, quality, note = local.File, local.Quality, local.Note
		width, height = local.Width, local.Height
		if local.MIME != "" {
			ticket.Type = local.MIME
		} else if local.Compatible {
			ticket.Type = "video/mp4"
		}
	} else if mode == "poster" {
		ticket.URL = resource.Thumbnail
		ticket.Type = "image"
		if ticket.URL == "" && resource.Type == "image" {
			ticket.URL = resource.URL
		}
		if ticket.URL == "" {
			return nil, errors.New("该资源没有封面")
		}
		ticket.Headers = resource.privateHeaders.Clone()
		source, _ := url.Parse(resource.URL)
		poster, _ := url.Parse(ticket.URL)
		if source == nil || poster == nil || source.Hostname() != poster.Hostname() {
			ticket.Headers.Del("Cookie")
			ticket.Headers.Del("Authorization")
		}
	} else {
		if resource.CaptureRequired || resource.Kind == "wechat-page" {
			return nil, errors.New("先播放目标视频，等待捕获完整信息")
		}
		if resource.Type != "video" && resource.Type != "audio" {
			return nil, errors.New("此资源不支持媒体预览")
		}
		ticket.URL = resource.URL
		ticket.Headers = resource.privateHeaders.Clone()
		if resource.Kind == "page" || resource.Kind == "playlist" {
			ctx, cancel := context.WithTimeout(e.ctx, 90*time.Second)
			defer cancel()
			u, _ := url.Parse(resource.URL)
			if isDouyinPage(u) {
				v, _, err := e.extractPlayableDouyin(ctx, resource.URL)
				if err != nil {
					return nil, err
				}
				ticket.URL = douyinText(v.Formats[0]["url"])
				ticket.Headers = http.Header{"User-Agent": {v.UserAgent}, "Referer": {v.Source}}
			} else if isPublicPlatformPage(u) {
				v, err := e.extractPublicPlatform(ctx, resource.URL)
				if err != nil {
					return nil, err
				}
				ticket.URL = douyinText(v.Formats[0]["url"])
				ticket.Headers = http.Header{"User-Agent": {v.UserAgent}, "Referer": {v.Source}}
			} else {
				var err error
				ticket.URL, ticket.Headers, err = e.extractSinglePreview(ctx, &resource)
				if err != nil {
					return nil, err
				}
			}
			ticket.Headers.Del("Cookie")
			ticket.Headers.Del("Authorization")
		}
		if resource.WechatVideo {
			if resource.WechatPlaybackURL != "" {
				ticket.URL = resource.WechatPlaybackURL
			}
			mask, err := e.previewWechatMask(&resource, ticket.URL)
			if err != nil {
				return nil, err
			}
			ticket.Mask = mask
			ticket.Type = "video/mp4"
		}
	}
	if ticket.File == "" && !publicMediaURL(ticket.URL) {
		return nil, errors.New("预览地址无效")
	}
	key := make([]byte, 24)
	if _, err := rand.Read(key); err != nil {
		return nil, errors.New("无法创建预览票据")
	}
	token := hex.EncodeToString(key)
	e.mu.Lock()
	if e.mediaTickets == nil {
		e.mediaTickets = map[string]mediaTicket{}
	}
	for k, t := range e.mediaTickets {
		if time.Now().After(t.Expires) {
			delete(e.mediaTickets, k)
		}
	}
	if len(e.mediaTickets) >= 512 {
		e.mu.Unlock()
		return nil, errors.New("预览请求过多，请稍后重试")
	}
	ticket.Expires = time.Now().Add(10 * time.Minute)
	e.mediaTickets[token] = ticket
	e.mu.Unlock()
	return map[string]any{"url": e.baseURL + "/media/" + token, "name": ticket.Name, "type": ticket.Type, "quality": quality, "note": note, "width": width, "height": height}, nil
}

func mediaFileType(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".ape":
		return "audio/x-ape"
	case ".wma":
		return "audio/x-ms-wma"
	case ".alac":
		return "audio/alac"
	case ".aiff":
		return "audio/aiff"
	case ".mka":
		return "audio/x-matroska"
	}
	mt := mime.TypeByExtension(filepath.Ext(file))
	if mt == "" {
		return "application/octet-stream"
	}
	return strings.Split(mt, ";")[0]
}

func (e *Engine) extractSinglePreview(ctx context.Context, r *Resource) (string, http.Header, error) {
	dir, err := e.videoTools()
	if err != nil {
		return "", nil, err
	}
	args := extractorArgs(dir, r.privateHeaders)
	args = append(args, "-f", "b[ext=mp4]/b", "--skip-download", "--dump-single-json", "--", r.URL)
	out := &limitedBuffer{limit: 16 << 20}
	stderr := &tailBuffer{limit: 4096}
	cmd := exec.Command(toolPath(dir, "yt-dlp"), args...)
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := runManagedCommand(ctx, cmd); err != nil {
		return "", nil, errors.New("此页面暂未提供可直接预览的完整音视频流，下载完成后可播放")
	}
	var info struct {
		URL     string            `json:"url"`
		VCodec  string            `json:"vcodec"`
		ACodec  string            `json:"acodec"`
		DRM     bool              `json:"is_drm"`
		Headers map[string]string `json:"http_headers"`
	}
	if json.Unmarshal(out.Bytes(), &info) != nil || info.DRM || info.URL == "" || info.VCodec == "none" || info.ACodec == "none" {
		return "", nil, errors.New("此视频需要先合并音轨，下载完成后可播放")
	}
	headers := r.privateHeaders.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	for _, key := range []string{"User-Agent", "Referer", "Origin"} {
		if v := info.Headers[key]; v != "" && !strings.ContainsAny(v, "\r\n") {
			headers.Set(key, v)
		}
	}
	return info.URL, headers, nil
}

func (e *Engine) previewWechatMask(r *Resource, raw string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	copy := *r
	copy.URL = raw
	if copy.privateHeaders == nil {
		copy.privateHeaders = http.Header{}
	}
	req, err := downloadRequest(ctx, "GET", &copy)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes=0-4095")
	resp, err := e.mediaClient().Do(req)
	if err != nil {
		return nil, errors.New("视频号预览地址暂不可访问")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return nil, fmt.Errorf("视频号预览返回 HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode == 206 {
		start, _, _, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != 0 {
			return nil, errors.New("视频号预览范围无效")
		}
	}
	prefix, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || len(prefix) < 12 {
		return nil, errors.New("视频号预览文件头无效")
	}
	if string(prefix[4:8]) == "ftyp" {
		return nil, nil
	}
	mask, err := wechatDecodeMask(r.wechatSeed)
	if err != nil {
		return nil, errors.New("视频号尚未捕获解码信息，请重新播放")
	}
	for i := range prefix {
		prefix[i] ^= mask[i]
	}
	if string(prefix[4:8]) != "ftyp" {
		return nil, errors.New("视频号预览不能解码为 MP4")
	}
	return mask, nil
}

func (e *Engine) mediaClient() *http.Client {
	return &http.Client{Transport: e.transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !publicMediaURL(req.URL.String()) {
			return errors.New("媒体跳转无效")
		}
		if len(via) > 0 && req.URL.Hostname() != via[0].URL.Hostname() {
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
		}
		return nil
	}}
}

var mediaRangePattern = regexp.MustCompile(`^bytes=(\d+-\d*|-\d+)$`)

func (e *Engine) serveMedia(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() {
		http.Error(w, "Local requests only", 403)
		return
	}
	base, _ := url.Parse(e.baseURL)
	if base == nil || r.Host != base.Host || !e.allowedOrigin(r.Header.Get("Origin")) {
		http.Error(w, "Origin rejected", 403)
		return
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		if err != nil || !e.allowedOrigin(u.Scheme+"://"+u.Host) {
			http.Error(w, "Referrer rejected", 403)
			return
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method rejected", 405)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/media/")
	e.mu.RLock()
	ticket, ok := e.mediaTickets[token]
	e.mu.RUnlock()
	if !ok || time.Now().After(ticket.Expires) {
		http.Error(w, "Preview expired", 404)
		return
	}
	if rg := r.Header.Get("Range"); rg != "" && !mediaRangePattern.MatchString(rg) {
		http.Error(w, "Invalid media range", 416)
		return
	}
	if ticket.File != "" {
		file, err := os.Open(ticket.File)
		if err != nil {
			http.Error(w, "File unavailable", 404)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.Error(w, "File unavailable", 404)
			return
		}
		mt := mime.TypeByExtension(filepath.Ext(ticket.File))
		if mt == "" {
			mt = ticket.Type
		}
		w.Header().Set("Content-Type", mt)
		http.ServeContent(w, r, ticket.Name, info.ModTime(), file)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, ticket.URL, nil)
	if err != nil {
		http.Error(w, "Media unavailable", 502)
		return
	}
	for key, vs := range ticket.Headers {
		if strings.EqualFold(key, "Range") || strings.EqualFold(key, "Host") || strings.EqualFold(key, "Connection") {
			continue
		}
		for _, v := range vs {
			if !strings.ContainsAny(v, "\r\n") {
				req.Header.Add(key, v)
			}
		}
	}
	req.Header.Set("Accept-Encoding", "identity")
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}
	resp, err := e.mediaClient().Do(req)
	if err != nil {
		http.Error(w, "Media unavailable", 502)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 416 {
		if cr := resp.Header.Get("Content-Range"); strings.HasPrefix(cr, "bytes */") {
			w.Header().Set("Content-Range", cr)
		}
		w.WriteHeader(416)
		return
	}
	if resp.StatusCode != 200 && resp.StatusCode != 206 || htmlMIME(resp.Header.Get("Content-Type")) {
		http.Error(w, "Media unavailable", 502)
		return
	}
	offset := int64(0)
	if resp.StatusCode == 206 {
		start, _, _, valid := parseContentRange(resp.Header.Get("Content-Range"))
		if !valid {
			http.Error(w, "Invalid media range", 502)
			return
		}
		offset = start
		w.Header().Set("Content-Range", resp.Header.Get("Content-Range"))
	}
	mt := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if ticket.Type == "image" {
		if !strings.HasPrefix(mt, "image/") || mt == "image/svg+xml" {
			http.Error(w, "Poster unavailable", 502)
			return
		}
	} else {
		if strings.HasPrefix(ticket.Type, "video/") || strings.HasPrefix(ticket.Type, "audio/") {
			mt = ticket.Type
		} else {
			mt = "video/mp4"
		}
	}
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Accept-Ranges", "bytes")
	if length := resp.Header.Get("Content-Length"); length != "" {
		w.Header().Set("Content-Length", length)
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			for i := 0; i < n && offset+int64(i) < int64(len(ticket.Mask)); i++ {
				buffer[i] ^= ticket.Mask[offset+int64(i)]
			}
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			offset += int64(n)
		}
		if readErr != nil {
			return
		}
	}
}
