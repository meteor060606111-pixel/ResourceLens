package main

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed helpers/wechat-bridge.js
var wechatBridgeScript string

var wechatGetter = regexp.MustCompile(`get\s+media\(\)\s*\{`)
var wechatDetail = regexp.MustCompile(`(?s)async\s+finderGetCommentDetail\(([\w$]+)\)\s*\{return(.*?)\}\s*async`)
var wechatScriptAttribute = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*["']([^"']+)["']`)
var wechatScriptReference = regexp.MustCompile(`["']([^"'\s\\]+\.js(?:\?[^"'\s\\]*)?)["']`)

type wechatResponseSummary struct {
	Host   string `json:"host"`
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	Status int    `json:"status"`
	Count  int    `json:"count"`
}

func wechatHTMLRequest(req *http.Request) bool {
	return req.Method == http.MethodGet && hostIn(req.URL.Hostname(), "channels.weixin.qq.com") && strings.HasPrefix(req.URL.Path, "/web/pages/")
}

func wechatScriptRequest(req *http.Request) bool {
	host, requestPath := req.URL.Hostname(), req.URL.Path
	if !hostIn(host, "res.wx.qq.com", "channels.weixin.qq.com") || !strings.HasSuffix(strings.ToLower(requestPath), ".js") {
		return false
	}
	referrer, _ := url.Parse(req.Header.Get("Referer"))
	return strings.Contains(requestPath, "web-finder") || strings.Contains(requestPath, "/finder/") || req.URL.Query().Has("__rlwv") || (referrer != nil && hostIn(referrer.Hostname(), "channels.weixin.qq.com"))
}

func (e *Engine) refreshWechatRequest(req *http.Request) {
	if !wechatHTMLRequest(req) && !wechatScriptRequest(req) {
		return
	}
	e.mu.RLock()
	active := e.capture
	e.mu.RUnlock()
	if active {
		req.Header.Del("If-None-Match")
		req.Header.Del("If-Modified-Since")
		req.Header.Set("Cache-Control", "no-cache")
	}
}

func (e *Engine) refreshWechatHTML(data []byte, base *url.URL) []byte {
	sum := sha256.Sum256([]byte("wechat-asset-version:" + e.token))
	version := hex.EncodeToString(sum[:8])
	return wechatScriptAttribute.ReplaceAllFunc(data, func(attribute []byte) []byte {
		parts := wechatScriptAttribute.FindSubmatchIndex(attribute)
		if len(parts) != 4 {
			return attribute
		}
		start, end := parts[2], parts[3]
		original := html.UnescapeString(string(attribute[start:end]))
		u, err := url.Parse(original)
		if err != nil {
			return attribute
		}
		resolved := base.ResolveReference(u)
		if !hostIn(resolved.Hostname(), "res.wx.qq.com", "channels.weixin.qq.com") || !strings.HasSuffix(strings.ToLower(resolved.Path), ".js") {
			return attribute
		}
		// Keep signatures and existing parameters byte-for-byte; append one cache
		// version marker instead of parsing and reserializing their query string.
		separator := "?"
		if u.RawQuery != "" {
			separator = "&"
		}
		fragment := u.Fragment
		u.Fragment = ""
		updated := u.String() + separator + "__rlwv=" + version
		if fragment != "" {
			updated += "#" + fragment
		}
		result := append([]byte{}, attribute[:start]...)
		result = append(result, html.EscapeString(updated)...)
		return append(result, attribute[end:]...)
	})
}

func (e *Engine) refreshWechatScript(data string, base *url.URL) string {
	sum := sha256.Sum256([]byte("wechat-asset-version:" + e.token))
	version := hex.EncodeToString(sum[:8])
	return wechatScriptReference.ReplaceAllStringFunc(data, func(reference string) string {
		if reference[0] != reference[len(reference)-1] {
			return reference
		}
		original := reference[1 : len(reference)-1]
		u, err := url.Parse(original)
		if err != nil {
			return reference
		}
		resolved := base.ResolveReference(u)
		if !hostIn(resolved.Hostname(), "res.wx.qq.com", "channels.weixin.qq.com") || !strings.HasSuffix(strings.ToLower(resolved.Path), ".js") || u.Query().Has("__rlwv") {
			return reference
		}
		separator := "?"
		if u.RawQuery != "" {
			separator = "&"
		}
		fragment := u.Fragment
		u.Fragment = ""
		updated := u.String() + separator + "__rlwv=" + version
		if fragment != "" {
			updated += "#" + fragment
		}
		return reference[:1] + updated + reference[len(reference)-1:]
	})
}

func (e *Engine) wechatBridgePath() string {
	// A narrow metadata capability; never expose the local application API token.
	return "/__resourcelens__/wechat/" + e.wechatBridgeKey
}

func loadWechatBridgeKey(dir string) (string, error) {
	path := filepath.Join(dir, "wechat-bridge.key")
	if data, err := os.ReadFile(path); err == nil {
		key := strings.TrimSpace(string(data))
		decoded, decodeErr := hex.DecodeString(key)
		if decodeErr != nil || len(decoded) != 24 {
			return "", errors.New("视频号回传配置无效")
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	key := make([]byte, 24)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	encoded := hex.EncodeToString(key)
	if err := atomicWrite(path, []byte(encoded), 0600); err != nil {
		return "", err
	}
	return encoded, nil
}

func wechatMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && (u.Scheme == "http" || u.Scheme == "https") && hostIn(u.Hostname(), "finder.video.qq.com", "findermp.video.qq.com")
}

func wechatMediaIdentity(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if key := u.Query().Get("encfilekey"); key != "" {
		return u.Hostname() + u.Path + ":" + key
	}
	return raw
}

func wechatEncryptedURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && wechatMediaURL(raw) && u.Query().Get("encfilekey") != ""
}

func wechatValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

func (e *Engine) collectWechatMetadata(raw []byte, headers http.Header) int {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root any
	if decoder.Decode(&root) != nil {
		return 0
	}
	added, budget := 0, 5000
	var walk func(any, int)
	walk = func(value any, depth int) {
		budget--
		if depth > 24 || budget < 0 {
			return
		}
		switch obj := value.(type) {
		case []any:
			for _, item := range obj {
				walk(item, depth+1)
			}
		case map[string]any:
			if media, ok := obj["media"].([]any); ok {
				for _, entry := range media {
					m, ok := entry.(map[string]any)
					if !ok || wechatValue(m["mediaType"]) == "9" {
						continue
					}
					mediaURL := wechatValue(m["url"]) + wechatValue(m["urlToken"])
					if !wechatMediaURL(mediaURL) {
						continue
					}
					seed := wechatValue(m["decodeKey"])
					if seed != "" {
						if _, err := wechatDecodeMask(seed); err != nil {
							continue
						}
					}
					name := wechatValue(obj["description"])
					if name == "" {
						name = "微信视频号 " + time.Now().Format("20060102-150405")
					}
					size, _ := strconv.ParseInt(wechatValue(m["fileSize"]), 10, 64)
					if size <= 0 {
						size = -1
					}
					safeHeaders := http.Header{"User-Agent": []string{headers.Get("User-Agent")}, "Referer": []string{"https://channels.weixin.qq.com/"}, "Origin": []string{"https://channels.weixin.qq.com"}}
					identity := wechatMediaIdentity(mediaURL)
					e.mu.RLock()
					var r *Resource
					for _, existing := range e.resources {
						if wechatMediaURL(existing.URL) && wechatMediaIdentity(existing.URL) == identity {
							r = existing
							break
						}
					}
					e.mu.RUnlock()
					if r == nil {
						r = e.remember(mediaURL, safeHeaders, "video/mp4", size, "", false)
					}
					if r == nil {
						continue
					}
					u, _ := url.Parse(mediaURL)
					e.mu.Lock()
					r.Host, r.Name, r.Type, r.MIME, r.Kind, r.Site = u.Host, safeFilename(name+".mp4"), "video", "video/mp4", "direct", "微信视频号"
					if r.NameOverride != "" {
						r.Name = r.NameOverride
					}
					r.Thumbnail = wechatValue(m["thumbUrl"])
					if r.Thumbnail == "" {
						r.Thumbnail = wechatValue(m["coverUrl"])
					}
					r.Duration, _ = strconv.ParseFloat(wechatValue(m["videoPlayLen"]), 64)
					if contact, ok := obj["contact"].(map[string]any); ok {
						r.Author = wechatValue(contact["nickname"])
					}
					r.GroupID = mediaGroup(mediaURL, "")
					r.WechatPlaybackURL = mediaURL
					if size > 0 {
						r.WechatSourceSize = size
					}
					if formats := wechatFormats(m["spec"]); len(formats) > 0 {
						r.WechatFormats = formats
					}
					if r.WechatResolvedQuality == "" {
						r.URL, r.Size = mediaURL, -1
					}
					r.WechatVideo, r.privateHeaders = true, safeHeaders
					if seed != "" {
						r.wechatSeed = seed
					}
					r.CaptureRequired = false
					r.Headers = map[string]string{"User-Agent": safeHeaders.Get("User-Agent"), "Referer": safeHeaders.Get("Referer"), "Origin": safeHeaders.Get("Origin")}
					e.mu.Unlock()
					added++
				}
			}
			for key, nested := range obj {
				if key != "media" {
					walk(nested, depth+1)
				}
			}
		}
	}
	walk(root, 0)
	if added > 0 {
		e.wechatMetadataCount.Add(uint64(added))
		e.saveSession()
	}
	return added
}

func (e *Engine) handleWechatBridge(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/__resourcelens__/wechat/") {
		return false
	}
	// Reserved paths are never sent upstream, including after capture has stopped.
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != e.wechatBridgePath() || !hostIn(r.URL.Hostname(), "channels.weixin.qq.com", "weixin.qq.com") {
		http.Error(w, "Unknown bridge", 404)
		return true
	}
	e.mu.RLock()
	active := e.capture
	e.mu.RUnlock()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-ResourceLens-Bridge", "wechat-v2")
	if r.Method == http.MethodGet {
		epoch := sha256.Sum256([]byte(fmt.Sprintf("wechat-capture:%s:%d", e.token, e.wechatCaptureSequence.Load())))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"capture": active, "epoch": hex.EncodeToString(epoch[:12])})
		return true
	}
	if !active || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Metadata too large", 413)
		return true
	}
	e.collectWechatMetadata(data, r.Header)
	w.WriteHeader(http.StatusNoContent)
	return true
}

type wechatReplayBody struct {
	io.Reader
	closer io.Closer
}

func (b *wechatReplayBody) Close() error { return b.closer.Close() }

func (e *Engine) inspectWechatResponse(req *http.Request, resp *http.Response) {
	host, requestPath := req.URL.Hostname(), req.URL.Path
	if !hostIn(host, "channels.weixin.qq.com", "res.wx.qq.com", "weixin.qq.com") {
		return
	}
	typeName := strings.ToLower(resp.Header.Get("Content-Type"))
	// A bounded in-memory route summary, without query strings or response bodies.
	if hostIn(host, "channels.weixin.qq.com") || wechatScriptRequest(req) {
		e.mu.Lock()
		if e.wechatResponses == nil {
			e.wechatResponses = map[string]wechatResponseSummary{}
		}
		key := host + requestPath + ":" + strconv.Itoa(resp.StatusCode)
		if previous, ok := e.wechatResponses[key]; ok {
			previous.Count++
			e.wechatResponses[key] = previous
		} else if len(e.wechatResponses) < 64 {
			e.wechatResponses[key] = wechatResponseSummary{Host: host, Path: requestPath, MIME: typeName, Status: resp.StatusCode, Count: 1}
		}
		e.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	isHTML := wechatHTMLRequest(req) && strings.Contains(typeName, "html")
	isScript := wechatScriptRequest(req)
	isJSON := hostIn(host, "channels.weixin.qq.com") && !isHTML && !isScript && (strings.Contains(typeName, "json") || strings.Contains(typeName, "text/") || strings.Contains(typeName, "octet-stream"))
	if !isJSON && !isScript && !isHTML {
		return
	}
	const limit = 12 << 20
	originalBody := resp.Body
	encoded, err := io.ReadAll(io.LimitReader(originalBody, limit+1))
	if err != nil || len(encoded) > limit {
		resp.Body = &wechatReplayBody{Reader: io.MultiReader(bytes.NewReader(encoded), originalBody), closer: originalBody}
		return
	}
	data := encoded
	var compressed io.ReadCloser
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "", "identity":
	case "gzip":
		compressed, err = gzip.NewReader(bytes.NewReader(encoded))
	case "deflate":
		compressed, err = zlib.NewReader(bytes.NewReader(encoded))
	default:
		err = errors.New("unsupported compression")
	}
	if compressed != nil {
		data, err = io.ReadAll(io.LimitReader(compressed, limit+1))
		compressed.Close()
	}
	resp.Body = &wechatReplayBody{Reader: bytes.NewReader(encoded), closer: originalBody}
	if err != nil || len(data) > limit {
		return
	}
	if isJSON {
		e.collectWechatMetadata(data, req.Header)
		return
	}
	if isHTML {
		modified := e.refreshWechatHTML(data, req.URL)
		e.wechatHTMLPatches.Add(1)
		replaceWechatBody(resp, originalBody, modified)
		return
	}
	bridgePath, _ := json.Marshal(e.wechatBridgePath())
	bootstrap := strings.ReplaceAll(wechatBridgeScript, "__RESOURCE_LENS_WECHAT_BRIDGE__", string(bridgePath))
	// Propagate cache refresh through module imports and dynamically loaded chunks,
	// so the metadata getter module cannot remain in the browser's old cache.
	script := e.refreshWechatScript(string(data), req.URL)
	e.wechatGetterPatches.Add(uint64(len(wechatGetter.FindAllStringIndex(script, -1))))
	e.wechatDetailPatches.Add(uint64(len(wechatDetail.FindAllStringIndex(script, -1))))
	// Preserve the original getter body and result; this adds only a guarded observation.
	script = wechatGetter.ReplaceAllStringFunc(script, func(found string) string {
		return found + `try{globalThis.__resourceLensWechatCollect?.(this.objectDesc)}catch(__rlError){}`
	})
	script = wechatDetail.ReplaceAllStringFunc(script, func(found string) string {
		parts := wechatDetail.FindStringSubmatch(found)
		// Keep the returned expression as a function body: nested object literals,
		// callbacks and trailing semicolons remain valid JavaScript.
		return `async finderGetCommentDetail(` + parts[1] + `){const __rlResult=await(async()=>{return` + parts[2] + `})();try{globalThis.__resourceLensWechatCollect?.(__rlResult)}catch(__rlError){}return __rlResult}async`
	})
	modified := []byte(bootstrap + "\n" + script)
	e.wechatScriptPatches.Add(1)
	replaceWechatBody(resp, originalBody, modified)
}

func replaceWechatBody(resp *http.Response, originalBody io.Closer, modified []byte) {
	resp.Body = &wechatReplayBody{Reader: bytes.NewReader(modified), closer: originalBody}
	resp.ContentLength = int64(len(modified))
	resp.Header.Set("Content-Length", strconv.Itoa(len(modified)))
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("ETag")
	resp.Header.Del("Content-MD5")
	resp.Header.Set("Cache-Control", "no-store")
}

func decryptWechatPrefix(ctx context.Context, fileName, seed string) error {
	mask, err := wechatDecodeMask(seed)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f, err := os.OpenFile(fileName, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	prefix := make([]byte, len(mask))
	n, err := io.ReadFull(f, prefix)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	for i := 0; i < n; i++ {
		prefix[i] ^= mask[i]
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, err = f.WriteAt(prefix[:n], 0); err != nil {
		return fmt.Errorf("视频号文件处理失败：%w", err)
	}
	return f.Sync()
}

func wechatFileHasMP4Header(fileName string) bool {
	f, err := os.Open(fileName)
	if err != nil {
		return false
	}
	defer f.Close()
	var prefix [12]byte
	_, err = io.ReadFull(f, prefix[:])
	return err == nil && string(prefix[4:8]) == "ftyp"
}

func (e *Engine) addWechatPage(raw string) (*Resource, error) {
	r := e.remember(raw, nil, "video/mp4", -1, "", true)
	if r == nil {
		return nil, errors.New("资源列表已满，请清空后重试")
	}
	e.mu.Lock()
	r.Name, r.Site, r.Kind, r.CaptureRequired = "视频号：待播放捕获.mp4", "微信视频号", "wechat-page", true
	copy := *r
	e.mu.Unlock()
	return &copy, nil
}
