package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const douyinUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
const douyinAccessMessage = "抖音未向当前请求提供可下载的播放地址，可能要求浏览器验证、登录，或作品已不可用。可以在拾流开启资源捕获，再用浏览器正常播放该视频，下载捕获到的视频流；不会把分享网页保存成 MP4。"

//go:embed helpers/douyin-browser.mjs
var douyinBrowserScript []byte

var sharedHTTPURL = regexp.MustCompile(`(?i)https?://[^\s<>"'，。；、（）【】]+`)
var douyinVideoPath = regexp.MustCompile(`^/(?:share/)?video/(\d{8,24})(?:/|$)`)
var douyinNotePath = regexp.MustCompile(`^/(?:share/)?note/(\d+)(?:/|$)`)
var douyinScript = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
var douyinRenderID = regexp.MustCompile(`(?i)\bid\s*=\s*["'](?:RENDER_DATA|__NEXT_DATA__|SIGI_STATE|__UNIVERSAL_DATA_FOR_REHYDRATION__)["']`)
var douyinRouterAssignment = regexp.MustCompile(`(?:window\.)?_ROUTER_DATA\s*=\s*`)

func isDouyinPage(u *url.URL) bool {
	return u != nil && hostIn(u.Hostname(), "douyin.com", "iesdouyin.com")
}

// Sharing from the app includes a caption and copy instructions around the URL.
// Extract only a Douyin URL here; ordinary direct links retain their exact query.
func normalizeVideoInput(input string) (string, error) {
	input = strings.TrimSpace(input)
	if u, err := url.Parse(input); err == nil && u != nil && u.Host != "" && u.User == nil && (u.Scheme == "http" || u.Scheme == "https") && !strings.ContainsAny(input, "\r\n\t ") {
		if !isDouyinPage(u) {
			return input, nil
		}
		return strings.TrimRight(input, "，。；、！!\"'）)]}"), nil
	}
	for _, prefix := range []string{"v.douyin.com/", "www.douyin.com/", "douyin.com/", "www.iesdouyin.com/", "iesdouyin.com/", "www.kuaishou.com/", "v.kuaishou.com/", "kuaishou.com/", "www.xiaohongshu.com/", "xiaohongshu.com/", "xhslink.com/"} {
		if strings.HasPrefix(strings.ToLower(input), prefix) && !strings.ContainsAny(input, "\r\n\t ") {
			return "https://" + strings.TrimRight(input, "，。；、！!\"'）)]}"), nil
		}
	}
	found := ""
	for _, candidate := range sharedHTTPURL.FindAllString(input, -1) {
		candidate = html.UnescapeString(strings.TrimRight(candidate, "，。；、！!\"'）)]}"))
		u, err := url.Parse(candidate)
		if err != nil || u.User != nil || !isDouyinPage(u) && !isPublicPlatformPage(u) {
			continue
		}
		if found != "" && found != candidate {
			return "", errors.New("一次添加一个作品的分享文本；多个作品请使用批量添加")
		}
		found = candidate
	}
	if found != "" {
		return found, nil
	}
	return input, nil
}

func douyinID(u *url.URL) string {
	if u == nil {
		return ""
	}
	if match := douyinVideoPath.FindStringSubmatch(u.Path); len(match) == 2 {
		return match[1]
	}
	for _, key := range []string{"modal_id", "vid", "aweme_id", "item_id"} {
		id := u.Query().Get(key)
		if len(id) < 8 || len(id) > 24 {
			continue
		}
		valid := true
		for _, c := range id {
			valid = valid && c >= '0' && c <= '9'
		}
		if valid {
			return id
		}
	}
	return ""
}

func canonicalDouyinURL(id string, source *url.URL) string {
	u, _ := url.Parse("https://www.douyin.com/video/" + id)
	q := url.Values{}
	for _, key := range []string{"share_scene", "share_token", "aweme_type", "scene_from", "from_aid"} {
		if value := source.Query().Get(key); value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

type douyinPublicVideo struct {
	ID, Title, Source string
	UserAgent         string
	Duration          float64
	Size              int64
	Quality           string
	Formats           []map[string]any
	Ladder            []map[string]any
	Thumbnail, Author string
}

func (v *douyinPublicVideo) extractorInfo() map[string]any {
	return map[string]any{"id": v.ID, "title": v.Title, "duration": v.Duration, "webpage_url": v.Source, "extractor": "DouyinPublic", "extractor_key": "Douyin", "formats": v.Formats}
}

// Use an isolated anonymous HTTP session. Only cookies set by these public page
// requests are accepted; browser profiles and logged-in account cookies are never read.
func (e *Engine) douyinPage(ctx context.Context, client *http.Client, raw, referer string) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", douyinUserAgent)
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Encoding", "identity")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Request.URL, fmt.Errorf("抖音页面返回 HTTP %d", resp.StatusCode)
	}
	var body io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		z, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, resp.Request.URL, err
		}
		defer z.Close()
		body = z
	}
	b, err := io.ReadAll(io.LimitReader(body, (8<<20)+1))
	if len(b) > 8<<20 {
		return nil, resp.Request.URL, errors.New("抖音页面数据超出解析范围")
	}
	return b, resp.Request.URL, err
}

func decodeDouyinJSON(b []byte) any {
	var data any
	d := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(b)))
	d.UseNumber() // Video identifiers exceed JavaScript's exact integer range.
	if d.Decode(&data) != nil {
		return nil
	}
	return data
}

func douyinPageData(body []byte) []any {
	var data []any
	if v := decodeDouyinJSON(body); v != nil {
		data = append(data, v)
	}
	for _, script := range douyinScript.FindAllSubmatch(body, -1) {
		text := script[2]
		if pos := douyinRouterAssignment.FindIndex(text); pos != nil {
			if v := decodeDouyinJSON(text[pos[1]:]); v != nil {
				data = append(data, v)
			}
		} else if douyinRenderID.Match(script[1]) {
			decoded := html.UnescapeString(strings.TrimSpace(string(text)))
			if strings.HasPrefix(decoded, "%") {
				if unescaped, err := url.PathUnescape(decoded); err == nil {
					decoded = unescaped
				}
			}
			if v := decodeDouyinJSON([]byte(decoded)); v != nil {
				data = append(data, v)
			}
		}
	}
	return data
}

func douyinText(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	}
	return ""
}
func douyinNumber(v any) float64 {
	switch number := v.(type) {
	case float64:
		return number
	case float32:
		return float64(number)
	case int:
		return float64(number)
	case int64:
		return float64(number)
	case int32:
		return float64(number)
	}
	n, _ := strconv.ParseFloat(douyinText(v), 64)
	return n
}

func douyinFirstNumber(values ...any) float64 {
	for _, value := range values {
		if n := douyinNumber(value); n > 0 {
			return n
		}
	}
	return 0
}
func douyinFlag(v any) bool {
	return v == true || douyinNumber(v) > 0 || douyinText(v) == "true"
}

func publicDouyinItem(item map[string]any, id, source string) *douyinPublicVideo {
	itemID := ""
	for _, key := range []string{"aweme_id", "awemeId", "id", "itemId"} {
		if value := douyinText(item[key]); value != "" {
			itemID = value
			break
		}
	}
	if itemID != id {
		return nil // Never substitute a recommendation for the requested work.
	}
	video, ok := item["video"].(map[string]any)
	if !ok || douyinFlag(video["is_drm"]) || douyinFlag(item["is_drm"]) || douyinNumber(video["drm_type"]) > 0 || douyinFlag(video["is_bytevc2"]) {
		return nil
	}
	if status, ok := item["status"].(map[string]any); ok && (douyinFlag(status["is_private"]) || douyinFlag(status["is_prohibited"])) {
		return nil
	}
	title := douyinText(item["desc"])
	if title == "" {
		title = douyinText(item["title"])
	}
	if title == "" {
		title = "抖音视频 " + id
	}
	v := &douyinPublicVideo{ID: id, Title: title, Source: source, UserAgent: douyinUserAgent, Size: -1, Duration: douyinNumber(video["duration"]) / 1000}
	if author, ok := item["author"].(map[string]any); ok {
		v.Author = douyinText(author["nickname"])
	}
	if cover, ok := video["cover"].(map[string]any); ok {
		if urls, ok := cover["url_list"].([]any); ok && len(urls) > 0 {
			v.Thumbnail = douyinText(urls[0])
		}
	}
	seen := map[string]int{}
	add := func(addr, metadata map[string]any, codec string, rate float64, base bool) {
		if addr == nil {
			return
		}
		urls, _ := addr["url_list"].([]any)
		if len(urls) == 0 {
			urls, _ = addr["UrlList"].([]any)
		}
		size := int64(max(douyinNumber(addr["data_size"]), douyinNumber(addr["DataSize"])))
		// The root video dimensions describe its default rendition, not every
		// entry in the bitrate ladder. Prefer each address's own dimensions.
		width := douyinFirstNumber(addr["width"], addr["Width"], metadata["width"], metadata["Width"])
		height := douyinFirstNumber(addr["height"], addr["Height"], metadata["height"], metadata["Height"])
		fps := douyinFirstNumber(metadata["FPS"], metadata["fps"], addr["FPS"], addr["fps"])
		if base {
			width = douyinFirstNumber(width, video["width"])
			height = douyinFirstNumber(height, video["height"])
			fps = douyinFirstNumber(fps, video["FPS"], video["fps"])
		}
		unavailable := ""
		if codec == "bytevc2" {
			unavailable = "该编码暂不支持"
		} else if douyinFlag(metadata["is_drm"]) || douyinNumber(metadata["drm_type"]) > 0 {
			unavailable = "受保护的视频流"
		} else if strings.EqualFold(douyinText(metadata["format"]), "dash") {
			// These URLs are a single video track. A file_id in video_extra is
			// not an audio URL; do not claim AAC or produce a silently muted file.
			unavailable = "DASH 未提供可合并的音轨地址"
		}
		for _, candidate := range urls {
			media := html.UnescapeString(douyinText(candidate))
			u, err := url.Parse(media)
			if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(media, "\r\n") {
				continue
			}
			format, duplicate := map[string]any(nil), false
			if index, ok := seen[media]; ok {
				format, duplicate = v.Formats[index], true
			} else {
				format = map[string]any{"format_id": fmt.Sprintf("public-%d", len(v.Formats)), "url": media, "ext": "mp4", "vcodec": codec, "acodec": "aac", "http_headers": map[string]string{"User-Agent": douyinUserAgent, "Referer": source}}
				seen[media] = len(v.Formats)
			}
			// Enrich duplicate default URLs with the ladder's FPS/bitrate rather
			// than dropping the more precise metadata encountered later.
			if width > 0 {
				format["width"] = width
			}
			if height > 0 {
				format["height"] = height
			}
			if fps > 0 {
				format["fps"] = fps
			}
			if rate > 0 {
				format["tbr"] = rate / 1000
			}
			if size > 0 {
				// Page sizes are hints only. Verification will replace them with
				// the selected address's HTTP total, or remove them if unknown.
				format["filesize_approx"] = size
			}
			if unavailable != "" {
				format["_douyin_unavailable"] = unavailable
				format["acodec"] = "none"
			}
			if !duplicate {
				v.Formats = append(v.Formats, format)
			}
		}
	}
	codec := "h264"
	if douyinFlag(video["is_bytevc1"]) || douyinFlag(video["is_h265"]) {
		codec = "h265"
	}
	for _, key := range []string{"play_addr_h264", "play_addr", "download_addr", "play_addr_265", "play_addr_bytevc1"} {
		addr, _ := video[key].(map[string]any)
		c := codec
		if key == "play_addr_h264" || key == "download_addr" {
			c = "h264"
		} else if key == "play_addr_bytevc1" || key == "play_addr_265" {
			c = "h265"
		}
		add(addr, video, c, 0, true)
	}
	if rates, ok := video["bit_rate"].([]any); ok {
		for _, entry := range rates {
			b, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			addr, _ := b["play_addr"].(map[string]any)
			c := "h264"
			if douyinFlag(b["is_bytevc1"]) || douyinFlag(b["is_h265"]) {
				c = "h265"
			}
			if douyinFlag(b["is_bytevc2"]) {
				c = "bytevc2"
			}
			add(addr, b, c, douyinNumber(b["bit_rate"]), false)
		}
	}
	if len(v.Formats) == 0 {
		return nil
	}
	return v
}

func findPublicDouyinVideo(value any, id, source string, depth int) *douyinPublicVideo {
	if depth > 40 {
		return nil
	}
	switch data := value.(type) {
	case map[string]any:
		if v := publicDouyinItem(data, id, source); v != nil {
			return v
		}
		for _, key := range []string{"aweme_detail", "item_list", "videoInfoRes", "awemeDetail", "loaderData"} {
			if v := findPublicDouyinVideo(data[key], id, source, depth+1); v != nil {
				return v
			}
		}
		for key, nested := range data {
			if key == "aweme_detail" || key == "item_list" || key == "videoInfoRes" || key == "awemeDetail" || key == "loaderData" {
				continue
			}
			if v := findPublicDouyinVideo(nested, id, source, depth+1); v != nil {
				return v
			}
		}
	case []any:
		for _, nested := range data {
			if v := findPublicDouyinVideo(nested, id, source, depth+1); v != nil {
				return v
			}
		}
	}
	return nil
}

func (e *Engine) extractPublicDouyin(ctx context.Context, raw string) (*douyinPublicVideo, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	u, err := url.Parse(raw)
	if err != nil || !isDouyinPage(u) {
		return nil, raw, errors.New("请输入抖音单个视频的分享链接")
	}
	if douyinNotePath.MatchString(u.Path) {
		return nil, raw, errors.New("这是抖音图文作品，没有可下载为 MP4 的视频流")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: e.transport, Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 6 || !isDouyinPage(req.URL) || req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
			return errors.New("抖音分享链接跳转到不支持的页面")
		}
		return nil
	}}
	id := douyinID(u)
	if id == "" {
		if u.Hostname() != "v.douyin.com" {
			return nil, raw, errors.New("请粘贴抖音单个视频的分享链接；用户主页、直播和图文作品不支持导出 MP4")
		}
		body, redirected, err := e.douyinPage(ctx, client, raw, "")
		if err != nil {
			return nil, raw, err
		}
		id = douyinID(redirected)
		if id == "" {
			return nil, raw, errors.New(douyinAccessMessage)
		}
		u = redirected
		for _, data := range douyinPageData(body) {
			if v := findPublicDouyinVideo(data, id, u.String(), 0); v != nil {
				return v, canonicalDouyinURL(id, u), nil
			}
		}
	}
	canonical := canonicalDouyinURL(id, u)
	share, _ := url.Parse("https://www.iesdouyin.com/share/video/" + id + "/")
	query := url.Values{}
	for _, key := range []string{"share_scene", "share_token", "aweme_type", "scene_from", "from_aid"} {
		if value := u.Query().Get(key); value != "" {
			query.Set(key, value)
		}
	}
	share.RawQuery = query.Encode()
	body, _, err := e.douyinPage(ctx, client, share.String(), "")
	if err != nil {
		return nil, canonical, err
	}
	for _, data := range douyinPageData(body) {
		if v := findPublicDouyinVideo(data, id, share.String(), 0); v != nil {
			return v, canonical, nil
		}
	}
	// This is the same public iteminfo endpoint used by the shipped share-page
	// JavaScript (67454.f), with no forged security signatures or account state.
	api, _ := url.Parse("https://www.iesdouyin.com/web/api/v2/aweme/iteminfo/")
	query.Del("from_aid")
	query.Set("item_ids", id)
	query.Set("use_new_select_scope", "0")
	query.Set("reflow_id", "")
	api.RawQuery = query.Encode()
	body, _, err = e.douyinPage(ctx, client, api.String(), share.String())
	if err == nil {
		if v := findPublicDouyinVideo(decodeDouyinJSON(body), id, share.String(), 0); v != nil {
			return v, canonical, nil
		}
	}
	if ctx.Err() != nil {
		return nil, canonical, ctx.Err()
	}
	return nil, canonical, errors.New(douyinAccessMessage)
}

func (e *Engine) resolveDouyinVideo(raw string, headers http.Header) (*Resource, error) {
	ctx, cancel := context.WithTimeout(e.ctx, 90*time.Second)
	defer cancel()
	v, canonical, err := e.extractPlayableDouyin(ctx, raw)
	if err != nil {
		if ctx.Err() != nil {
			return nil, videoError(ctx, "", err)
		}
		// Keep the official extractor as a fallback for publicly accessible detail
		// responses. Its Cookie/login rejection is reported as a platform limitation.
		if canonical == raw && (strings.Contains(err.Error(), "图文") || strings.Contains(err.Error(), "用户主页")) {
			return nil, err
		}
		r, fallbackErr := e.resolveVideoWithContext(ctx, canonical, headers)
		if fallbackErr == nil {
			e.mu.Lock()
			r.Site = "抖音"
			if r.Quality == "" {
				r.Quality = "最高可用原流（待解析器确认）"
			}
			if stored := e.resources[r.ID]; stored != nil {
				stored.Site = r.Site
				stored.Quality = r.Quality
			}
			e.mu.Unlock()
			return r, nil
		}
		return nil, douyinVideoError(ctx, fallbackErr)
	}
	requestHeaders := http.Header{"User-Agent": []string{v.UserAgent}, "Referer": []string{v.Source}}
	r := e.remember(canonical, requestHeaders, "video/mp4", v.Size, "", true)
	if r == nil {
		return nil, errors.New("资源列表已满，请清空后重试")
	}
	e.mu.Lock()
	r.Kind, r.Type, r.MIME = "page", "video", "video/mp4"
	r.Site, r.Duration, r.Name = "抖音", v.Duration, safeFilename(v.Title+".mp4")
	if r.NameOverride != "" {
		r.Name = r.NameOverride
	}
	r.Size = v.Size // Also clear a previous known size when the new HTTP total is unknown.
	r.Quality = v.Quality
	r.Width, r.Height = int(douyinNumber(v.Formats[0]["width"])), int(douyinNumber(v.Formats[0]["height"]))
	r.Thumbnail, r.Author = v.Thumbnail, v.Author
	r.Formats = douyinMediaFormats(v)
	r.FPS = douyinNumber(v.Formats[0]["fps"])
	r.Bitrate = douyinNumber(v.Formats[0]["tbr"])
	copyResource := *r
	e.mu.Unlock()
	return &copyResource, nil
}

func (e *Engine) updateDouyinSource(r *Resource, size int64, width, height int, quality string) {
	// r is the download's resource copy. Refresh its size as well as the list;
	// otherwise the progress parser can retain an obsolete, larger page total.
	r.Size, r.Width, r.Height, r.Quality = size, width, height, quality
	e.mu.Lock()
	if stored := e.resources[r.ID]; stored != nil {
		stored.Size, stored.Width, stored.Height, stored.Quality = size, width, height, quality
	}
	for _, job := range e.jobs {
		if job.ResourceID == r.ID && activeStatus(job.Status) {
			job.Total, job.Quality = size, quality
		}
	}
	e.mu.Unlock()
}

// Refresh expiring media URLs for each attempt, then let the existing video
// downloader handle progress, Range continuation, cancellation, remuxing and MP4 validation.
func (e *Engine) videoDownloadInput(ctx context.Context, r *Resource, staging string) ([]string, error) {
	u, _ := url.Parse(r.URL)
	if isPublicPlatformPage(u) {
		return e.publicPlatformDownloadInput(ctx, r, staging)
	}
	if !isDouyinPage(u) {
		return []string{"--", r.URL}, nil
	}
	prepareCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	v, canonical, err := e.extractPlayableDouyin(prepareCtx, r.URL)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if prepareCtx.Err() != nil {
			return nil, errors.New("抖音播放地址刷新超时，已有下载数据会保留，请重试")
		}
		if strings.HasPrefix(r.SelectedFormat, "dy:") {
			return nil, errors.New("指定抖音档位刷新失败，未更换画质，请重新查询或重试")
		}
		e.updateDouyinSource(r, -1, 0, 0, "浏览器播放源未确认；由解析器选择最高可用原流")
		return []string{"--", canonical}, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	metadata := filepath.Join(staging, "source.info.json")
	if strings.HasPrefix(r.SelectedFormat, "dy:") {
		matches := []map[string]any{}
		for _, format := range v.Ladder {
			if douyinChoiceID(format) == r.SelectedFormat {
				matches = append(matches, format)
			}
		}
		if len(matches) == 0 {
			return nil, errors.New("选定抖音档位已经失效，未更换画质，请重新查询")
		}
		v.Formats = matches
		if err := e.verifyDouyinMedia(ctx, v); err != nil {
			return nil, fmt.Errorf("指定抖音档位无法获取，未更换画质：%w", err)
		}
		v.Quality = strings.Replace(v.Quality, "最高可用原流", "指定档位原流", 1)
	}
	if !pathInside(staging, metadata) {
		return nil, errors.New("视频任务目录无效")
	}
	b, err := json.Marshal(v.extractorInfo())
	if err != nil {
		return nil, err
	}
	if err = atomicWrite(metadata, b, 0600); err != nil {
		return nil, fmt.Errorf("保存视频播放数据失败：%w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	width, height := int(douyinNumber(v.Formats[0]["width"])), int(douyinNumber(v.Formats[0]["height"]))
	quality := v.Quality
	if r.Width > 0 && r.Height > 0 && width*height > 0 && width*height < r.Width*r.Height {
		quality += "；较此前选择的分辨率降低"
	} else if r.Quality != "" && strings.Split(r.Quality, "；")[0] != strings.Split(quality, "；")[0] {
		quality += "；来源档位已更新"
	}
	e.updateDouyinSource(r, v.Size, width, height, quality)
	return []string{"--load-info-json", metadata}, nil
}

func douyinBrowserExecutable() string {
	for _, candidate := range []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
	} {
		if filepath.IsAbs(candidate) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

func (e *Engine) extractBrowserDouyin(ctx context.Context, raw string) (*douyinPublicVideo, string, error) {
	dir, err := e.videoTools()
	if err != nil {
		return nil, raw, err
	}
	node := filepath.Join(dir, "node.exe")
	if info, err := os.Stat(node); err != nil || info.IsDir() {
		return nil, raw, errors.New("缺少浏览器解析运行时，请保留 video-engine 中的 node.exe")
	}
	browser := douyinBrowserExecutable()
	if browser == "" {
		return nil, raw, errors.New("抖音自动浏览器解析需要本机安装 Microsoft Edge 或 Chrome")
	}
	root := filepath.Join(e.root, "data", "douyin-browser")
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, raw, err
	}
	requestDir, err := os.MkdirTemp(root, "request-")
	if err != nil {
		return nil, raw, err
	}
	defer func() {
		// This exact directory is owned by the request. No browser profile outside
		// our temporary root is loaded or removed, including on cancellation.
		if pathInside(root, requestDir) {
			for n := 0; n < 6; n++ {
				if os.RemoveAll(requestDir) == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		os.Remove(root)
	}()
	script := filepath.Join(requestDir, "browser-helper.mjs")
	if err = os.WriteFile(script, douyinBrowserScript, 0600); err != nil {
		return nil, raw, err
	}
	browserCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	out := &limitedBuffer{limit: 8 << 20}
	stderr := &tailBuffer{limit: 8192}
	cmd := exec.Command(node, script, "--browser", browser, "--profile", filepath.Join(requestDir, "profile"), "--url", raw, "--timeout", "45000")
	cmd.Stdout, cmd.Stderr = out, stderr
	if err = runManagedCommand(browserCtx, cmd); err != nil {
		if ctx.Err() != nil {
			return nil, raw, ctx.Err()
		}
		if browserCtx.Err() != nil {
			return nil, raw, errors.New("抖音浏览器解析超时，请重试或在资源捕获中正常播放该视频")
		}
		return nil, raw, fmt.Errorf("抖音浏览器解析失败：%s", strings.TrimSpace(stderr.String()))
	}
	var response struct {
		Item      json.RawMessage `json:"item"`
		URL       string          `json:"page_url"`
		UserAgent string          `json:"user_agent"`
	}
	if json.Unmarshal(out.Bytes(), &response) != nil {
		return nil, raw, errors.New("抖音浏览器未返回有效播放信息")
	}
	u, err := url.Parse(response.URL)
	if err != nil || !isDouyinPage(u) || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, raw, errors.New("抖音浏览器跳转到不支持的页面")
	}
	id := douyinID(u)
	requested, _ := url.Parse(raw)
	if expected := douyinID(requested); expected != "" && expected != id {
		return nil, raw, errors.New("浏览器返回的抖音作品与请求不一致")
	}
	v := findPublicDouyinVideo(decodeDouyinJSON(response.Item), id, response.URL, 0)
	if v == nil {
		return nil, raw, errors.New("浏览器没有获取到该作品的可下载视频流")
	}
	if response.UserAgent != "" && !strings.ContainsAny(response.UserAgent, "\r\n") {
		v.UserAgent = response.UserAgent
		for _, format := range v.Formats {
			format["http_headers"] = map[string]string{"User-Agent": v.UserAgent, "Referer": v.Source}
		}
	}
	return v, canonicalDouyinURL(id, u), nil
}

func douyinPixels(format map[string]any) float64 {
	return douyinNumber(format["width"]) * douyinNumber(format["height"])
}

// A quality choice describes the provider's rendition, not a signed URL or its
// changing position in the mirror list. Download cache IDs separately hash bytes.
func douyinChoiceID(format map[string]any) string {
	identity := fmt.Sprintf("%g|%g|%g|%g|%s|%s", douyinNumber(format["width"]), douyinNumber(format["height"]), douyinNumber(format["fps"]), douyinNumber(format["tbr"]), douyinText(format["vcodec"]), douyinText(format["_douyin_unavailable"]))
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("dy:%x", sum[:10])
}
func douyinMediaFormats(v *douyinPublicVideo) []MediaFormat {
	formats := v.Ladder
	if len(formats) == 0 {
		formats = v.Formats
	}
	out := []MediaFormat{}
	seen := map[string]bool{}
	for _, f := range orderedDouyinFormats(formats) {
		id := douyinChoiceID(f)
		if seen[id] {
			continue
		}
		seen[id] = true
		w, h := int(douyinNumber(f["width"])), int(douyinNumber(f["height"]))
		label := strings.ToUpper(douyinText(f["vcodec"]))
		if w > 0 && h > 0 {
			label = fmt.Sprintf("%d×%d · %s", w, h, label)
		}
		fps, rate := douyinNumber(f["fps"]), douyinNumber(f["tbr"])
		if fps > 0 {
			label += fmt.Sprintf(" · %g fps", fps)
		}
		if rate > 0 {
			label += fmt.Sprintf(" · %.0f kbps", rate)
		}
		reason := douyinText(f["_douyin_unavailable"])
		out = append(out, MediaFormat{ID: id, Label: label, Width: w, Height: h, FPS: fps, Bitrate: rate, Size: int64(max(douyinNumber(f["filesize"]), douyinNumber(f["filesize_approx"]))), Approx: douyinNumber(f["filesize"]) <= 0, Available: reason == "", Reason: reason})
	}
	return out
}

func betterDouyinQuality(a, b map[string]any) bool {
	if pixelsA, pixelsB := douyinPixels(a), douyinPixels(b); pixelsA != pixelsB {
		return pixelsA > pixelsB
	}
	if fpsA, fpsB := douyinNumber(a["fps"]), douyinNumber(b["fps"]); fpsA != fpsB {
		return fpsA > fpsB
	}
	// HEVC can preserve comparable detail with fewer bits than AVC. Comparing
	// raw bitrates across codecs would incorrectly call it a lower-quality file.
	return a["vcodec"] == b["vcodec"] && douyinNumber(a["tbr"]) > douyinNumber(b["tbr"])
}

func orderedDouyinFormats(formats []map[string]any) []map[string]any {
	ordered := append([]map[string]any(nil), formats...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if douyinPixels(a) != douyinPixels(b) {
			return douyinPixels(a) > douyinPixels(b)
		}
		return douyinNumber(a["fps"]) > douyinNumber(b["fps"])
	})
	// Bitrate only orders formats within one codec at the same resolution/FPS.
	// Keep codec groups in provider order; a conditional cross-codec comparison
	// inside sort.Less would be non-transitive and produce unstable choices.
	for start := 0; start < len(ordered); {
		end := start + 1
		for end < len(ordered) && douyinPixels(ordered[end]) == douyinPixels(ordered[start]) && douyinNumber(ordered[end]["fps"]) == douyinNumber(ordered[start]["fps"]) {
			end++
		}
		groups := map[string][]map[string]any{}
		var codecs []string
		for _, format := range ordered[start:end] {
			codec := douyinText(format["vcodec"])
			if _, ok := groups[codec]; !ok {
				codecs = append(codecs, codec)
			}
			groups[codec] = append(groups[codec], format)
		}
		position := start
		for _, codec := range codecs {
			group := groups[codec]
			sort.SliceStable(group, func(i, j int) bool {
				return douyinNumber(group[i]["tbr"]) > douyinNumber(group[j]["tbr"])
			})
			position += copy(ordered[position:end], group)
		}
		start = end
	}
	return ordered
}

func douyinQualityLabel(format map[string]any, alternatives []map[string]any) string {
	parts := []string{"最高可用原流"}
	width, height := int(douyinNumber(format["width"])), int(douyinNumber(format["height"]))
	if width > 0 && height > 0 {
		parts = append(parts, fmt.Sprintf("%d×%d", width, height))
	} else {
		parts = append(parts, "分辨率未提供")
	}
	if fps := douyinNumber(format["fps"]); fps > 0 {
		parts = append(parts, fmt.Sprintf("%g fps", fps))
	}
	if codec := douyinText(format["vcodec"]); codec != "" {
		parts = append(parts, strings.ToUpper(codec))
	}
	if rate := douyinNumber(format["tbr"]); rate > 0 {
		parts = append(parts, fmt.Sprintf("%.2f Mbps", rate/1000))
	}
	label := strings.Join(parts, " · ")
	for _, candidate := range alternatives {
		if !betterDouyinQuality(candidate, format) {
			continue
		}
		if reason := douyinText(candidate["_douyin_unavailable"]); reason != "" {
			return label + "；较高档无法使用（" + reason + "）"
		}
		return label + "；较高档地址不可用，已回退"
	}
	return label
}

func (e *Engine) verifyDouyinMedia(ctx context.Context, v *douyinPublicVideo) error {
	// Probe the quality ladder from best to worst, not the page's default
	// H.264 playback URL. The caller's 90-second budget also bounds mirror probes.
	formats := orderedDouyinFormats(v.Formats)
	v.Ladder = append([]map[string]any(nil), formats...)
	for _, format := range formats {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if douyinText(format["_douyin_unavailable"]) != "" {
			continue
		}
		raw, _ := format["url"].(string)
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		req, err := http.NewRequestWithContext(probeCtx, "GET", raw, nil)
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("User-Agent", v.UserAgent)
		req.Header.Set("Referer", v.Source)
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("Range", "bytes=0-4095")
		resp, err := (&http.Client{Transport: e.transport}).Do(req)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		prefix, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if (resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent) || readErr != nil || htmlMIME(resp.Header.Get("Content-Type")) || len(prefix) < 12 || string(prefix[4:8]) != "ftyp" {
			continue
		}
		size := int64(-1)
		if resp.StatusCode == http.StatusOK {
			size = resp.ContentLength
		}
		if _, _, total, ok := parseContentRange(resp.Header.Get("Content-Range")); ok {
			size = total
		}
		// Never keep the largest page-reported size when a different rendition
		// is selected. A partial response's Content-Length is only prefix bytes.
		v.Size = size
		delete(format, "filesize_approx")
		delete(format, "filesize")
		if size > 0 {
			format["filesize"] = size
		}
		// Cache filenames must identify media bytes, not their ordinal in a fresh
		// CDN list. Expiring signatures or mirror order changes may keep the same
		// partial file; a changed stream must not be appended to its old bytes.
		identity := sha256.New()
		fmt.Fprintf(identity, "%s\x00%s\x00%d\x00", v.ID, format["vcodec"], v.Size)
		identity.Write(prefix)
		format["format_id"] = fmt.Sprintf("public-%x", identity.Sum(nil)[:10])
		v.Quality = douyinQualityLabel(format, formats)
		format["format_note"] = v.Quality
		v.Formats = []map[string]any{format}
		return nil
	}
	return errors.New("抖音返回的媒体地址暂时不可访问或不是有效 MP4，未创建网页下载任务")
}

func (e *Engine) extractPlayableDouyin(ctx context.Context, raw string) (*douyinPublicVideo, string, error) {
	v, canonical, err := e.extractPublicDouyin(ctx, raw)
	if err == nil {
		if err = e.verifyDouyinMedia(ctx, v); err == nil {
			return v, canonical, nil
		}
	}
	if ctx.Err() != nil {
		return nil, canonical, ctx.Err()
	}
	if strings.Contains(err.Error(), "图文") || strings.Contains(err.Error(), "用户主页") {
		return nil, canonical, err
	}
	v, source, browserErr := e.extractBrowserDouyin(ctx, canonical)
	if browserErr != nil {
		return nil, canonical, browserErr
	}
	if err = e.verifyDouyinMedia(ctx, v); err != nil {
		return nil, source, err
	}
	return v, source, nil
}

func douyinVideoError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return videoError(ctx, "", err)
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "平台要求登录") || strings.Contains(lower, "cookie") || strings.Contains(lower, "403") || strings.Contains(lower, "unsupported") || strings.Contains(lower, "不支持此播放页") || strings.Contains(lower, "解析结果无效") || strings.Contains(lower, "没有可下载") {
		return errors.New(douyinAccessMessage)
	}
	return err
}

func platformVideoError(r *Resource, ctx context.Context, details string, err error) error {
	err = videoError(ctx, details, err)
	u, _ := url.Parse(r.URL)
	if isDouyinPage(u) {
		return douyinVideoError(ctx, err)
	}
	return err
}
